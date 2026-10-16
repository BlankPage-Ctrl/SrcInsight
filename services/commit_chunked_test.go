package services

import (
	"context"
	"fmt"
	"sort"
	"testing"

	"srcinsight/internal/storage/cold"
)

func TestComputeBudgetFlushIsDynamic(t *testing.T) {
	small := computeResolveBudget(1200, 10, 8)
	large := computeResolveBudget(20000, 200, 8)
	if small.flushThreshold == large.flushThreshold {
		t.Fatalf("flush fixed at %d for both loads", small.flushThreshold)
	}
	if small.workers < 1 || large.workers < 1 {
		t.Fatalf("workers must be positive: %+v %+v", small, large)
	}
	if large.refsPerWorker <= 0 {
		t.Fatalf("refsPerWorker must be positive: %+v", large)
	}
	t.Setenv("SRCINSIGHT_COMMIT_PRESET", "extreme")
	extreme := computeResolveBudget(20000, 200, 8)
	if extreme.flushThreshold <= large.flushThreshold {
		t.Fatalf("extreme flush=%d should exceed balanced flush=%d", extreme.flushThreshold, large.flushThreshold)
	}
	t.Setenv("SRCINSIGHT_COMMIT_PRESET", "")
	t.Setenv("SRCINSIGHT_COMMIT_FLUSH_REFS", "777")
	if got := computeResolveBudget(20000, 200, 8); got.flushThreshold != 777 {
		t.Fatalf("flush override = %d, want 777", got.flushThreshold)
	}
}

func TestShardBundlesCoversAll(t *testing.T) {
	bundles := make([]cold.OriginBundle, 10)
	for i := range bundles {
		for j := 0; j < (i+1)*10; j++ {
			bundles[i].LooseEnds = append(bundles[i].LooseEnds, cold.LooseEndRow{Name: fmt.Sprint(j)})
		}
	}
	shards := shardBundles(bundles, 4)
	seen := map[int]int{}
	for _, s := range shards {
		for _, idx := range s {
			seen[idx]++
		}
	}
	if len(seen) != len(bundles) {
		t.Fatalf("covered %d of %d bundles", len(seen), len(bundles))
	}
	for idx, n := range seen {
		if n != 1 {
			t.Fatalf("bundle %d assigned %d times", idx, n)
		}
	}
}

func chainFiles(n, calls int) map[string]string {
	files := make(map[string]string, n)
	for i := range n {
		next := (i + 1) % n
		body := ""
		for c := range calls {
			body += fmt.Sprintf("  total += f%d(x + %d);\n", next, c)
		}
		for c := range 5 {
			body += fmt.Sprintf("  total += unknown%d(x);\n", c)
		}
		files[fmt.Sprintf("src/a%d.ts", i)] = fmt.Sprintf(`import { f%d } from './a%d';

export function f%d(x: number): number {
  let total = 0;
%s  return total;
}
`, next, next, i, body)
	}
	return files
}

func vaultSignature(t *testing.T, p *Pipeline) []string {
	t.Helper()
	ctx := context.Background()
	var sig []string
	links, err := p.Vault().FetchAllLinks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range links {
		sig = append(sig, fmt.Sprintf("L %s %s %s %d %d", l.Origin, l.Target, l.Kind, l.SiteStart, l.SiteEnd))
	}
	loose, err := p.Vault().FetchAllLooseEnds(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range loose {
		sig = append(sig, fmt.Sprintf("R %s %s %s %s", l.From, l.Name, l.Kind, l.Scope))
	}
	sort.Strings(sig)
	return sig
}

func TestChunkedCommitMatchesSequential(t *testing.T) {
	ctx := context.Background()
	files := chainFiles(50, 25)

	pChunk, _ := testPipeline(t, files)
	defer pChunk.Close()
	if _, err := pChunk.FullScan(ctx, true); err != nil {
		t.Fatal(err)
	}
	chunked := vaultSignature(t, pChunk)
	if len(chunked) < 1000 {
		t.Fatalf("fixture too small to trigger chunked path: %d refs", len(chunked))
	}

	t.Setenv("SRCINSIGHT_COMMIT_CHUNKED", "0")
	pSeq, _ := testPipeline(t, files)
	defer pSeq.Close()
	if _, err := pSeq.FullScan(ctx, true); err != nil {
		t.Fatal(err)
	}
	sequential := vaultSignature(t, pSeq)

	if len(chunked) != len(sequential) {
		t.Fatalf("chunked=%d refs sequential=%d refs", len(chunked), len(sequential))
	}
	for i := range chunked {
		if chunked[i] != sequential[i] {
			t.Fatalf("first diff at %d:\nchunked: %s\nsequential: %s", i, chunked[i], sequential[i])
		}
	}
}
