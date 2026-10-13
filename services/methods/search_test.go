package methods

import (
	"context"
	"testing"

	"srcinsight/internal/model"
	"srcinsight/internal/storage/cold"
)

// testVault catalogs a fixed entry set plus one tracked origin per file so
// Stats.FilesScanned is predictable.
func testVault(t *testing.T) *cold.Vault {
	t.Helper()
	vault, err := cold.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = vault.Close() })
	ctx := context.Background()
	mk := func(id, name, qualified, file string, kind model.Kind, vessel string) cold.EntryRow {
		r := cold.EntryRow{
			ID: model.EntryID(id), Kind: kind, Name: name, Qualified: qualified,
			FilePath: file, Dialect: model.LangTypeScript, StartLine: 1, EndLine: 2,
		}
		if vessel != "" {
			r.VesselKind = model.ContainerClass
			r.VesselName = vessel
			r.VesselID = model.EntryID("typescript:" + file + ":class:" + vessel + ":1")
		}
		return r
	}
	rows := []cold.EntryRow{
		mk("typescript:src/auth/validator.ts:class:AuthService:1", "AuthService", "AuthService", "src/auth/validator.ts", model.KindClassDef, ""),
		mk("typescript:src/auth/validator.ts:method:AuthService.validateToken:1", "validateToken", "AuthService.validateToken", "src/auth/validator.ts", model.KindMethod, "AuthService"),
		mk("typescript:src/auth/validator.ts:function:validateUser:1", "validateUser", "validateUser", "src/auth/validator.ts", model.KindFunction, ""),
		mk("typescript:src/user/service.ts:method:UserService.getUser:1", "getUser", "UserService.getUser", "src/user/service.ts", model.KindMethod, "UserService"),
	}
	if _, err := vault.CatalogEntries(ctx, rows); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"src/auth/validator.ts", "src/user/service.ts"} {
		if err := vault.NoteOrigin(ctx, cold.OriginRecord{Path: f, Fingerprint: "test", Dialect: model.LangTypeScript}); err != nil {
			t.Fatal(err)
		}
	}
	return vault
}

func searchNames(res model.SearchResult) []string {
	out := make([]string, 0, len(res.Hits))
	for _, h := range res.Hits {
		out = append(out, h.Name)
	}
	return out
}

func assertHitNames(t *testing.T, what string, res model.SearchResult, want []string) {
	t.Helper()
	got := searchNames(res)
	if len(got) != len(want) {
		t.Fatalf("%s = %v, want %v", what, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s = %v, want %v", what, got, want)
		}
	}
}

func TestSearchModes(t *testing.T) {
	ctx := context.Background()
	vault := testVault(t)

	cases := []struct {
		name string
		opts SearchOptions
		want []string
	}{
		{"exact", SearchOptions{Query: "validateToken", Mode: ModeExact}, []string{"validateToken"}},
		{"exact qualified", SearchOptions{Query: "authservice.validatetoken", Mode: ModeExact}, []string{"validateToken"}},
		{"prefix", SearchOptions{Query: "valid", Mode: ModePrefix}, []string{"validateUser", "validateToken"}},
		{"substring", SearchOptions{Query: "lidate", Mode: ModeSubstring}, []string{"validateUser", "validateToken"}},
		{"fts multi-token", SearchOptions{Query: "authservice validate", Mode: ModeFTS}, []string{"validateToken"}},
		{"auto exact hit", SearchOptions{Query: "getUser"}, []string{"getUser"}},
		{"auto falls to prefix", SearchOptions{Query: "valid"}, []string{"validateUser", "validateToken"}},
		{"auto falls to substring", SearchOptions{Query: "lidate"}, []string{"validateUser", "validateToken"}},
		{"auto miss", SearchOptions{Query: "nope"}, []string{}},
	}
	for _, c := range cases {
		res, err := Search(ctx, vault, c.opts)
		if err != nil {
			t.Fatal(err)
		}
		assertHitNames(t, c.name, res, c.want)
		if res.Query != c.opts.Query {
			t.Fatalf("%s query = %q, want %q", c.name, res.Query, c.opts.Query)
		}
	}
}

func TestSearchMultiTokenOR(t *testing.T) {
	ctx := context.Background()
	vault := testVault(t)

	cases := []struct {
		name string
		opts SearchOptions
		want []string
	}{
		// Tokens are unioned: exact hits for both names in one call.
		{"exact or", SearchOptions{Query: "validateToken getUser", Mode: ModeExact}, []string{"validateToken", "getUser"}},
		// Commas/semicolons also separate tokens.
		{"exact or comma", SearchOptions{Query: "validateToken, getUser", Mode: ModeExact}, []string{"validateToken", "getUser"}},
		{"exact or semicolon", SearchOptions{Query: "validateToken;getUser", Mode: ModeExact}, []string{"validateToken", "getUser"}},
		// Duplicates collapse by entry ID.
		{"dedup", SearchOptions{Query: "valid validate", Mode: ModePrefix}, []string{"validateUser", "validateToken"}},
		// Token order wins: first token's hits come first.
		{"token order", SearchOptions{Query: "getUser validateToken", Mode: ModeExact}, []string{"getUser", "validateToken"}},
		// Auto fans out per token: one exact hit plus one prefix hit.
		{"auto or", SearchOptions{Query: "getUser valid"}, []string{"getUser", "validateUser", "validateToken"}},
		// Miss tokens contribute nothing.
		{"partial miss", SearchOptions{Query: "getUser nope", Mode: ModeExact}, []string{"getUser"}},
		// Limit applies to the final union, not per token.
		{"or limit", SearchOptions{Query: "validateToken getUser", Mode: ModeExact, Limit: 1}, []string{"validateToken"}},
		// Explicit FTS keeps whole-query AND semantics.
		{"fts and intact", SearchOptions{Query: "authservice validate", Mode: ModeFTS}, []string{"validateToken"}},
	}
	for _, c := range cases {
		res, err := Search(ctx, vault, c.opts)
		if err != nil {
			t.Fatal(err)
		}
		assertHitNames(t, c.name, res, c.want)
		if res.Query != c.opts.Query {
			t.Fatalf("%s query = %q, want %q", c.name, res.Query, c.opts.Query)
		}
	}

	// OR + file filter narrows the union before the limit cut.
	res, err := Search(ctx, vault, SearchOptions{Query: "validate getUser", Mode: ModePrefix, File: "src/user/service.ts"})
	if err != nil {
		t.Fatal(err)
	}
	assertHitNames(t, "or file filter", res, []string{"getUser"})

	// Blank separators match nothing.
	res, err = Search(ctx, vault, SearchOptions{Query: "  , ; "})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 0 {
		t.Fatalf("blank separators hits = %v, want empty", searchNames(res))
	}
}

func TestSplitQueryTokens(t *testing.T) {
	got := splitQueryTokens("  validateToken, getUser;valid  ")
	want := []string{"validateToken", "getUser", "valid"}
	if len(got) != len(want) {
		t.Fatalf("tokens = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("tokens = %v, want %v", got, want)
		}
	}
	if len(splitQueryTokens("  , ; ")) != 0 {
		t.Fatal("separator-only query must yield zero tokens")
	}
}
func TestSearchFilters(t *testing.T) {
	ctx := context.Background()
	vault := testVault(t)

	res, err := Search(ctx, vault, SearchOptions{Query: "User", Mode: ModeSubstring, File: "src/user/service.ts"})
	if err != nil {
		t.Fatal(err)
	}
	assertHitNames(t, "file filter", res, []string{"getUser"})

	res, err = Search(ctx, vault, SearchOptions{Query: "validate", Mode: ModePrefix, Container: "AuthService"})
	if err != nil {
		t.Fatal(err)
	}
	assertHitNames(t, "container filter (class)", res, []string{"validateToken"})

	res, err = Search(ctx, vault, SearchOptions{Query: "validate", Mode: ModePrefix, Container: "validator.ts"})
	if err != nil {
		t.Fatal(err)
	}
	assertHitNames(t, "container filter (file base)", res, []string{"validateUser", "validateToken"})

	// Filters narrow before the limit cut: a limit of 1 over two
	// same-file hits still returns one hit, not zero.
	res, err = Search(ctx, vault, SearchOptions{Query: "validate", Mode: ModePrefix, File: "src/auth/validator.ts", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	assertHitNames(t, "file filter + limit", res, []string{"validateUser"})
}

func TestSearchLimitsAndEmpty(t *testing.T) {
	ctx := context.Background()
	vault := testVault(t)

	res, err := Search(ctx, vault, SearchOptions{Query: "valid", Mode: ModePrefix, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	assertHitNames(t, "limit 1", res, []string{"validateUser"})

	res, err = Search(ctx, vault, SearchOptions{Query: "valid", Mode: ModePrefix, Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 2 {
		t.Fatalf("capped limit hits = %v, want 2", searchNames(res))
	}

	for _, q := range []string{"", "   "} {
		res, err = Search(ctx, vault, SearchOptions{Query: q})
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Hits) != 0 {
			t.Fatalf("blank query hits = %v, want empty", searchNames(res))
		}
	}

	// Hostile FTS input must not error.
	res, err = Search(ctx, vault, SearchOptions{Query: `"(validate)" AND OR NOT NEAR : *`, Mode: ModeFTS})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 0 {
		t.Fatalf("hostile fts hits = %v, want empty", searchNames(res))
	}

	if _, err := Search(ctx, vault, SearchOptions{Query: "x", Mode: "bogus"}); err == nil {
		t.Fatal("unknown mode must error")
	}
}

func TestSearchStatsAndShape(t *testing.T) {
	ctx := context.Background()
	vault := testVault(t)

	res, err := Search(ctx, vault, SearchOptions{Query: "validate", Mode: ModePrefix})
	if err != nil {
		t.Fatal(err)
	}
	if res.Stats.FunctionsIndexed != 4 {
		t.Fatalf("functionsIndexed = %d, want 4", res.Stats.FunctionsIndexed)
	}
	if res.Stats.FilesScanned != 2 {
		t.Fatalf("filesScanned = %d, want 2", res.Stats.FilesScanned)
	}
	if res.Stats.NodesReturned != len(res.Hits) {
		t.Fatalf("nodesReturned = %d, hits = %d", res.Stats.NodesReturned, len(res.Hits))
	}
	h := res.Hits[0]
	if h.ID == "" || h.Kind == "" || h.FilePath == "" || h.LineRange.StartLine == 0 {
		t.Fatalf("hit missing identity fields: %+v", h)
	}
	if h.Qualified == "" || h.Dialect == "" {
		t.Fatalf("hit missing context fields: %+v", h)
	}
	if h.Mode != "prefix" {
		t.Fatalf("hit mode = %q, want prefix", h.Mode)
	}
	if h.Score != 0 {
		t.Fatalf("prefix hit score = %v, want 0", h.Score)
	}
}

func TestSearchFTSScoresAndShape(t *testing.T) {
	ctx := context.Background()
	vault := testVault(t)

	res, err := Search(ctx, vault, SearchOptions{Query: "authservice validate", Mode: ModeFTS})
	if err != nil {
		t.Fatal(err)
	}
	assertHitNames(t, "fts scored", res, []string{"validateToken"})
	h := res.Hits[0]
	if h.Mode != "fts" {
		t.Fatalf("hit mode = %q, want fts", h.Mode)
	}
	if h.Score >= 0 {
		t.Fatalf("fts hit score = %v, want negative bm25", h.Score)
	}
	if h.Rank != 0 {
		t.Fatalf("hit rank = %d, want 0", h.Rank)
	}
	if h.Qualified != "AuthService.validateToken" {
		t.Fatalf("hit qualified = %q", h.Qualified)
	}
	if h.Vessel == nil || h.Vessel.Name != "AuthService" {
		t.Fatalf("hit vessel = %+v, want AuthService", h.Vessel)
	}
}

func TestValidate(t *testing.T) {
	for _, m := range []SearchMode{"", ModeAuto, ModeExact, ModePrefix, ModeSubstring, ModeFTS} {
		if err := m.Validate(); err != nil {
			t.Fatalf("mode %q must validate: %v", m, err)
		}
	}
	if err := SearchMode("bogus").Validate(); err == nil {
		t.Fatal("bogus mode must not validate")
	}
}
