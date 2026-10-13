// Package typescript is the filesystem-free half of TypeScript support: tsconfig parsing, extends merging, and paths matching.
// It uses only stdlib and never touches disk, so the sibling-independence guard stays green.
//
// All I/O, walk-up lookup, and caching live in services/resolve/ts, which composes this package with filesystem.Root.
package typescript

import (
	"encoding/json"
	"path"
	"sort"
	"strings"
)

// Raw is one parsed config file before extends merging; Extends keeps declaration order.
// BaseURL and Paths are nil when unset.
type Raw struct {
	Extends []string
	BaseURL *string
	Paths   map[string][]string
}

// Config is a fully merged config for one directory; Paths merge per-key with child keys winning.
// PathBases keeps the baseUrl active when each pattern was defined; fallback is BaseURL then Dir.
type Config struct {
	Dir       string
	File      string
	BaseURL   string
	Paths     map[string][]string
	PathBases map[string]string
}

// Parse parses JSONC (// and /* */ comments, trailing commas) into a Raw.
// Unknown fields are ignored. A missing compilerOptions is not an error.
func Parse(data []byte) (Raw, error) {
	cleaned := stripJSONC(data)
	var wire struct {
		Extends         json.RawMessage `json:"extends"`
		CompilerOptions *struct {
			BaseURL *string             `json:"baseUrl"`
			Paths   map[string][]string `json:"paths"`
		} `json:"compilerOptions"`
	}
	var out Raw
	if err := json.Unmarshal(cleaned, &wire); err != nil {
		return Raw{}, err
	}
	if len(wire.Extends) > 0 {
		trimmed := strings.TrimSpace(string(wire.Extends))
		if trimmed != "" && trimmed != "null" {
			var single string
			if err := json.Unmarshal(wire.Extends, &single); err == nil {
				if strings.TrimSpace(single) != "" {
					out.Extends = []string{single}
				}
			} else {
				var list []string
				if err := json.Unmarshal(wire.Extends, &list); err != nil {
					return Raw{}, err
				}
				for _, e := range list {
					if strings.TrimSpace(e) != "" {
						out.Extends = append(out.Extends, e)
					}
				}
			}
		}
	}
	if wire.CompilerOptions != nil {
		out.BaseURL = wire.CompilerOptions.BaseURL
		out.Paths = wire.CompilerOptions.Paths
	}
	return out, nil
}

// FromRaw resolves one file's Raw against its extends parent, with the file's own baseUrl winning.
// Paths merge per-key with the child's base; parent entries keep their original base.
func FromRaw(dir, file string, raw Raw, parent *Config) Config {
	base := dir
	if raw.BaseURL != nil {
		base = joinBase(dir, *raw.BaseURL)
	} else if parent != nil {
		base = parent.BaseURL
		if base == "" {
			base = parent.Dir
		}
	}
	merged := make(map[string][]string)
	bases := make(map[string]string)
	if parent != nil {
		for k, v := range parent.Paths {
			merged[k] = append([]string(nil), v...)
			if parent.PathBases != nil {
				if b, ok := parent.PathBases[k]; ok {
					bases[k] = b
				} else {
					b := parent.BaseURL
					if b == "" {
						b = parent.Dir
					}
					bases[k] = b
				}
			} else {
				b := parent.BaseURL
				if b == "" {
					b = parent.Dir
				}
				bases[k] = b
			}
		}
		// carry any PathBases whose Paths were already merged via MergeParent
		if parent.PathBases != nil {
			for k, b := range parent.PathBases {
				if _, ok := bases[k]; !ok {
					bases[k] = b
				}
			}
		}
	}
	for k, v := range raw.Paths {
		merged[k] = append([]string(nil), v...)
		b := base
		if b == "" {
			b = dir
		}
		bases[k] = b
	}
	// drop empty bases map to keep nil-like for legacy callers
	if len(bases) == 0 {
		bases = nil
	}
	return Config{Dir: dir, File: file, BaseURL: base, Paths: merged, PathBases: bases}
}

// MatchPattern matches a TS paths pattern against a source, returning the wildcard suffix.
// Only a single "*" is supported; patterns with more are treated as literals.
func MatchPattern(pattern, source string) (string, bool) {
	if strings.Count(pattern, "*") > 1 {
		return "", pattern == source
	}
	if !strings.Contains(pattern, "*") {
		return "", pattern == source
	}
	pre, suf, _ := strings.Cut(pattern, "*")
	if !strings.HasPrefix(source, pre) {
		return "", false
	}
	rest := source[len(pre):]
	if suf != "" && !strings.HasSuffix(rest, suf) {
		return "", false
	}
	return rest[:len(rest)-len(suf)], true
}

// ExpandBases expands source through cfg.Paths into root-relative base paths in deterministic order.
// Targets with "*" substitute the matched suffix; results are cleaned, deduplicated, and joined to base.
func ExpandBases(cfg *Config, source string) []string {
	if cfg == nil || len(cfg.Paths) == 0 {
		return nil
	}
	keys := make([]string, 0, len(cfg.Paths))
	for k := range cfg.Paths {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		pi := strings.Index(keys[i], "*")
		if pi < 0 {
			pi = len(keys[i]) + 1000 // exact keys first
		}
		pj := strings.Index(keys[j], "*")
		if pj < 0 {
			pj = len(keys[j]) + 1000
		}
		if pi != pj {
			return pi > pj // longer prefix first
		}
		if len(keys[i]) != len(keys[j]) {
			return len(keys[i]) > len(keys[j])
		}
		return keys[i] < keys[j]
	})
	var out []string
	seen := make(map[string]struct{})
	for _, k := range keys {
		suffix, ok := MatchPattern(k, source)
		if !ok {
			continue
		}
		var base string
		hasSpecific := false
		if cfg.PathBases != nil {
			if b, ok := cfg.PathBases[k]; ok {
				base = b
				hasSpecific = true
			}
		}
		if !hasSpecific {
			base = cfg.BaseURL
			if base == "" {
				base = cfg.Dir
			}
		}
		for _, target := range cfg.Paths[k] {
			t := strings.TrimSpace(target)
			if t == "" {
				continue
			}
			var expanded string
			if strings.Contains(t, "*") {
				expanded = strings.ReplaceAll(t, "*", suffix)
			} else {
				expanded = t
			}
			var joined string
			if path.IsAbs(expanded) {
				joined = path.Clean(expanded)
			} else if base == "" || base == "." {
				joined = path.Clean(expanded)
			} else {
				joined = path.Clean(path.Join(base, expanded))
			}
			if _, dup := seen[joined]; dup {
				continue
			}
			seen[joined] = struct{}{}
			out = append(out, joined)
		}
	}
	return out
}

// joinBase joins a config dir with a raw baseUrl value. Empty baseUrl means
// the dir itself. "." also collapses to dir.
func joinBase(dir, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "." {
		return dir
	}
	if path.IsAbs(raw) {
		return path.Clean(raw)
	}
	if dir == "" {
		return path.Clean(raw)
	}
	return path.Clean(path.Join(dir, raw))
}

// IsConfigRel reports whether rel is a TS config file whose change must invalidate the cache.
func IsConfigRel(rel string) bool {
	base := rel
	if i := strings.LastIndex(rel, "/"); i >= 0 {
		base = rel[i+1:]
	}
	if base == "tsconfig.json" || base == "jsconfig.json" {
		return true
	}
	return strings.HasPrefix(base, "tsconfig.") && strings.HasSuffix(base, ".json")
}

// ExtendsCandidates expands an extends base to the files TS would probe: as-is, plus .json, plus /tsconfig.json.
func ExtendsCandidates(base string) []string {
	if strings.HasSuffix(base, ".json") {
		return []string{base}
	}
	return []string{base, base + ".json", path.Join(base, "tsconfig.json")}
}

// MergeParent folds two resolved extends parents (a then b, b wins per-key) for FromRaw.
func MergeParent(a, b *Config) *Config {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	out := &Config{
		BaseURL:   b.BaseURL,
		Paths:     make(map[string][]string, len(a.Paths)+len(b.Paths)),
		PathBases: make(map[string]string, len(a.Paths)+len(b.Paths)),
	}
	for k, v := range a.Paths {
		out.Paths[k] = append([]string(nil), v...)
		if a.PathBases != nil {
			if bv, ok := a.PathBases[k]; ok {
				out.PathBases[k] = bv
			} else {
				base := a.BaseURL
				if base == "" {
					base = a.Dir
				}
				out.PathBases[k] = base
			}
		} else {
			base := a.BaseURL
			if base == "" {
				base = a.Dir
			}
			out.PathBases[k] = base
		}
	}
	// carry any PathBases without Paths (defensive)
	if a.PathBases != nil {
		for k, bv := range a.PathBases {
			if _, ok := out.PathBases[k]; !ok {
				out.PathBases[k] = bv
			}
		}
	}
	for k, v := range b.Paths {
		out.Paths[k] = append([]string(nil), v...)
		if b.PathBases != nil {
			if bv, ok := b.PathBases[k]; ok {
				out.PathBases[k] = bv
			} else {
				base := b.BaseURL
				if base == "" {
					base = b.Dir
				}
				out.PathBases[k] = base
			}
		} else {
			base := b.BaseURL
			if base == "" {
				base = b.Dir
			}
			out.PathBases[k] = base
		}
	}
	if b.PathBases != nil {
		for k, bv := range b.PathBases {
			if _, ok := b.Paths[k]; !ok {
				// b has a base for a key not in Paths (should not happen, but keep)
				if _, exists := out.PathBases[k]; !exists {
					out.PathBases[k] = bv
				}
			}
		}
	}
	if len(out.PathBases) == 0 {
		out.PathBases = nil
	}
	return out
}

// stripJSONC removes // line comments, /* */ blocks and trailing commas.
// It respects double-quoted strings and backslash escapes.
func stripJSONC(data []byte) []byte {
	out := make([]byte, 0, len(data))
	inStr := false
	esc := false
	i := 0
	for i < len(data) {
		c := data[i]
		if inStr {
			out = append(out, c)
			if esc {
				esc = false
			} else if c == '\\' {
				esc = true
			} else if c == '"' {
				inStr = false
			}
			i++
			continue
		}
		switch c {
		case '"':
			inStr = true
			out = append(out, c)
			i++
		case '/':
			if i+1 < len(data) && data[i+1] == '/' {
				i += 2
				for i < len(data) && data[i] != '\n' {
					i++
				}
			} else if i+1 < len(data) && data[i+1] == '*' {
				i += 2
				for i+1 < len(data) && !(data[i] == '*' && data[i+1] == '/') {
					if data[i] == '\n' {
						out = append(out, '\n')
					}
					i++
				}
				i += 2 // skip */
			} else {
				out = append(out, c)
				i++
			}
		default:
			out = append(out, c)
			i++
		}
	}
	return stripTrailingCommas(out)
}

// stripTrailingCommas removes "," directly before "}" or "]" outside strings.
func stripTrailingCommas(data []byte) []byte {
	out := make([]byte, 0, len(data))
	inStr := false
	esc := false
	for i := 0; i < len(data); i++ {
		c := data[i]
		if inStr {
			out = append(out, c)
			if esc {
				esc = false
			} else if c == '\\' {
				esc = true
			} else if c == '"' {
				inStr = false
			}
			continue
		}
		if c == '"' {
			inStr = true
			out = append(out, c)
			continue
		}
		if c == ',' {
			j := i + 1
			for j < len(data) && (data[j] == ' ' || data[j] == '\t' || data[j] == '\n' || data[j] == '\r') {
				j++
			}
			if j < len(data) && (data[j] == '}' || data[j] == ']') {
				continue // drop trailing comma
			}
		}
		out = append(out, c)
	}
	return out
}
