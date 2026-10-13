package services

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"srcinsight/internal/model"
)

// Ablation gate tests for SRCINSIGHT_RECV. These pin the behavioral
// difference each mode makes on fixtures only recv can resolve:
//   - "0"       : recv links disappear entirely
//   - "forward" : fresh-index links survive (forward pass), but reverse
//     promotion on later-arriving types does not run
// Default (unset) behavior is covered by recv_resolve_test.go.

func TestRecvGateOffDropsRecvLinks(t *testing.T) {
	t.Setenv("SRCINSIGHT_RECV", "0")
	ctx := context.Background()
	p, _ := testPipeline(t, map[string]string{
		"src/types.ts": recvTypesSrc,
		"src/use.ts":   recvDirectSrc,
		"src/core.ts":  recvCoreSrc,
		"src/use2.ts":  recvFactorySrc,
		"src/base.ts":  recvBaseSrc,
		"src/sub.ts":   recvSubSrc,
	})
	defer p.Close()
	if _, err := p.FullScan(ctx, true); err != nil {
		t.Fatal(err)
	}
	run := model.EntryID("typescript:src/use.ts:function:run:1")
	safe := model.EntryID("typescript:src/types.ts:method:ZodType.safeParse:1")
	if links := recvLinks(t, p, run); hasLink(links, safe) {
		t.Fatalf("gate off: run calls = %v, want NO link to %s", links, safe)
	}
	run2 := model.EntryID("typescript:src/use2.ts:function:run2:1")
	if links := recvLinks(t, p, run2); hasLink(links, safe) {
		t.Fatalf("gate off: run2 calls = %v, want NO link to %s", links, safe)
	}
	bar := model.EntryID("typescript:src/sub.ts:method:Sub.bar:1")
	foo := model.EntryID("typescript:src/base.ts:method:Base.foo:1")
	if links := recvLinks(t, p, bar); hasLink(links, foo) {
		t.Fatalf("gate off: bar calls = %v, want NO link to %s", links, foo)
	}
}

func TestRecvGateForwardKeepsFreshLinks(t *testing.T) {
	t.Setenv("SRCINSIGHT_RECV", "forward")
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
		t.Fatalf("gate forward: run calls = %v, want link to %s", links, safe)
	}
}

func TestRecvGateForwardSkipsReverse(t *testing.T) {
	t.Setenv("SRCINSIGHT_RECV", "forward")
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
	// The type file arrives later. Reverse promotion is gated off, so the
	// link must NOT appear without re-parsing the caller (compare
	// TestRecvReverseOnLaterType with gate unset, where it does).
	write("src/types.ts", recvTypesSrc)
	if _, err := p.FullScan(ctx, false); err != nil {
		t.Fatal(err)
	}
	run := model.EntryID("typescript:src/use.ts:function:run:1")
	safe := model.EntryID("typescript:src/types.ts:method:ZodType.safeParse:1")
	if links := recvLinks(t, p, run); hasLink(links, safe) {
		t.Fatalf("gate forward, types later: run calls = %v, want NO link to %s", links, safe)
	}
}
