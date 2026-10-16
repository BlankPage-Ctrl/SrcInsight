package methods

import (
	"context"
	"testing"

	"srcinsight/internal/model"
	"srcinsight/internal/storage/cold"
)

func transportVault(t *testing.T) *cold.Vault {
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

func TestTransportByPath(t *testing.T) {
	rep, err := Transport(context.Background(), transportVault(t), TransportAsk{Method: "GET", Path: "/cats/1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Flows) != 1 {
		t.Fatalf("flows = %+v, want 1", rep.Flows)
	}
	f := rep.Flows[0]
	if f.Score != 0.7 {
		t.Fatalf("score = %v, want 0.7 (method unknown on caller)", f.Score)
	}
	if len(rep.Edges) != 1 || rep.Edges[0].Kind != model.EdgeTransport {
		t.Fatalf("edges = %+v, want one transport edge", rep.Edges)
	}
	if len(rep.Nodes) != 2 {
		t.Fatalf("nodes = %d, want 2", len(rep.Nodes))
	}
}

func TestTransportRequiresPathOrOperation(t *testing.T) {
	if _, err := Transport(context.Background(), transportVault(t), TransportAsk{}); err == nil {
		t.Fatal("expected error for empty ask")
	}
}
