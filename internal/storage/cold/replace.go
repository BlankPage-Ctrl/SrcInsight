package cold

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"srcinsight/internal/model"
)

// incomingLink is one cross-file edge into a file being replaced. The target
// rides as (kind, name): IDs embed source order, names re-resolve cleanly.
type incomingLink struct {
	origin     model.EntryID
	kind       string
	targetKind string
	targetName string
	siteStart  int
	siteEnd    int
	cond       string
}

// placeholdersCache avoids per-chunk placeholder rebuilds.
var placeholdersCache [501]string

func init() {
	for i := 1; i <= 500; i++ {
		s := strings.Repeat("?,", i)
		placeholdersCache[i] = s[:len(s)-1]
	}
}

// ReplaceOrigin atomically replaces everything one file owns. It snapshots
// incoming links, deletes, inserts the bundle, then re-attaches callers.
func (v *Vault) ReplaceOrigin(ctx context.Context, b OriginBundle) error {
	return v.Transact(ctx, func(tx *sql.Tx) error {
		incoming, err := v.snapshotIncoming(ctx, tx, b.Origin.Path)
		if err != nil {
			return err
		}
		if err := v.clearOrigin(ctx, tx, b.Origin.Path); err != nil {
			return err
		}
		// Batched inserts keep single-file replaces to one Exec per 50 rows.
		bs := []OriginBundle{b}
		w := newBulkWriter(tx)
		defer w.close()
		if err := v.insertEntriesBatchTx(ctx, w, bs); err != nil {
			return err
		}
		b.Origin.EntryCount = bs[0].Origin.EntryCount
		b.Origin.CatalogedAt = bs[0].Origin.CatalogedAt
		if err := v.insertLinksBatchTx(ctx, w, bs); err != nil {
			return err
		}
		if err := v.insertRestBatchTx(ctx, w, bs); err != nil {
			return err
		}
		if err := v.reattachIncoming(ctx, tx, b.Origin.Path, incoming); err != nil {
			return err
		}
		if _, err := tx.StmtContext(ctx, v.stmts.upsertOrigin).ExecContext(ctx,
			b.Origin.Path, b.Origin.Fingerprint, string(b.Origin.Dialect), b.Origin.Bytes,
			b.Origin.AlteredAt, b.Origin.CatalogedAt, b.Origin.EntryCount, b.Origin.Faults,
		); err != nil {
			return fmt.Errorf("cold: replace note origin %s: %w", b.Origin.Path, err)
		}
		return nil
	})
}

func (v *Vault) snapshotIncoming(ctx context.Context, tx *sql.Tx, path string) ([]incomingLink, error) {
	rows, err := tx.StmtContext(ctx, v.stmts.incomingLinks).QueryContext(ctx, path, path)
	if err != nil {
		return nil, fmt.Errorf("cold: snapshot incoming %s: %w", path, err)
	}
	defer rows.Close()
	var out []incomingLink
	for rows.Next() {
		var l incomingLink
		if err := rows.Scan(&l.origin, &l.kind, &l.targetKind, &l.targetName, &l.siteStart, &l.siteEnd, &l.cond); err != nil {
			return nil, fmt.Errorf("cold: snapshot incoming %s: %w", path, err)
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// clearOrigin deletes one file's entries, imports, bindings and origin row.
// Links, loose ends and FTS rows follow via cascades and triggers.
func (v *Vault) clearOrigin(ctx context.Context, tx *sql.Tx, path string) error {
	if _, err := tx.StmtContext(ctx, v.stmts.deleteEntriesByFile).ExecContext(ctx, path); err != nil {
		return fmt.Errorf("cold: replace clear entries %s: %w", path, err)
	}
	if _, err := tx.StmtContext(ctx, v.stmts.deleteBroughtInByFile).ExecContext(ctx, path); err != nil {
		return fmt.Errorf("cold: replace clear imports %s: %w", path, err)
	}
	if _, err := tx.StmtContext(ctx, v.stmts.deleteRecvBindByFile).ExecContext(ctx, path); err != nil {
		return fmt.Errorf("cold: replace clear recv binds %s: %w", path, err)
	}
	if _, err := tx.StmtContext(ctx, v.stmts.deleteTransportEPByFile).ExecContext(ctx, path); err != nil {
		return fmt.Errorf("cold: replace clear transport endpoints %s: %w", path, err)
	}
	if _, err := tx.StmtContext(ctx, v.stmts.deleteTransportCallByFile).ExecContext(ctx, path); err != nil {
		return fmt.Errorf("cold: replace clear transport calls %s: %w", path, err)
	}
	if _, err := tx.StmtContext(ctx, v.stmts.deleteOrigin).ExecContext(ctx, path); err != nil {
		return fmt.Errorf("cold: replace clear origin %s: %w", path, err)
	}
	return nil
}

// insertEntriesTx writes entries, skipping invalid rows.
func (v *Vault) insertEntriesTx(ctx context.Context, tx *sql.Tx, rows []EntryRow) (int, error) {
	stmt := tx.StmtContext(ctx, v.stmts.insertEntry)
	defer stmt.Close()
	written := 0
	for _, r := range rows {
		if !r.Valid() {
			continue
		}
		if _, err := stmt.ExecContext(ctx,
			string(r.ID), string(r.Kind), r.Name, r.Qualified, r.FilePath, string(r.Dialect),
			r.StartLine, r.EndLine, r.Signature, r.Doc, r.TestTitle, EncodeTags(r.Tags),
			string(r.VesselKind), r.VesselName, string(r.VesselID),
		); err != nil {
			return 0, fmt.Errorf("cold: replace entry %s: %w", r.ID, err)
		}
		written++
	}
	return written, nil
}

// insertLinksTx writes links whose endpoints both exist, so one stale edge
// never aborts the replace on a foreign-key violation.
func (v *Vault) insertLinksTx(ctx context.Context, tx *sql.Tx, rows []LinkRow) error {
	present, err := v.existingIDs(ctx, tx, linkEndpoints(rows))
	if err != nil {
		return err
	}
	stmt := tx.StmtContext(ctx, v.stmts.insertLink)
	defer stmt.Close()
	for _, r := range rows {
		if !r.Valid() {
			continue
		}
		if !present[r.Origin] || !present[r.Target] {
			continue
		}
		if _, err := stmt.ExecContext(ctx, string(r.Origin), string(r.Target), r.Kind, r.SiteStart, r.SiteEnd, r.Cond); err != nil {
			return fmt.Errorf("cold: replace link %s->%s: %w", r.Origin, r.Target, err)
		}
	}
	return nil
}

func (v *Vault) insertBundleRestTx(ctx context.Context, tx *sql.Tx, b OriginBundle) error {
	insImp := tx.StmtContext(ctx, v.stmts.insertBroughtIn)
	defer insImp.Close()
	for _, r := range b.Imports {
		if _, err := insImp.ExecContext(ctx, r.FilePath, r.LocalName, r.Source, r.Kind, r.Alias); err != nil {
			return fmt.Errorf("cold: replace import %s: %w", r.FilePath, err)
		}
	}
	if err := insertRecvBindsTx(ctx, tx, v, b.RecvBinds); err != nil {
		return err
	}
	insLoose := tx.StmtContext(ctx, v.stmts.insertLooseEnd)
	defer insLoose.Close()
	for _, r := range b.LooseEnds {
		if r.From == "" || r.Name == "" {
			continue
		}
		if _, err := insLoose.ExecContext(ctx,
			string(r.From), r.Name, r.Kind, r.Scope, r.FilePath, string(r.Dialect), r.SiteStart, r.SiteEnd, r.Cond,
		); err != nil {
			return fmt.Errorf("cold: replace loose end %s: %w", r.From, err)
		}
	}
	return nil
}

// reattachIncoming re-points snapshotted links at fresh rows by (kind, name).
// Renamed targets find no match and stay dropped.
func (v *Vault) reattachIncoming(ctx context.Context, tx *sql.Tx, path string, incoming []incomingLink) error {
	if len(incoming) == 0 {
		return nil
	}
	find := tx.StmtContext(ctx, v.stmts.targetByKindName)
	defer find.Close()
	ins := tx.StmtContext(ctx, v.stmts.insertLink)
	defer ins.Close()
	for _, l := range incoming {
		var targetID model.EntryID
		if err := find.QueryRowContext(ctx, path, l.targetKind, l.targetName).Scan(&targetID); err == sql.ErrNoRows {
			continue
		} else if err != nil {
			return fmt.Errorf("cold: reattach resolve %s: %w", l.targetName, err)
		}
		if _, err := ins.ExecContext(ctx, string(l.origin), string(targetID), l.kind, l.siteStart, l.siteEnd, l.cond); err != nil {
			return fmt.Errorf("cold: reattach link %s: %w", l.origin, err)
		}
	}
	return nil
}

// querier abstracts *sql.DB and *sql.Tx for checks inside or outside tx.
type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func linkEndpoints(rows []LinkRow) []model.EntryID {
	seen := make(map[model.EntryID]struct{}, 2*len(rows))
	out := make([]model.EntryID, 0, 2*len(rows))
	for _, r := range rows {
		for _, id := range []model.EntryID{r.Origin, r.Target} {
			if id == "" {
				continue
			}
			if _, ok := seen[id]; !ok {
				seen[id] = struct{}{}
				out = append(out, id)
			}
		}
	}
	return out
}

// ExistingIDs reports which IDs are present, chunked to bound query size.
func (v *Vault) ExistingIDs(ctx context.Context, q querier, ids []model.EntryID) (map[model.EntryID]bool, error) {
	return v.existingIDs(ctx, q, ids)
}

func (v *Vault) existingIDs(ctx context.Context, q querier, ids []model.EntryID) (map[model.EntryID]bool, error) {
	present := make(map[model.EntryID]bool, len(ids))
	// Short IN lists stay fast; long ones degrade superlinearly.
	const chunk = 500
	for i := 0; i < len(ids); i += chunk {
		end := i + chunk
		if end > len(ids) {
			end = len(ids)
		}
		n := end - i
		var placeholders string
		if n <= len(placeholdersCache)-1 && placeholdersCache[n] != "" {
			placeholders = placeholdersCache[n]
		} else {
			s := strings.Repeat("?,", n)
			placeholders = s[:len(s)-1]
		}
		args := make([]any, 0, n)
		for _, id := range ids[i:end] {
			args = append(args, string(id))
		}
		rows, err := q.QueryContext(ctx,
			`SELECT id FROM entries WHERE id IN (`+placeholders+`)`, args...)
		if err != nil {
			return nil, fmt.Errorf("cold: check endpoints: %w", err)
		}
		if err := func() error {
			defer rows.Close()
			for rows.Next() {
				var id model.EntryID
				if err := rows.Scan(&id); err != nil {
					return fmt.Errorf("cold: check endpoints: %w", err)
				}
				present[id] = true
			}
			return rows.Err()
		}(); err != nil {
			return nil, fmt.Errorf("cold: check endpoints: %w", err)
		}
	}
	return present, nil
}
