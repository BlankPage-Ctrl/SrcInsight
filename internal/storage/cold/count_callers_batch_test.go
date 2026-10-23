package cold

import (
	"context"
	"fmt"
	"testing"

	"srcinsight/internal/model"
)

func TestCountCallersBatchCountsOnlyCallsAcrossChunks(t *testing.T) {
	ctx := context.Background()
	v := openMemory(t)

	mk := func(kind model.Kind, file, name string) EntryRow {
		return EntryRow{
			ID:   model.EntryID(fmt.Sprintf("typescript:%s:%s:%s:1", file, kind, name)),
			Kind: kind, Name: name, Qualified: name,
			FilePath: file, Dialect: model.LangTypeScript, StartLine: 1, EndLine: 2,
		}
	}
	var rows []EntryRow
	var links []LinkRow
	var ids []model.EntryID
	for i := 0; i < 405; i++ {
		target := mk(model.KindFunction, fmt.Sprintf("src/t%d.ts", i), fmt.Sprintf("target%03d", i))
		caller := mk(model.KindFunction, fmt.Sprintf("src/c%d.ts", i), fmt.Sprintf("caller%03d", i))
		rows = append(rows, target, caller)
		links = append(links, LinkRow{Origin: caller.ID, Target: target.ID, Kind: model.EdgeCalls})
		ids = append(ids, target.ID)
	}
	// Non-call edges and duplicate callers must not inflate the count.
	extra := mk(model.KindConst, "src/extra.ts", "extra")
	rows = append(rows, extra)
	links = append(links,
		LinkRow{Origin: extra.ID, Target: ids[0], Kind: model.EdgeUses},
		LinkRow{Origin: extra.ID, Target: ids[1], Kind: model.EdgeCalls},
		LinkRow{Origin: extra.ID, Target: ids[1], Kind: model.EdgeCalls},
	)
	if _, err := v.CatalogEntries(ctx, rows); err != nil {
		t.Fatal(err)
	}
	if _, err := v.CatalogLinks(ctx, links); err != nil {
		t.Fatal(err)
	}

	got, err := v.CountCallersBatch(ctx, append(ids, "typescript:missing.ts:function:nope:1"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(ids) {
		t.Fatalf("got %d counts, want %d", len(got), len(ids))
	}
	for i, id := range ids {
		want := 1
		if i == 1 {
			want = 2
		}
		if got[id] != want {
			t.Fatalf("count[%d] = %d, want %d", i, got[id], want)
		}
	}
}
