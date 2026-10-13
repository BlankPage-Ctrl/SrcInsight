package ts

import (
	"os"
	"path/filepath"
	"testing"

	"srcinsight/internal/filesystem"
)

func writeWSFile(t *testing.T, root, rel, content string) {
	t.Helper()
	abs := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newWorkspace(t *testing.T, files map[string]string) *Workspace {
	t.Helper()
	dir := t.TempDir()
	for rel, src := range files {
		writeWSFile(t, dir, rel, src)
	}
	root, err := filesystem.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	return NewWorkspace(root)
}

func TestWorkspaceCandidatePaths(t *testing.T) {
	w := newWorkspace(t, map[string]string{
		"pnpm-workspace.yaml":         "packages:\n  - 'packages/*'\n",
		"packages/pkg/package.json":   `{"name":"pkg","exports":{".":{"types":"./index.d.ts","import":"./index.js"}}}`,
		"packages/pkg/src/index.ts":   `export const x = 1;`,
		"packages/app/package.json":   `{"name":"app"}`,
		"packages/other/package.json": `{"name":"other"}`,
	})
	got := w.CandidatePaths("pkg")
	if len(got) == 0 || got[0] != "packages/pkg/src/index.ts" {
		t.Fatalf("candidates = %v, want packages/pkg/src/index.ts first", got)
	}
	if !w.IsInternal("pkg") || w.IsInternal("lodash") {
		t.Fatalf("IsInternal wrong: pkg=%v lodash=%v", w.IsInternal("pkg"), w.IsInternal("lodash"))
	}
	if w.IsInternal("./rel") {
		t.Fatal("relative source must not be workspace-internal")
	}
}

func TestWorkspaceInvalidate(t *testing.T) {
	dir := t.TempDir()
	writeWSFile(t, dir, "pnpm-workspace.yaml", "packages:\n  - 'packages/*'\n")
	writeWSFile(t, dir, "packages/pkg/package.json", `{"name":"pkg"}`)
	root, err := filesystem.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	w := NewWorkspace(root)
	if !w.IsInternal("pkg") {
		t.Fatal("warm cache first")
	}
	w.Invalidate()
	if !w.IsInternal("pkg") {
		t.Fatal("reload after invalidate")
	}
}
