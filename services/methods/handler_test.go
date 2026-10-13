package methods

import (
	"context"
	"encoding/json"
	"testing"

	"srcinsight/internal/model"
	"srcinsight/transport/stdio"
)

func handleSearchResult(t *testing.T, res any, rerr *stdio.RPCError) model.SearchResult {
	t.Helper()
	if rerr != nil {
		t.Fatalf("handler error: %+v", rerr)
	}
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	var sr model.SearchResult
	if err := json.Unmarshal(raw, &sr); err != nil {
		t.Fatalf("result is not a SearchResult: %v", err)
	}
	return sr
}

func TestHandleSearchRoundTrip(t *testing.T) {
	svc := New(testVault(t))
	res, rerr := svc.HandleSearch(context.Background(), json.RawMessage(
		`{"query":"validate","mode":"prefix","limit":10}`))
	sr := handleSearchResult(t, res, rerr)
	assertHitNames(t, "handler search", sr, []string{"validateUser", "validateToken"})
	if sr.Query != "validate" {
		t.Fatalf("query = %q", sr.Query)
	}
}

func TestHandleSearchEmptyParams(t *testing.T) {
	svc := New(testVault(t))
	// Missing params entirely behaves like a blank query: empty hits, no error.
	for _, raw := range []json.RawMessage{nil, {}, json.RawMessage(`{}`)} {
		res, rerr := svc.HandleSearch(context.Background(), raw)
		sr := handleSearchResult(t, res, rerr)
		if len(sr.Hits) != 0 {
			t.Fatalf("params %q hits = %v, want empty", raw, searchNames(sr))
		}
	}
}

func TestHandleSearchInvalidParams(t *testing.T) {
	svc := New(testVault(t))

	_, rerr := svc.HandleSearch(context.Background(), json.RawMessage(`{"query":`))
	if rerr == nil || rerr.Code != stdio.CodeInvalidParams {
		t.Fatalf("malformed params = %+v, want -32602", rerr)
	}

	_, rerr = svc.HandleSearch(context.Background(), json.RawMessage(`{"query":"x","mode":"bogus"}`))
	if rerr == nil || rerr.Code != stdio.CodeInvalidParams {
		t.Fatalf("bogus mode = %+v, want -32602", rerr)
	}
}

func TestHandleSearchCancelled(t *testing.T) {
	svc := New(testVault(t))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, rerr := svc.HandleSearch(ctx, json.RawMessage(`{"query":"validate"}`))
	if rerr == nil || rerr.Code != stdio.CodeInternalError {
		t.Fatalf("cancelled = %+v, want -32603", rerr)
	}
}

func TestMustRegister(t *testing.T) {
	srv := stdio.New()
	svc := New(testVault(t))
	MustRegister(srv, "search", svc.HandleSearch)

	defer func() {
		if recover() == nil {
			t.Fatal("registering a reserved name must panic")
		}
	}()
	MustRegister(srv, "ping", svc.HandleSearch)
}
