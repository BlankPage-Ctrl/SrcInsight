package services

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"srcinsight/internal/model"
)

func ctxOf(t *testing.T) context.Context {
	t.Helper()
	return context.Background()
}

// entryID finds the single entry named name in file.
func entryID(t *testing.T, p *Pipeline, file, name string) model.EntryID {
	t.Helper()
	rows, err := p.Vault().SearchExact(ctxOf(t), name, 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.FilePath == file && r.Name == name {
			return r.ID
		}
	}
	t.Fatalf("entry %s in %s not found", name, file)
	return ""
}

// writeWS lays out a tiny pnpm monorepo: app imports bare "pkg".
func writeWS(t *testing.T, dir string) {
	t.Helper()
	files := map[string]string{
		"pnpm-workspace.yaml":              "packages:\n  - 'packages/*'\n",
		"packages/pkg/package.json":        `{"name":"pkg","exports":{".":{"types":"./index.d.ts","import":"./index.js"}}}`,
		"packages/pkg/src/index.ts":        "export * from './core/index.js';\n",
		"packages/pkg/src/core/index.ts":   "export { make } from './factory.js';\nexport class Widget { run(): number { return 1; } }\n",
		"packages/pkg/src/core/factory.ts": "import { Widget } from './widget.js';\nexport function make(): Widget { return null as unknown as Widget; }\n",
		"packages/pkg/src/core/widget.ts":  "export class Widget { run(): number { return 1; } }\n",
		"packages/app/main.ts":             "import * as pkg from 'pkg';\nconst w = pkg.make();\nexport function go(): number { return w.run(); }\n",
	}
	for rel, src := range files {
		abs := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestWorkspaceCandidatePaths(t *testing.T) {
	dir := t.TempDir()
	writeWS(t, dir)
	p, _ := testPipelineAt(t, dir)
	defer p.Close()
	got := p.ws.CandidatePaths("pkg")
	if len(got) == 0 || got[0] != "packages/pkg/src/index.ts" {
		t.Fatalf("workspace candidates = %v, want packages/pkg/src/index.ts first", got)
	}
	if !p.ws.IsInternal("pkg") || p.ws.IsInternal("lodash") {
		t.Fatalf("IsInternal wrong: pkg=%v lodash=%v", p.ws.IsInternal("pkg"), p.ws.IsInternal("lodash"))
	}
	if p.ws.IsInternal("./rel") {
		t.Fatal("relative source must not be workspace-internal")
	}
}

func TestWorkspaceRecvEndToEnd(t *testing.T) {
	dir := t.TempDir()
	writeWS(t, dir)
	p, _ := testPipelineAt(t, dir)
	defer p.Close()
	if _, err := p.FullScan(ctxOf(t), true); err != nil {
		t.Fatal(err)
	}
	// w.run() with w from pkg.make(): factory in another package, type in a
	// third file behind export * chains.
	goID := entryID(t, p, "packages/app/main.ts", "go")
	runID := entryID(t, p, "packages/pkg/src/core/widget.ts", "run")
	links := recvLinks(t, p, goID)
	if !hasLink(links, runID) {
		t.Fatalf("go calls = %v, want link to %s", links, runID)
	}
}
