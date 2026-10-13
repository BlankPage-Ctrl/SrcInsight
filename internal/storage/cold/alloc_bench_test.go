package cold

import (
	"fmt"
	"testing"

	"srcinsight/internal/model"
)

// allocBench creates N mock bundles with realistic sizes for Zod-like workload.
// No DB writes in the alloc-only benches; they isolate Go-side allocations.
func mockBundles(nFiles, entriesPerFile, linksPerFile, importsPerFile, loosePerFile int) []OriginBundle {
	bundles := make([]OriginBundle, 0, nFiles)
	for i := 0; i < nFiles; i++ {
		file := fmt.Sprintf("src/%d.ts", i)
		entries := make([]EntryRow, 0, entriesPerFile)
		for j := 0; j < entriesPerFile; j++ {
			id := model.EntryID(fmt.Sprintf("typescript:%s:function:fn%d:%d", file, j, j))
			entries = append(entries, EntryRow{
				ID: id, Kind: model.KindFunction, Name: fmt.Sprintf("fn%d", j),
				Qualified: fmt.Sprintf("fn%d", j), FilePath: file, Dialect: model.LangTypeScript,
				StartLine: j*10 + 1, EndLine: j*10 + 5, Signature: "fn()", Doc: "doc",
			})
		}
		links := make([]LinkRow, 0, linksPerFile)
		for j := 0; j < linksPerFile && j+1 < len(entries); j++ {
			links = append(links, LinkRow{Origin: entries[j].ID, Target: entries[j+1].ID, Kind: model.EdgeCalls})
		}
		imps := make([]ImportRow, 0, importsPerFile)
		for j := 0; j < importsPerFile; j++ {
			imps = append(imps, ImportRow{FilePath: file, LocalName: fmt.Sprintf("imp%d", j), Source: "./other", Kind: "named"})
		}
		loose := make([]LooseEndRow, 0, loosePerFile)
		for j := 0; j < loosePerFile; j++ {
			loose = append(loose, LooseEndRow{From: entries[0].ID, Name: fmt.Sprintf("ext%d", j), Kind: model.EdgeCalls, Scope: "external:mod", FilePath: file, Dialect: model.LangTypeScript})
		}
		bundles = append(bundles, OriginBundle{
			Origin:  OriginRecord{Path: file, Fingerprint: "h", Dialect: model.LangTypeScript, Bytes: 1000, AlteredAt: 1},
			Entries: entries, Links: links, Imports: imps, LooseEnds: loose,
		})
	}
	return bundles
}

func BenchmarkAlloc_insertEntriesBatchTx(b *testing.B) {
	// Zod-ish: 200 files * ~40 entries = 8000 rows — compare old grow vs fixed preallocate
	bundles := mockBundles(200, 40, 6, 3, 100)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		total := 0
		for _, bb := range bundles {
			total += len(bb.Entries)
		}
		rows := make([]EntryRow, 0, total)
		for _, bb := range bundles {
			for _, r := range bb.Entries {
				if r.Valid() {
					rows = append(rows, r)
				}
			}
		}
		_ = rows
	}
}

func BenchmarkAlloc_linkEndpoints(b *testing.B) {
	bundles := mockBundles(200, 40, 6, 0, 0)
	var all []LinkRow
	for _, bb := range bundles {
		all = append(all, bb.Links...)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = linkEndpoints(all)
	}
}

func BenchmarkAlloc_existingIDs_buildArgs(b *testing.B) {
	ids := make([]model.EntryID, 500)
	for i := range ids {
		ids[i] = model.EntryID(fmt.Sprintf("typescript:src/a.ts:function:fn%d:1", i))
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// mimic existingIDs chunk arg building
		args := make([]any, 0, len(ids))
		for _, id := range ids {
			args = append(args, string(id))
		}
		_ = args
	}
}

func BenchmarkAlloc_insertRestBatchTx_slices(b *testing.B) {
	bundles := mockBundles(200, 40, 6, 3, 100)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		totalImps, totalLoose := 0, 0
		for _, bb := range bundles {
			totalImps += len(bb.Imports)
			totalLoose += len(bb.LooseEnds)
		}
		imps := make([]ImportRow, 0, totalImps)
		loose := make([]LooseEndRow, 0, totalLoose)
		for _, bb := range bundles {
			imps = append(imps, bb.Imports...)
			for _, r := range bb.LooseEnds {
				if r.From == "" || r.Name == "" {
					continue
				}
				loose = append(loose, r)
			}
		}
		_, _ = imps, loose
	}
}

func BenchmarkAlloc_FetchTargetsBatch_grouping(b *testing.B) {
	pairs := make([]struct{ File, Name string }, 1000)
	for i := range pairs {
		pairs[i] = struct{ File, Name string }{File: fmt.Sprintf("src/%d.ts", i%200), Name: fmt.Sprintf("fn%d", i%40)}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		byFile := make(map[string]map[string]struct{}, len(pairs)/4+1)
		for _, p := range pairs {
			if _, ok := byFile[p.File]; !ok {
				byFile[p.File] = make(map[string]struct{}, 4)
			}
			byFile[p.File][p.Name] = struct{}{}
		}
		_ = byFile
	}
}

// openMemoryForBench reuses openMemory helper without *testing.T cleanup noise.
func openMemoryForBench(b *testing.B) *Vault {
	b.Helper()
	v, err := Open(":memory:")
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = v.Close() })
	return v
}
