package typescript

import (
	"path"
	"strings"
)

// SplitPackage splits a bare specifier into package name and subpath:
// "zod/mini" -> ("zod", "./mini"); "@org/pkg/sub" -> ("@org/pkg", "./sub").
func SplitPackage(source string) (pkg, sub string) {
	if strings.HasPrefix(source, "@") {
		parts := strings.Split(source, "/")
		if len(parts) < 2 {
			return source, "."
		}
		pkg = parts[0] + "/" + parts[1]
		rest := strings.Join(parts[2:], "/")
		if rest == "" {
			return pkg, "."
		}
		return pkg, "./" + rest
	}
	if i := strings.Index(source, "/"); i >= 0 {
		return source[:i], "." + source[i:]
	}
	return source, "."
}

// ExportStrings flattens an exports entry to its target strings: plain
// strings plus the values of condition maps (types/import/require/...).
func ExportStrings(v any) []string {
	switch t := v.(type) {
	case string:
		if t != "" {
			return []string{t}
		}
	case map[string]any:
		var out []string
		for _, sub := range t {
			out = append(out, ExportStrings(sub)...)
		}
		return out
	}
	return nil
}

// ExpandWorkspaceCandidates maps a bare workspace import to vault file candidates, exports first then fallbacks.
// Unverified paths are left for the caller's filtered reads.
func ExpandWorkspaceCandidates(dir string, exports map[string]any, source string) []string {
	pkg, sub := SplitPackage(source)
	_ = pkg
	var out []string
	seen := map[string]struct{}{}
	push := func(p string) {
		p = path.Clean(p)
		if _, dup := seen[p]; dup {
			return
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	// Exports-derived: map dist values (./x.js, ./index.d.ts) to source
	// twins (src/x.ts). Longest suffix first so ".d.ts" beats ".ts".
	for _, v := range ExportStrings(exports[sub]) {
		base := strings.TrimPrefix(v, "./")
		for _, suf := range []string{".d.ts", ".d.cts", ".d.mts", ".js", ".cjs", ".mjs", ".cts", ".mts", ".tsx", ".ts"} {
			if strings.HasSuffix(base, suf) {
				base = strings.TrimSuffix(base, suf)
				break
			}
		}
		for _, c := range []string{
			path.Join(dir, "src", base+".ts"),
			path.Join(dir, "src", base+".tsx"),
			path.Join(dir, "src", base+".mts"),
			path.Join(dir, "src", base+".cts"),
			path.Join(dir, base+".ts"),
		} {
			push(c)
		}
	}
	// Source fallbacks cover packages without usable exports.
	subBase := strings.TrimPrefix(sub, "./")
	if subBase == "" || subBase == "." {
		for _, c := range []string{
			path.Join(dir, "src", "index.ts"),
			path.Join(dir, "src", "index.mts"),
			path.Join(dir, "src", "index.tsx"),
			path.Join(dir, "index.ts"),
		} {
			push(c)
		}
	} else {
		for _, c := range []string{
			path.Join(dir, "src", subBase+".ts"),
			path.Join(dir, "src", subBase+".tsx"),
			path.Join(dir, "src", subBase+".ts", "index.ts"),
			path.Join(dir, subBase+".ts"),
			path.Join(dir, subBase, "index.ts"),
		} {
			push(c)
		}
	}
	return out
}

// ParseWorkspaceGlobs extracts the packages list from pnpm-workspace.yaml with a minimal hand parser.
// It returns raw globs; expansion lives in the I/O layer with filesystem access.
func ParseWorkspaceGlobs(raw string) []string {
	var out []string
	inList := false
	for _, line := range strings.Split(raw, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") || trimmed == "" {
			continue
		}
		if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			inList = trimmed == "packages:"
			continue
		}
		if !inList {
			continue
		}
		item := strings.TrimSpace(strings.TrimPrefix(trimmed, "-"))
		item = strings.TrimSpace(item)
		if i := strings.Index(item, "#"); i >= 0 {
			item = strings.TrimSpace(item[:i])
		}
		item = strings.Trim(item, "'\"")
		if item == "" {
			continue
		}
		out = append(out, item)
	}
	return out
}
