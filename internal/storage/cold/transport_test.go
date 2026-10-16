package cold

import (
	"context"
	"testing"

	"srcinsight/internal/model"
)

func transportVault(t *testing.T) *Vault {
	t.Helper()
	vault, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = vault.Close() })
	return vault
}

func TestTransportRoundTrip(t *testing.T) {
	ctx := context.Background()
	v := transportVault(t)
	handler := EntryRow{ID: "typescript:src/cats.controller.ts:method:CatsController.findOne:1", Kind: model.KindMethod, Name: "findOne", Qualified: "CatsController.findOne", FilePath: "src/cats.controller.ts", Dialect: model.LangTypeScript, StartLine: 3, EndLine: 5}
	caller := EntryRow{ID: "typescript:src/loader.ts:function:load:1", Kind: model.KindFunction, Name: "load", Qualified: "load", FilePath: "src/loader.ts", Dialect: model.LangTypeScript, StartLine: 1, EndLine: 3}
	b := OriginBundle{
		Origin:  OriginRecord{Path: "src/cats.controller.ts", Fingerprint: "a", Dialect: model.LangTypeScript},
		Entries: []EntryRow{handler},
		Endpoints: []TransportEndpointRow{
			{FilePath: "src/cats.controller.ts", Owner: handler.ID, Transport: "http", Method: "GET", Path: "/cats/{param}", Framework: "nestjs", SiteStart: 3},
		},
	}
	if err := v.ReplaceOrigin(ctx, b); err != nil {
		t.Fatal(err)
	}
	b2 := OriginBundle{
		Origin:  OriginRecord{Path: "src/loader.ts", Fingerprint: "b", Dialect: model.LangTypeScript},
		Entries: []EntryRow{caller},
		Calls: []TransportCallRow{
			{FilePath: "src/loader.ts", Owner: caller.ID, Transport: "http", Method: "GET", URL: "/cats/42", Lib: "bare-fetch", SiteStart: 2},
		},
	}
	if err := v.ReplaceOrigin(ctx, b2); err != nil {
		t.Fatal(err)
	}
	eps, err := v.FetchTransportEndpointsByOwner(ctx, handler.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(eps) != 1 || eps[0].Path != "/cats/{param}" {
		t.Fatalf("endpoints = %+v", eps)
	}
	byMethod, err := v.FetchTransportEndpointsByMethod(ctx, "GET", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(byMethod) != 1 {
		t.Fatalf("by method = %+v", byMethod)
	}
	calls, err := v.FetchTransportCallsByOwner(ctx, caller.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0].URL != "/cats/42" {
		t.Fatalf("calls = %+v", calls)
	}
	if ver, err := v.VaultVersion(); err != nil || ver < 17 {
		t.Fatalf("vault version = %d, %v", ver, err)
	}
}

func TestTransportForgetOrigin(t *testing.T) {
	ctx := context.Background()
	v := transportVault(t)
	b := OriginBundle{
		Origin:  OriginRecord{Path: "src/x.ts", Fingerprint: "a", Dialect: model.LangTypeScript},
		Entries: []EntryRow{{ID: "typescript:src/x.ts:function:f:1", Kind: model.KindFunction, Name: "f", Qualified: "f", FilePath: "src/x.ts", Dialect: model.LangTypeScript, StartLine: 1, EndLine: 2}},
		Endpoints: []TransportEndpointRow{
			{FilePath: "src/x.ts", Transport: "http", Method: "GET", Path: "/x", SiteStart: 1},
		},
		Calls: []TransportCallRow{
			{FilePath: "src/x.ts", Owner: "typescript:src/x.ts:function:f:1", Transport: "http", URL: "/y", Lib: "bare-fetch", SiteStart: 1},
		},
	}
	if err := v.ReplaceOrigin(ctx, b); err != nil {
		t.Fatal(err)
	}
	if err := v.ForgetOrigin(ctx, "src/x.ts"); err != nil {
		t.Fatal(err)
	}
	eps, err := v.FetchTransportEndpointsByMethod(ctx, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(eps) != 0 {
		t.Fatalf("endpoints survive forget: %+v", eps)
	}
	calls, err := v.FetchTransportCallsByMethod(ctx, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 0 {
		t.Fatalf("calls survive forget: %+v", calls)
	}
}
