package cold

import (
	"context"
	"testing"

	"srcinsight/internal/model"
)

func seedSearchEntries(t *testing.T, v *Vault) {
	t.Helper()
	mk := func(id, name, qualified, file string, kind model.Kind) EntryRow {
		return EntryRow{
			ID: model.EntryID(id), Kind: kind, Name: name, Qualified: qualified,
			FilePath: file, Dialect: model.LangTypeScript, StartLine: 1, EndLine: 2,
		}
	}
	rows := []EntryRow{
		mk("typescript:src/auth/validator.ts:class:AuthService:1", "AuthService", "AuthService", "src/auth/validator.ts", model.KindClassDef),
		mk("typescript:src/auth/validator.ts:method:AuthService.validateToken:1", "validateToken", "AuthService.validateToken", "src/auth/validator.ts", model.KindMethod),
		mk("typescript:src/auth/validator.ts:function:validateUser:1", "validateUser", "validateUser", "src/auth/validator.ts", model.KindFunction),
		mk("typescript:src/auth/validator.ts:function:validateXtoken:1", "validateXtoken", "validateXtoken", "src/auth/validator.ts", model.KindFunction),
		mk("typescript:src/user/service.ts:method:UserService.getUser:1", "getUser", "UserService.getUser", "src/user/service.ts", model.KindMethod),
		mk("python:src/util.py:function:validate_token:1", "validate_token", "validate_token", "src/util.py", model.KindFunction),
	}
	if _, err := v.CatalogEntries(context.Background(), rows); err != nil {
		t.Fatal(err)
	}
}

func hitNames(rows []ScoredEntryRow) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Name)
	}
	return out
}

func assertNames(t *testing.T, what string, got []ScoredEntryRow, want []string) {
	t.Helper()
	names := hitNames(got)
	if len(names) != len(want) {
		t.Fatalf("%s = %v, want %v", what, names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("%s = %v, want %v", what, names, want)
		}
	}
}

func TestSearchExact(t *testing.T) {
	ctx := context.Background()
	v := openMemory(t)
	seedSearchEntries(t, v)

	got, err := v.SearchExact(ctx, "validateToken", 0)
	if err != nil {
		t.Fatal(err)
	}
	assertNames(t, "exact name", got, []string{"validateToken"})

	got, err = v.SearchExact(ctx, "VALIDATETOKEN", 0)
	if err != nil {
		t.Fatal(err)
	}
	assertNames(t, "exact case-insensitive", got, []string{"validateToken"})

	got, err = v.SearchExact(ctx, "authservice.validatetoken", 0)
	if err != nil {
		t.Fatal(err)
	}
	assertNames(t, "exact qualified (Sebutan)", got, []string{"validateToken"})

	got, err = v.SearchExact(ctx, "nope", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("exact miss = %v, want empty", hitNames(got))
	}

	got, err = v.SearchExact(ctx, "   ", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("exact empty = %v, want empty", hitNames(got))
	}
}

func TestSearchPrefix(t *testing.T) {
	ctx := context.Background()
	v := openMemory(t)
	seedSearchEntries(t, v)

	got, err := v.SearchPrefix(ctx, "valid", 0)
	if err != nil {
		t.Fatal(err)
	}
	// ID order (BINARY): "python:..." first, then
	// "...:function:validateUser", "...:function:validateXtoken",
	// "...:method:AuthService.validateToken" ('f' < 'm').
	assertNames(t, "prefix", got, []string{"validate_token", "validateUser", "validateXtoken", "validateToken"})

	got, err = v.SearchPrefix(ctx, "AuthService.", 0)
	if err != nil {
		t.Fatal(err)
	}
	assertNames(t, "prefix qualified", got, []string{"validateToken"})

	// "_" must match literally: only validate_token, not validateXtoken.
	got, err = v.SearchPrefix(ctx, "validate_", 0)
	if err != nil {
		t.Fatal(err)
	}
	assertNames(t, "prefix underscore literal", got, []string{"validate_token"})

	// "%" must match literally: nothing is named with a percent sign.
	got, err = v.SearchPrefix(ctx, "valid%", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("prefix percent = %v, want empty", hitNames(got))
	}

	got, err = v.SearchPrefix(ctx, "valid", 2)
	if err != nil {
		t.Fatal(err)
	}
	assertNames(t, "prefix limit", got, []string{"validate_token", "validateUser"})
}

func TestSearchSubstring(t *testing.T) {
	ctx := context.Background()
	v := openMemory(t)
	seedSearchEntries(t, v)

	got, err := v.SearchSubstring(ctx, "lidate", 0)
	if err != nil {
		t.Fatal(err)
	}
	assertNames(t, "substring", got, []string{"validate_token", "validateUser", "validateXtoken", "validateToken"})

	got, err = v.SearchSubstring(ctx, "TOKEN", 0)
	if err != nil {
		t.Fatal(err)
	}
	assertNames(t, "substring case-insensitive", got, []string{"validate_token", "validateXtoken", "validateToken"})

	got, err = v.SearchSubstring(ctx, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("substring empty = %v, want empty", hitNames(got))
	}
}

func TestSearchFTS(t *testing.T) {
	ctx := context.Background()
	v := openMemory(t)
	seedSearchEntries(t, v)

	// Single token prefix: every validate* entry (unicode61 splits
	// validate_token into "validate"+"token"; "validatetoken" and
	// "validateuser" carry the "validate" prefix as one token).
	got, err := v.SearchFTS(ctx, "validate", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Fatalf("fts single = %v, want 4 hits", hitNames(got))
	}

	// Multi-token AND: only the method lives under AuthService with a
	// validate* token.
	got, err = v.SearchFTS(ctx, "authservice validate", 0)
	if err != nil {
		t.Fatal(err)
	}
	assertNames(t, "fts multi-token", got, []string{"validateToken"})

	// Sebutan path with dot: tokens "authservice" + "validatetoken".
	got, err = v.SearchFTS(ctx, "AuthService.validateToken", 0)
	if err != nil {
		t.Fatal(err)
	}
	assertNames(t, "fts qualified", got, []string{"validateToken"})

	// Hostile input must not error: quoted parens match nothing.
	got, err = v.SearchFTS(ctx, `"(validate)" AND OR NOT NEAR : *`, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("fts hostile = %v, want empty", hitNames(got))
	}

	got, err = v.SearchFTS(ctx, "   ", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("fts empty = %v, want empty", hitNames(got))
	}
}

func TestSearchScores(t *testing.T) {
	ctx := context.Background()
	v := openMemory(t)
	seedSearchEntries(t, v)

	got, err := v.SearchFTS(ctx, "validate", 0)
	if err != nil {
		t.Fatal(err)
	}
	for i, r := range got {
		if r.Mode != "fts" {
			t.Fatalf("fts mode = %q, want fts", r.Mode)
		}
		if r.Rank != i {
			t.Fatalf("fts rank = %d, want %d", r.Rank, i)
		}
		if r.Score >= 0 {
			t.Fatalf("fts score = %v, want negative bm25", r.Score)
		}
	}

	exact, err := v.SearchExact(ctx, "validateToken", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(exact) != 1 || exact[0].Score != 0 || exact[0].Mode != "exact" || exact[0].Rank != 0 {
		t.Fatalf("exact scored = %+v, want score 0 mode exact rank 0", exact)
	}
}

func TestSearchFTSColumnWeights(t *testing.T) {
	ctx := context.Background()
	v := openMemory(t)
	mk := func(id, name, qualified, file string, doc string) EntryRow {
		return EntryRow{
			ID: model.EntryID(id), Kind: model.KindFunction, Name: name,
			Qualified: qualified, FilePath: file, Dialect: model.LangTypeScript,
			StartLine: 1, EndLine: 2, Doc: doc,
		}
	}
	rows := []EntryRow{
		mk("typescript:f.ts:function:unrelated:1", "unrelated", "unrelated", "f.ts", "mentions zebra husbandry at length"),
		mk("typescript:f.ts:function:zebra:1", "zebra", "zebra", "f.ts", ""),
	}
	if _, err := v.CatalogEntries(ctx, rows); err != nil {
		t.Fatal(err)
	}
	got, err := v.SearchFTS(ctx, "zebra", 0)
	if err != nil {
		t.Fatal(err)
	}
	assertNames(t, "name weight beats doc weight", got, []string{"zebra", "unrelated"})
	if !(got[0].Score < got[1].Score) {
		t.Fatalf("scores not ordered by weight: %v then %v", got[0].Score, got[1].Score)
	}
}

func TestBuildFTSMatch(t *testing.T) {
	match, ok := buildFTSMatch("auth valid")
	if !ok {
		t.Fatal("want ok")
	}
	want := `{name qualified_name doc} : "auth"* "valid"*`
	if match != want {
		t.Fatalf("match = %q, want %q", match, want)
	}
	if _, ok := buildFTSMatch("  "); ok {
		t.Fatal("blank query must not be ok")
	}
}
