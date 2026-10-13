package cold

import (
	"context"
	"testing"

	"srcinsight/internal/model"
)

func mkBatchEntry(id, file string) EntryRow {
	return EntryRow{ID: model.EntryID(id), Kind: model.KindFunction, Name: id, Qualified: id, FilePath: file, Dialect: model.LangTypeScript, StartLine: 1, EndLine: 2, Signature: id + "()"}
}

func TestReplaceOriginsBatchEquivalence(t *testing.T) {
	ctx := context.Background()

	// Reference vault via per-file ReplaceOrigin.
	ref := openMemory(t)
	seed := []OriginBundle{
		{Origin: OriginRecord{Path: "a.ts", Fingerprint: "h", Dialect: model.LangTypeScript},
			Entries: []EntryRow{mkBatchEntry("a-foo", "a.ts"), mkBatchEntry("a-bar", "a.ts")}},
		{Origin: OriginRecord{Path: "b.ts", Fingerprint: "h", Dialect: model.LangTypeScript},
			Entries: []EntryRow{mkBatchEntry("b-caller", "b.ts")},
			Links:   []LinkRow{{Origin: "b-caller", Target: "a-foo", Kind: model.EdgeCalls}},
			LooseEnds: []LooseEndRow{
				{From: "b-caller", Name: "express.Router", Kind: model.EdgeCalls, Scope: "external:express", FilePath: "b.ts", Dialect: model.LangTypeScript},
			},
			Imports: []ImportRow{{FilePath: "b.ts", LocalName: "x", Source: "./a", Kind: "named"}}},
	}
	for _, b := range seed {
		if err := ref.ReplaceOrigin(ctx, b); err != nil {
			t.Fatal(err)
		}
	}

	// Batch vault via single ReplaceOriginsBatch.
	bat := openMemory(t)
	// Copy bundles (batch mutates EntryCount/CatalogedAt in place).
	var cp []OriginBundle
	for _, b := range seed {
		cp = append(cp, b)
	}
	if err := bat.ReplaceOriginsBatch(ctx, cp); err != nil {
		t.Fatal(err)
	}

	rn, rl, _ := ref.Counts(ctx)
	bn, bl, _ := bat.Counts(ctx)
	if rn != bn || rl != bl {
		t.Fatalf("counts ref=%d/%d batch=%d/%d", rn, rl, bn, bl)
	}
	rlinks, _ := ref.FetchAllLinks(ctx)
	blinks, _ := bat.FetchAllLinks(ctx)
	if len(rlinks) != len(blinks) {
		t.Fatalf("links ref=%+v batch=%+v", rlinks, blinks)
	}
	rloose, _ := ref.FetchAllLooseEnds(ctx)
	bloose, _ := bat.FetchAllLooseEnds(ctx)
	if len(rloose) != len(bloose) || (len(rloose) > 0 && rloose[0] != bloose[0]) {
		t.Fatalf("loose ref=%+v batch=%+v", rloose, bloose)
	}
	rimps, _ := ref.FetchAllImports(ctx)
	bimps, _ := bat.FetchAllImports(ctx)
	if len(rimps) != len(bimps) {
		t.Fatalf("imports ref=%+v batch=%+v", rimps, bimps)
	}
}

func TestBulkLoadFTSRebuild(t *testing.T) {
	ctx := context.Background()
	v := openMemory(t)
	if err := v.BeginBulkLoad(); err != nil {
		t.Fatal(err)
	}
	b := OriginBundle{
		Origin:  OriginRecord{Path: "a.ts", Fingerprint: "h", Dialect: model.LangTypeScript},
		Entries: []EntryRow{mkBatchEntry("searchable-foo", "a.ts")},
	}
	if err := v.ReplaceOriginsBatch(ctx, []OriginBundle{b}); err != nil {
		t.Fatal(err)
	}
	if err := v.EndBulkLoad(); err != nil {
		t.Fatal(err)
	}
	// FTS must serve the bulk-inserted row after rebuild.
	rows, err := v.SearchFTS(ctx, "searchable-foo", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || string(rows[0].ID) != "searchable-foo" {
		t.Fatalf("fts after bulk = %+v", rows)
	}
	var fts int
	if err := v.db.QueryRow(`SELECT COUNT(*) FROM entries_fts`).Scan(&fts); err != nil || fts != 1 {
		t.Fatalf("fts rows = %d, %v", fts, err)
	}
}

func TestCommitBundlesGrouping(t *testing.T) {
	ctx := context.Background()
	v := openMemory(t)
	var bundles []OriginBundle
	for i := 0; i < 5; i++ {
		id := string(rune('a'+i)) + "-fn"
		file := string(rune('a'+i)) + ".ts"
		bundles = append(bundles, OriginBundle{
			Origin:  OriginRecord{Path: file, Fingerprint: "h", Dialect: model.LangTypeScript},
			Entries: []EntryRow{mkBatchEntry(id, file)},
		})
	}
	n, err := v.CommitBundles(ctx, bundles)
	if err != nil {
		t.Fatal(err)
	}
	if n != 5 {
		t.Fatalf("written = %d, want 5", n)
	}
	cn, _, _ := v.Counts(ctx)
	if cn != 5 {
		t.Fatalf("entries = %d, want 5", cn)
	}
}

func TestSetBulkPragmasRoundTrip(t *testing.T) {
	v := openMemory(t)
	if err := v.SetBulkPragmas(true); err != nil {
		t.Fatal(err)
	}
	var sync string
	if err := v.db.QueryRow(`PRAGMA synchronous`).Scan(&sync); err != nil {
		t.Fatal(err)
	}
	if err := v.SetBulkPragmas(false); err != nil {
		t.Fatal(err)
	}
	var sync2 string
	if err := v.db.QueryRow(`PRAGMA synchronous`).Scan(&sync2); err != nil {
		t.Fatal(err)
	}
	if sync == sync2 && sync == "NORMAL" {
		t.Fatal("bulk pragmas did not toggle synchronous")
	}
}
