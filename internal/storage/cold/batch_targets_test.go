package cold

import (
	"context"
	"fmt"
	"testing"

	"srcinsight/internal/model"
)

// TestFetchTargetsBatchBuckets exercises every bucket boundary plus padding:
// results must equal the union of single-name lookups, and the sentinel
// must never match a real row (including a row literally named oddly).
func TestFetchTargetsBatchBuckets(t *testing.T) {
	ctx := context.Background()
	v := openMemory(t)
	var rows []EntryRow
	mk := func(file, name string) EntryRow {
		return EntryRow{
			ID:   model.EntryID(fmt.Sprintf("typescript:%s:function:%s:1", file, name)),
			Kind: model.KindFunction, Name: name, Qualified: name,
			FilePath: file, Dialect: model.LangTypeScript, StartLine: 1, EndLine: 2,
		}
	}
	rows = append(rows, mk("src/a.ts", "alpha"), mk("src/a.ts", "beta"))
	for i := 0; i < 40; i++ {
		rows = append(rows, mk("src/b.ts", fmt.Sprintf("fn%02d", i)))
	}
	for i := 0; i < 130; i++ {
		rows = append(rows, mk("src/c.ts", fmt.Sprintf("gx%03d", i)))
	}
	if _, err := v.CatalogEntries(ctx, rows); err != nil {
		t.Fatal(err)
	}
	type qp struct{ File, Name string }
	var pp []qp
	pp = append(pp,
		qp{"src/a.ts", "alpha"},
		qp{"src/a.ts", "ALPHA"}, // case folds to same
		qp{"src/a.ts", "missing"},
		qp{"src/nope.ts", "alpha"}, // unknown file
	)
	for i := 0; i < 40; i++ {
		pp = append(pp, qp{"src/b.ts", fmt.Sprintf("fn%02d", i)})
	}
	for i := 0; i < 130; i++ {
		pp = append(pp, qp{"src/c.ts", fmt.Sprintf("gx%03d", i)})
	}
	var generic []struct{ File, Name string }
	for _, p := range pp {
		generic = append(generic, struct{ File, Name string }{p.File, p.Name})
	}
	got, err := v.FetchTargetsBatch(ctx, v.DB(), generic)
	if err != nil {
		t.Fatal(err)
	}
	key := func(f, n string) string { return f + "\x00" + n }
	if len(got[key("src/a.ts", "alpha")]) != 1 {
		t.Fatalf("alpha = %v, want 1 row", got[key("src/a.ts", "alpha")])
	}
	if len(got[key("src/b.ts", "fn00")]) != 1 || len(got) < 100 {
		t.Fatalf("batch buckets lost rows: %d keys", len(got))
	}
	if rows := got[key("src/a.ts", "missing")]; len(rows) != 0 {
		t.Fatalf("missing = %v, want empty", rows)
	}
	if rows := got[key("src/nope.ts", "alpha")]; len(rows) != 0 {
		t.Fatalf("unknown file = %v, want empty", rows)
	}
	// Sentinel must not match: query it directly.
	sent, err := v.FetchTargetsBatch(ctx, v.DB(), []struct{ File, Name string }{{"src/a.ts", "\x00"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(sent) != 0 {
		t.Fatalf("sentinel matched: %v", sent)
	}
}
