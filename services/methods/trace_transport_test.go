package methods

import (
	"context"
	"testing"

	"srcinsight/internal/model"
	"srcinsight/internal/storage/cold"
)

// traceTransportVault builds caller load ->(http)-> handler findOne with no
// calls link, so only transport synthesis can connect them.
func traceTransportVault(t *testing.T) *cold.Vault {
	t.Helper()
	vault, err := cold.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = vault.Close() })
	ctx := context.Background()
	handler := cold.EntryRow{ID: "typescript:src/cats.controller.ts:method:CatsController.findOne:1", Kind: model.KindMethod, Name: "findOne", Qualified: "CatsController.findOne", FilePath: "src/cats.controller.ts", Dialect: model.LangTypeScript, StartLine: 3, EndLine: 5}
	caller := cold.EntryRow{ID: "typescript:src/loader.ts:function:load:1", Kind: model.KindFunction, Name: "load", Qualified: "load", FilePath: "src/loader.ts", Dialect: model.LangTypeScript, StartLine: 1, EndLine: 3}
	if err := vault.ReplaceOrigin(ctx, cold.OriginBundle{
		Origin:  cold.OriginRecord{Path: "src/cats.controller.ts", Fingerprint: "a", Dialect: model.LangTypeScript},
		Entries: []cold.EntryRow{handler},
		Endpoints: []cold.TransportEndpointRow{
			{FilePath: "src/cats.controller.ts", Owner: handler.ID, Transport: "http", Method: "GET", Path: "/cats/{param}", Framework: "nestjs", SiteStart: 3},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := vault.ReplaceOrigin(ctx, cold.OriginBundle{
		Origin:  cold.OriginRecord{Path: "src/loader.ts", Fingerprint: "b", Dialect: model.LangTypeScript},
		Entries: []cold.EntryRow{caller},
		Calls: []cold.TransportCallRow{
			{FilePath: "src/loader.ts", Owner: caller.ID, Transport: "http", Method: "", URL: "/cats/42", Lib: "bare-fetch", SiteStart: 2},
		},
	}); err != nil {
		t.Fatal(err)
	}
	return vault
}

func hasEdge(edges []model.Edge, from, to model.EntryID, kind string) bool {
	for _, e := range edges {
		if e.From == from && e.To == to && e.Kind == kind {
			return true
		}
	}
	return false
}

func TestTraceNoTransportHintsUnchanged(t *testing.T) {
	rep, err := Trace(context.Background(), traceVault(t), TraceAsk{Query: "alpha gamma"})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range rep.Edges {
		if e.Kind == model.EdgeTransport {
			t.Fatalf("edges = %+v, want no transport edges without hints", rep.Edges)
		}
	}
}

func TestTraceCrossTransportUndirected(t *testing.T) {
	rep, err := Trace(context.Background(), traceTransportVault(t), TraceAsk{Query: "load findOne"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Trails) != 1 || len(rep.Trails[0].Steps) != 2 {
		t.Fatalf("trails = %+v, want one 2-step trail", rep.Trails)
	}
	from := model.EntryID("typescript:src/loader.ts:function:load:1")
	to := model.EntryID("typescript:src/cats.controller.ts:method:CatsController.findOne:1")
	if rep.Trails[0].Steps[0] != from || rep.Trails[0].Steps[1] != to {
		t.Fatalf("steps = %v, want load->findOne", rep.Trails[0].Steps)
	}
	if !hasEdge(rep.Edges, from, to, model.EdgeTransport) {
		t.Fatalf("edges = %+v, want transport edge load->findOne", rep.Edges)
	}
}

func TestTraceCrossTransportDirected(t *testing.T) {
	rep, err := Trace(context.Background(), traceTransportVault(t), TraceAsk{From: "load", To: "findOne"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Trails) != 1 || len(rep.Trails[0].Steps) != 2 {
		t.Fatalf("trails = %+v, want one 2-step trail", rep.Trails)
	}
}
