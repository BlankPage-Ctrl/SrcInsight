package cold

import (
	"context"
	"database/sql"
	"fmt"
)

// Meta keys owned by the vault itself. Orchestrator-level keys (extractor
// version, project root) live alongside under their own names.
const (
	MetaRoot      = "root"
	MetaExtractor = "extractor"
)

func (v *Vault) PutMeta(ctx context.Context, key, value string) error {
	if _, err := v.stmts.putMeta.ExecContext(ctx, key, value, nowMilli()); err != nil {
		return fmt.Errorf("cold: put meta %s: %w", key, err)
	}
	return nil
}

// GetMeta reads one vault metadata entry; found is false when absent.
func (v *Vault) GetMeta(ctx context.Context, key string) (value string, found bool, err error) {
	if err := v.stmts.getMeta.QueryRowContext(ctx, key).Scan(&value); err == sql.ErrNoRows {
		return "", false, nil
	} else if err != nil {
		return "", false, fmt.Errorf("cold: get meta %s: %w", key, err)
	}
	return value, true, nil
}

// Wipe deletes every content row for a forced rebuild. Schema and metadata survive.
func (v *Vault) Wipe(ctx context.Context) error {
	return v.Transact(ctx, func(tx *sql.Tx) error {
		for _, stmt := range []string{
			`DELETE FROM loose_ends`,
			`DELETE FROM links`,
			`DELETE FROM brought_in`,
			`DELETE FROM recv_binds`,
			`DELETE FROM entries`,
			`DELETE FROM origins`,
		} {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				return fmt.Errorf("cold: wipe: %w", err)
			}
		}
		return nil
	})
}

// Counts reports entry and link totals for status output.
func (v *Vault) Counts(ctx context.Context) (entries, links int, err error) {
	if err := v.stmts.countEntries.QueryRowContext(ctx).Scan(&entries); err != nil {
		return 0, 0, fmt.Errorf("cold: count entries: %w", err)
	}
	if err := v.stmts.countLinks.QueryRowContext(ctx).Scan(&links); err != nil {
		return 0, 0, fmt.Errorf("cold: count links: %w", err)
	}
	return entries, links, nil
}
