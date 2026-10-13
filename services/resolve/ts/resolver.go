package ts

import (
	"path"
	"strings"

	"srcinsight/internal/model"
)

// TSResolver handles relative imports plus alias and workspace mapping.
type TSResolver struct {
	Alias *Resolver
	WS    *Workspace
}

func (t TSResolver) Dialect() model.Language { return model.LangTypeScript }

// CandidatePaths returns possible vault paths; empty means unresolvable.
func (t TSResolver) CandidatePaths(currentFile, source string) []string {
	if source == "" {
		return nil
	}
	if strings.HasPrefix(source, ".") {
		return RelativeCandidates(currentFile, source)
	}
	if t.Alias != nil {
		var out []string
		seen := make(map[string]struct{})
		for _, base := range t.Alias.CandidateBases(currentFile, source) {
			for _, c := range ExpandBaseCandidates(base) {
				if _, dup := seen[c]; dup {
					continue
				}
				seen[c] = struct{}{}
				out = append(out, c)
			}
		}
		if len(out) > 0 {
			return out
		}
		// baseUrl fallback runs paths order; workspace names win over blind joins.
		if base := t.Alias.BaseURLBase(currentFile); base != "" {
			if t.WS == nil || !t.WS.IsInternal(source) {
				if cands := ExpandBaseCandidatesFor(base, source); len(cands) > 0 {
					return cands
				}
			}
		}
	}
	// Workspace packages last; absent files yield empty downstream.
	if t.WS != nil {
		return t.WS.CandidatePaths(source)
	}
	return nil
}

// IsInternalSource reports project-internal imports; alias imports stay internal.
func (t TSResolver) IsInternalSource(currentFile, source string) bool {
	if strings.HasPrefix(source, ".") {
		return true
	}
	if t.Alias != nil && t.Alias.IsInternalSource(currentFile, source) {
		return true
	}
	return t.WS != nil && t.WS.IsInternal(source)
}

// RelativeCandidates expands relative imports without altering behaviour.
func RelativeCandidates(currentFile, source string) []string {
	dir := path.Dir(currentFile)
	base := path.Join(dir, source)
	base = path.Clean(base)
	ext := strings.ToLower(path.Ext(base))
	switch ext {
	case ".ts", ".tsx", ".mts", ".cts", ".mjs", ".cjs", ".vue":
		return []string{base}
	case ".js":
		trim := strings.TrimSuffix(base, ext)
		return []string{
			trim + ".ts",
			trim + ".tsx",
			trim + ".mts",
			trim + ".cts",
			base,
			path.Join(trim, "index.ts"),
			path.Join(trim, "index.tsx"),
		}
	case ".jsx":
		trim := strings.TrimSuffix(base, ext)
		return []string{
			trim + ".tsx",
			trim + ".ts",
			base,
			path.Join(trim, "index.tsx"),
		}
	default:
		return []string{
			base + ".ts",
			base + ".tsx",
			base + ".mts",
			base + ".cts",
			base + ".jsx",
			base + ".js",
			base + ".vue",
			path.Join(base, "index.ts"),
			path.Join(base, "index.tsx"),
			path.Join(base, "index.jsx"),
			path.Join(base, "index.js"),
			path.Join(base, "index.vue"),
		}
	}
}

// ExpandBaseCandidatesFor joins a base with a source and expands candidates.
func ExpandBaseCandidatesFor(base, source string) []string {
	if base == "" || source == "" {
		return nil
	}
	return ExpandBaseCandidates(path.Join(base, source))
}

// ExpandBaseCandidates expands one alias base using relative import rules.
func ExpandBaseCandidates(base string) []string {
	base = path.Clean(base)
	ext := strings.ToLower(path.Ext(base))
	switch ext {
	case ".ts", ".tsx", ".mts", ".cts", ".mjs", ".cjs", ".vue":
		return []string{base}
	case ".js":
		trim := strings.TrimSuffix(base, ext)
		return []string{
			trim + ".ts",
			trim + ".tsx",
			trim + ".mts",
			trim + ".cts",
			base,
			path.Join(trim, "index.ts"),
			path.Join(trim, "index.tsx"),
		}
	case ".jsx":
		trim := strings.TrimSuffix(base, ext)
		return []string{
			trim + ".tsx",
			trim + ".ts",
			base,
			path.Join(trim, "index.tsx"),
		}
	default:
		return []string{
			base + ".ts",
			base + ".tsx",
			base + ".mts",
			base + ".cts",
			base + ".jsx",
			base + ".js",
			base + ".vue",
			path.Join(base, "index.ts"),
			path.Join(base, "index.tsx"),
			path.Join(base, "index.jsx"),
			path.Join(base, "index.js"),
			path.Join(base, "index.vue"),
		}
	}
}
