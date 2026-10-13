package services

import (
	"context"
	"strings"

	"srcinsight/internal/storage/cold"
	"srcinsight/services/resolve"
)

// resolveOutgoingBatch orders generic promotion, then Go, DI, and recv passes.

func (p *Pipeline) resolveOutgoingBatch(ctx context.Context, bundles []cold.OriginBundle) error {
	// Build the pending map for intra-batch resolution.
	pending := make(resolve.Pending)
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
	lc := resolve.NewLookupCache()
	// Gate candidates on known files; pending files join the set.
	known, err := resolve.KnownFiles(ctx, p, bundles)
	if err != nil {
		return err
	}
	lc.SetKnown(known)
	// Prime every (file, name) the batch could need before resolving.
	if err := resolve.PrimeLookup(ctx, p, bundles, bundleImports, pending, lc); err != nil {
		return err
	}
	for i := range bundles {
		if err := resolve.ResolveOutgoingCrossWithPending(ctx, p, &bundles[i], pending, bundleImports, lc); err != nil {
			return err
		}
	}
	// Run the Go same-package pass on still-loose plain names only.
	if err := p.resolveGoPackageBatch(ctx, bundles, pending, bundleImports, lc); err != nil {
		return err
	}
	// Run the DI pass on primed candidates.
	if err := p.resolveDIBatch(ctx, bundles, pending, lc); err != nil {
		return err
	}
	// Recv pass runs only when SRCINSIGHT_RECV enables forward resolution.
	if !recvForwardEnabled() {
		return nil
	}
	return p.resolveRecvBatch(ctx, bundles, pending, lc)
}

// promoteReverseCross promotes other files' loose ends that new bundles satisfy.
func (p *Pipeline) promoteReverseCross(ctx context.Context, bundles []cold.OriginBundle) error {
	if err := resolve.PromoteReverseCross(ctx, p, bundles); err != nil {
		return err
	}
	return p.promoteGoPackageReverse(ctx, bundles)
}
