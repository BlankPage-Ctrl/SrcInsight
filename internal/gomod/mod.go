// Package gomod parses go.mod and go.work files without I/O, so resolvers stay testable on stdlib only.
// It is line-oriented and best-effort: only module, replace, and use are understood.
//
// Module paths and local dirs never contain spaces, so whitespace splitting is safe.
package gomod

import (
	"strings"
)

// Replacement is one replace directive; Local is true when New is a filesystem path.
type Replacement struct {
	Old   string
	New   string
	Local bool
}

// File is the parsed subset of one go.mod.
type File struct {
	Module   string
	Replaces []Replacement
}

// Work is the parsed subset of one go.work: the `use` directories.
type Work struct {
	Uses []string
}

// Parse parses go.mod content. Never fails: unparseable input yields a File
// with whatever subset was recognized.
func Parse(data []byte) *File {
	f := &File{}
	var block string // directive owning the current paren block
	for _, line := range strings.Split(string(data), "\n") {
		line = stripComment(line)
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if fields[0] == ")" {
			block = ""
			continue
		}
		// Block opener: `require (` etc.
		if len(fields) == 2 && fields[1] == "(" {
			block = fields[0]
			continue
		}
		if block != "" {
			fields = append([]string{block}, fields...)
		}
		switch fields[0] {
		case "module":
			if len(fields) >= 2 && f.Module == "" {
				f.Module = fields[1]
			}
		case "replace":
			if r, ok := parseReplace(fields[1:]); ok {
				f.Replaces = append(f.Replaces, r)
			}
		}
	}
	return f
}

// ParseWork parses go.work content, returning the `use` directories.
func ParseWork(data []byte) *Work {
	w := &Work{}
	var block string
	for _, line := range strings.Split(string(data), "\n") {
		line = stripComment(line)
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if fields[0] == ")" {
			block = ""
			continue
		}
		if len(fields) == 2 && fields[1] == "(" {
			block = fields[0]
			continue
		}
		if block != "" {
			fields = append([]string{block}, fields...)
		}
		if fields[0] != "use" || len(fields) < 2 {
			continue
		}
		dir := fields[1]
		if dir != "" {
			w.Uses = append(w.Uses, dir)
		}
	}
	return w
}

// stripComment cuts a trailing // comment. Module paths, versions and local
// dirs never contain "//", so the first occurrence always starts a comment.
func stripComment(line string) string {
	if i := strings.Index(line, "//"); i >= 0 {
		return line[:i]
	}
	return line
}

// parseReplace parses the remainder of a replace directive (after the
// `replace` keyword, or a block line): `old [oldver] => new [newver]`.
func parseReplace(fields []string) (Replacement, bool) {
	var r Replacement
	arrow := -1
	for i, tok := range fields {
		if tok == "=>" {
			arrow = i
			break
		}
	}
	if arrow < 0 {
		return r, false
	}
	left, right := fields[:arrow], fields[arrow+1:]
	if len(left) == 0 || len(right) == 0 {
		return r, false
	}
	r.Old = left[0]
	r.New = right[0]
	r.Local = isLocalPath(r.New)
	return r, r.Old != "" && r.New != ""
}

// isLocalPath reports whether a replace target is a filesystem path rather
// than a module path: explicit relative, absolute, or Windows-absolute.
func isLocalPath(s string) bool {
	if s == "" {
		return false
	}
	if strings.HasPrefix(s, ".") || strings.HasPrefix(s, "/") {
		return true
	}
	if len(s) >= 3 && s[1] == ':' && (s[2] == '\\' || s[2] == '/') {
		return true
	}
	return false
}

// ReplaceDir maps an import source through local replacements, preferring exact then longest-prefix matches.
// It returns the replacement dir plus any remainder subpath, or "" when none applies.
func (f *File) ReplaceDir(source string) string {
	if f == nil || source == "" {
		return ""
	}
	best := -1
	for i, r := range f.Replaces {
		if !r.Local {
			continue
		}
		if source == r.Old || strings.HasPrefix(source, r.Old+"/") {
			if best < 0 || len(r.Old) > len(f.Replaces[best].Old) {
				best = i
			}
		}
	}
	if best < 0 {
		return ""
	}
	r := f.Replaces[best]
	rest := strings.TrimPrefix(source, r.Old)
	rest = strings.TrimPrefix(rest, "/")
	if rest == "" {
		return r.New
	}
	return r.New + "/" + rest
}

// ModuleDir maps an import source into a module-relative dir ("" for the root).
// ok is false for foreign paths.
func (f *File) ModuleDir(source string) (rel string, ok bool) {
	if f == nil || f.Module == "" || source == "" {
		return "", false
	}
	if source == f.Module {
		return "", true
	}
	if strings.HasPrefix(source, f.Module+"/") {
		return strings.TrimPrefix(source, f.Module+"/"), true
	}
	return "", false
}
