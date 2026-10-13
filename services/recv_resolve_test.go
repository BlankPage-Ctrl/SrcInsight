package services

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"srcinsight/internal/filesystem"
	"srcinsight/internal/model"
	"srcinsight/internal/storage/cold"
)

// testPipelineAt opens a pipeline over an existing directory (for tests that
// add files between scans).
func testPipelineAt(t *testing.T, dir string) (*Pipeline, string) {
	t.Helper()
	root, err := filesystem.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	vault, err := cold.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = vault.Close() })
	return newForTest(root, vault), dir
}

const recvTypesSrc = `export class ZodType {
  safeParse(v: unknown): boolean { return true; }
}
export class ZodString extends ZodType {
}
`

const recvDirectSrc = `import { ZodString } from './types';

const s: ZodString = make();

export function run(): boolean {
  return s.safeParse(1);
}
`

const recvCoreSrc = `import { ZodString } from './types';

export function make(): ZodString {
  return null as unknown as ZodString;
}
`

const recvFactorySrc = `import { make } from './core';

const s = make();

export function run2(): boolean {
  return s.safeParse(1);
}
`

const recvBaseSrc = `export class Base {
  foo(): number { return 1; }
}
`

const recvSubSrc = `import { Base } from './base';

export class Sub extends Base {
  bar(): number { return super.foo(); }
}
`

// recvLinks returns the calls links out of one entry.
func recvLinks(t *testing.T, p *Pipeline, from model.EntryID) []string {
	t.Helper()
	nes, err := p.Vault().FetchNeighborEdges(context.Background(), from, []string{model.EdgeCalls}, false)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, ne := range nes {
		out = append(out, string(ne.Ref.ID))
	}
	return out
}

func hasLink(links []string, want model.EntryID) bool {
	for _, l := range links {
		if l == string(want) {
			return true
		}
	}
	return false
}

func TestRecvDirectAndHeritage(t *testing.T) {
	ctx := context.Background()
	p, _ := testPipeline(t, map[string]string{
		"src/types.ts": recvTypesSrc,
		"src/use.ts":   recvDirectSrc,
	})
	defer p.Close()
	if _, err := p.FullScan(ctx, true); err != nil {
		t.Fatal(err)
	}
	run := model.EntryID("typescript:src/use.ts:function:run:1")
	safe := model.EntryID("typescript:src/types.ts:method:ZodType.safeParse:1")
	if links := recvLinks(t, p, run); !hasLink(links, safe) {
		t.Fatalf("run calls = %v, want link to %s", links, safe)
	}
}

func TestRecvFactoryChain(t *testing.T) {
	ctx := context.Background()
	p, _ := testPipeline(t, map[string]string{
		"src/types.ts": recvTypesSrc,
		"src/core.ts":  recvCoreSrc,
		"src/use2.ts":  recvFactorySrc,
	})
	defer p.Close()
	if _, err := p.FullScan(ctx, true); err != nil {
		t.Fatal(err)
	}
	run2 := model.EntryID("typescript:src/use2.ts:function:run2:1")
	safe := model.EntryID("typescript:src/types.ts:method:ZodType.safeParse:1")
	if links := recvLinks(t, p, run2); !hasLink(links, safe) {
		t.Fatalf("run2 calls = %v, want link to %s", links, safe)
	}
}

func TestRecvSuper(t *testing.T) {
	ctx := context.Background()
	p, _ := testPipeline(t, map[string]string{
		"src/base.ts": recvBaseSrc,
		"src/sub.ts":  recvSubSrc,
	})
	defer p.Close()
	if _, err := p.FullScan(ctx, true); err != nil {
		t.Fatal(err)
	}
	bar := model.EntryID("typescript:src/sub.ts:method:Sub.bar:1")
	foo := model.EntryID("typescript:src/base.ts:method:Base.foo:1")
	if links := recvLinks(t, p, bar); !hasLink(links, foo) {
		t.Fatalf("bar calls = %v, want link to %s", links, foo)
	}
}

func TestRecvAmbiguousStaysLoose(t *testing.T) {
	ctx := context.Background()
	p, _ := testPipeline(t, map[string]string{
		"src/a.ts": "export class C { m(): number { return 1; } }\n",
		"src/b.ts": "export class C { m(): number { return 2; } }\n",
		"src/use.ts": `import C from './a';
import { C } from './b';
const t: C = make();
export function run(): number { return t.m(); }
`,
	})
	defer p.Close()
	if _, err := p.FullScan(ctx, true); err != nil {
		t.Fatal(err)
	}
	run := model.EntryID("typescript:src/use.ts:function:run:1")
	if links := recvLinks(t, p, run); len(links) != 0 {
		t.Fatalf("ambiguous run calls = %v, want none", links)
	}
}

func TestRecvReverseOnLaterType(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	write := func(rel, src string) {
		t.Helper()
		abs := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("src/use.ts", recvDirectSrc)
	p, _ := testPipelineAt(t, dir)
	defer p.Close()
	if _, err := p.FullScan(ctx, true); err != nil {
		t.Fatal(err)
	}
	run := model.EntryID("typescript:src/use.ts:function:run:1")
	if links := recvLinks(t, p, run); len(links) != 0 {
		t.Fatalf("before types land, run calls = %v, want none", links)
	}
	// The type file arrives later; reverse promotion must link without
	// re-parsing the caller.
	write("src/types.ts", recvTypesSrc)
	res, err := p.FullScan(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Changed() == 0 {
		t.Fatal("second scan changed nothing")
	}
	safe := model.EntryID("typescript:src/types.ts:method:ZodType.safeParse:1")
	if links := recvLinks(t, p, run); !hasLink(links, safe) {
		t.Fatalf("after types land, run calls = %v, want link to %s", links, safe)
	}
}
