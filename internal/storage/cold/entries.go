package cold

import (
	"context"
	"database/sql"
	"fmt"
)

// scanEntries maps entries rows in canonical column order.
func scanEntries(rows *sql.Rows) ([]EntryRow, error) {
	var out []EntryRow
	for rows.Next() {
		var r EntryRow
		var tags string
		if err := rows.Scan(
			&r.ID, &r.Kind, &r.Name, &r.Qualified, &r.FilePath, &r.Dialect,
			&r.StartLine, &r.EndLine, &r.Signature, &r.Doc, &r.TestTitle, &tags,
			&r.VesselKind, &r.VesselName, &r.VesselID,
		); err != nil {
			return nil, err
		}
		r.Tags = DecodeTags(tags)
		out = append(out, r)
	}
	return out, rows.Err()
}

// scanScoredEntries maps search rows with the trailing rank column.
func scanScoredEntries(rows *sql.Rows) ([]ScoredEntryRow, error) {
	var out []ScoredEntryRow
	for rows.Next() {
		var r ScoredEntryRow
		var tags string
		if err := rows.Scan(
			&r.ID, &r.Kind, &r.Name, &r.Qualified, &r.FilePath, &r.Dialect,
			&r.StartLine, &r.EndLine, &r.Signature, &r.Doc, &r.TestTitle, &tags,
			&r.VesselKind, &r.VesselName, &r.VesselID, &r.Score,
		); err != nil {
			return nil, err
		}
		r.Tags = DecodeTags(tags)
		out = append(out, r)
	}
	return out, rows.Err()
}

// CatalogEntries writes a file's entries, skipping invalid rows that would
// violate foreign keys downstream. Returns the rows written.
func (v *Vault) CatalogEntries(ctx context.Context, rows []EntryRow) (int, error) {
	written := 0
	err := v.Transact(ctx, func(tx *sql.Tx) error {
		stmt := tx.StmtContext(ctx, v.stmts.insertEntry)
		defer stmt.Close()
		for _, r := range rows {
			if !r.Valid() {
				continue
			}
			if _, err := stmt.ExecContext(ctx,
				string(r.ID), string(r.Kind), r.Name, r.Qualified, r.FilePath, string(r.Dialect),
				r.StartLine, r.EndLine, r.Signature, r.Doc, r.TestTitle, EncodeTags(r.Tags),
				string(r.VesselKind), r.VesselName, string(r.VesselID),
			); err != nil {
				return fmt.Errorf("cold: catalog entry %s: %w", r.ID, err)
			}
			written++
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return written, nil
}

// FetchEntriesByFile returns one file's entries in ID order.
func (v *Vault) FetchEntriesByFile(ctx context.Context, path string) ([]EntryRow, error) {
	rows, err := v.stmts.entriesByFile.QueryContext(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("cold: fetch entries %s: %w", path, err)
	}
	defer rows.Close()
	out, err := scanEntries(rows)
	if err != nil {
		return nil, fmt.Errorf("cold: fetch entries %s: %w", path, err)
	}
	return out, nil
}

func (v *Vault) FetchEntryByID(ctx context.Context, id string) (EntryRow, bool, error) {
	rows, err := v.stmts.entryByID.QueryContext(ctx, id)
	if err != nil {
		return EntryRow{}, false, fmt.Errorf("cold: fetch entry %s: %w", id, err)
	}
	defer rows.Close()
	out, err := scanEntries(rows)
	if err != nil {
		return EntryRow{}, false, fmt.Errorf("cold: fetch entry %s: %w", id, err)
	}
	if len(out) == 0 {
		return EntryRow{}, false, nil
	}
	return out[0], true, nil
}

// FetchEntriesByIDs returns entries for IDs, chunked under the SQLite
// variable limit. Missing IDs stay silent.
func (v *Vault) FetchEntriesByIDs(ctx context.Context, ids []string) ([]EntryRow, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	seen := make(map[string]struct{}, len(ids))
	uniq := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == "" {
			continue
		}
		if _, ok := seen[id]; !ok {
			seen[id] = struct{}{}
			uniq = append(uniq, id)
		}
	}
	if len(uniq) == 0 {
		return nil, nil
	}
	var out []EntryRow
	const chunk = 500
	for i := 0; i < len(uniq); i += chunk {
		end := i + chunk
		if end > len(uniq) {
			end = len(uniq)
		}
		sub := uniq[i:end]
		placeholders := placeholdersCache[len(sub)]
		args := make([]any, 0, len(sub))
		for _, id := range sub {
			args = append(args, id)
		}
		query := `SELECT id, kind, name, qualified_name, file_path, dialect, start_line, end_line, signature, doc, test_title, tags, vessel_kind, vessel_name, vessel_id FROM entries WHERE id IN (` + placeholders + `) ORDER BY id`
		rows, err := v.db.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, fmt.Errorf("cold: fetch entries by ids: %w", err)
		}
		batch, err := scanEntries(rows)
		rows.Close()
		if err != nil {
			return nil, fmt.Errorf("cold: fetch entries by ids: %w", err)
		}
		out = append(out, batch...)
	}
	return out, nil
}

// FetchAllEntries returns every entry in ID order.
//
// Deprecated: test-only full-table scan, never for production paths.
func (v *Vault) FetchAllEntries(ctx context.Context) ([]EntryRow, error) {
	rows, err := v.stmts.allEntries.QueryContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("cold: fetch all entries: %w", err)
	}
	defer rows.Close()
	out, err := scanEntries(rows)
	if err != nil {
		return nil, fmt.Errorf("cold: fetch all entries: %w", err)
	}
	return out, nil
}
