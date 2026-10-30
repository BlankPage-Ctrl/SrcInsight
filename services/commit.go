package services

import (
	"context"
	"os"
	"strconv"
	"strings"

	"srcinsight/internal/storage/cold"
	"srcinsight/services/adapters"
)

// Bulk recipe tuning from calibration: the bulk window rebuilds secondary
// indexes over the whole DB, so it only pays off when the batch is a large
// share of the index. Huge batches always go bulk to keep transactions grouped.
const (
	bulkBatchFiles = 200
	bulkFraction   = 0.25
)

// useBulkRecipe picks the bulk recipe for bundles. An explicit
// SRCINSIGHT_BULK_THRESHOLD keeps the old file-count behavior.
func (p *Pipeline) useBulkRecipe(ctx context.Context, bundles []OriginBundleAlias) bool {
	if raw := strings.TrimSpace(os.Getenv("SRCINSIGHT_BULK_THRESHOLD")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			return len(bundles) >= n
		}
	}
	if len(bundles) >= bulkBatchFiles {
		return true
	}
	batch := totalEntries(bundles)
	if batch == 0 {
		return false
	}
	existing, _, err := p.vault.Counts(ctx)
	if err != nil {
		return true
	}
	return float64(batch)/float64(existing+batch) >= bulkFraction
}

// commitBulk writes bundles with the bulk recipe and restores pragmas on failure.
func (p *Pipeline) commitBulk(ctx context.Context, bundles []OriginBundleAlias, fresh bool) error {
	if shouldChunkCommit(bundles) {
		if err := p.commitBulkChunked(ctx, bundles, fresh); err != nil {
			return err
		}
	} else {
		if err := p.resolveOutgoingBatch(ctx, bundles); err != nil {
			return err
		}
		if err := commitBulkBundles(ctx, p.vault, bundles, fresh); err != nil {
			return err
		}
	}
	if fresh {
		return nil
	}

	if err := p.promoteReverseCross(ctx, bundles); err != nil {
		return err
	}

	if err := p.langs.Reverse(ctx, p, bundles, adapters.ReverseLanguage); err != nil {
		return err
	}
	return p.langs.Reverse(ctx, p, bundles, adapters.ReverseRecv)
}

// OriginBundleAlias is exactly cold.OriginBundle.
type OriginBundleAlias = cold.OriginBundle

// commitBulkBundles runs the vault bulk recipe.
func commitBulkBundles(ctx context.Context, v *cold.Vault, bundles []cold.OriginBundle, fresh bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// Valve bounds WAL growth while autocheckpoint is deferred.
	useValve := shouldUseWalValve(v)
	var valve *cold.WalValve
	if useValve {
		valve = cold.NewWalValve(v)
		valve.Start()
		defer valve.Stop()
	}
	if err := v.BeginBulkLoad(); err != nil {
		return err
	}
	// Rebuild secondary indexes only for large fresh loads.
	useParseDefer := fresh && totalEntries(bundles) >= 100000
	if useParseDefer {
		if err := v.BeginBulkParseLoad(); err != nil {
			_ = v.EndBulkLoad()
			return err
		}
	}
	if err := v.SetBulkPragmas(true); err != nil {
		_ = v.EndBulkLoad()
		if useParseDefer {
			_ = v.EndBulkParseLoad()
		}
		return err
	}
	commitErr := func() error {
		if fresh {
			return v.InsertOriginsBatch(ctx, bundles)
		}
		// Incremental bulk updates keep replace semantics per group.
		const txGroup = 100
		for i := 0; i < len(bundles); i += txGroup {
			if valve != nil {
				if ch := valve.Backpressure(); ch != nil {
					select {
					case <-ch:
					case <-ctx.Done():
						return ctx.Err()
					}
				}
			}
			end := i + txGroup
			if end > len(bundles) {
				end = len(bundles)
			}
			if err := v.ReplaceOriginsBatch(ctx, bundles[i:end]); err != nil {
				return err
			}
		}
		return nil
	}()
	// Fold WAL before rebuilds so post-bulk reads stay fast.
	if valve != nil {
		_ = valve.FoldNow()
	}
	// Restore pragmas before rebuilding so rebuilds run durable.
	restoreErr := v.SetBulkPragmas(false)
	var endParseErr error
	if useParseDefer {
		endParseErr = v.EndBulkParseLoad()
	}
	endErr := v.EndBulkLoad()
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

func totalEntries(bundles []cold.OriginBundle) int {
	total := 0
	for _, b := range bundles {
		total += len(b.Entries)
	}
	return total
}

func shouldUseWalValve(v *cold.Vault) bool {
	if v == nil {
		return false
	}
	if strings.TrimSpace(os.Getenv("SRCINSIGHT_NO_WAL_DEFER")) == "1" {
		return false
	}
	if v.Path() == ":memory:" {
		return false
	}
	jm := strings.ToLower(strings.TrimSpace(v.GetJournalMode()))
	return jm == "wal"
}
