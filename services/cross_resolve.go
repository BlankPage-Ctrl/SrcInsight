package services

import (
	"context"
	"strings"

	"srcinsight/internal/storage/cold"
	"srcinsight/services/adapters"
	"srcinsight/services/resolve"
)

// resolveOutgoingBatch runs the generic cross pass, then language passes in
// registry order: Go same-package, TypeScript DI, then receiver (gated).
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
	return p.langs.Forward(ctx, p, bundles, pending, bundleImports, lc)
}

// promoteReverseCross promotes other files' loose ends that new bundles satisfy.
func (p *Pipeline) promoteReverseCross(ctx context.Context, bundles []cold.OriginBundle) error {
	if err := resolve.PromoteReverseCross(ctx, p, bundles); err != nil {
		return err
	}
	return p.langs.Reverse(ctx, p, bundles, adapters.ReverseCross)
}
