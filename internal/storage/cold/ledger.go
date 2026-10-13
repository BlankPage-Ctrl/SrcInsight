package cold

import (
	"context"
	"database/sql"
	"fmt"
)

// scanOrigin maps one origins row in canonical column order.
func scanOrigin(row *sql.Row) (OriginRecord, error) {
	var r OriginRecord
	if err := row.Scan(
		&r.Path, &r.Fingerprint, &r.Dialect, &r.Bytes,
		&r.AlteredAt, &r.CatalogedAt, &r.EntryCount, &r.Faults,
	); err != nil {
		return OriginRecord{}, err
	}
	return r, nil
}

func scanOrigins(rows *sql.Rows) ([]OriginRecord, error) {
	var out []OriginRecord
	for rows.Next() {
		var r OriginRecord
		if err := rows.Scan(
			&r.Path, &r.Fingerprint, &r.Dialect, &r.Bytes,
			&r.AlteredAt, &r.CatalogedAt, &r.EntryCount, &r.Faults,
		); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// NoteOrigin records (or refreshes) one tracked file. Upsert, never fail.
func (v *Vault) NoteOrigin(ctx context.Context, r OriginRecord) error {
	_, err := v.stmts.upsertOrigin.ExecContext(ctx,
		r.Path, r.Fingerprint, string(r.Dialect), r.Bytes,
		r.AlteredAt, r.CatalogedAt, r.EntryCount, r.Faults,
	)
	if err != nil {
		return fmt.Errorf("cold: note origin %s: %w", r.Path, err)
	}
	return nil
}

// ForgetOrigin removes a file and everything it owns. Links and loose ends
// follow via cascades; brought_in deletes explicitly (no cascade).
func (v *Vault) ForgetOrigin(ctx context.Context, path string) error {
	return v.Transact(ctx, func(tx *sql.Tx) error {
		if _, err := tx.StmtContext(ctx, v.stmts.deleteEntriesByFile).ExecContext(ctx, path); err != nil {
			return fmt.Errorf("cold: forget entries %s: %w", path, err)
		}
		if _, err := tx.StmtContext(ctx, v.stmts.deleteBroughtInByFile).ExecContext(ctx, path); err != nil {
			return fmt.Errorf("cold: forget imports %s: %w", path, err)
		}
		if _, err := tx.StmtContext(ctx, v.stmts.deleteRecvBindByFile).ExecContext(ctx, path); err != nil {
			return fmt.Errorf("cold: forget recv binds %s: %w", path, err)
		}
		if _, err := tx.StmtContext(ctx, v.stmts.deleteOrigin).ExecContext(ctx, path); err != nil {
			return fmt.Errorf("cold: forget origin %s: %w", path, err)
		}
		return nil
	})
}

// FetchOrigin returns one tracked file. found=false when untracked.
func (v *Vault) FetchOrigin(ctx context.Context, path string) (rec OriginRecord, found bool, err error) {
	rec, err = scanOrigin(v.stmts.originByPath.QueryRowContext(ctx, path))
	if err == sql.ErrNoRows {
		return OriginRecord{}, false, nil
	}
	if err != nil {
		return OriginRecord{}, false, fmt.Errorf("cold: fetch origin %s: %w", path, err)
	}
	return rec, true, nil
}

// FetchTracked returns every tracked file in path order for the diff.
func (v *Vault) FetchTracked(ctx context.Context) ([]TrackedOrigin, error) {
	rows, err := v.stmts.allOrigins.QueryContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("cold: fetch tracked: %w", err)
	}
	defer rows.Close()
	recs, err := scanOrigins(rows)
	if err != nil {
		return nil, fmt.Errorf("cold: fetch tracked: %w", err)
	}
	out := make([]TrackedOrigin, 0, len(recs))
	for _, r := range recs {
		out = append(out, TrackedOrigin{
			Path:        r.Path,
			Fingerprint: r.Fingerprint,
			Dialect:     r.Dialect,
			Bytes:       r.Bytes,
			AlteredAt:   r.AlteredAt,
		})
	}
	return out, nil
}
