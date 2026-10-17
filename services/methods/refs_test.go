package methods

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"srcinsight/internal/model"
	"srcinsight/internal/storage/cold"
	"srcinsight/transport/stdio"
)

const (
	refsTarget = "typescript:src/a.ts:function:foo:1"
	refsCaller = "typescript:src/b.ts:function:bar:1"
)

// refsVault holds one resolved call link plus one unresolved same-name ref,
// so refs can show index sites and loose candidates side by side.
func refsVault(t *testing.T) *cold.Vault {
	t.Helper()
	vault, err := cold.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = vault.Close() })
	ctx := context.Background()
	rows := []cold.EntryRow{
		{ID: refsTarget, Kind: model.KindFunction, Name: "foo", Qualified: "foo", FilePath: "src/a.ts", Dialect: model.LangTypeScript, StartLine: 1, EndLine: 5},
		{ID: refsCaller, Kind: model.KindFunction, Name: "bar", Qualified: "bar", FilePath: "src/b.ts", Dialect: model.LangTypeScript, StartLine: 1, EndLine: 9},
	}
	if _, err := vault.CatalogEntries(ctx, rows); err != nil {
		t.Fatal(err)
	}
	if _, err := vault.CatalogLinks(ctx, []cold.LinkRow{
		{Origin: refsCaller, Target: refsTarget, Kind: model.EdgeCalls, SiteStart: 10, SiteEnd: 12, SiteStartCol: 3, SiteEndCol: 6},
	}); err != nil {
		t.Fatal(err)
	}
	if err := vault.CatalogLooseEnds(ctx, []cold.LooseEndRow{
		{From: refsCaller, Name: "foo", Kind: model.EdgeCalls, Scope: model.ScopeInternal, FilePath: "src/b.ts", Dialect: model.LangTypeScript, SiteStart: 20, SiteEnd: 22, SiteStartCol: 7, SiteEndCol: 10},
	}); err != nil {
		t.Fatal(err)
	}
	if err := vault.CatalogImports(ctx, []cold.ImportRow{
		{FilePath: "src/b.ts", LocalName: "foo", Source: "./a", Kind: "named"},
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

// fakeLive returns canned confirmations; the real import pass is covered in
// services, so the method layer only needs the contract.
type fakeLive struct {
	refs []LiveRef
	err  error
}

func (f fakeLive) VerifyRefs(context.Context, cold.EntryRow, []cold.LooseEndRow) ([]LiveRef, error) {
	return f.refs, f.err
}

func TestRefsIndexOnlyWhenLiveOff(t *testing.T) {
	rep, err := Refs(context.Background(), refsVault(t), fakeLive{}, RefsOptions{ID: refsTarget, Live: LiveOff})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Sites) != 1 {
		t.Fatalf("sites = %d, want 1", len(rep.Sites))
	}
	s := rep.Sites[0]
	if s.Source != "index" || s.Caller != refsCaller || s.SiteRange.StartLine != 10 || s.SiteRange.EndLine != 12 {
		t.Fatalf("index site = %+v", s)
	}
	if len(rep.Loose) != 1 || rep.Loose[0].Name != "foo" {
		t.Fatalf("loose = %+v, want one foo", rep.Loose)
	}
}

func TestRefsLiveConfirmsLoose(t *testing.T) {
	live := fakeLive{refs: []LiveRef{{
		Caller:    refsCaller,
		FilePath:  "src/b.ts",
		Kind:      model.EdgeCalls,
		SiteRange: model.Range{StartLine: 20, EndLine: 22, StartCol: 7, EndCol: 10},
	}}}
	rep, err := Refs(context.Background(), refsVault(t), live, RefsOptions{ID: refsTarget})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Sites) != 2 {
		t.Fatalf("sites = %d, want 2 (index + live)", len(rep.Sites))
	}
	var liveSite *RefSite
	for i := range rep.Sites {
		if rep.Sites[i].Source == "live" {
			liveSite = &rep.Sites[i]
		}
	}
	if liveSite == nil {
		t.Fatal("no live site in report")
	}
	if liveSite.CallerName != "bar" {
		t.Fatalf("live caller name = %q, want bar", liveSite.CallerName)
	}
	if len(rep.Loose) != 0 {
		t.Fatalf("loose = %+v, want empty after confirmation", rep.Loose)
	}
}

func TestRefsLooseSurvivesUnconfirmed(t *testing.T) {
	rep, err := Refs(context.Background(), refsVault(t), fakeLive{}, RefsOptions{ID: refsTarget})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Sites) != 1 {
		t.Fatalf("sites = %d, want 1", len(rep.Sites))
	}
	if len(rep.Loose) != 1 || rep.Loose[0].SiteRange.StartLine != 20 {
		t.Fatalf("loose = %+v, want the unconfirmed site", rep.Loose)
	}
}

func TestRefsValidation(t *testing.T) {
	vault := refsVault(t)
	ctx := context.Background()
	if _, err := Refs(ctx, vault, nil, RefsOptions{}); err == nil {
		t.Fatal("missing id must fail")
	}
	if _, err := Refs(ctx, vault, nil, RefsOptions{ID: "bogus"}); err == nil {
		t.Fatal("malformed id must fail")
	}
	if _, err := Refs(ctx, vault, nil, RefsOptions{ID: refsTarget, Kinds: []string{"nope"}}); err == nil {
		t.Fatal("unknown kind must fail")
	}
	if _, err := Refs(ctx, vault, nil, RefsOptions{ID: "typescript:src/a.ts:function:missing:1"}); err == nil {
		t.Fatal("unknown entry must fail")
	}
}

func TestHandleRefsRoundTrip(t *testing.T) {
	svc := New(refsVault(t), WithLive(fakeLive{}))
	res, rerr := svc.HandleRefs(context.Background(), json.RawMessage(`{"id":"`+refsTarget+`","live":false}`))
	if rerr != nil {
		t.Fatalf("handler error: %+v", rerr)
	}
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	var rep RefsReport
	if err := json.Unmarshal(raw, &rep); err != nil {
		t.Fatalf("result is not a RefsReport: %v", err)
	}
	if len(rep.Sites) != 1 || rep.Target.Name != "foo" {
		t.Fatalf("report = %+v", rep)
	}
}

func TestHandleRefsInvalidParams(t *testing.T) {
	svc := New(refsVault(t))
	_, rerr := svc.HandleRefs(context.Background(), json.RawMessage(`{"id":`))
	if rerr == nil || rerr.Code != stdio.CodeInvalidParams {
		t.Fatalf("malformed params = %+v, want -32602", rerr)
	}
	_, rerr = svc.HandleRefs(context.Background(), json.RawMessage(`{"id":"`+refsTarget+`","kinds":["bogus"]}`))
	if rerr == nil || rerr.Code != stdio.CodeInvalidParams {
		t.Fatalf("bogus kind = %+v, want -32602", rerr)
	}
}

func TestHandleRefsNotFound(t *testing.T) {
	svc := New(refsVault(t))
	_, rerr := svc.HandleRefs(context.Background(), json.RawMessage(`{"id":"typescript:src/a.ts:function:missing:1"}`))
	if rerr == nil || rerr.Code != stdio.CodeInvalidParams {
		t.Fatalf("missing entry = %+v, want -32602", rerr)
	}
}

func TestHandleRefsCancelled(t *testing.T) {
	svc := New(refsVault(t))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, rerr := svc.HandleRefs(ctx, json.RawMessage(`{"id":"`+refsTarget+`"}`))
	if rerr == nil || rerr.Code != stdio.CodeInternalError {
		t.Fatalf("cancelled = %+v, want -32603", rerr)
	}
}

func TestLiveModeUnmarshal(t *testing.T) {
	cases := []struct {
		raw  string
		want LiveMode
	}{
		{`"auto"`, LiveAuto},
		{`true`, LiveOn},
		{`false`, LiveOff},
		{`"on"`, LiveOn},
		{`"off"`, LiveOff},
		{`""`, LiveAuto},
	}
	for _, c := range cases {
		var m LiveMode
		if err := json.Unmarshal([]byte(c.raw), &m); err != nil {
			t.Fatalf("unmarshal %s: %v", c.raw, err)
		}
		if m != c.want {
			t.Fatalf("mode %s = %q, want %q", c.raw, m, c.want)
		}
	}
	var bad LiveMode
	if err := json.Unmarshal([]byte(`"maybe"`), &bad); err == nil {
		t.Fatal("unknown mode must fail")
	}
}

func TestRefsFindsReceiverQualifiedLoose(t *testing.T) {
	vault, err := cold.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = vault.Close() })
	ctx := context.Background()
	if _, err := vault.CatalogEntries(ctx, []cold.EntryRow{
		{ID: refsTarget, Kind: model.KindFunction, Name: "foo", Qualified: "foo", FilePath: "src/a.ts", Dialect: model.LangTypeScript, StartLine: 1, EndLine: 5},
		{ID: refsCaller, Kind: model.KindFunction, Name: "bar", Qualified: "bar", FilePath: "src/b.ts", Dialect: model.LangTypeScript, StartLine: 1, EndLine: 9},
	}); err != nil {
		t.Fatal(err)
	}
	if err := vault.CatalogLooseEnds(ctx, []cold.LooseEndRow{
		{From: refsCaller, Name: "svc.foo", Kind: model.EdgeCalls, Scope: model.ScopeInternal, FilePath: "src/b.ts", Dialect: model.LangTypeScript, SiteStart: 4, SiteEnd: 4},
	}); err != nil {
		t.Fatal(err)
	}
	rep, err := Refs(ctx, vault, fakeLive{}, RefsOptions{ID: refsTarget, Live: LiveOff})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Loose) != 1 || rep.Loose[0].Name != "svc.foo" {
		t.Fatalf("loose = %+v, want receiver-qualified svc.foo", rep.Loose)
	}
}

func TestRefsLiveError(t *testing.T) {
	_, err := Refs(context.Background(), refsVault(t), fakeLive{err: errors.New("boom")}, RefsOptions{ID: refsTarget})
	if err == nil {
		t.Fatal("live scanner error must propagate")
	}
}

// TestRefsCollapsesOnePosition covers the index emitting several edges for a
// single expression: nested callers and a call plus a value use must collapse to
// one site, preferring the call.
func TestRefsCollapsesOnePosition(t *testing.T) {
	// Same file: the index emits one expression several times, attributing it to
	// nested enclosing vessels.
	const (
		callerB = "typescript:src/b.ts:function:bar:1"
		callerC = "typescript:src/b.ts:function:inner:1"
	)
	vault, err := cold.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = vault.Close() })
	ctx := context.Background()
	if _, err := vault.CatalogEntries(ctx, []cold.EntryRow{
		{ID: refsTarget, Kind: model.KindFunction, Name: "foo", Qualified: "foo", FilePath: "src/a.ts", Dialect: model.LangTypeScript, StartLine: 1, EndLine: 5},
		{ID: callerB, Kind: model.KindFunction, Name: "bar", Qualified: "bar", FilePath: "src/b.ts", Dialect: model.LangTypeScript, StartLine: 1, EndLine: 9},
		{ID: callerC, Kind: model.KindFunction, Name: "inner", Qualified: "inner", FilePath: "src/b.ts", Dialect: model.LangTypeScript, StartLine: 3, EndLine: 7},
	}); err != nil {
		t.Fatal(err)
	}
	site := func(origin, kind string) cold.LinkRow {
		return cold.LinkRow{Origin: model.EntryID(origin), Target: refsTarget, Kind: kind, SiteStart: 10, SiteEnd: 12, SiteStartCol: 3, SiteEndCol: 6}
	}
	if _, err := vault.CatalogLinks(ctx, []cold.LinkRow{
		site(callerB, model.EdgeCalls),
		site(callerC, model.EdgeCalls),
		site(callerC, model.EdgeUses),
	}); err != nil {
		t.Fatal(err)
	}
	rep, err := Refs(ctx, vault, nil, RefsOptions{ID: refsTarget, Live: LiveOff})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Sites) != 1 {
		t.Fatalf("sites = %d, want 1 collapsed position: %+v", len(rep.Sites), rep.Sites)
	}
	if rep.Sites[0].Kind != model.EdgeCalls {
		t.Fatalf("kept kind = %q, want calls", rep.Sites[0].Kind)
	}
	if rep.Sites[0].Caller != callerC {
		t.Fatalf("kept caller = %q, want innermost caller inner", rep.Sites[0].Caller)
	}
}
