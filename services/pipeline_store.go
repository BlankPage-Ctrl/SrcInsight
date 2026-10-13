package services

import (
	"context"
	"database/sql"

	"srcinsight/internal/model"
	"srcinsight/internal/storage/cold"
	"srcinsight/services/resolve"
)

// Pipeline adapts to resolve.Store so language strategies read the vault without a cycle.

var _ resolve.Store = (*Pipeline)(nil)

// ResolverFor returns the alias-bound resolver per dialect, or stateless when unconfigured.
func (p *Pipeline) ResolverFor(lang model.Language) resolve.ImportResolver {
	return p.resolverFor(lang)
}

// FetchTracked reads tracked origins.
func (p *Pipeline) FetchTracked(ctx context.Context) ([]cold.TrackedOrigin, error) {
	return p.vault.FetchTracked(ctx)
}

// FetchTargets reads entries by file and name.
func (p *Pipeline) FetchTargets(ctx context.Context, file, name string) ([]cold.EntryRow, error) {
	return p.vault.FetchTargetsByFileAndName(ctx, p.vault.DB(), file, name)
}

// FetchTargetsBatch reads entries for file-name pairs.
func (p *Pipeline) FetchTargetsBatch(ctx context.Context, pairs []resolve.FileName) (map[string][]cold.EntryRow, error) {
	raw := make([]struct {
		File string
		Name string
	}, 0, len(pairs))
	for _, fn := range pairs {
		raw = append(raw, struct {
			File string
			Name string
		}{File: fn.File, Name: fn.Name})
	}
	return p.vault.FetchTargetsBatch(ctx, p.vault.DB(), raw)
}

// FetchImports reads imports by file.
func (p *Pipeline) FetchImports(ctx context.Context, file string) ([]cold.ImportRow, error) {
	return p.vault.FetchImportsByFile(ctx, p.vault.DB(), file)
}

// FetchEntries reads entries by file.
func (p *Pipeline) FetchEntries(ctx context.Context, file string) ([]cold.EntryRow, error) {
	return p.vault.FetchEntriesByFile(ctx, file)
}

// FetchEntriesByIDs reads entries by ID.
func (p *Pipeline) FetchEntriesByIDs(ctx context.Context, ids []string) ([]cold.EntryRow, error) {
	return p.vault.FetchEntriesByIDs(ctx, ids)
}

// FetchRecvBindsByFile reads receiver bindings by file.
func (p *Pipeline) FetchRecvBindsByFile(ctx context.Context, file string) ([]cold.RecvBindRow, error) {
	return p.vault.FetchRecvBindsByFile(ctx, file)
}

// FetchRecvBindsByType reads receiver bindings by type.
func (p *Pipeline) FetchRecvBindsByType(ctx context.Context, typeName string) ([]cold.RecvBindRow, error) {
	return p.vault.FetchRecvBindsByType(ctx, typeName)
}

// FetchLooseEnds reads loose ends by file.
func (p *Pipeline) FetchLooseEnds(ctx context.Context, file string) ([]cold.LooseEndRow, error) {
	return p.vault.FetchLooseEndsByFile(ctx, file)
}

// FetchLooseByNames reads loose ends by ref names.
func (p *Pipeline) FetchLooseByNames(ctx context.Context, names []string, exclude map[string]struct{}) ([]cold.LooseEndRow, error) {
	return p.vault.FetchLooseByRefNames(ctx, p.vault.DB(), names, exclude)
}

// FetchNeighborEdges reads outgoing edges for one node.
func (p *Pipeline) FetchNeighborEdges(ctx context.Context, id model.EntryID, kinds []string, reverse bool) ([]cold.NeighborEdge, error) {
	return p.vault.FetchNeighborEdges(ctx, id, kinds, reverse)
}

// PromoteLinks inserts links and deletes resolved loose ends in one transaction.
func (p *Pipeline) PromoteLinks(ctx context.Context, links []cold.LinkRow, deletes []cold.LooseEndRow) error {
	if len(links) == 0 {
		return nil
	}
	return p.vault.Transact(ctx, func(tx *sql.Tx) error {
		ids := make([]model.EntryID, 0, len(links)*2)
		seenID := make(map[model.EntryID]struct{})
		for _, l := range links {
			for _, id := range []model.EntryID{l.Origin, l.Target} {
				if _, ok := seenID[id]; !ok {
					seenID[id] = struct{}{}
					ids = append(ids, id)
				}
			}
		}
		present, err := p.vault.ExistingIDs(ctx, tx, ids)
		if err != nil {
			return err
		}
		for _, l := range links {
			if !present[l.Origin] || !present[l.Target] {
				continue
			}
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO links (origin, target, kind, site_start, site_end) VALUES (?, ?, ?, ?, ?)`, string(l.Origin), string(l.Target), l.Kind, l.SiteStart, l.SiteEnd); err != nil {
				return err
			}
		}
		for _, le := range deletes {
			if _, err := tx.ExecContext(ctx, `DELETE FROM loose_ends WHERE from_entry = ? AND ref_name = ? AND ref_kind = ? AND site_start = ? AND site_end = ?`, string(le.From), le.Name, le.Kind, le.SiteStart, le.SiteEnd); err != nil {
				return err
			}
		}
		return nil
	})
}
