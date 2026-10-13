package services

import (
	"context"

	"srcinsight/internal/storage/cold"
	"srcinsight/services/shared"
)

// FullScan converges the vault with disk; force re-parses even unchanged files.
func (p *Pipeline) FullScan(ctx context.Context, force bool) (SyncResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.fullScanLocked(ctx, force)
}

// TryFullScan runs FullScan without waiting; it reports busy instead of blocking.
func (p *Pipeline) TryFullScan(ctx context.Context, force bool) (SyncResult, bool, error) {
	if !p.mu.TryLock() {
		return SyncResult{}, false, nil
	}
	defer p.mu.Unlock()
	res, err := p.fullScanLocked(ctx, force)
	return res, true, err
}

// fullScanLocked needs p.mu held; FullScan, TryFullScan, and syncLocked lock it.
func (p *Pipeline) fullScanLocked(ctx context.Context, force bool) (SyncResult, error) {

	// Drop alias caches so edited configs re-read lazily during this run.
	p.tsAlias.InvalidateAll()
	p.ws.Invalidate()
	p.goMods.InvalidateAll()

	var res SyncResult
	if !force {
		ok, err := p.checkVersion(ctx)
		if err != nil {
			return res, err
		}
		if !ok {
			force = true
		}
	}

	disk, err := shared.CollectDisk(ctx, p.root, p.sourceFilter())
	if err != nil {
		return res, err
	}
	res.FilesChecked = len(disk)
	phaseMark := phaseTimer()
	phaseMark("collect")

	var candidates []shared.DiskFile
	var tracked []cold.TrackedOrigin
	var byTracked map[string]cold.TrackedOrigin
	if existing, fetchErr := p.vault.FetchTracked(ctx); fetchErr != nil {
		return res, fetchErr
	} else {
		tracked = existing
		byTracked = make(map[string]cold.TrackedOrigin, len(tracked))
		for _, t := range tracked {
			byTracked[t.Path] = t
		}
	}
	if force {
		candidates = disk
	} else {
		for _, d := range disk {
			t, ok := byTracked[d.Rel]
			if !ok || t.Bytes != d.Size || t.AlteredAt != d.AlteredAt {
				candidates = append(candidates, d)
			}
		}
	}

	// Read and hash candidates without parsing yet.
	raws, err := p.readRaws(ctx, candidates)
	if err != nil {
		return res, err
	}
	phaseMark("readhash")

	// Identical hashes refresh stat identity only; new content parses.
	var toParse []rawFile
	var refresh []shared.DiskFile
	var preFaults []cold.OriginBundle
	for _, r := range raws {
		if r.skip {
			continue
		}
		if r.bad {
			preFaults = append(preFaults, r.fault)
			continue
		}
		if !force {
			if t, ok := byTracked[r.df.Rel]; ok && t.Fingerprint != "" && t.Fingerprint == r.hash {
				refresh = append(refresh, r.df)
				continue
			}
		}
		toParse = append(toParse, r)
	}

	bundles, faults, _ := p.buildBundles(ctx, toParse)
	phaseMark("parse")
	// Large runs use the bulk recipe; small runs keep per-file commits.
	all := append(append([]cold.OriginBundle{}, bundles...), append(preFaults, faults...)...)
	fresh := len(tracked) == 0 && len(all) > 0
	if fresh || len(all) >= bulkCommitThreshold {
		if err := p.commitBulk(ctx, all, fresh); err != nil {
			return res, err
		}
		res.Added += len(all)
	} else {
		if err := p.resolveOutgoingBatch(ctx, bundles); err != nil {
			return res, err
		}
		if len(bundles) > 0 {
			if err := p.vault.ReplaceOriginsBatch(ctx, bundles); err != nil {
				return res, err
			}
			res.Added += len(bundles)
		}
		for _, f := range append(preFaults, faults...) {
			if err := ctx.Err(); err != nil {
				return res, err
			}
			if err := p.vault.ReplaceOrigin(ctx, f); err != nil {
				return res, err
			}
			res.Added++
		}
		if err := p.promoteReverseCross(ctx, bundles); err != nil {
			return res, err
		}
		// DI reverse promotes loose ends bound to freshly indexed types.
		if err := p.promoteDIReverse(ctx, bundles); err != nil {
			return res, err
		}
		// Recv reverse runs only when SRCINSIGHT_RECV enables it.
		if recvReverseEnabled() {
			if err := p.promoteRecvReverse(ctx, bundles); err != nil {
				return res, err
			}
		}
	}
	phaseMark("commit")
	// Refresh stat identity without rewriting rows so next scan skips again.
	for _, df := range refresh {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		rec, found, err := p.vault.FetchOrigin(ctx, df.Rel)
		if err != nil || !found {
			continue
		}
		rec.Bytes = df.Size
		rec.AlteredAt = df.AlteredAt
		if err := p.vault.NoteOrigin(ctx, rec); err != nil {
			return res, err
		}
	}

	tracked, err = p.vault.FetchTracked(ctx)
	if err != nil {
		return res, err
	}
	onDisk := make(map[string]struct{}, len(disk))
	for _, d := range disk {
		onDisk[d.Rel] = struct{}{}
	}
	for _, t := range tracked {
		if _, ok := onDisk[t.Path]; !ok {
			if err := p.vault.ForgetOrigin(ctx, t.Path); err != nil {
				return res, err
			}
			res.Removed++
		}
	}

	if err := p.stampVersion(ctx); err != nil {
		return res, err
	}
	if res.Changed() > 0 {
		if err := p.vault.Maintain(); err != nil {
			return res, err
		}
	}
	phaseMark("tail")
	return res, nil
}
