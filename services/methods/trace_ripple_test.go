package methods

import (
	"context"
	"fmt"
	"testing"

	"srcinsight/internal/model"
	"srcinsight/internal/storage/cold"
)

// rippleVault builds lib <= app <= t plus an outside caller:
// app calls lib, t (a test file) calls app, other calls lib.
func rippleVault(t *testing.T) *cold.Vault {
	t.Helper()
	vault, err := cold.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = vault.Close() })
	ctx := context.Background()
	rows := []cold.EntryRow{
		{ID: "typescript:src/lib.ts:function:lib:1", Kind: model.KindFunction, Name: "lib", Qualified: "lib", FilePath: "src/lib.ts", Dialect: model.LangTypeScript, StartLine: 1, EndLine: 5, Signature: "function lib()"},
		{ID: "typescript:src/app.ts:function:app:1", Kind: model.KindFunction, Name: "app", Qualified: "app", FilePath: "src/app.ts", Dialect: model.LangTypeScript, StartLine: 1, EndLine: 5, Signature: "function app()"},
		{ID: "typescript:src/app.test.ts:function:t:1", Kind: model.KindFunction, Name: "t", Qualified: "t", FilePath: "src/app.test.ts", Dialect: model.LangTypeScript, StartLine: 1, EndLine: 5, Signature: "function t()"},
		{ID: "typescript:src/other.ts:function:other:1", Kind: model.KindFunction, Name: "other", Qualified: "other", FilePath: "src/other.ts", Dialect: model.LangTypeScript, StartLine: 1, EndLine: 5, Signature: "function other()"},
	}
	if _, err := vault.CatalogEntries(ctx, rows); err != nil {
		t.Fatal(err)
	}
	links := []cold.LinkRow{
		{Origin: "typescript:src/app.ts:function:app:1", Target: "typescript:src/lib.ts:function:lib:1", Kind: model.EdgeCalls},
		{Origin: "typescript:src/app.test.ts:function:t:1", Target: "typescript:src/app.ts:function:app:1", Kind: model.EdgeCalls},
		{Origin: "typescript:src/other.ts:function:other:1", Target: "typescript:src/lib.ts:function:lib:1", Kind: model.EdgeCalls},
	}
	if _, err := vault.CatalogLinks(ctx, links); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"src/lib.ts", "src/app.ts", "src/app.test.ts", "src/other.ts"} {
		if err := vault.NoteOrigin(ctx, cold.OriginRecord{Path: f, Fingerprint: "test", Dialect: model.LangTypeScript}); err != nil {
			t.Fatal(err)
		}
	}
	return vault
}

func TestRippleCountsCallersAndTests(t *testing.T) {
	rep, err := Trace(context.Background(), rippleVault(t), TraceAsk{Query: "lib app"})
	if err != nil {
		t.Fatal(err)
	}
	lib := rep.Ripples["typescript:src/lib.ts:function:lib:1"]
	if lib.Callers != 2 {
		t.Fatalf("lib callers = %d, want 2 (app + other)", lib.Callers)
	}
	if len(lib.Files) != 2 || lib.Files[0] != "src/app.ts" || lib.Files[1] != "src/other.ts" {
		t.Fatalf("lib files = %v, want [src/app.ts src/other.ts]", lib.Files)
	}
	// t is two hops out (t => app => lib) and must surface as a test.
	if len(lib.Tests) != 1 || lib.Tests[0] != "src/app.test.ts" {
		t.Fatalf("lib tests = %v, want [src/app.test.ts]", lib.Tests)
	}
	// totals stay exact: two caller files, one test file.
	if lib.FileTotal != 2 || lib.TestTotal != 1 {
		t.Fatalf("lib totals = files %d tests %d, want 2 and 1", lib.FileTotal, lib.TestTotal)
	}
	if lib.Truncated {
		t.Fatal("lib truncated = true, want false on a tiny vault")
	}
	app := rep.Ripples["typescript:src/app.ts:function:app:1"]
	if app.Callers != 1 || len(app.Tests) != 1 {
		t.Fatalf("app ripple = %+v, want 1 caller + 1 test", app)
	}
}

func TestRippleBlankAskHasNone(t *testing.T) {
	rep, err := Trace(context.Background(), traceVault(t), TraceAsk{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Ripples) != 0 {
		t.Fatalf("blank ripples = %v, want none", rep.Ripples)
	}
}

func TestCollectRipplesTruncates(t *testing.T) {
	vault, err := cold.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = vault.Close() })
	ctx := context.Background()
	rows := []cold.EntryRow{
		{ID: "typescript:src/root.ts:function:root:1", Kind: model.KindFunction, Name: "root", Qualified: "root", FilePath: "src/root.ts", Dialect: model.LangTypeScript, StartLine: 1, EndLine: 2, Signature: "function root()"},
	}
	var links []cold.LinkRow
	for i := 0; i < rippleMaxNodes+10; i++ {
		id := model.EntryID(fmt.Sprintf("typescript:src/c%d.ts:function:c:1", i))
		rows = append(rows, cold.EntryRow{ID: id, Kind: model.KindFunction, Name: "c", Qualified: "c", FilePath: fmt.Sprintf("src/c%d.ts", i), Dialect: model.LangTypeScript, StartLine: 1, EndLine: 2, Signature: "function c()"})
		links = append(links, cold.LinkRow{Origin: id, Target: "typescript:src/root.ts:function:root:1", Kind: model.EdgeCalls})
	}
	if _, err := vault.CatalogEntries(ctx, rows); err != nil {
		t.Fatal(err)
	}
	if _, err := vault.CatalogLinks(ctx, links); err != nil {
		t.Fatal(err)
	}
	got, err := collectRipples(ctx, vault, []model.EntryID{"typescript:src/root.ts:function:root:1"})
	if err != nil {
		t.Fatal(err)
	}
	r := got["typescript:src/root.ts:function:root:1"]
	if r.Callers != rippleMaxNodes+10 {
		t.Fatalf("callers = %d, want exact %d despite the cap", r.Callers, rippleMaxNodes+10)
	}
	if !r.Truncated {
		t.Fatal("truncated = false, want true past the node cap")
	}
	if len(r.Files) != rippleMaxFiles {
		t.Fatalf("files = %d, want capped %d", len(r.Files), rippleMaxFiles)
	}
	// The node cap cuts the walk itself: 1 head + 149 callers seen, the
	// rest unvisited. Totals count what was seen; Truncated owns honesty.
	if r.FileTotal != rippleMaxNodes-1 {
		t.Fatalf("fileTotal = %d, want %d (seen before the cap)", r.FileTotal, rippleMaxNodes-1)
	}
}

// TestTraceCarriesTestTitle proves a captured case title rides the vault
// all the way into hydrated trace nodes.
func TestTraceCarriesTestTitle(t *testing.T) {
	vault, err := cold.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = vault.Close() })
	ctx := context.Background()
	rows := []cold.EntryRow{
		{ID: "typescript:src/a.test.ts:arrow:anonymous:1", Kind: model.KindArrow, Name: "anonymous", Qualified: "anonymous", FilePath: "src/a.test.ts", Dialect: model.LangTypeScript, StartLine: 9, EndLine: 12, Signature: "async () =>", TestTitle: "string async parse"},
		{ID: "typescript:src/a.ts:function:lib:1", Kind: model.KindFunction, Name: "lib", Qualified: "lib", FilePath: "src/a.ts", Dialect: model.LangTypeScript, StartLine: 1, EndLine: 3, Signature: "function lib()"},
	}
	if _, err := vault.CatalogEntries(ctx, rows); err != nil {
		t.Fatal(err)
	}
	if _, err := vault.CatalogLinks(ctx, []cold.LinkRow{
		{Origin: "typescript:src/a.test.ts:arrow:anonymous:1", Target: "typescript:src/a.ts:function:lib:1", Kind: model.EdgeCalls},
	}); err != nil {
		t.Fatal(err)
	}
	rep, err := Trace(context.Background(), vault, TraceAsk{Query: "lib"})
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range rep.Nodes {
		if n.Name == "anonymous" && n.TestTitle != "string async parse" {
			t.Fatalf("node title = %q", n.TestTitle)
		}
	}
}
