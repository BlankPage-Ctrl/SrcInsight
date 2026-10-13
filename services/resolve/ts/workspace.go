package ts

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"srcinsight/internal/filesystem"
	its "srcinsight/internal/typescript"
)

// Workspace maps bare package names to monorepo dirs; callers verify existence.
type Workspace struct {
	mu   sync.Mutex
	root *filesystem.Root
	once bool
	// name -> slash-relative package dir (e.g. "zod" -> "packages/zod")
	dirs map[string]string
	// dir -> exports map for subpath resolution ("./mini" -> target)
	exports map[string]map[string]any
}

func NewWorkspace(root *filesystem.Root) *Workspace {
	return &Workspace{root: root}
}

func (w *Workspace) ensure() {
	if w == nil || w.root == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.once {
		return
	}
	w.once = true
	w.dirs = map[string]string{}
	w.exports = map[string]map[string]any{}
	raw, err := w.root.ReadFile("pnpm-workspace.yaml")
	if err != nil {
		return
	}
	base := w.root.Base()
	for _, pattern := range its.ParseWorkspaceGlobs(string(raw)) {
		for _, dir := range expandWorkspaceGlob(base, pattern) {
			data, err := w.root.ReadFile(dir + "/package.json")
			if err != nil {
				continue
			}
			var meta struct {
				Name    string         `json:"name"`
				Exports map[string]any `json:"exports"`
			}
			if err := json.Unmarshal(data, &meta); err != nil || meta.Name == "" {
				continue
			}
			if _, dup := w.dirs[meta.Name]; !dup {
				w.dirs[meta.Name] = dir
				if len(meta.Exports) > 0 {
					w.exports[dir] = meta.Exports
				}
			}
		}
	}
}

// expandWorkspaceGlob expands one workspace pattern to package dirs.
func expandWorkspaceGlob(base, pattern string) []string {
	pat := filepath.Join(base, filepath.FromSlash(replaceGlobStar(pattern)))
	dirs, _ := filepath.Glob(pat)
	var out []string
	for _, d := range dirs {
		fi, err := os.Stat(d)
		if err != nil || !fi.IsDir() {
			continue
		}
		rel, err := filepath.Rel(base, d)
		if err != nil {
			continue
		}
		out = append(out, filepath.ToSlash(rel))
	}
	return out
}

func replaceGlobStar(pattern string) string {
	out := ""
	for i := 0; i < len(pattern); i++ {
		if i+1 < len(pattern) && pattern[i] == '*' && pattern[i+1] == '*' {
			out += "*"
			i++
			continue
		}
		out += string(pattern[i])
	}
	return out
}

func (w *Workspace) Invalidate() {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.once = false
	w.dirs = nil
	w.exports = nil
}

// CandidatePaths maps a bare workspace import to vault candidates.
func (w *Workspace) CandidatePaths(source string) []string {
	w.ensure()
	if w == nil || len(w.dirs) == 0 {
		return nil
	}
	pkg, _ := its.SplitPackage(source)
	dir, ok := w.dirs[pkg]
	if !ok {
		return nil
	}
	return its.ExpandWorkspaceCandidates(dir, w.exports[dir], source)
}

func (w *Workspace) IsInternal(source string) bool {
	w.ensure()
	if w == nil || len(w.dirs) == 0 {
		return false
	}
	pkg, _ := its.SplitPackage(source)
	_, ok := w.dirs[pkg]
	return ok
}
