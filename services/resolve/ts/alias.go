// Package ts resolves TypeScript modules via tsconfig lookup, aliasing, and workspaces.
package ts

import (
	"path"
	"strings"
	"sync"

	"srcinsight/internal/filesystem"
	its "srcinsight/internal/typescript"
)

// maxExtendsDepth bounds extends-chain recursion.
const maxExtendsDepth = 10

var configFiles = []string{"tsconfig.json", "jsconfig.json"}

// Resolver caches one merged config per directory plus baseUrl probes.
type Resolver struct {
	root *filesystem.Root

	mu     sync.RWMutex
	loaded map[string]bool
	cached map[string]*its.Config
	seen   map[[2]string]bool
}

// New returns a Resolver bound to root; nil root yields nil.
func New(root *filesystem.Root) *Resolver {
	return &Resolver{
		root:   root,
		loaded: make(map[string]bool),
		cached: make(map[string]*its.Config),
		seen:   make(map[[2]string]bool),
	}
}

// IsConfigRel reports TS config files whose change invalidates the cache.
func IsConfigRel(rel string) bool { return its.IsConfigRel(rel) }

// ForFile returns the nearest config above the file directory.
func (r *Resolver) ForFile(currentFile string) *its.Config {
	if r == nil || r.root == nil {
		return nil
	}
	dir := path.Dir(path.Clean(currentFile))
	if dir == "." {
		dir = ""
	}
	for d := dir; ; {
		cfg, ok := r.forDir(d)
		if ok && cfg != nil {
			return cfg
		}
		if d == "" {
			return nil
		}
		parent := path.Dir(d)
		if parent == "." {
			parent = ""
		}
		d = parent
	}
}

func (r *Resolver) forDir(dir string) (*its.Config, bool) {
	r.mu.RLock()
	if r.loaded[dir] {
		cfg := r.cached[dir]
		r.mu.RUnlock()
		return cfg, true
	}
	r.mu.RUnlock()

	cfg := r.loadDir(dir)

	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.loaded[dir] {
		r.loaded[dir] = true
		r.cached[dir] = cfg
		return cfg, true
	}
	return r.cached[dir], true
}

// loadDir reads the dir config and resolves its extends chain.
func (r *Resolver) loadDir(dir string) *its.Config {
	for _, name := range configFiles {
		rel := name
		if dir != "" {
			rel = dir + "/" + name
		}
		data, err := r.root.ReadFile(rel)
		if err != nil {
			continue
		}
		raw, err := its.Parse(data)
		if err != nil {
			return nil
		}
		visiting := map[string]bool{rel: true}
		return r.mergeWithExtends(dir, rel, raw, visiting, 0)
	}
	return nil
}

// mergeWithExtends folds the extends chain under the file own values.
func (r *Resolver) mergeWithExtends(dir, file string, raw its.Raw, visiting map[string]bool, depth int) *its.Config {
	var acc *its.Config
	if depth < maxExtendsDepth {
		for _, ext := range raw.Extends {
			target := r.firstReadableExtendsTarget(dir, ext)
			if target == "" {
				continue
			}
			if visiting[target] {
				continue
			}
			visiting[target] = true
			child := r.loadFileChain(target, visiting, depth+1)
			delete(visiting, target)
			acc = its.MergeParent(acc, child)
		}
	}
	cfg := its.FromRaw(dir, file, raw, acc)
	return &cfg
}

func (r *Resolver) loadFileChain(file string, visiting map[string]bool, depth int) *its.Config {
	data, err := r.root.ReadFile(file)
	if err != nil {
		return nil
	}
	raw, err := its.Parse(data)
	if err != nil {
		return nil
	}
	dir := path.Dir(file)
	if dir == "." {
		dir = ""
	}
	return r.mergeWithExtends(dir, file, raw, visiting, depth)
}

// firstReadableExtendsTarget resolves extends to the first readable file.
func (r *Resolver) firstReadableExtendsTarget(dir, ext string) string {
	ext = strings.TrimSpace(ext)
	if ext == "" || strings.HasPrefix(ext, "/") {
		return ""
	}
	var candidates []string
	if strings.HasPrefix(ext, ".") {
		base := path.Clean(path.Join(dir, ext))
		candidates = its.ExtendsCandidates(base)
	} else {
		candidates = its.ExtendsCandidates(path.Join("node_modules", ext))
	}
	for _, c := range candidates {
		if _, err := r.root.ReadFile(c); err == nil {
			return c
		}
	}
	return ""
}

// CandidateBases expands an import source to root-relative base paths.
func (r *Resolver) CandidateBases(currentFile, source string) []string {
	cfg := r.ForFile(currentFile)
	if cfg == nil {
		return nil
	}
	return its.ExpandBases(cfg, source)
}

// BaseURLBase returns the base directory for bare imports.
func (r *Resolver) BaseURLBase(currentFile string) string {
	cfg := r.ForFile(currentFile)
	if cfg == nil {
		return ""
	}
	base := cfg.BaseURL
	if base == "" {
		base = cfg.Dir
	}
	if base == "" {
		return ""
	}
	if len(base) > 0 && base[0] == '/' {
		return ""
	}
	return base
}

// BaseURLExists reports whether the baseUrl fallback lands on a real file.
func (r *Resolver) BaseURLExists(currentFile, source string, expand func(base string) []string) bool {
	if r == nil || r.root == nil || expand == nil {
		return false
	}
	base := r.BaseURLBase(currentFile)
	if base == "" {
		return false
	}
	key := [2]string{base, source}
	r.mu.RLock()
	if v, ok := r.seen[key]; ok {
		r.mu.RUnlock()
		return v
	}
	r.mu.RUnlock()
	found := false
	for _, c := range expand(base) {
		if _, err := r.root.ReadFile(c); err == nil {
			found = true
			break
		}
	}
	r.mu.Lock()
	r.seen[key] = found
	r.mu.Unlock()
	return found
}

// IsInternalSource reports project-internal imports; bare packages stay external.
func (r *Resolver) IsInternalSource(currentFile, source string) bool {
	if strings.HasPrefix(source, ".") {
		return true
	}
	if len(r.CandidateBases(currentFile, source)) > 0 {
		return true
	}
	return r.BaseURLExists(currentFile, source, func(base string) []string {
		return ExpandBaseCandidatesFor(base, source)
	})
}

// Invalidate drops cached configs for the directory subtree.
func (r *Resolver) Invalidate(rel string) {
	if r == nil {
		return
	}
	dir := path.Dir(path.Clean(rel))
	if dir == "." {
		dir = ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for k := range r.loaded {
		if k == dir || (dir != "" && strings.HasPrefix(k, dir+"/")) || dir == "" {
			delete(r.loaded, k)
			delete(r.cached, k)
		}
	}
	for k := range r.seen {
		if k[0] == dir || (dir != "" && strings.HasPrefix(k[0], dir+"/")) || dir == "" {
			delete(r.seen, k)
		}
	}
}

// InvalidateAll drops the whole cache so edited configs re-read lazily.
func (r *Resolver) InvalidateAll() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.loaded = make(map[string]bool)
	r.cached = make(map[string]*its.Config)
	r.seen = make(map[[2]string]bool)
}
