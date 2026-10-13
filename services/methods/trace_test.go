package methods

import (
	"context"
	"encoding/json"
	"testing"

	"srcinsight/internal/model"
	"srcinsight/internal/storage/cold"
	"srcinsight/transport/stdio"
)

// traceVault builds alpha=>beta=>gamma plus one loose end and one import.
func traceVault(t *testing.T) *cold.Vault {
	t.Helper()
	vault, err := cold.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = vault.Close() })
	ctx := context.Background()
	rows := []cold.EntryRow{
		{ID: "typescript:src/a.ts:function:alpha:1", Kind: model.KindFunction, Name: "alpha", Qualified: "alpha", FilePath: "src/a.ts", Dialect: model.LangTypeScript, StartLine: 1, EndLine: 5, Signature: "function alpha()"},
		{ID: "typescript:src/a.ts:function:beta:1", Kind: model.KindFunction, Name: "beta", Qualified: "beta", FilePath: "src/a.ts", Dialect: model.LangTypeScript, StartLine: 7, EndLine: 11, Signature: "function beta()"},
		{ID: "typescript:src/b.ts:function:gamma:1", Kind: model.KindFunction, Name: "gamma", Qualified: "gamma", FilePath: "src/b.ts", Dialect: model.LangTypeScript, StartLine: 1, EndLine: 5, Signature: "function gamma()"},
		{ID: "typescript:src/b.ts:function:solo:1", Kind: model.KindFunction, Name: "solo", Qualified: "solo", FilePath: "src/b.ts", Dialect: model.LangTypeScript, StartLine: 7, EndLine: 9, Signature: "function solo()"},
	}
	if _, err := vault.CatalogEntries(ctx, rows); err != nil {
		t.Fatal(err)
	}
	links := []cold.LinkRow{
		{Origin: "typescript:src/a.ts:function:alpha:1", Target: "typescript:src/a.ts:function:beta:1", Kind: model.EdgeCalls},
		{Origin: "typescript:src/a.ts:function:beta:1", Target: "typescript:src/b.ts:function:gamma:1", Kind: model.EdgeCalls},
	}
	if _, err := vault.CatalogLinks(ctx, links); err != nil {
		t.Fatal(err)
	}
	if err := vault.CatalogLooseEnds(ctx, []cold.LooseEndRow{
		{From: "typescript:src/b.ts:function:gamma:1", Name: "log", Kind: model.EdgeCalls, Scope: model.ScopeRuntime, FilePath: "src/b.ts", Dialect: model.LangTypeScript},
	}); err != nil {
		t.Fatal(err)
	}
	if err := vault.CatalogImports(ctx, []cold.ImportRow{
		{FilePath: "src/b.ts", LocalName: "beta", Source: "./a", Kind: "named"},
	}); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"src/a.ts", "src/b.ts"} {
		if err := vault.NoteOrigin(ctx, cold.OriginRecord{Path: f, Fingerprint: "test", Dialect: model.LangTypeScript}); err != nil {
			t.Fatal(err)
		}
	}
	return vault
}

func TestTraceUndirected(t *testing.T) {
	rep, err := Trace(context.Background(), traceVault(t), TraceAsk{Query: "alpha gamma"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Tokens) != 2 {
		t.Fatalf("tokens = %v, want 2", rep.Tokens)
	}
	if len(rep.Trails) != 1 {
		t.Fatalf("trails = %d, want 1", len(rep.Trails))
	}
	got := rep.Trails[0].Steps
	want := []model.EntryID{
		"typescript:src/a.ts:function:alpha:1",
		"typescript:src/a.ts:function:beta:1",
		"typescript:src/b.ts:function:gamma:1",
	}
	if len(got) != len(want) {
		t.Fatalf("steps = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("steps = %v, want %v", got, want)
		}
	}
	if len(rep.Nodes) != 3 {
		t.Fatalf("nodes = %d, want 3 (solo excluded)", len(rep.Nodes))
	}
	if len(rep.Edges) != 2 {
		t.Fatalf("edges = %v, want 2", rep.Edges)
	}
	// Per-node relations hydrated for downstream renderers.
	var alpha *model.Function
	for i := range rep.Nodes {
		if rep.Nodes[i].Name == "alpha" {
			alpha = &rep.Nodes[i]
		}
	}
	if alpha == nil || len(alpha.Calls) != 1 || alpha.Calls[0].Name != "beta" {
		t.Fatalf("alpha.calls = %+v, want [beta]", alpha)
	}
	if len(rep.Loose["typescript:src/b.ts:function:gamma:1"]) != 1 {
		t.Fatalf("loose = %v, want gamma's log", rep.Loose)
	}
	if len(rep.Imports["src/b.ts"]) != 1 {
		t.Fatalf("imports = %v, want b.ts import", rep.Imports)
	}
	if rep.Stats.NodesReturned != 3 || rep.Stats.EdgesReturned != 2 {
		t.Fatalf("stats = %+v, want 3/2", rep.Stats)
	}
}

func TestTraceDirected(t *testing.T) {
	rep, err := Trace(context.Background(), traceVault(t), TraceAsk{From: "alpha", To: "gamma"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Trails) != 1 || len(rep.Trails[0].Steps) != 3 {
		t.Fatalf("directed trails = %+v, want one 3-step trail", rep.Trails)
	}
	if rep.Trails[0].Steps[0] != "typescript:src/a.ts:function:alpha:1" ||
		rep.Trails[0].Steps[2] != "typescript:src/b.ts:function:gamma:1" {
		t.Fatalf("directed steps = %v", rep.Trails[0].Steps)
	}
}

func TestTraceBlankAndInvalid(t *testing.T) {
	vault := traceVault(t)
	rep, err := Trace(context.Background(), vault, TraceAsk{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Trails) != 0 || len(rep.Nodes) != 0 {
		t.Fatalf("blank ask = %+v, want empty", rep)
	}
	if _, err := Trace(context.Background(), vault, TraceAsk{From: "alpha"}); err == nil {
		t.Fatal("from without to must error")
	}
	if _, err := Trace(context.Background(), vault, TraceAsk{Kinds: []string{"bogus"}}); err == nil {
		t.Fatal("bogus kind must error")
	}
}

func TestTraceNoPathKeepsAnchors(t *testing.T) {
	rep, err := Trace(context.Background(), traceVault(t), TraceAsk{Query: "alpha solo"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Trails) != 0 {
		t.Fatalf("trails = %+v, want none", rep.Trails)
	}
	if len(rep.Anchors["alpha"]) != 1 || len(rep.Anchors["solo"]) != 1 {
		t.Fatalf("anchors = %v, want both resolved", rep.Anchors)
	}
	// No trail, but alpha's 1-hop neighborhood still pulls beta in.
	if len(rep.Nodes) != 3 {
		t.Fatalf("nodes = %d, want alpha+beta+solo", len(rep.Nodes))
	}
	if len(rep.Edges) != 1 {
		t.Fatalf("edges = %v, want alpha→beta only", rep.Edges)
	}
}

func TestHandleTraceRoundTrip(t *testing.T) {
	svc := New(traceVault(t))
	res, rerr := svc.HandleTrace(context.Background(), json.RawMessage(`{"query":"alpha gamma"}`))
	if rerr != nil {
		t.Fatalf("handler error: %+v", rerr)
	}
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	var rep TraceReport
	if err := json.Unmarshal(raw, &rep); err != nil {
		t.Fatalf("result is not a TraceReport: %v", err)
	}
	if len(rep.Trails) != 1 {
		t.Fatalf("trails = %d, want 1", len(rep.Trails))
	}

	_, rerr = svc.HandleTrace(context.Background(), json.RawMessage(`{"from":"alpha"}`))
	if rerr == nil || rerr.Code != stdio.CodeInvalidParams {
		t.Fatalf("from-without-to = %+v, want -32602", rerr)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, rerr = svc.HandleTrace(ctx, json.RawMessage(`{"query":"alpha"}`))
	if rerr == nil || rerr.Code != stdio.CodeInternalError {
		t.Fatalf("cancelled = %+v, want -32603", rerr)
	}
}
