package services

import (
	"context"

	"srcinsight/internal/storage/cold"
	"srcinsight/services/resolve"
	tspkg "srcinsight/services/resolve/ts"
)

// resolveRecvBatch and promoteRecvReverse adapt the ts receiver logic to the pipeline.

// hasRecvCalls checks for receiver-qualified loose ends.
func hasRecvCalls(loose []cold.LooseEndRow) bool {
	return tspkg.HasRecvCalls(loose)
}

// resolveRecvBatch runs the forward receiver pass over current bundles.
func (p *Pipeline) resolveRecvBatch(ctx context.Context, bundles []cold.OriginBundle, pending resolve.Pending, lc *resolve.LookupCache) error {
	return tspkg.ResolveRecvBatch(ctx, p, bundles, pending, lc)
}

// promoteRecvReverse revisits loose ends bound to freshly indexed types.
func (p *Pipeline) promoteRecvReverse(ctx context.Context, bundles []cold.OriginBundle) error {
	return tspkg.PromoteRecvReverse(ctx, p, bundles)
}
