// Package goresolve resolves Go imports via go.mod mapping plus go.work support.
package goresolve

import (
	"path"
	"sort"
	"strings"
	"sync"

	"srcinsight/internal/filesystem"
	"srcinsight/internal/gomod"
	"srcinsight/internal/model"
)

type GoResolver struct {
	// Mods binds module/work lookups to the project root. Nil keeps the
	// legacy same-file-only behaviour (unit tests without a pipeline).
	Mods *ModResolver
}

func (GoResolver) Dialect() model.Language { return model.LangGo }

// CandidatePaths returns vault paths for an import source; tests are excluded.
func (g GoResolver) CandidatePaths(currentFile, source string) []string {
	if source == "" || g.Mods == nil {
		return nil
	}
	if strings.HasPrefix(source, ".") {
		dir := path.Clean(path.Join(path.Dir(currentFile), source))
		return g.Mods.PackageFiles(dir, false)
	}
	var out []string
	seen := make(map[string]struct{})
	for _, m := range g.Mods.modulesFor(currentFile) {
		var pkgDir string
		if repl := m.mod.ReplaceDir(source); repl != "" {
			pkgDir = path.Clean(path.Join(m.dir, repl))
		} else if rel, ok := m.mod.ModuleDir(source); ok {
			pkgDir = path.Clean(path.Join(m.dir, rel))
		} else {
			continue
		}
		for _, f := range g.Mods.PackageFiles(pkgDir, false) {
			if _, dup := seen[f]; dup {
				continue
			}
			seen[f] = struct{}{}
			out = append(out, f)
		}
	}
	sort.Strings(out)
	return out
}

// IsInternalSource reports project-internal imports; module imports stay internal.
func (g GoResolver) IsInternalSource(currentFile, source string) bool {
	if source == "" || g.Mods == nil {
		return false
	}
	if strings.HasPrefix(source, ".") {
		return true
	}
	for _, m := range g.Mods.modulesFor(currentFile) {
		if m.mod.ReplaceDir(source) != "" {
			return true
		}
		if _, ok := m.mod.ModuleDir(source); ok {
			return true
		}
	}
	return false
}

type moduleHit struct {
	dir string
	mod *gomod.File
}

type workHit struct {
	dir  string
	work *gomod.Work
}

// ModResolver caches go.mod and go.work lookups plus package listings.
type ModResolver struct {
	root *filesystem.Root

	mu      sync.RWMutex
	loaded  map[string]bool
	mods    map[string]*moduleHit
	wloaded map[string]bool
	works   map[string]*workHit
	pkgs    map[string][]string
}

// New returns a ModResolver bound to root; nil root yields nothing.
func New(root *filesystem.Root) *ModResolver {
	return &ModResolver{
		root:    root,
		loaded:  make(map[string]bool),
		mods:    make(map[string]*moduleHit),
		wloaded: make(map[string]bool),
		works:   make(map[string]*workHit),
		pkgs:    make(map[string][]string),
	}
}

// InvalidateAll drops the whole cache so config edits take effect.
func (m *ModResolver) InvalidateAll() {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.loaded = make(map[string]bool)
	m.mods = make(map[string]*moduleHit)
	m.wloaded = make(map[string]bool)
	m.works = make(map[string]*workHit)
	m.pkgs = make(map[string][]string)
}

// moduleFor returns the nearest go.mod above the file directory.
func (m *ModResolver) moduleFor(file string) *moduleHit {
	if m == nil || m.root == nil {
		return nil
	}
	dir := path.Dir(file)
	m.mu.RLock()
	if m.loaded[dir] {
		hit := m.mods[dir]
		m.mu.RUnlock()
		return hit
	}
	m.mu.RUnlock()

	var probe []string
	var hit *moduleHit
	for d := dir; ; d = path.Dir(d) {
		m.mu.RLock()
		loaded := m.loaded[d]
		cached := m.mods[d]
		m.mu.RUnlock()
		if loaded {
			hit = cached
			break
		}
		probe = append(probe, d)
		if data, err := m.root.ReadFile(path.Join(d, "go.mod")); err == nil {
			modDir := d
			if modDir == "." {
				modDir = ""
			}
			hit = &moduleHit{dir: modDir, mod: gomod.Parse(data)}
			break
		}
		if d == "." || d == "/" || d == "" {
			break
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, d := range probe {
		m.loaded[d] = true
		m.mods[d] = hit
	}
	return hit
}

// workFor returns the nearest go.work above the file directory.
func (m *ModResolver) workFor(file string) *workHit {
	if m == nil || m.root == nil {
		return nil
	}
	dir := path.Dir(file)
	m.mu.RLock()
	if m.wloaded[dir] {
		hit := m.works[dir]
		m.mu.RUnlock()
		return hit
	}
	m.mu.RUnlock()

	var probe []string
	var hit *workHit
	for d := dir; ; d = path.Dir(d) {
		m.mu.RLock()
		loaded := m.wloaded[d]
		cached := m.works[d]
		m.mu.RUnlock()
		if loaded {
			hit = cached
			break
		}
		probe = append(probe, d)
		if data, err := m.root.ReadFile(path.Join(d, "go.work")); err == nil {
			hit = &workHit{dir: d, work: gomod.ParseWork(data)}
			if d == "." {
				hit.dir = ""
			}
			break
		}
		if d == "." || d == "/" || d == "" {
			break
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, d := range probe {
		m.wloaded[d] = true
		m.works[d] = hit
	}
	return hit
}

// modulesFor returns modules satisfying imports: nearest go.mod, then work members.
func (m *ModResolver) modulesFor(file string) []moduleHit {
	if m == nil || m.root == nil {
		return nil
	}
	var out []moduleHit
	if hit := m.moduleFor(file); hit != nil && hit.mod != nil {
		out = append(out, *hit)
	}
	w := m.workFor(file)
	if w == nil || w.work == nil {
		return out
	}
	for _, use := range w.work.Uses {
		modDir := path.Clean(path.Join(w.dir, use))
		data, err := m.root.ReadFile(path.Join(modDir, "go.mod"))
		if err != nil {
			continue
		}
		mod := gomod.Parse(data)
		if mod.Module == "" {
			continue
		}
		dup := false
		for _, o := range out {
			if o.mod.Module == mod.Module {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, moduleHit{dir: modDir, mod: mod})
		}
	}
	return out
}

// PackageFiles lists sorted .go files in a package dir; vendor yields nothing.
func (m *ModResolver) PackageFiles(pkgDir string, includeTests bool) []string {
	if m == nil || m.root == nil {
		return nil
	}
	pkgDir = path.Clean(pkgDir)
	if pkgDir == "." {
		pkgDir = ""
	}
	if isVendorDir(pkgDir) {
		return nil
	}
	key := pkgDir + "\x00" + boolStr(includeTests)
	m.mu.RLock()
	if files, ok := m.pkgs[key]; ok {
		m.mu.RUnlock()
		return files
	}
	m.mu.RUnlock()

	f, err := m.root.Open(openDir(pkgDir))
	if err != nil {
		m.mu.Lock()
		m.pkgs[key] = nil
		m.mu.Unlock()
		return nil
	}
	names, err := f.Readdirnames(-1)
	f.Close()
	if err != nil {
		m.mu.Lock()
		m.pkgs[key] = nil
		m.mu.Unlock()
		return nil
	}
	var out []string
	for _, n := range names {
		if !strings.HasSuffix(n, ".go") {
			continue
		}
		if !includeTests && strings.HasSuffix(n, "_test.go") {
			continue
		}
		rel := n
		if pkgDir != "" {
			rel = pkgDir + "/" + n
		}
		out = append(out, rel)
	}
	sort.Strings(out)
	m.mu.Lock()
	m.pkgs[key] = out
	m.mu.Unlock()
	return out
}

func openDir(pkgDir string) string {
	if pkgDir == "" {
		return "."
	}
	return pkgDir
}

// isVendorDir reports vendored paths, never resolution targets.
func isVendorDir(dir string) bool {
	return dir == "vendor" || strings.HasPrefix(dir, "vendor/") ||
		strings.Contains(dir, "/vendor/") || strings.HasSuffix(dir, "/vendor")
}

func boolStr(b bool) string {
	if b {
		return "1"
	}
	return "0"
}
