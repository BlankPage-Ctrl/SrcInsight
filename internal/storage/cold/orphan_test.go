package cold

import (
	"context"
	"testing"

	"srcinsight/internal/model"
)

// A loose end pointing at a skipped (invalid) entry must not reach the DB.
// The insert filters it in Go, so the write succeeds with FKs on.
func TestInsertDropsOrphanLooseEnds(t *testing.T) {
	ctx := context.Background()
	v, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	good := EntryRow{ID: "typescript:f.ts:function:g:1", Kind: model.KindFunction,
		Name: "g", Qualified: "g", FilePath: "f.ts", Dialect: model.LangTypeScript}
	bad := EntryRow{ID: "typescript:f.ts:function::2", Kind: "",
		Name: "h", Qualified: "h", FilePath: "f.ts", Dialect: model.LangTypeScript}
	bundles := []OriginBundle{{
		Origin:  OriginRecord{Path: "f.ts", Fingerprint: "h", Dialect: model.LangTypeScript},
		Entries: []EntryRow{good, bad},
		LooseEnds: []LooseEndRow{
			{From: good.ID, Name: "ext", Kind: model.EdgeCalls, Scope: "external:m", FilePath: "f.ts", Dialect: model.LangTypeScript},
			{From: bad.ID, Name: "orphan", Kind: model.EdgeCalls, Scope: "external:m", FilePath: "f.ts", Dialect: model.LangTypeScript},
		},
	}}
	if err := v.InsertOriginsBatch(ctx, bundles); err != nil {
		t.Fatalf("orphan loose end should be filtered, not fail: %v", err)
	}
	loose, err := v.FetchLooseEndsByFile(ctx, "f.ts")
	if err != nil {
		t.Fatal(err)
	}
	if len(loose) != 1 || loose[0].Name != "ext" {
		t.Fatalf("loose = %+v, want only ext", loose)
	}
}
