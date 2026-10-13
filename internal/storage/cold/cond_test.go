package cold

import (
	"context"
	"testing"

	"srcinsight/internal/model"
)

// TestCondRoundTrip proves branch conditions survive catalog to read, for
// both links and loose ends.
func TestCondRoundTrip(t *testing.T) {
	ctx := context.Background()
	v := openMemory(t)
	entries := []EntryRow{
		{ID: "go:a.go:function:flow:1", Kind: model.KindFunction, Name: "flow", Qualified: "flow", FilePath: "a.go", Dialect: model.LangGo, StartLine: 1, EndLine: 10, Signature: "func flow()"},
		{ID: "go:a.go:function:guarded:1", Kind: model.KindFunction, Name: "guarded", Qualified: "guarded", FilePath: "a.go", Dialect: model.LangGo, StartLine: 12, EndLine: 14, Signature: "func guarded()"},
	}
	if _, err := v.CatalogEntries(ctx, entries); err != nil {
		t.Fatal(err)
	}
	links := []LinkRow{
		{Origin: "go:a.go:function:flow:1", Target: "go:a.go:function:guarded:1", Kind: model.EdgeCalls, SiteStart: 3, SiteEnd: 3, Cond: "ok"},
	}
	if _, err := v.CatalogLinks(ctx, links); err != nil {
		t.Fatal(err)
	}
	got, err := v.FetchAllLinks(ctx)
	if err != nil || len(got) != 1 || got[0].Cond != "ok" {
		t.Fatalf("links = %+v, %v", got, err)
	}
	loose := []LooseEndRow{
		{From: "go:a.go:function:flow:1", Name: "missing", Kind: model.EdgeCalls, Scope: model.ScopeUnresolved, FilePath: "a.go", Dialect: model.LangGo, SiteStart: 5, SiteEnd: 5, Cond: "!(ok)"},
	}
	if err := v.CatalogLooseEnds(ctx, loose); err != nil {
		t.Fatal(err)
	}
	gl, err := v.FetchAllLooseEnds(ctx)
	if err != nil || len(gl) != 1 || gl[0].Cond != "!(ok)" {
		t.Fatalf("loose = %+v, %v", gl, err)
	}
	// Neighbor hydration carries cond onto the call ref.
	refs, err := v.FetchNeighbors(ctx, "go:a.go:function:flow:1", []string{model.EdgeCalls}, false)
	if err != nil || len(refs) != 1 || refs[0].Cond != "ok" {
		t.Fatalf("neighbors = %+v, %v", refs, err)
	}
}

// TestCondBackCompat proves pre-v14/v15 rows (written without the column)
// still read, with empty cond.
func TestCondBackCompat(t *testing.T) {
	ctx := context.Background()
	v := openMemory(t)
	if _, err := v.db.ExecContext(ctx, `INSERT OR REPLACE INTO entries (id, kind, name, qualified_name, file_path, dialect, start_line, end_line, signature, doc, vessel_kind, vessel_name, vessel_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"go:old.go:function:flow:1", "function", "flow", "flow", "old.go", "go", 1, 2, "func flow()", "", "", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := v.db.ExecContext(ctx, `INSERT OR REPLACE INTO entries (id, kind, name, qualified_name, file_path, dialect, start_line, end_line, signature, doc, vessel_kind, vessel_name, vessel_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"go:old.go:function:guarded:1", "function", "guarded", "guarded", "old.go", "go", 1, 2, "func guarded()", "", "", "", ""); err != nil {
		t.Fatal(err)
	}
	// Simulate a pre-migration row: explicit column list without cond.
	if _, err := v.db.ExecContext(ctx, `INSERT OR IGNORE INTO links (origin, target, kind, site_start, site_end) VALUES (?, ?, ?, ?, ?)`,
		"go:old.go:function:flow:1", "go:old.go:function:guarded:1", "calls", 1, 1); err != nil {
		t.Fatal(err)
	}
	got, err := v.FetchAllLinks(ctx)
	if err != nil || len(got) != 1 || got[0].Cond != "" {
		t.Fatalf("backcompat links = %+v, %v", got, err)
	}
}
