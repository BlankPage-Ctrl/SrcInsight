// Ignore is the project scope policy: which relative paths discovery, watching, and listing must skip.
// Precedence, low to high: defaults < root .gitignore < root .srcinsightignore < child .gitignore < child .srcinsightignore.
//
// Within one file later lines win; deeper files win across files, and a bare "!" re-includes a parent ignore.
// alwaysIgnored segments (.git, .srcinsight, .out, .cache) are non-overridable; editor temp files stay in the watcher, not here.
package filesystem

import (
	"os"
	"path/filepath"
	"strings"
	"sync"

	gitignore "github.com/sabhiram/go-gitignore"
)

// defaultIgnoredDirs sit at the bottom of the precedence chain, so any ignore file can re-include them.
var defaultIgnoredDirs = []string{
	"node_modules", ".git", "dist", "build", "__pycache__", "venv", ".venv", ".out", ".cache",
	"vendor",
}

var defaultIgnoredFileSuffixes = []string{
	".min.js", ".min.ts", ".min.tsx",
}

// alwaysIgnored segments can never be re-included, not even by "!".
var alwaysIgnoredSegments = []string{".git", ".srcinsight", ".out", ".cache"}

// probeSuffix lets dir queries hit "dir/"-style patterns; go-gitignore misses bare dirs.
// Dir queries also test rel plus probeSuffix.
const probeSuffix = "/.fgprobe"

// scopeMatcher holds one directory scope's compiled rules.
type scopeMatcher struct {
	matcher *gitignore.GitIgnore // full lines (positives + negations), later wins
	neg     *gitignore.GitIgnore // stripped "!" lines only, for cross-file re-include
}

// Ignore holds the scope policy for one project root. Safe for concurrent use.
type Ignore struct {
	mu     sync.Mutex
	root   string
	scopes map[string]*scopeMatcher // key: slash rel dir, "" = root
	loaded map[string]bool
}

// Load reads root/.gitignore and root/.srcinsightignore (both optional) and
// returns the scope policy for root. Missing files are not an error.
func Load(root string) *Ignore {
	abs, err := filepath.Abs(strings.TrimSpace(root))
	if err != nil || strings.TrimSpace(root) == "" {
		abs = filepath.Clean(root)
	} else {
		abs = filepath.Clean(abs)
	}
	ig := &Ignore{
		root:   abs,
		scopes: make(map[string]*scopeMatcher),
		loaded: make(map[string]bool),
	}
	ig.mu.Lock()
	defer ig.mu.Unlock()
	ig.loadScopeLocked("")
	return ig
}

// Root returns the absolute project root this policy was loaded for.
func (ig *Ignore) Root() string {
	if ig == nil {
		return ""
	}
	return ig.root
}

// IgnoredDir reports whether a slash-separated root-relative directory should
// be skipped (and pruned: children are skipped too).
func (ig *Ignore) IgnoredDir(rel string) bool {
	if ig == nil {
		return false
	}
	norm, ok := normalizeRel(rel)
	if !ok {
		return false
	}
	ig.mu.Lock()
	defer ig.mu.Unlock()
	if hasAlwaysIgnoredSegment(norm) {
		return true
	}
	ig.ensureLoadedLocked(norm, true)
	result := dirDefaultsMatch(norm)
	for _, scopeDir := range scopeChain(norm, true) {
		if v, ok := ig.scopeOpinionLocked(scopeDir, norm, true); ok {
			result = v
		}
	}
	return result
}

// IgnoredFile reports whether a slash-separated root-relative file should be
// skipped.
func (ig *Ignore) IgnoredFile(rel string) bool {
	if ig == nil {
		return false
	}
	norm, ok := normalizeRel(rel)
	if !ok {
		return false
	}
	ig.mu.Lock()
	defer ig.mu.Unlock()
	if hasAlwaysIgnoredSegment(norm) {
		return true
	}
	ig.ensureLoadedLocked(norm, false)
	result := fileDefaultsMatch(norm)
	for _, scopeDir := range scopeChain(norm, false) {
		if v, ok := ig.scopeOpinionLocked(scopeDir, norm, false); ok {
			result = v
		}
	}
	return result
}

// AsFilter adapts the policy to a Walk Filter: directories return prune,
// files return skip. Cheap per entry after scopes are cached.
func (ig *Ignore) AsFilter() Filter {
	return func(relSlash string, d os.DirEntry) bool {
		if d != nil && d.IsDir() {
			return ig.IgnoredDir(relSlash)
		}
		return ig.IgnoredFile(relSlash)
	}
}

// scopeOpinionLocked evaluates one scope ("" = root) against norm and reports
// (value, hasOpinion). Caller must hold ig.mu.
func (ig *Ignore) scopeOpinionLocked(scopeDir, norm string, isDir bool) (bool, bool) {
	sc, ok := ig.scopes[scopeDir]
	if !ok || sc == nil || sc.matcher == nil {
		return false, false
	}
	relToScope := norm
	if scopeDir != "" {
		if norm == scopeDir {
			// A scope's own file governs paths UNDER it, never itself.
			return false, false
		}
		relToScope = strings.TrimPrefix(norm, scopeDir+"/")
	}
	if v, pat := sc.matcher.MatchesPathHow(relToScope); pat != nil {
		return v, true
	}
	if isDir {
		if v, pat := sc.matcher.MatchesPathHow(relToScope + probeSuffix); pat != nil {
			return v, true
		}
	}
	// Cross-file re-include: bare "!" with no positive in this file is "no
	// opinion" to go-gitignore but means re-include in git.
	if sc.neg != nil {
		if _, pat := sc.neg.MatchesPathHow(relToScope); pat != nil {
			return false, true
		}
		if isDir {
			if _, pat := sc.neg.MatchesPathHow(relToScope + probeSuffix); pat != nil {
				return false, true
			}
		}
	}
	return false, false
}

// ensureLoadedLocked loads the root scope plus every ancestor scope of norm
// (and norm itself when it is a dir query). Caller must hold ig.mu.
func (ig *Ignore) ensureLoadedLocked(norm string, isDir bool) {
	ig.loadScopeLocked("")
	parts := strings.Split(norm, "/")
	depth := len(parts) - 1 // ancestors of a file
	if isDir {
		depth = len(parts) // include self for dir queries
	}
	for i := 1; i <= depth; i++ {
		ig.loadScopeLocked(strings.Join(parts[:i], "/"))
	}
}

// loadScopeLocked compiles the dirRel scope from .gitignore plus .srcinsightignore, which wins within the scope.
// Missing files and re-loads are no-ops; caller must hold ig.mu.
func (ig *Ignore) loadScopeLocked(dirRel string) {
	if ig.loaded[dirRel] {
		return
	}
	ig.loaded[dirRel] = true
	dirAbs := ig.root
	if dirRel != "" {
		dirAbs = filepath.Join(ig.root, filepath.FromSlash(dirRel))
	}
	lines := readIgnoreFile(dirAbs, ".gitignore")
	lines = append(lines, readIgnoreFile(dirAbs, ".srcinsightignore")...)
	matcher := gitignore.CompileIgnoreLines(lines...)
	var negs []string
	for _, ln := range lines {
		if strings.HasPrefix(ln, "!") && len(ln) > 1 {
			negs = append(negs, strings.TrimPrefix(ln, "!"))
		}
	}
	ig.scopes[dirRel] = &scopeMatcher{
		matcher: matcher,
		neg:     gitignore.CompileIgnoreLines(negs...),
	}
}

// scopeChain returns governing scopes shallowest first; a scope file never governs itself.
func scopeChain(norm string, isDir bool) []string {
	parts := strings.Split(norm, "/")
	depth := len(parts) - 1
	if isDir {
		depth = len(parts) - 1 // exclude self; self scope loaded but not consulted
	}
	out := make([]string, 0, depth+1)
	out = append(out, "")
	for i := 1; i <= depth; i++ {
		out = append(out, strings.Join(parts[:i], "/"))
	}
	return out
}

// normalizeRel cleans a root-relative slash path. Returns false for the root
// itself (never ignored) and for escapes.
func normalizeRel(rel string) (string, bool) {
	rel = filepath.ToSlash(strings.TrimSpace(rel))
	rel = strings.Trim(rel, "/")
	if rel == "" || rel == "." {
		return "", false
	}
	if rel == ".." || strings.HasPrefix(rel, "../") {
		return "", false
	}
	return rel, true
}

func hasAlwaysIgnoredSegment(norm string) bool {
	for _, seg := range strings.Split(norm, "/") {
		for _, a := range alwaysIgnoredSegments {
			if seg == a {
				return true
			}
		}
	}
	return false
}

func dirDefaultsMatch(norm string) bool {
	for _, seg := range strings.Split(norm, "/") {
		for _, d := range defaultIgnoredDirs {
			if seg == d {
				return true
			}
		}
	}
	return false
}

func fileDefaultsMatch(norm string) bool {
	if dirDefaultsMatch(norm) {
		return true
	}
	base := norm
	if i := strings.LastIndex(norm, "/"); i >= 0 {
		base = norm[i+1:]
	}
	for _, suf := range defaultIgnoredFileSuffixes {
		if strings.HasSuffix(base, suf) {
			return true
		}
	}
	return false
}

// readIgnoreFile returns normalized non-comment lines of dirAbs/name, or nil when absent.
// Trailing "/" (dir-only) and leading "/" (anchored) are kept.
func readIgnoreFile(dirAbs, name string) []string {
	data, err := os.ReadFile(filepath.Join(dirAbs, name))
	if err != nil {
		return nil
	}
	var out []string
	for _, ln := range strings.Split(string(data), "\n") {
		ln = strings.TrimRight(strings.TrimSpace(ln), "\r")
		if ln == "" || strings.HasPrefix(ln, "#") {
			continue
		}
		out = append(out, ln)
	}
	return out
}
