package cold

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"srcinsight/internal/model"
)

// CatalogLinks writes resolved edges, dropping links with a missing endpoint
// (OR IGNORE cannot suppress foreign-key violations). Returns rows written.
func (v *Vault) CatalogLinks(ctx context.Context, rows []LinkRow) (int, error) {
	present, err := v.existingIDs(ctx, v.db, linkEndpoints(rows))
	if err != nil {
		return 0, err
	}
	written := 0
	err = v.Transact(ctx, func(tx *sql.Tx) error {
		stmt := tx.StmtContext(ctx, v.stmts.insertLink)
		defer stmt.Close()
		for _, r := range rows {
			if !r.Valid() {
				continue
			}
			if !present[r.Origin] || !present[r.Target] {
				continue
			}
			if _, err := stmt.ExecContext(ctx, string(r.Origin), string(r.Target), r.Kind, r.SiteStart, r.SiteEnd, r.SiteStartCol, r.SiteEndCol, r.Cond); err != nil {
				return fmt.Errorf("cold: catalog link %s->%s: %w", r.Origin, r.Target, err)
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

// RecatalogLinks replaces the whole link set in one transaction.
//
// Deprecated: test-only full-table rewrite, never for production paths.
func (v *Vault) RecatalogLinks(ctx context.Context, rows []LinkRow) error {
	return v.Transact(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM links`); err != nil {
			return fmt.Errorf("cold: clear links: %w", err)
		}
		stmt := tx.StmtContext(ctx, v.stmts.insertLink)
		defer stmt.Close()
		for _, r := range rows {
			if !r.Valid() {
				continue
			}
			if _, err := stmt.ExecContext(ctx, string(r.Origin), string(r.Target), r.Kind, r.SiteStart, r.SiteEnd, r.SiteStartCol, r.SiteEndCol, r.Cond); err != nil {
				return fmt.Errorf("cold: recatalog link %s->%s: %w", r.Origin, r.Target, err)
			}
		}
		return nil
	})
}

// FetchAllLinks returns every link in deterministic order.
//
// Deprecated: test-only full-table scan, never for production paths.
func (v *Vault) FetchAllLinks(ctx context.Context) ([]LinkRow, error) {
	rows, err := v.stmts.allLinks.QueryContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("cold: fetch all links: %w", err)
	}
	defer rows.Close()
	var out []LinkRow
	for rows.Next() {
		var r LinkRow
		if err := rows.Scan(&r.Origin, &r.Target, &r.Kind, &r.SiteStart, &r.SiteEnd, &r.SiteStartCol, &r.SiteEndCol, &r.Cond); err != nil {
			return nil, fmt.Errorf("cold: fetch all links: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// CountCallers counts incoming calls edges: exact COUNT, so renderers print
// honest totals beside truncated neighbor pages.
func (v *Vault) CountCallers(ctx context.Context, id model.EntryID) (int, error) {
	var n int
	err := v.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM links WHERE target = ? AND kind = ?`,
		string(id), model.EdgeCalls).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("cold: count callers %s: %w", id, err)
	}
	return n, nil
}

// FetchNeighbors resolves one node's edge neighbors in ID order. The join
// keeps live rows only, so removed targets never leak.
func (v *Vault) FetchNeighbors(ctx context.Context, id model.EntryID, kinds []string, reverse bool) ([]model.FunctionRef, error) {
	if len(kinds) == 0 {
		kinds = []string{model.EdgeCalls}
	}
	placeholders := strings.Repeat("?,", len(kinds))
	placeholders = placeholders[:len(placeholders)-1]
	var query string
	if reverse {
		query = `SELECT t.id, t.name, t.file_path, t.start_line, t.end_line, l.site_start, l.site_end, l.site_start_col, l.site_end_col, l.cond FROM links l
			JOIN entries t ON t.id = l.origin
			WHERE l.target = ? AND l.kind IN (` + placeholders + `) ORDER BY t.id`
	} else {
		query = `SELECT t.id, t.name, t.file_path, t.start_line, t.end_line, l.site_start, l.site_end, l.site_start_col, l.site_end_col, l.cond FROM links l
			JOIN entries t ON t.id = l.target
			WHERE l.origin = ? AND l.kind IN (` + placeholders + `) ORDER BY t.id`
	}
	args := make([]any, 0, len(kinds)+1)
	args = append(args, string(id))
	for _, k := range kinds {
		args = append(args, k)
	}
	rows, err := v.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("cold: fetch neighbors %s: %w", id, err)
	}
	defer rows.Close()
	var out []model.FunctionRef
	for rows.Next() {
		var ref model.FunctionRef
		if err := rows.Scan(&ref.ID, &ref.Name, &ref.FilePath, &ref.LineRange.StartLine, &ref.LineRange.EndLine, &ref.SiteRange.StartLine, &ref.SiteRange.EndLine, &ref.SiteRange.StartCol, &ref.SiteRange.EndCol, &ref.Cond); err != nil {
			return nil, fmt.Errorf("cold: fetch neighbors %s: %w", id, err)
		}
		ref.Scope = model.InternalScope(ref.FilePath)
		out = append(out, ref)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("cold: fetch neighbors %s: %w", id, err)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// NeighborEdge is one directed edge plus the neighbor node reference.
type NeighborEdge struct {
	Ref  model.FunctionRef
	Kind string
}

// FetchNeighborEdges resolves neighbors with their edge kind in ID order.
func (v *Vault) FetchNeighborEdges(ctx context.Context, id model.EntryID, kinds []string, reverse bool) ([]NeighborEdge, error) {
	if len(kinds) == 0 {
		kinds = []string{model.EdgeCalls}
	}
	placeholders := strings.Repeat("?,", len(kinds))
	placeholders = placeholders[:len(placeholders)-1]
	var query string
	if reverse {
		query = `SELECT t.id, t.name, t.file_path, t.start_line, t.end_line, l.kind, l.site_start, l.site_end, l.site_start_col, l.site_end_col, l.cond FROM links l
			JOIN entries t ON t.id = l.origin
			WHERE l.target = ? AND l.kind IN (` + placeholders + `) ORDER BY t.id, l.kind`
	} else {
		query = `SELECT t.id, t.name, t.file_path, t.start_line, t.end_line, l.kind, l.site_start, l.site_end, l.site_start_col, l.site_end_col, l.cond FROM links l
			JOIN entries t ON t.id = l.target
			WHERE l.origin = ? AND l.kind IN (` + placeholders + `) ORDER BY t.id, l.kind`
	}
	args := make([]any, 0, len(kinds)+1)
	args = append(args, string(id))
	for _, k := range kinds {
		args = append(args, k)
	}
	rows, err := v.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("cold: fetch neighbor edges %s: %w", id, err)
	}
	defer rows.Close()
	var out []NeighborEdge
	for rows.Next() {
		var ne NeighborEdge
		if err := rows.Scan(&ne.Ref.ID, &ne.Ref.Name, &ne.Ref.FilePath, &ne.Ref.LineRange.StartLine, &ne.Ref.LineRange.EndLine, &ne.Kind, &ne.Ref.SiteRange.StartLine, &ne.Ref.SiteRange.EndLine, &ne.Ref.SiteRange.StartCol, &ne.Ref.SiteRange.EndCol, &ne.Ref.Cond); err != nil {
			return nil, fmt.Errorf("cold: fetch neighbor edges %s: %w", id, err)
		}
		ne.Ref.Scope = model.InternalScope(ne.Ref.FilePath)
		out = append(out, ne)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("cold: fetch neighbor edges %s: %w", id, err)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Ref.ID != out[j].Ref.ID {
			return out[i].Ref.ID < out[j].Ref.ID
		}
		return out[i].Kind < out[j].Kind
	})
	return out, nil
}
