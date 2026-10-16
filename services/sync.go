package services

import (
	"context"
	"sort"

	"srcinsight/internal/model"
	"srcinsight/internal/storage/cold"
	"srcinsight/services/shared"
)

// Sync updates the vault incrementally; a version mismatch forces a FullScan.
func (p *Pipeline) Sync(ctx context.Context) (SyncResult, error) {
	return p.SyncWithHints(ctx, nil)
}

// TrySync runs Sync without waiting; it reports busy instead of blocking.
func (p *Pipeline) TrySync(ctx context.Context) (SyncResult, bool, error) {
	if !p.mu.TryLock() {
		return SyncResult{}, false, nil
	}
	defer p.mu.Unlock()
	res, err := p.syncLocked(ctx, nil)
	return res, true, err
}

// SyncWithHints stats only hinted paths, else falls back to a full disk scan.
func (p *Pipeline) SyncWithHints(ctx context.Context, hints []string) (SyncResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.syncLocked(ctx, hints)
}

// syncLocked needs p.mu held; Sync, SyncWithHints, and TrySync lock it.
func (p *Pipeline) syncLocked(ctx context.Context, hints []string) (SyncResult, error) {

	var res SyncResult
	ok, err := p.checkVersion(ctx)
	if err != nil {
		return res, err
	}
	if !ok {
		return p.fullScanLocked(ctx, true)
	}

	// Drop alias caches so edited configs re-read lazily.
	p.tsAlias.InvalidateAll()
	p.ws.Invalidate()
	p.goMods.InvalidateAll()

	var disk []shared.DiskFile
	var hintMode bool
	if hints != nil && len(hints) > 0 {
		unique := dedupHints(hints)
		disk, err = p.collectHints(ctx, unique)
		if err != nil {
			return res, err
		}
		hintMode = true
		res.FilesChecked = len(disk)
	}
	if !hintMode {
		disk, err = shared.CollectDisk(ctx, p.root, p.sourceFilter())
		if err != nil {
			return res, err
		}
		res.FilesChecked = len(disk)
	}

	byRel := shared.ByRel(disk)
	var tracked []cold.TrackedOrigin
	if hintMode {
		// Load tracked rows only for hinted paths; deletions need them too.
		seen := make(map[string]struct{}, len(disk))
		for _, d := range disk {
			seen[d.Rel] = struct{}{}
		}
		// Include hinted paths missing from disk as possible deletions.
		for _, h := range hints {
			if _, ok := seen[h]; !ok {
				if rec, found, rerr := p.vault.FetchOrigin(ctx, h); rerr == nil && found {
					tracked = append(tracked, cold.TrackedOrigin{
						Path:        rec.Path,
						Fingerprint: rec.Fingerprint,
						Dialect:     rec.Dialect,
						Bytes:       rec.Bytes,
						AlteredAt:   rec.AlteredAt,
					})

				}
			}
		}

		for _, d := range disk {
			if rec, found, rerr := p.vault.FetchOrigin(ctx, d.Rel); rerr == nil && found {
				tracked = append(tracked, cold.TrackedOrigin{
					Path:        rec.Path,
					Fingerprint: rec.Fingerprint,
					Dialect:     rec.Dialect,
					Bytes:       rec.Bytes,
					AlteredAt:   rec.AlteredAt,
				})
			}
		}

		tracked = dedupTracked(tracked)
	} else {
		var fetchErr error
		tracked, fetchErr = p.vault.FetchTracked(ctx)
		if fetchErr != nil {
			return res, fetchErr
		}
	}
	added, changed, removed := cold.Reconcile(tracked, shared.ToDiskStats(disk))
	// Hint-mode removals cover only hinted paths, which is the correct scope.

	for _, rel := range removed {
		if err := p.vault.ForgetOrigin(ctx, rel); err != nil {
			return res, err
		}
		res.Removed++
	}

	// Hash-confirm candidates so timestamp-only touches stay out.
	var hashed []cold.HashedFile
	var tooLarge []shared.DiskFile
	for _, rel := range changed {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		meta, herr := p.root.Hash(rel)
		if herr != nil {
			if shared.IsTooLarge(herr) {
				if df, ok := byRel[rel]; ok {
					tooLarge = append(tooLarge, df)
				}
				continue
			}
			continue
		}
		hashed = append(hashed, cold.HashedFile{Rel: rel, Hash: meta.Hash})
	}
	modified := cold.Verify(tracked, hashed)

	var toProcess []shared.DiskFile
	for _, rel := range added {
		if df, ok := byRel[rel]; ok {
			toProcess = append(toProcess, df)
		}
	}
	for _, rel := range modified {
		if df, ok := byRel[rel]; ok {
			toProcess = append(toProcess, df)
		}
	}
	// Sort for deterministic commit order.
	sort.Slice(toProcess, func(i, j int) bool { return toProcess[i].Rel < toProcess[j].Rel })

	bundles, faults, err := p.processFiles(ctx, toProcess)
	if err != nil {
		return res, err
	}
	// Use the bulk recipe for large syncs.
	var syncBundles []cold.OriginBundle
	syncBundles = append(syncBundles, bundles...)
	for _, df := range tooLarge {
		syncBundles = append(syncBundles, shared.FaultBundle(df, model.Language(""), "", []string{"file too large"}))
	}
	syncBundles = append(syncBundles, faults...)
	if p.useBulkRecipe(ctx, syncBundles) {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		if err := p.commitBulk(ctx, syncBundles, false); err != nil {
			return res, err
		}
	} else {
		// Resolve outgoing links before commit so intra-batch targets exist.
		if err := p.resolveOutgoingBatch(ctx, bundles); err != nil {
			return res, err
		}
		if len(bundles) > 0 {
			if err := p.vault.ReplaceOriginsBatch(ctx, bundles); err != nil {
				return res, err
			}
		}
		// Record too-large faults so their stat identity skips future reads.
		for _, df := range tooLarge {
			fb := shared.FaultBundle(df, model.Language(""), "", []string{"file too large"})
			if err := p.vault.ReplaceOrigin(ctx, fb); err != nil {
				return res, err
			}
		}
		for _, f := range faults {
			if err := p.vault.ReplaceOrigin(ctx, f); err != nil {
				return res, err
			}
		}

		if err := p.promoteReverseCross(ctx, bundles); err != nil {
			return res, err
		}
		// Recv reverse runs only when SRCINSIGHT_RECV enables it.
		if recvReverseEnabled() {
			if err := p.promoteRecvReverse(ctx, bundles); err != nil {
				return res, err
			}
		}
	}
	res.Added = len(added)
	res.Modified = len(modified) + len(tooLarge)

	if res.Changed() > 0 {
		// Maintain is costly, so single-file syncs skip it.
		if res.Changed() == 1 {
		} else if err := p.vault.Maintain(); err != nil {
			return res, err
		}
	}
	return res, nil
}

// dedupHints keeps first occurrence order.
func dedupHints(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, v := range in {
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

func dedupTracked(in []cold.TrackedOrigin) []cold.TrackedOrigin {
	seen := make(map[string]struct{}, len(in))
	out := make([]cold.TrackedOrigin, 0, len(in))
	for _, v := range in {
		if _, ok := seen[v.Path]; ok {
			continue
		}
		seen[v.Path] = struct{}{}
		out = append(out, v)
	}
	return out
}

// collectHints stats only hinted rels that pass the source filter.
func (p *Pipeline) collectHints(ctx context.Context, hints []string) ([]shared.DiskFile, error) {
	var out []shared.DiskFile
	filter := p.sourceFilter()
	for _, rel := range hints {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		// Replicate Walk filtering without a DirEntry.
		if !IsSourceFile(rel) {
			// Non-source paths still need a removal check when tracked.
			if _, found, _ := p.vault.FetchOrigin(ctx, rel); !found {
				continue
			}
		}
		if p.ignore.IgnoredFile(rel) || p.ignore.IgnoredDir(rel) {
			// Ignored but tracked paths still need a removal check.
			if _, found, _ := p.vault.FetchOrigin(ctx, rel); !found {
				continue
			}
		}
		meta, err := p.root.Stat(rel)
		if err != nil {
			continue
		}
		_ = filter
		out = append(out, shared.DiskFile{
			Rel:       meta.Rel,
			Size:      meta.Size,
			AlteredAt: meta.ModTime.UnixMilli(),
		})
	}
	return out, nil
}
