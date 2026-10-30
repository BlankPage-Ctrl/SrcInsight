package services

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"srcinsight/internal/storage/cold"
	"srcinsight/services/resolve"
)

// Chunked commit splits the bulk recipe into count, budget, worker resolve,
// and budget-driven flush stages. Flushes stay single-writer and bundle-atomic
// so link endpoint checks see the same semantics as the sequential path.

// resolveBudget is computed once per commit; workers and flush both follow it.
type resolveBudget struct {
	workers        int
	refsPerWorker  int
	flushThreshold int
}

// countRefs totals Links plus LooseEnds across bundles.
func countRefs(bundles []cold.OriginBundle) int {
	total := 0
	for i := range bundles {
		total += len(bundles[i].Links) + len(bundles[i].LooseEnds)
	}
	return total
}

// computeResolveBudget derives worker count and flush size from total refs.
// flushThreshold is dynamic: it scales with load and preset, never fixed.
func computeResolveBudget(totalRefs, numBundles, maxWorkers int) resolveBudget {
	if maxWorkers < 1 {
		maxWorkers = 1
	}
	preset := strings.ToLower(strings.TrimSpace(os.Getenv("SRCINSIGHT_COMMIT_PRESET")))
	if preset == "extreme" {
		workers := maxWorkers
		if workers > numBundles {
			workers = numBundles
		}
		if workers < 1 {
			workers = 1
		}
		rpw := ceilDiv(totalRefs, workers)
		return resolveBudget{
			workers:        workers,
			refsPerWorker:  rpw,
			flushThreshold: clamp(totalRefs/(workers*2), 1000, 8000),
		}
	}
	workers := clamp(ceilDiv(totalRefs, 1500), 2, maxWorkers)
	if workers > numBundles {
		workers = numBundles
	}
	if workers < 1 {
		workers = 1
	}
	rpw := ceilDiv(totalRefs, workers)
	b := resolveBudget{
		workers:        workers,
		refsPerWorker:  rpw,
		flushThreshold: clamp(ceilDiv(rpw, 4), 250, 2000),
	}
	if raw := strings.TrimSpace(os.Getenv("SRCINSIGHT_COMMIT_FLUSH_REFS")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			b.flushThreshold = n
		}
	}
	if raw := strings.TrimSpace(os.Getenv("SRCINSIGHT_RESOLVE_WORKERS")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			b.workers = n
			if b.workers > numBundles {
				b.workers = numBundles
			}
			b.refsPerWorker = ceilDiv(totalRefs, b.workers)
		}
	}
	return b
}

func ceilDiv(a, b int) int {
	if b <= 0 {
		return a
	}
	return (a + b - 1) / b
}

func clamp(n, lo, hi int) int {
	if n < lo {
		return lo
	}
	if n > hi {
		return hi
	}
	return n
}

// shardBundles groups bundle indices so each worker owns a fair ref share.
// Workers mutate only their own bundles; pending and imports stay read-only.
func shardBundles(bundles []cold.OriginBundle, workers int) [][]int {
	if workers < 1 {
		workers = 1
	}
	if workers > len(bundles) {
		workers = len(bundles)
	}
	order := make([]int, len(bundles))
	for i := range order {
		order[i] = i
	}
	sort.Slice(order, func(a, b int) bool {
		ra := len(bundles[order[a]].Links) + len(bundles[order[a]].LooseEnds)
		rb := len(bundles[order[b]].Links) + len(bundles[order[b]].LooseEnds)
		return ra > rb
	})
	shards := make([][]int, workers)
	load := make([]int, workers)
	for _, idx := range order {
		best := 0
		for w := 1; w < workers; w++ {
			if load[w] < load[best] {
				best = w
			}
		}
		shards[best] = append(shards[best], idx)
		load[best] += len(bundles[idx].Links) + len(bundles[idx].LooseEnds)
	}
	return shards
}

// resolveShardForward runs every forward pass except recv over one worker's
// bundles. Recv needs whole-batch links, so it runs in a later stage.
func (p *Pipeline) resolveShardForward(ctx context.Context, bundles []cold.OriginBundle, idx []int, pending resolve.Pending, bundleImports map[string][]cold.ImportRow, lc *resolve.LookupCache) error {
	sub := make([]cold.OriginBundle, 0, len(idx))
	for _, i := range idx {
		sub = append(sub, bundles[i])
	}
	for k := range sub {
		if err := resolve.ResolveOutgoingCrossWithPending(ctx, p, &sub[k], pending, bundleImports, lc); err != nil {
			return err
		}
	}
	if err := p.langs.ForwardShard(ctx, p, sub, pending, bundleImports, lc); err != nil {
		return err
	}
	copyBack(bundles, idx, sub)
	return nil
}

// copyBack returns worker-local results to their bundle slots.
// Passes that take slices reallocate, so index mapping must be restored.
func copyBack(bundles []cold.OriginBundle, idx []int, sub []cold.OriginBundle) {
	for k, i := range idx {
		bundles[i] = sub[k]
	}
}

// shouldChunkCommit selects the chunked recipe for runs worth parallelizing.
func shouldChunkCommit(bundles []cold.OriginBundle) bool {
	if strings.TrimSpace(os.Getenv("SRCINSIGHT_COMMIT_CHUNKED")) == "0" {
		return false
	}
	if len(bundles) < 8 {
		return false
	}
	return countRefs(bundles) >= 1000
}

// commitBulkChunked resolves with budgeted workers then flushes per threshold.
func (p *Pipeline) commitBulkChunked(ctx context.Context, bundles []OriginBundleAlias, fresh bool) error {
	total := countRefs(bundles)
	budget := computeResolveBudget(total, len(bundles), p.pool.Size())
	if os.Getenv("SRCINSIGHT_RESOLVE_TIMINGS") == "1" {
		fmt.Fprintf(os.Stderr, "[budget] refs=%d workers=%d refsPerWorker=%d flush=%d\n",
			total, budget.workers, budget.refsPerWorker, budget.flushThreshold)
	}
	pending := make(resolve.Pending, len(bundles))
	bundleImports := make(map[string][]cold.ImportRow, len(bundles))
	for _, b := range bundles {
		m := make(map[string][]cold.EntryRow)
		for _, e := range b.Entries {
			ln := strings.ToLower(e.Name)
			m[ln] = append(m[ln], e)
		}
		pending[b.Origin.Path] = m
		bundleImports[b.Origin.Path] = b.Imports
	}
	shared := resolve.NewLookupCache()
	known, err := resolve.KnownFiles(ctx, p, bundles)
	if err != nil {
		return err
	}
	shared.SetKnown(known)
	if err := resolve.PrimeLookup(ctx, p, bundles, bundleImports, pending, shared); err != nil {
		return err
	}
	shards := shardBundles(bundles, budget.workers)
	errs := make([]error, len(shards))
	resolveStart := time.Now()
	runJobs(ctx, len(shards), len(shards), func(w int) {
		errs[w] = p.resolveShardForward(ctx, bundles, shards[w], pending, bundleImports, shared.Clone())
	})
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	if p.langs.HasRecvShard() {
		for w := range errs {
			errs[w] = nil
		}
		runJobs(ctx, len(shards), len(shards), func(w int) {
			errs[w] = p.langs.RecvShard(ctx, p, bundles, shards[w], pending, shared.Clone())
		})
		if err := ctx.Err(); err != nil {
			return err
		}
		for _, err := range errs {
			if err != nil {
				return err
			}
		}
	}
	if os.Getenv("SRCINSIGHT_RESOLVE_TIMINGS") == "1" {
		fmt.Fprintf(os.Stderr, "[chunked] resolve wall=%s flush starts\n", time.Since(resolveStart).Round(time.Millisecond))
	}
	return p.flushChunked(ctx, bundles, fresh, budget)
}

// flushChunked writes bundle-atomic groups; each group holds flushThreshold refs.
func (p *Pipeline) flushChunked(ctx context.Context, bundles []OriginBundleAlias, fresh bool, budget resolveBudget) error {
	if fresh {
		return p.flushChunkedFresh(ctx, bundles)
	}
	useValve := shouldUseWalValve(p.vault)
	var valve *cold.WalValve
	if useValve {
		valve = cold.NewWalValve(p.vault)
		valve.Start()
		defer valve.Stop()
	}
	if err := p.vault.BeginBulkLoad(); err != nil {
		return err
	}
	if err := p.vault.SetBulkPragmas(true); err != nil {
		_ = p.vault.EndBulkLoad()
		return err
	}
	commitErr := p.writeFlushGroups(ctx, bundles, budget, valve)
	if valve != nil {
		_ = valve.FoldNow()
	}
	restoreErr := p.vault.SetBulkPragmas(false)
	endErr := p.vault.EndBulkLoad()
	if valve != nil {
		valve.Drain()
	}
	if commitErr != nil {
		return commitErr
	}
	if restoreErr != nil {
		return restoreErr
	}
	return endErr
}

// writeFlushGroups commits one group per flushThreshold refs, capped at 100 files.
func (p *Pipeline) writeFlushGroups(ctx context.Context, bundles []OriginBundleAlias, budget resolveBudget, valve *cold.WalValve) error {
	const maxGroupFiles = 100
	start, acc := 0, 0
	flush := func(end int) error {
		if valve != nil {
			if ch := valve.Backpressure(); ch != nil {
				select {
				case <-ch:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		}
		return p.vault.ReplaceOriginsBatch(ctx, bundles[start:end])
	}
	for i := range bundles {
		if err := ctx.Err(); err != nil {
			return err
		}
		acc += len(bundles[i].Links)
		if acc >= budget.flushThreshold || i == len(bundles)-1 || i-start+1 >= maxGroupFiles {
			if err := flush(i + 1); err != nil {
				return err
			}
			start, acc = i+1, 0
		}
	}
	return nil
}

// flushChunkedFresh keeps the fresh recipe: all entries visible before links.
func (p *Pipeline) flushChunkedFresh(ctx context.Context, bundles []OriginBundleAlias) error {
	useValve := shouldUseWalValve(p.vault)
	var valve *cold.WalValve
	if useValve {
		valve = cold.NewWalValve(p.vault)
		valve.Start()
		defer valve.Stop()
	}
	if err := p.vault.BeginBulkLoad(); err != nil {
		return err
	}
	useParseDefer := totalEntries(bundles) >= 100000
	if useParseDefer {
		if err := p.vault.BeginBulkParseLoad(); err != nil {
			_ = p.vault.EndBulkLoad()
			return err
		}
	}
	if err := p.vault.SetBulkPragmas(true); err != nil {
		_ = p.vault.EndBulkLoad()
		if useParseDefer {
			_ = p.vault.EndBulkParseLoad()
		}
		return err
	}
	// Endpoint checks already ran in Go and orphans are filtered at insert,
	// so per-row FK probes are redundant here. Replace paths keep FKs on
	// because clearOrigin relies on cascade deletes.
	if err := p.vault.DrainPragma(`PRAGMA foreign_keys = OFF`); err != nil {
		return err
	}
	commitErr := p.vault.InsertOriginsBatch(ctx, bundles)
	if valve != nil {
		_ = valve.FoldNow()
	}
	_ = p.vault.DrainPragma(`PRAGMA foreign_keys = ON`)
	restoreErr := p.vault.SetBulkPragmas(false)
	var endParseErr error
	if useParseDefer {
		endParseErr = p.vault.EndBulkParseLoad()
	}
	endErr := p.vault.EndBulkLoad()
	if valve != nil {
		valve.Drain()
	}
	if commitErr != nil {
		return commitErr
	}
	if restoreErr != nil {
		return restoreErr
	}
	if endParseErr != nil {
		return endParseErr
	}
	return endErr
}
