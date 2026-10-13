package cold

import (
	"context"
	"testing"

	"srcinsight/internal/model"
)

// TestTagsRoundTrip proves convention tags survive catalog to model.
func TestTagsRoundTrip(t *testing.T) {
	ctx := context.Background()
	v := openMemory(t)
	rows := []EntryRow{
		{ID: "typescript:cats.controller.ts:class:CatsController:1", Kind: model.KindClassDef, Name: "CatsController", Qualified: "CatsController", FilePath: "cats.controller.ts", Dialect: model.LangTypeScript, StartLine: 1, EndLine: 10, Signature: "class CatsController", Tags: []string{"controller"}},
		{ID: "typescript:cats.controller.ts:method:CatsController.findAll:1", Kind: model.KindMethod, Name: "findAll", Qualified: "CatsController.findAll", FilePath: "cats.controller.ts", Dialect: model.LangTypeScript, StartLine: 3, EndLine: 5, Signature: "findAll()", Tags: []string{"route:Get"}},
	}
	if _, err := v.CatalogEntries(ctx, rows); err != nil {
		t.Fatal(err)
	}
	got, err := v.FetchEntriesByFile(ctx, "cats.controller.ts")
	if err != nil || len(got) != 2 {
		t.Fatalf("fetch = %+v, %v", got, err)
	}
	byName := map[string][]string{}
	for _, r := range got {
		byName[r.Name] = append([]string(nil), r.Tags...)
	}
	if len(byName["CatsController"]) != 1 || byName["CatsController"][0] != "controller" {
		t.Fatalf("class tags = %v", byName)
	}
	if len(byName["findAll"]) != 1 || byName["findAll"][0] != "route:Get" {
		t.Fatalf("method tags = %v", byName)
	}
	if m := got[0].Model(); len(m.Tags) == 0 {
		t.Fatalf("model tags empty: %+v", got[0])
	}
	byID, found, err := v.FetchEntryByID(ctx, "typescript:cats.controller.ts:method:CatsController.findAll:1")
	if err != nil || !found || len(byID.Tags) != 1 || byID.Tags[0] != "route:Get" {
		t.Fatalf("byID = %+v, %v, %v", byID, found, err)
	}
}

// TestTagsBackCompat proves pre-v13 rows (written without the column)
// still read, with empty tags.
func TestTagsBackCompat(t *testing.T) {
	ctx := context.Background()
	v := openMemory(t)
	_, err := v.db.ExecContext(ctx, `INSERT OR REPLACE INTO entries (id, kind, name, qualified_name, file_path, dialect, start_line, end_line, signature, doc, vessel_kind, vessel_name, vessel_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"typescript:old.ts:function:foo:1", "function", "foo", "foo", "old.ts", "typescript", 1, 2, "foo()", "", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	got, err := v.FetchEntriesByFile(ctx, "old.ts")
	if err != nil || len(got) != 1 || len(got[0].Tags) != 0 {
		t.Fatalf("fetch = %+v, %v", got, err)
	}
}
