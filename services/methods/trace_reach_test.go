package methods

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"srcinsight/internal/model"
	"srcinsight/internal/storage/cold"
)

// reachVault builds eight "parse" definitions under src/v3 and one under
// src/v4, mirroring dependency-free sibling subtrees (zod v3 vs v4).
func reachVault(t *testing.T) *cold.Vault {
	t.Helper()
	vault, err := cold.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = vault.Close() })
	ctx := context.Background()
	var rows []cold.EntryRow
	var files []string
	for i := 0; i < 8; i++ {
		f := fmt.Sprintf("src/v3/f%d.ts", i)
		files = append(files, f)
		rows = append(rows, cold.EntryRow{
			ID: model.EntryID(fmt.Sprintf("typescript:%s:function:parse:1", f)), Kind: model.KindFunction,
			Name: "parse", Qualified: "parse", FilePath: f, Dialect: model.LangTypeScript,
			StartLine: 1, EndLine: 2, Signature: "function parse()",
		})
	}
	v4 := "src/v4/core/parse.ts"
	files = append(files, v4)
	rows = append(rows, cold.EntryRow{
		ID: model.EntryID(fmt.Sprintf("typescript:%s:function:parse:1", v4)), Kind: model.KindFunction,
		Name: "parse", Qualified: "parse", FilePath: v4, Dialect: model.LangTypeScript,
		StartLine: 1, EndLine: 2, Signature: "function parse()",
	})
	if _, err := vault.CatalogEntries(ctx, rows); err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if err := vault.NoteOrigin(ctx, cold.OriginRecord{Path: f, Fingerprint: "test", Dialect: model.LangTypeScript}); err != nil {
			t.Fatal(err)
		}
	}
	return vault
}

func TestTraceAnchorReachAcrossSubtrees(t *testing.T) {
	rep, err := Trace(context.Background(), reachVault(t), TraceAsk{Query: "parse"})
	if err != nil {
		t.Fatal(err)
	}
	hits := rep.Anchors["parse"]
	if len(hits) != traceMaxPerToken {
		t.Fatalf("anchors = %d, want %d", len(hits), traceMaxPerToken)
	}
	found := false
	for _, h := range hits {
		if strings.HasPrefix(h.FilePath, "src/v4/") {
			found = true
		}
	}
	if !found {
		t.Fatalf("anchors = %+v, want a src/v4 hit", hits)
	}
}

func TestSpreadByBucketSpreadsAndRefills(t *testing.T) {
	row := func(file string) cold.ScoredEntryRow {
		return cold.ScoredEntryRow{EntryRow: cold.EntryRow{FilePath: file, Name: "parse"}}
	}
	var rows []cold.ScoredEntryRow
	for i := 0; i < 10; i++ {
		rows = append(rows, row(fmt.Sprintf("src/a/f%d.ts", i)))
	}
	for i := 0; i < 10; i++ {
		rows = append(rows, row(fmt.Sprintf("src/b/f%d.ts", i)))
	}
	out := spreadByBucket(rows, 6)
	if len(out) != 6 {
		t.Fatalf("len = %d, want 6", len(out))
	}
	var a, b int
	for _, r := range out {
		if strings.HasPrefix(r.FilePath, "src/a/") {
			a++
		}
		if strings.HasPrefix(r.FilePath, "src/b/") {
			b++
		}
	}
	if a == 0 || b == 0 {
		t.Fatalf("spread a=%d b=%d, want both present", a, b)
	}

	// A single folder still fills to the cap via the global refill.
	single := rows[:0:0]
	for i := 0; i < 10; i++ {
		single = append(single, row(fmt.Sprintf("src/a/f%d.ts", i)))
	}
	if got := len(spreadByBucket(single, 6)); got != 6 {
		t.Fatalf("single-bucket len = %d, want 6", got)
	}
}

func TestAnchorPerTokenCoverageFirst(t *testing.T) {
	row := func(file string) cold.ScoredEntryRow {
		return cold.ScoredEntryRow{EntryRow: cold.EntryRow{FilePath: file}}
	}
	var wide []cold.ScoredEntryRow
	for i := 0; i < 9; i++ {
		wide = append(wide, row(fmt.Sprintf("src/d%d/f.ts", i)))
	}
	pol := anchorPolicy{search: 48, ceiling: 40}
	if got := anchorPerToken(wide, pol); got != 9 {
		t.Fatalf("coverage cap = %d, want 9", got)
	}
	if got := anchorPerToken(wide[:2], pol); got != traceMaxPerToken {
		t.Fatalf("floor cap = %d, want %d", got, traceMaxPerToken)
	}
	if got := anchorPerToken(wide, anchorPolicy{search: 48, ceiling: 8}); got != 8 {
		t.Fatalf("ceiling cap = %d, want 8", got)
	}
}

func TestNewAnchorPolicy(t *testing.T) {
	if p := newAnchorPolicy(cold.TraceStats{}); p.search != traceMaxPerToken*8 || p.ceiling != traceMaxPerToken {
		t.Fatalf("default policy = %+v", p)
	}
	// Zod-scale vault saturates both bounds.
	if p := newAnchorPolicy(cold.TraceStats{Entries: 20424, Files: 478, Dirs: 44}); p.search != cold.MaxSearchLimit || p.ceiling != traceMaxAnchors {
		t.Fatalf("large policy = %+v", p)
	}
	// Small vault stays at the floors.
	if p := newAnchorPolicy(cold.TraceStats{Entries: 500, Files: 20, Dirs: 8}); p.search != traceMaxPerToken*8 || p.ceiling != 8 {
		t.Fatalf("small policy = %+v", p)
	}
}
