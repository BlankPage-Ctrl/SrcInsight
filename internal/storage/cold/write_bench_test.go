package cold

import (
	"context"
	"testing"

	"srcinsight/internal/model"
)

// Write DB benches — measure actual vault writes on :memory: (WAL off but
// SQL parsing + trigger + index maintenance included). For Go-side alloc only
// see alloc_bench_test.go (mockBundles without DB).

func BenchmarkWrite_ReplaceOriginsBatch_10(b *testing.B) {
	bundles := mockBundles(10, 40, 6, 3, 20)
	v := openMemoryForBench(b)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := v.ReplaceOriginsBatch(ctx, bundles); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkWrite_ReplaceOriginsBatch_50(b *testing.B) {
	bundles := mockBundles(50, 40, 6, 3, 20)
	v := openMemoryForBench(b)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := v.ReplaceOriginsBatch(ctx, bundles); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkWrite_InsertOriginsBatch_50(b *testing.B) {
	bundles := mockBundles(50, 40, 6, 3, 20)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		v := openMemoryForBench(b)
		if err := v.InsertOriginsBatch(ctx, bundles); err != nil {
			b.Fatal(err)
		}
		_ = v.Close()
	}
}

func BenchmarkWrite_InsertOriginsBatch_200(b *testing.B) {
	// Large fresh index ~ Zod size: 200 files * 40 entries = 8000 entries
	bundles := mockBundles(200, 40, 6, 3, 20)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		v, err := Open(":memory:")
		if err != nil {
			b.Fatal(err)
		}
		if err := v.InsertOriginsBatch(ctx, bundles); err != nil {
			b.Fatal(err)
		}
		_ = v.Close()
	}
}

func BenchmarkWrite_ReplaceOrigin_Single(b *testing.B) {
	bundle := mockBundles(1, 40, 6, 3, 20)[0]
	v := openMemoryForBench(b)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := v.ReplaceOrigin(ctx, bundle); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkWrite_CatalogEntries_40(b *testing.B) {
	rows := mockBundles(1, 40, 0, 0, 0)[0].Entries
	v := openMemoryForBench(b)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = v.Wipe(ctx)
		if _, err := v.CatalogEntries(ctx, rows); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkWrite_ExistingIDs_500(b *testing.B) {
	ids := make([]model.EntryID, 500)
	for i := range ids {
		ids[i] = model.EntryID("typescript:src/a.ts:function:fn" + string(rune('0'+i%10)) + ":1")
	}
	// seed DB with those IDs so existence check hits ~50%
	bundles := mockBundles(10, 50, 0, 0, 0)
	v := openMemoryForBench(b)
	ctx := context.Background()
	_ = v.InsertOriginsBatch(ctx, bundles)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := v.ExistingIDs(ctx, v.DB(), ids); err != nil {
			b.Fatal(err)
		}
	}
}
