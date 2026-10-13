package services

import (
	"context"

	"srcinsight/internal/model"
	"srcinsight/internal/storage/cold"
	"srcinsight/services/resolve"
	tspkg "srcinsight/services/resolve/ts"
)

// diChainTarget and friends adapt the ts DI logic to the pipeline.

// diChainTarget resolves one DI loose end against one importing file.
func (p *Pipeline) diChainTarget(
	ctx context.Context,
	b *cold.OriginBundle,
	byID map[model.EntryID]cold.EntryRow,
	importByLocal map[string][]cold.ImportRow,
	resolver ImportResolver,
	pending resolve.Pending,
	lc *resolve.LookupCache,
	le cold.LooseEndRow,
) ([]cold.EntryRow, error) {
	return tspkg.DIChainTarget(ctx, p, b, byID, importByLocal, resolver, pending, lc, le)
}

// resolveDIBatch runs the forward DI pass over current bundles.
func (p *Pipeline) resolveDIBatch(
	ctx context.Context,
	bundles []cold.OriginBundle,
	pending resolve.Pending,
	lc *resolve.LookupCache,
) error {
	return tspkg.ResolveDIBatch(ctx, p, bundles, pending, lc)
}

// promoteDIReverse revisits DI loose ends bound to freshly indexed types.
func (p *Pipeline) promoteDIReverse(ctx context.Context, bundles []cold.OriginBundle) error {
	return tspkg.PromoteDIReverse(ctx, p, bundles)
}

// splitThisChain splits "this.field.rest" into field and method.
func splitThisChain(name string) (field, method string, ok bool) {
	return tspkg.SplitThisChain(name)
}

// diChainTargets resolves a this-chain call against the caller's file.
func diChainTargets(caller *model.Function, byName map[string][]*model.Function, ctorBinds map[string]map[string]string, field, method string) []*model.Function {
	return tspkg.DIChainTargets(caller, byName, ctorBinds, field, method)
}
