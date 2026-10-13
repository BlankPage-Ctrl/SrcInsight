package cold

import (
	"context"
	"database/sql"
	"fmt"

	"srcinsight/internal/model"
)

// CatalogImports appends import rows in one transaction. Imports carry no
// foreign keys, so clearing stays the replacer's job.
func (v *Vault) CatalogImports(ctx context.Context, rows []ImportRow) error {
	return v.Transact(ctx, func(tx *sql.Tx) error {
		stmt := tx.StmtContext(ctx, v.stmts.insertBroughtIn)
		defer stmt.Close()
		for _, r := range rows {
			if _, err := stmt.ExecContext(ctx, r.FilePath, r.LocalName, r.Source, r.Kind, r.Alias); err != nil {
				return fmt.Errorf("cold: catalog import %s: %w", r.FilePath, err)
			}
		}
		return nil
	})
}

// FetchAllImports returns every import grouped by file.
//
// Deprecated: test-only full-table scan, never for production paths.
func (v *Vault) FetchAllImports(ctx context.Context) (map[string][]model.ImportStmt, error) {
	rows, err := v.stmts.allBroughtIn.QueryContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("cold: fetch all imports: %w", err)
	}
	defer rows.Close()
	out := make(map[string][]model.ImportStmt)
	for rows.Next() {
		var r ImportRow
		if err := rows.Scan(&r.FilePath, &r.LocalName, &r.Source, &r.Kind, &r.Alias); err != nil {
			return nil, fmt.Errorf("cold: fetch all imports: %w", err)
		}
		out[r.FilePath] = append(out[r.FilePath], r.Model())
	}
	return out, rows.Err()
}
