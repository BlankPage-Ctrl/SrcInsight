package cold

import (
	"context"
	"testing"

	"srcinsight/internal/model"
)

// TestTitleRoundTrip proves a captured case title survives catalog to model.
func TestTitleRoundTrip(t *testing.T) {
	ctx := context.Background()
	v := openMemory(t)
	rows := []EntryRow{
		{ID: "typescript:a.test.ts:arrow:anonymous:1", Kind: model.KindArrow, Name: "anonymous", Qualified: "anonymous", FilePath: "a.test.ts", Dialect: model.LangTypeScript, StartLine: 9, EndLine: 20, Signature: "async () =>", TestTitle: "string async parse"},
	}
	if _, err := v.CatalogEntries(ctx, rows); err != nil {
		t.Fatal(err)
	}
	got, err := v.FetchEntriesByFile(ctx, "a.test.ts")
	if err != nil || len(got) != 1 || got[0].TestTitle != "string async parse" {
		t.Fatalf("fetch = %+v, %v", got, err)
	}
	if got[0].Model().TestTitle != "string async parse" {
		t.Fatalf("model title = %q", got[0].Model().TestTitle)
	}
	byID, found, err := v.FetchEntryByID(ctx, "typescript:a.test.ts:arrow:anonymous:1")
	if err != nil || !found || byID.TestTitle != "string async parse" {
		t.Fatalf("byID = %+v, %v, %v", byID, found, err)
	}
}

// TestTitleBackCompat proves pre-v12 rows (written without the column)
// still read, with an empty title.
func TestTitleBackCompat(t *testing.T) {
	ctx := context.Background()
	v := openMemory(t)
	_, err := v.db.ExecContext(ctx, `INSERT OR REPLACE INTO entries (id, kind, name, qualified_name, file_path, dialect, start_line, end_line, signature, doc, vessel_kind, vessel_name, vessel_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"typescript:old.ts:function:foo:1", "function", "foo", "foo", "old.ts", "typescript", 1, 2, "foo()", "", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	got, err := v.FetchEntriesByFile(ctx, "old.ts")
	if err != nil || len(got) != 1 || got[0].TestTitle != "" {
		t.Fatalf("fetch = %+v, %v", got, err)
	}
}

// TestLedgerAtV13 pins the migration: fresh vaults carry test_title + tags.
func TestLedgerAtV13(t *testing.T) {
	v := openMemory(t)
	ver, err := v.VaultVersion()
	if err != nil || ver != CurrentLedgerVersion || ver < 13 {
		t.Fatalf("version = %d, %v", ver, err)
	}
	for _, col := range []string{"test_title", "tags"} {
		var name string
		err = v.db.QueryRow(`SELECT name FROM pragma_table_info('entries') WHERE name = ?`, col).Scan(&name)
		if err != nil || name != col {
			t.Fatalf("%s column missing: %v", col, err)
		}
	}
}
