package resolve

import (
	"context"

	"srcinsight/internal/model"
	"srcinsight/internal/storage/cold"
)

type FileName struct {
	File string
	Name string
}

// Pending covers in-batch bundles; lookups check pending first.
type Pending map[string]map[string][]cold.EntryRow

// Store is the vault-access contract every resolve pass consumes.
type Store interface {
	// ResolverFor returns the dialect resolver, or nil when unsupported.
	ResolverFor(lang model.Language) ImportResolver

	// FetchTracked lists every tracked origin.
	FetchTracked(ctx context.Context) ([]cold.TrackedOrigin, error)

	// FetchTargets finds entries named name in one file.
	FetchTargets(ctx context.Context, file, name string) ([]cold.EntryRow, error)

	// FetchTargetsBatch serves many pairs in bulk keyed by PairKey.
	FetchTargetsBatch(ctx context.Context, pairs []FileName) (map[string][]cold.EntryRow, error)

	FetchImports(ctx context.Context, file string) ([]cold.ImportRow, error)

	FetchEntries(ctx context.Context, file string) ([]cold.EntryRow, error)

	FetchEntriesByIDs(ctx context.Context, ids []string) ([]cold.EntryRow, error)

	FetchRecvBindsByFile(ctx context.Context, file string) ([]cold.RecvBindRow, error)

	FetchRecvBindsByType(ctx context.Context, typeName string) ([]cold.RecvBindRow, error)

	FetchLooseEnds(ctx context.Context, file string) ([]cold.LooseEndRow, error)

	// FetchLooseByNames returns loose ends matching names outside exclude.
	FetchLooseByNames(ctx context.Context, names []string, exclude map[string]struct{}) ([]cold.LooseEndRow, error)

	FetchNeighborEdges(ctx context.Context, id model.EntryID, kinds []string, reverse bool) ([]cold.NeighborEdge, error)

	// PromoteLinks inserts links and deletes resolved loose ends atomically.
	PromoteLinks(ctx context.Context, links []cold.LinkRow, deletes []cold.LooseEndRow) error
}
