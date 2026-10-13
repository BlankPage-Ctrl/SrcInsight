package cold

import (
	"context"
	"database/sql"
	"fmt"

	"srcinsight/internal/model"
)

// CatalogLooseEnds appends unresolved references in one transaction. Rows die
// with their entries through the foreign-key cascade, so no clearing here.
func (v *Vault) CatalogLooseEnds(ctx context.Context, rows []LooseEndRow) error {
	return v.Transact(ctx, func(tx *sql.Tx) error {
		stmt := tx.StmtContext(ctx, v.stmts.insertLooseEnd)
		defer stmt.Close()
		for _, r := range rows {
			if r.From == "" || r.Name == "" {
				continue
			}
			if _, err := stmt.ExecContext(ctx,
				string(r.From), r.Name, r.Kind, r.Scope, r.FilePath, string(r.Dialect), r.SiteStart, r.SiteEnd, r.Cond,
			); err != nil {
				return fmt.Errorf("cold: catalog loose end %s: %w", r.From, err)
			}
		}
		return nil
	})
}

// FetchLooseEndsByOwner returns one entry's unresolved references, filtered
// by from_entry with scope labels intact.
func (v *Vault) FetchLooseEndsByOwner(ctx context.Context, owner model.EntryID) ([]LooseEndRow, error) {
	rows, err := v.stmts.looseEndsByOwner.QueryContext(ctx, string(owner))
	if err != nil {
		return nil, fmt.Errorf("cold: fetch loose ends %s: %w", owner, err)
	}
	defer rows.Close()
	var out []LooseEndRow
	for rows.Next() {
		var r LooseEndRow
		if err := rows.Scan(&r.From, &r.Name, &r.Kind, &r.Scope, &r.FilePath, &r.Dialect, &r.SiteStart, &r.SiteEnd, &r.Cond); err != nil {
			return nil, fmt.Errorf("cold: fetch loose ends %s: %w", owner, err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// FetchLooseEndsByFile returns one file's unresolved references, filtered
// by file_path for the reverse-promotion pass.
func (v *Vault) FetchLooseEndsByFile(ctx context.Context, file string) ([]LooseEndRow, error) {
	rows, err := v.stmts.looseEndsByFile.QueryContext(ctx, file)
	if err != nil {
		return nil, fmt.Errorf("cold: fetch loose ends by file %s: %w", file, err)
	}
	defer rows.Close()
	var out []LooseEndRow
	for rows.Next() {
		var r LooseEndRow
		if err := rows.Scan(&r.From, &r.Name, &r.Kind, &r.Scope, &r.FilePath, &r.Dialect, &r.SiteStart, &r.SiteEnd, &r.Cond); err != nil {
			return nil, fmt.Errorf("cold: fetch loose ends by file %s: %w", file, err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// FetchAllLooseEnds returns every unresolved reference.
//
// Deprecated: test-only full-table scan, never for production paths.
func (v *Vault) FetchAllLooseEnds(ctx context.Context) ([]LooseEndRow, error) {
	rows, err := v.stmts.allLooseEnds.QueryContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("cold: fetch all loose ends: %w", err)
	}
	defer rows.Close()
	var out []LooseEndRow
	for rows.Next() {
		var r LooseEndRow
		if err := rows.Scan(&r.From, &r.Name, &r.Kind, &r.Scope, &r.FilePath, &r.Dialect, &r.SiteStart, &r.SiteEnd, &r.Cond); err != nil {
			return nil, fmt.Errorf("cold: fetch all loose ends: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// RecatalogLooseEnds replaces the whole loose-end set in one transaction.
//
// Deprecated: test-only full-table rewrite, never for production paths.
func (v *Vault) RecatalogLooseEnds(ctx context.Context, rows []LooseEndRow) error {
	return v.Transact(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM loose_ends`); err != nil {
			return fmt.Errorf("cold: clear loose ends: %w", err)
		}
		stmt := tx.StmtContext(ctx, v.stmts.insertLooseEnd)
		defer stmt.Close()
		for _, r := range rows {
			if r.From == "" || r.Name == "" {
				continue
			}
			if _, err := stmt.ExecContext(ctx,
				string(r.From), r.Name, r.Kind, r.Scope, r.FilePath, string(r.Dialect), r.SiteStart, r.SiteEnd, r.Cond,
			); err != nil {
				return fmt.Errorf("cold: recatalog loose end %s: %w", r.From, err)
			}
		}
		return nil
	})
}
