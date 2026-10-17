package cold

import (
	"context"
	"testing"

	"srcinsight/internal/model"
)

func TestTraceStatsComputeAndRoundTrip(t *testing.T) {
	v, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = v.Close() })
	ctx := context.Background()
	rows := []EntryRow{
		{ID: "typescript:src/a/f.ts:function:f:1", Kind: model.KindFunction, Name: "f", Qualified: "f", FilePath: "src/a/f.ts", Dialect: model.LangTypeScript, StartLine: 1, EndLine: 2},
		{ID: "typescript:src/a/g.ts:function:g:1", Kind: model.KindFunction, Name: "g", Qualified: "g", FilePath: "src/a/g.ts", Dialect: model.LangTypeScript, StartLine: 1, EndLine: 2},
		{ID: "typescript:src/b/h.ts:function:h:1", Kind: model.KindFunction, Name: "h", Qualified: "h", FilePath: "src/b/h.ts", Dialect: model.LangTypeScript, StartLine: 1, EndLine: 2},
	}
	if _, err := v.CatalogEntries(ctx, rows); err != nil {
		t.Fatal(err)
	}
	st, err := v.ComputeTraceStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.Entries != 3 || st.Files != 3 || st.Dirs != 2 {
		t.Fatalf("stats = %+v, want entries=3 files=3 dirs=2", st)
	}
	if err := v.PutTraceStats(ctx, st); err != nil {
		t.Fatal(err)
	}
	got, found, err := v.GetTraceStats(ctx)
	if err != nil || !found {
		t.Fatalf("get = %+v, %v, %v", got, found, err)
	}
	if got != st {
		t.Fatalf("roundtrip = %+v, want %+v", got, st)
	}
	// Malformed values read as absent, never as an error.
	if err := v.PutMeta(ctx, MetaTraceStats, "{not json"); err != nil {
		t.Fatal(err)
	}
	if _, found, err := v.GetTraceStats(ctx); err != nil || found {
		t.Fatalf("malformed = found %v err %v, want false nil", found, err)
	}
}

func TestWipeDropsTraceStats(t *testing.T) {
	v, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = v.Close() })
	ctx := context.Background()
	if err := v.PutTraceStats(ctx, TraceStats{Entries: 1, Files: 1, Dirs: 1}); err != nil {
		t.Fatal(err)
	}
	if err := v.Wipe(ctx); err != nil {
		t.Fatal(err)
	}
	if _, found, err := v.GetTraceStats(ctx); err != nil || found {
		t.Fatalf("after wipe = found %v err %v, want false nil", found, err)
	}
}
