package cold

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"srcinsight/internal/model"
)

// Multi-row chunk sizes derive from the variable budget in bulk_chunks.go:
// rows x cols must fit the driver limit, probed down on error.

// bulkWriter carries one transaction plus its prepared multi-row statements.
// Statements key on full query text, so equal-length chunks reuse them
// instead of preparing on every Exec.
type bulkWriter struct {
	tx    *sql.Tx
	stmts map[string]*sql.Stmt
}

func newBulkWriter(tx *sql.Tx) *bulkWriter {
	return &bulkWriter{tx: tx, stmts: make(map[string]*sql.Stmt)}
}

func (w *bulkWriter) exec(ctx context.Context, query string, args ...any) error {
	st, ok := w.stmts[query]
	if !ok {
		var err error
		st, err = w.tx.PrepareContext(ctx, query)
		if err != nil {
			return err
		}
		w.stmts[query] = st
	}
	_, err := st.ExecContext(ctx, args...)
	return err
}

func (w *bulkWriter) close() {
	for _, st := range w.stmts {
		_ = st.Close()
	}
}

// ReplaceOriginsBatch writes bundles in one transaction with ReplaceOrigin
// semantics. Pair with the bulk-load window on the fresh-index path.
func (v *Vault) ReplaceOriginsBatch(ctx context.Context, bundles []OriginBundle) error {
	if len(bundles) == 0 {
		return nil
	}
	if len(bundles) == 1 {
		return v.ReplaceOrigin(ctx, bundles[0])
	}
	err := v.Transact(ctx, func(tx *sql.Tx) error {
		w := newBulkWriter(tx)
		defer w.close()
		return v.replaceBundlesTx(ctx, w, bundles, nil)
	})
	v.wtDump("replace")
	return err
}

// InsertOriginsBatch writes a fresh index without replace semantics.
// Entries commit in ~200-file chunks, then links write once visible.
func (v *Vault) InsertOriginsBatch(ctx context.Context, bundles []OriginBundle) error {
	if len(bundles) == 0 {
		return nil
	}
	const txGroup = 200
	for i := 0; i < len(bundles); i += txGroup {
		end := i + txGroup
		if end > len(bundles) {
			end = len(bundles)
		}
		chunk := bundles[i:end]
		if err := v.Transact(ctx, func(tx *sql.Tx) error {
			w := newBulkWriter(tx)
			defer w.close()
			if err := v.insertEntriesBatchTx(ctx, w, chunk); err != nil {
				return err
			}
			if err := v.insertRestBatchTx(ctx, w, chunk); err != nil {
				return err
			}
			return v.upsertOriginsBatchTx(ctx, w, chunk)
		}); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	// Links last: all entry IDs are now visible for the existence check.
	err := v.Transact(ctx, func(tx *sql.Tx) error {
		w := newBulkWriter(tx)
		defer w.close()
		return v.insertLinksBatchTx(ctx, w, bundles)
	})
	v.wtDump("insert")
	return err
}

// replaceBundlesTx runs the batch body inside tx. Snapshots come from
// preSnapshot when non-nil, else they are taken before the first delete.
func (v *Vault) replaceBundlesTx(ctx context.Context, w *bulkWriter, bundles []OriginBundle, preSnapshot map[string][]incomingLink) error {
	tx := w.tx
	// Snapshot incoming links before any delete.
	snaps := preSnapshot
	if snaps == nil {
		snaps = make(map[string][]incomingLink, len(bundles))
		for _, b := range bundles {
			incoming, err := v.snapshotIncomingTx(ctx, tx, b.Origin.Path)
			if err != nil {
				return err
			}
			snaps[b.Origin.Path] = incoming
		}
	}
	// Delete all files first so endpoint checks see a consistent world.
	for _, b := range bundles {
		if err := v.clearOriginTx(ctx, tx, b.Origin.Path); err != nil {
			return err
		}
	}
	if err := v.insertEntriesBatchTx(ctx, w, bundles); err != nil {
		return err
	}
	if err := v.insertLinksBatchTx(ctx, w, bundles); err != nil {
		return err
	}
	if err := v.insertRestBatchTx(ctx, w, bundles); err != nil {
		return err
	}
	for _, b := range bundles {
		if err := v.reattachIncomingTx(ctx, tx, b.Origin.Path, snaps[b.Origin.Path]); err != nil {
			return err
		}
	}
	if err := v.upsertOriginsBatchTx(ctx, w, bundles); err != nil {
		return err
	}
	return nil
}

func (v *Vault) snapshotIncomingTx(ctx context.Context, tx *sql.Tx, path string) ([]incomingLink, error) {
	return v.snapshotIncoming(ctx, tx, path)
}

func (v *Vault) clearOriginTx(ctx context.Context, tx *sql.Tx, path string) error {
	return v.clearOrigin(ctx, tx, path)
}

func (v *Vault) reattachIncomingTx(ctx context.Context, tx *sql.Tx, path string, incoming []incomingLink) error {
	return v.reattachIncoming(ctx, tx, path, incoming)
}

// insertEntriesBatchTx writes all bundles' entries as chunked multi-row
// INSERT OR REPLACE statements.
func (v *Vault) insertEntriesBatchTx(ctx context.Context, w *bulkWriter, bundles []OriginBundle) error {
	total := 0
	for _, b := range bundles {
		total += len(b.Entries)
	}
	rows := make([]EntryRow, 0, total)
	for _, b := range bundles {
		for _, r := range b.Entries {
			if r.Valid() {
				rows = append(rows, r)
			}
		}
	}
	const cols = 15
	var tb, te time.Duration
	nc := 0
	for i := 0; i < len(rows); {
		end := min(i+chunkRows(cols), len(rows))
		chunk := rows[i:end]
		t0 := time.Now()
		var sb strings.Builder
		sb.Grow(128 + len(chunk)*64)
		sb.WriteString(`INSERT OR REPLACE INTO entries (id, kind, name, qualified_name, file_path, dialect, start_line, end_line, signature, doc, test_title, tags, vessel_kind, vessel_name, vessel_id) VALUES `)
		args := make([]any, 0, len(chunk)*cols)
		for j, r := range chunk {
			if j > 0 {
				sb.WriteByte(',')
			}
			sb.WriteString(`(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
			args = append(args,
				string(r.ID), string(r.Kind), r.Name, r.Qualified, r.FilePath, string(r.Dialect),
				r.StartLine, r.EndLine, r.Signature, r.Doc, r.TestTitle, EncodeTags(r.Tags),
				string(r.VesselKind), r.VesselName, string(r.VesselID),
			)
		}
		tb += time.Since(t0)
		t0 = time.Now()
		if err := w.exec(ctx, sb.String(), args...); err != nil {
			if isVarLimitError(err) && shrinkVarBudget() {
				continue
			}
			return fmt.Errorf("cold: batch entries [%d:%d]: %w", i, end, err)
		}
		te += time.Since(t0)
		nc++
		i = end
	}
	v.wtAdd("entries", tb, te, nc)
	// Stamp per-bundle counts for the origin records below.
	for i := range bundles {
		n := 0
		for _, r := range bundles[i].Entries {
			if r.Valid() {
				n++
			}
		}
		bundles[i].Origin.EntryCount = n
		bundles[i].Origin.CatalogedAt = nowMilli()
	}
	return nil
}

// insertLinksBatchTx writes links whose endpoints both exist, checked once
// for all bundles. Dangling endpoints would violate the foreign keys.
func (v *Vault) insertLinksBatchTx(ctx context.Context, w *bulkWriter, bundles []OriginBundle) error {
	totalLinks := 0
	for _, b := range bundles {
		totalLinks += len(b.Links)
	}
	all := make([]LinkRow, 0, totalLinks)
	for _, b := range bundles {
		for _, r := range b.Links {
			if r.Valid() {
				all = append(all, r)
			}
		}
	}
	if len(all) == 0 {
		return nil
	}
	t0 := time.Now()
	present, err := v.existingIDs(ctx, w.tx, linkEndpoints(all))
	tCheck := time.Since(t0)
	v.wtAdd("links.check", 0, tCheck, 1)
	if err != nil {
		return err
	}
	keep := make([]LinkRow, 0, len(all))
	for _, r := range all {
		if present[r.Origin] && present[r.Target] {
			keep = append(keep, r)
		}
	}
	const cols = 6
	var tb, te time.Duration
	nc := 0
	for i := 0; i < len(keep); {
		end := min(i+chunkRows(cols), len(keep))
		chunk := keep[i:end]
		t0 := time.Now()
		var sb strings.Builder
		sb.Grow(128 + len(chunk)*16)
		sb.WriteString(`INSERT OR IGNORE INTO links (origin, target, kind, site_start, site_end, cond) VALUES `)
		args := make([]any, 0, len(chunk)*cols)
		for j, r := range chunk {
			if j > 0 {
				sb.WriteByte(',')
			}
			sb.WriteString(`(?,?,?,?,?,?)`)
			args = append(args, string(r.Origin), string(r.Target), r.Kind, r.SiteStart, r.SiteEnd, r.Cond)
		}
		tb += time.Since(t0)
		t0 = time.Now()
		if err := w.exec(ctx, sb.String(), args...); err != nil {
			if isVarLimitError(err) && shrinkVarBudget() {
				continue
			}
			return fmt.Errorf("cold: batch links [%d:%d]: %w", i, end, err)
		}
		te += time.Since(t0)
		nc++
		i = end
	}
	v.wtAdd("links", tb, te, nc)
	return nil
}

// insertRestBatchTx writes all imports + loose ends + recv binds + transport hints multi-row.
func (v *Vault) insertRestBatchTx(ctx context.Context, w *bulkWriter, bundles []OriginBundle) error {
	totalImps, totalLoose, totalBinds, totalEPs, totalCalls := 0, 0, 0, 0, 0
	for _, b := range bundles {
		totalImps += len(b.Imports)
		totalLoose += len(b.LooseEnds)
		totalBinds += len(b.RecvBinds)
		totalEPs += len(b.Endpoints)
		totalCalls += len(b.Calls)
	}
	imps := make([]ImportRow, 0, totalImps)
	loose := make([]LooseEndRow, 0, totalLoose)
	binds := make([]RecvBindRow, 0, totalBinds)
	endpoints := make([]TransportEndpointRow, 0, totalEPs)
	calls := make([]TransportCallRow, 0, totalCalls)
	// Loose ends must resolve to a written entry; From always lives in the
	// same bundle, so filter against this batch's valid entry IDs. Without
	// this, a skipped entry would leave a dangling loose end behind.
	owners := make(map[model.EntryID]struct{}, totalImps)
	for _, b := range bundles {
		for _, r := range b.Entries {
			if r.Valid() {
				owners[r.ID] = struct{}{}
			}
		}
	}
	for _, b := range bundles {
		imps = append(imps, b.Imports...)
		for _, r := range b.LooseEnds {
			if r.From == "" || r.Name == "" {
				continue
			}
			if _, ok := owners[r.From]; !ok {
				continue
			}
			loose = append(loose, r)
		}
		for _, r := range b.RecvBinds {
			if r.Valid() {
				binds = append(binds, r)
			}
		}
		for _, r := range b.Endpoints {
			if r.Valid() {
				endpoints = append(endpoints, r)
			}
		}
		for _, r := range b.Calls {
			if r.Valid() {
				calls = append(calls, r)
			}
		}
	}
	var tbImp, teImp, tbLoose, teLoose, tbBind, teBind time.Duration
	var ncImp, ncLoose, ncBind int
	for i := 0; i < len(imps); {
		end := min(i+chunkRows(5), len(imps))
		chunk := imps[i:end]
		t0 := time.Now()
		var sb strings.Builder
		sb.Grow(128 + len(chunk)*48)
		sb.WriteString(`INSERT INTO brought_in (file_path, local_name, source, kind, alias) VALUES `)
		args := make([]any, 0, len(chunk)*5)
		for j, r := range chunk {
			if j > 0 {
				sb.WriteByte(',')
			}
			sb.WriteString(`(?,?,?,?,?)`)
			args = append(args, r.FilePath, r.LocalName, r.Source, r.Kind, r.Alias)
		}
		tbImp += time.Since(t0)
		t0 = time.Now()
		if err := w.exec(ctx, sb.String(), args...); err != nil {
			if isVarLimitError(err) && shrinkVarBudget() {
				continue
			}
			return fmt.Errorf("cold: batch imports [%d:%d]: %w", i, end, err)
		}
		teImp += time.Since(t0)
		ncImp++
		i = end
	}
	v.wtAdd("rest.imports", tbImp, teImp, ncImp)
	for i := 0; i < len(loose); {
		end := min(i+chunkRows(9), len(loose))
		chunk := loose[i:end]
		t0 := time.Now()
		var sb strings.Builder
		sb.Grow(128 + len(chunk)*64)
		sb.WriteString(`INSERT INTO loose_ends (from_entry, ref_name, ref_kind, scope, file_path, dialect, site_start, site_end, cond) VALUES `)
		args := make([]any, 0, len(chunk)*9)
		for j, r := range chunk {
			if j > 0 {
				sb.WriteByte(',')
			}
			sb.WriteString(`(?,?,?,?,?,?,?,?,?)`)
			args = append(args, string(r.From), r.Name, r.Kind, r.Scope, r.FilePath, string(r.Dialect), r.SiteStart, r.SiteEnd, r.Cond)
		}
		tbLoose += time.Since(t0)
		t0 = time.Now()
		if err := w.exec(ctx, sb.String(), args...); err != nil {
			if isVarLimitError(err) && shrinkVarBudget() {
				continue
			}
			return fmt.Errorf("cold: batch loose ends [%d:%d]: %w", i, end, err)
		}
		teLoose += time.Since(t0)
		ncLoose++
		i = end
	}
	v.wtAdd("rest.loose", tbLoose, teLoose, ncLoose)
	for i := 0; i < len(binds); {
		end := min(i+chunkRows(5), len(binds))
		chunk := binds[i:end]
		t0 := time.Now()
		var sb strings.Builder
		sb.Grow(128 + len(chunk)*48)
		sb.WriteString(`INSERT INTO recv_binds (file_path, owner, recv, type_name, via_call) VALUES `)
		args := make([]any, 0, len(chunk)*5)
		for j, r := range chunk {
			if j > 0 {
				sb.WriteByte(',')
			}
			sb.WriteString(`(?,?,?,?,?)`)
			args = append(args, r.FilePath, r.Owner, r.Recv, r.Type, r.ViaCall)
		}
		tbBind += time.Since(t0)
		t0 = time.Now()
		if err := w.exec(ctx, sb.String(), args...); err != nil {
			if isVarLimitError(err) && shrinkVarBudget() {
				continue
			}
			return fmt.Errorf("cold: batch recv binds [%d:%d]: %w", i, end, err)
		}
		teBind += time.Since(t0)
		ncBind++
		i = end
	}
	v.wtAdd("rest.binds", tbBind, teBind, ncBind)
	for i := 0; i < len(endpoints); {
		end := min(i+chunkRows(9), len(endpoints))
		chunk := endpoints[i:end]
		var sb strings.Builder
		sb.Grow(128 + len(chunk)*64)
		sb.WriteString(`INSERT INTO transport_endpoints (file_path, owner, transport, method, path, operation, framework, site_start) VALUES `)
		args := make([]any, 0, len(chunk)*8)
		for j, r := range chunk {
			if j > 0 {
				sb.WriteByte(',')
			}
			sb.WriteString(`(?,?,?,?,?,?,?,?)`)
			args = append(args, r.FilePath, string(r.Owner), r.Transport, r.Method, r.Path, r.Operation, r.Framework, r.SiteStart)
		}
		if err := w.exec(ctx, sb.String(), args...); err != nil {
			if isVarLimitError(err) && shrinkVarBudget() {
				continue
			}
			return fmt.Errorf("cold: batch transport endpoints [%d:%d]: %w", i, end, err)
		}
		i = end
	}
	for i := 0; i < len(calls); {
		end := min(i+chunkRows(9), len(calls))
		chunk := calls[i:end]
		var sb strings.Builder
		sb.Grow(128 + len(chunk)*64)
		sb.WriteString(`INSERT INTO transport_calls (file_path, owner, transport, method, url, operation, lib, site_start) VALUES `)
		args := make([]any, 0, len(chunk)*8)
		for j, r := range chunk {
			if j > 0 {
				sb.WriteByte(',')
			}
			sb.WriteString(`(?,?,?,?,?,?,?,?)`)
			args = append(args, r.FilePath, string(r.Owner), r.Transport, r.Method, r.URL, r.Operation, r.Lib, r.SiteStart)
		}
		if err := w.exec(ctx, sb.String(), args...); err != nil {
			if isVarLimitError(err) && shrinkVarBudget() {
				continue
			}
			return fmt.Errorf("cold: batch transport calls [%d:%d]: %w", i, end, err)
		}
		i = end
	}
	return nil
}

// upsertOriginsBatchTx records origin rows multi-row (chunked: 100 x 8).
func (v *Vault) upsertOriginsBatchTx(ctx context.Context, w *bulkWriter, bundles []OriginBundle) error {
	if len(bundles) == 0 {
		return nil
	}
	const cols = 8
	var tb, te time.Duration
	nc := 0
	for i := 0; i < len(bundles); {
		end := min(i+chunkRows(cols), len(bundles))
		part := bundles[i:end]
		t0 := time.Now()
		var sb strings.Builder
		sb.Grow(128 + len(part)*32)
		sb.WriteString(`INSERT INTO origins (path, fingerprint, dialect, bytes, altered_at, cataloged_at, entry_count, faults) VALUES `)
		args := make([]any, 0, len(part)*8)
		for j, b := range part {
			if j > 0 {
				sb.WriteByte(',')
			}
			sb.WriteString(`(?,?,?,?,?,?,?,?)`)
			args = append(args,
				b.Origin.Path, b.Origin.Fingerprint, string(b.Origin.Dialect), b.Origin.Bytes,
				b.Origin.AlteredAt, b.Origin.CatalogedAt, b.Origin.EntryCount, b.Origin.Faults,
			)
		}
		sb.WriteString(` ON CONFLICT(path) DO UPDATE SET fingerprint=excluded.fingerprint, dialect=excluded.dialect, bytes=excluded.bytes, altered_at=excluded.altered_at, cataloged_at=excluded.cataloged_at, entry_count=excluded.entry_count, faults=excluded.faults`)
		tb += time.Since(t0)
		t0 = time.Now()
		if err := w.exec(ctx, sb.String(), args...); err != nil {
			if isVarLimitError(err) && shrinkVarBudget() {
				continue
			}
			return fmt.Errorf("cold: batch origins [%d:%d]: %w", i, end, err)
		}
		te += time.Since(t0)
		nc++
		i = end
	}
	v.wtAdd("origins", tb, te, nc)
	return nil
}

// CommitBundles writes bundles in groups of 100 per commit, leaving the
// bulk-load window alone.
func (v *Vault) CommitBundles(ctx context.Context, bundles []OriginBundle) (int, error) {
	const txGroup = 100
	written := 0
	for i := 0; i < len(bundles); i += txGroup {
		end := i + txGroup
		if end > len(bundles) {
			end = len(bundles)
		}
		if err := v.ReplaceOriginsBatch(ctx, bundles[i:end]); err != nil {
			return written, err
		}
		written += end - i
	}
	return written, nil
}

// Bulk-load window: FTS trigger defer plus WAL and pragma recipe.

// ftsTriggerNames must match schema.go.
var ftsTriggerNames = []string{"entries_ai", "entries_ad", "entries_au"}

// ftsTriggerDDLs recreates the deferred triggers (same text as schema.go).
var ftsTriggerDDLs = []string{
	`CREATE TRIGGER IF NOT EXISTS entries_ai AFTER INSERT ON entries BEGIN
    INSERT INTO entries_fts(rowid, id, name, qualified_name, signature, doc)
    VALUES (NEW.rowid, NEW.id, NEW.name, NEW.qualified_name, NEW.signature, NEW.doc);
END`,
	`CREATE TRIGGER IF NOT EXISTS entries_ad AFTER DELETE ON entries BEGIN
    INSERT INTO entries_fts(entries_fts, rowid, id, name, qualified_name, signature, doc)
    VALUES ('delete', OLD.rowid, OLD.id, OLD.name, OLD.qualified_name, OLD.signature, OLD.doc);
END`,
	`CREATE TRIGGER IF NOT EXISTS entries_au AFTER UPDATE ON entries BEGIN
    INSERT INTO entries_fts(entries_fts, rowid, id, name, qualified_name, signature, doc)
    VALUES ('delete', OLD.rowid, OLD.id, OLD.name, OLD.qualified_name, OLD.signature, OLD.doc);
    INSERT INTO entries_fts(rowid, id, name, qualified_name, signature, doc)
    VALUES (NEW.rowid, NEW.id, NEW.name, NEW.qualified_name, NEW.signature, NEW.doc);
END`,
}

// BeginBulkLoad drops per-row FTS triggers. Pair with EndBulkLoad; a crash
// inside the window heals on the next open.
func (v *Vault) BeginBulkLoad() error {
	t0 := time.Now()
	for _, t := range ftsTriggerNames {
		if _, err := v.db.Exec(`DROP TRIGGER IF EXISTS ` + t); err != nil {
			return fmt.Errorf("cold: begin bulk drop %s: %w", t, err)
		}
	}
	v.wtAdd("bulk.drop_fts", 0, time.Since(t0), len(ftsTriggerNames))
	return nil
}

// EndBulkLoad rebuilds the FTS index in one pass, then recreates triggers.
func (v *Vault) EndBulkLoad() error {
	t0 := time.Now()
	if _, err := v.db.Exec(`INSERT INTO entries_fts(entries_fts) VALUES('rebuild')`); err != nil {
		return fmt.Errorf("cold: end bulk rebuild fts: %w", err)
	}
	v.wtAdd("bulk.fts_rebuild", 0, time.Since(t0), 1)
	t0 = time.Now()
	for _, ddl := range ftsTriggerDDLs {
		if _, err := v.db.Exec(ddl); err != nil {
			return fmt.Errorf("cold: end bulk recreate trigger: %w", err)
		}
	}
	v.wtAdd("bulk.fts_triggers", 0, time.Since(t0), len(ftsTriggerDDLs))
	v.wtDump("window")
	return nil
}

// healBulkLoad closes a bulk window left open by a crash (missing triggers,
// stale FTS).
func (v *Vault) healBulkLoad() error {
	var c int
	if err := v.db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND name IN ('entries_ai','entries_ad','entries_au')`,
	).Scan(&c); err != nil {
		return err
	}
	if c >= len(ftsTriggerNames) {
		return nil
	}
	return v.EndBulkLoad()
}

// SetBulkPragmas enables the bulk-write recipe (safe only for a fresh index
// that a crash just re-runs). Pass false to restore NORMAL/1000.
func (v *Vault) SetBulkPragmas(on bool) error {
	t0 := time.Now()
	if on {
		if err := v.db.QueryRow(`PRAGMA cache_spill`).Scan(&v.savedCacheSpill); err == nil {
			v.hasCacheSpill = true
		}
		if err := v.drain(`PRAGMA cache_spill = OFF`); err != nil {
			return fmt.Errorf("cold: bulk cache spill off: %w", err)
		}
		if err := v.drain(`PRAGMA wal_autocheckpoint = 0`); err != nil {
			return fmt.Errorf("cold: bulk autocheckpoint off: %w", err)
		}
		if err := v.drain(`PRAGMA synchronous = OFF`); err != nil {
			return fmt.Errorf("cold: bulk synchronous off: %w", err)
		}
		v.wtAdd("bulk.pragmas_on", 0, time.Since(t0), 1)
		return nil
	}
	if err := v.drain(`PRAGMA synchronous = NORMAL`); err != nil {
		return fmt.Errorf("cold: bulk synchronous restore: %w", err)
	}
	if v.hasCacheSpill {
		if err := v.drain(fmt.Sprintf(`PRAGMA cache_spill = %d`, v.savedCacheSpill)); err != nil {
			return fmt.Errorf("cold: bulk cache spill restore: %w", err)
		}
		v.hasCacheSpill = false
	}
	if err := v.drain(`PRAGMA wal_autocheckpoint = 1000`); err != nil {
		return fmt.Errorf("cold: bulk autocheckpoint restore: %w", err)
	}
	v.wtAdd("bulk.pragmas_off", 0, time.Since(t0), 1)
	return nil
}

// Bulk secondary-index deferral for node and edge heavy bulk.

var bulkParseIndexes = []string{
	"idx_entries_kind", "idx_entries_name", "idx_entries_qualified",
	"idx_entries_file", "idx_entries_dialect", "idx_entries_file_line", "idx_entries_lower_name",
	"idx_looseends_from", "idx_looseends_file", "idx_looseends_ref", "idx_looseends_ref_scope",
	"idx_broughtin_file", "idx_broughtin_source",
	"idx_recvbinds_file", "idx_recvbinds_type",
	"idx_origins_dialect", "idx_origins_altered",
}

var bulkEdgeIndexes = []string{
	"idx_links_kind", "idx_links_origin_kind", "idx_links_target_kind",
}

// bulkIndexDDLs maps index name to CREATE statement (mirrors schema.go).
var bulkIndexDDLs = map[string]string{
	"idx_entries_kind":        `CREATE INDEX IF NOT EXISTS idx_entries_kind ON entries(kind)`,
	"idx_entries_name":        `CREATE INDEX IF NOT EXISTS idx_entries_name ON entries(name)`,
	"idx_entries_qualified":   `CREATE INDEX IF NOT EXISTS idx_entries_qualified ON entries(qualified_name)`,
	"idx_entries_file":        `CREATE INDEX IF NOT EXISTS idx_entries_file ON entries(file_path)`,
	"idx_entries_dialect":     `CREATE INDEX IF NOT EXISTS idx_entries_dialect ON entries(dialect)`,
	"idx_entries_file_line":   `CREATE INDEX IF NOT EXISTS idx_entries_file_line ON entries(file_path, start_line)`,
	"idx_entries_lower_name":  `CREATE INDEX IF NOT EXISTS idx_entries_lower_name ON entries(lower(name))`,
	"idx_looseends_from":      `CREATE INDEX IF NOT EXISTS idx_looseends_from ON loose_ends(from_entry)`,
	"idx_looseends_file":      `CREATE INDEX IF NOT EXISTS idx_looseends_file ON loose_ends(file_path)`,
	"idx_looseends_ref":       `CREATE INDEX IF NOT EXISTS idx_looseends_ref ON loose_ends(ref_name)`,
	"idx_looseends_ref_scope": `CREATE INDEX IF NOT EXISTS idx_looseends_ref_scope ON loose_ends(ref_name, scope)`,
	"idx_broughtin_file":      `CREATE INDEX IF NOT EXISTS idx_broughtin_file ON brought_in(file_path)`,
	"idx_broughtin_source":    `CREATE INDEX IF NOT EXISTS idx_broughtin_source ON brought_in(source)`,
	"idx_recvbinds_file":      `CREATE INDEX IF NOT EXISTS idx_recvbinds_file ON recv_binds(file_path)`,
	"idx_recvbinds_type":      `CREATE INDEX IF NOT EXISTS idx_recvbinds_type ON recv_binds(type_name)`,
	"idx_origins_dialect":     `CREATE INDEX IF NOT EXISTS idx_origins_dialect ON origins(dialect)`,
	"idx_origins_altered":     `CREATE INDEX IF NOT EXISTS idx_origins_altered ON origins(altered_at)`,
	"idx_links_kind":          `CREATE INDEX IF NOT EXISTS idx_links_kind ON links(kind)`,
	"idx_links_origin_kind":   `CREATE INDEX IF NOT EXISTS idx_links_origin_kind ON links(origin, kind)`,
	"idx_links_target_kind":   `CREATE INDEX IF NOT EXISTS idx_links_target_kind ON links(target, kind)`,
}

// BeginBulkParseLoad drops parse-time secondary indexes for bulk (fresh init).
func (v *Vault) BeginBulkParseLoad() error {
	t0 := time.Now()
	for _, idx := range bulkParseIndexes {
		if _, err := v.db.Exec(`DROP INDEX IF EXISTS ` + idx); err != nil {
			return fmt.Errorf("cold: begin bulk drop %s: %w", idx, err)
		}
	}
	v.wtAdd("bulk.drop_idx", 0, time.Since(t0), len(bulkParseIndexes))
	// Also drop edge indexes (keep UNIQUE identity).
	return v.BeginBulkEdgeLoad()
}

// EndBulkParseLoad recreates parse indexes.
func (v *Vault) EndBulkParseLoad() error {
	t0 := time.Now()
	for _, idx := range bulkParseIndexes {
		if ddl, ok := bulkIndexDDLs[idx]; ok {
			if _, err := v.db.Exec(ddl); err != nil {
				return fmt.Errorf("cold: end bulk create %s: %w", idx, err)
			}
		}
	}
	v.wtAdd("bulk.create_idx", 0, time.Since(t0), len(bulkParseIndexes))
	return v.EndBulkEdgeLoad()
}

// BeginBulkEdgeLoad drops edge secondary indexes during resolution bulk.
func (v *Vault) BeginBulkEdgeLoad() error {
	t0 := time.Now()
	for _, idx := range bulkEdgeIndexes {
		if _, err := v.db.Exec(`DROP INDEX IF EXISTS ` + idx); err != nil {
			return fmt.Errorf("cold: begin bulk edge drop %s: %w", idx, err)
		}
	}
	v.wtAdd("bulk.drop_edgeidx", 0, time.Since(t0), len(bulkEdgeIndexes))
	return nil
}

// EndBulkEdgeLoad recreates edge indexes.
func (v *Vault) EndBulkEdgeLoad() error {
	t0 := time.Now()
	for _, idx := range bulkEdgeIndexes {
		if ddl, ok := bulkIndexDDLs[idx]; ok {
			if _, err := v.db.Exec(ddl); err != nil {
				return fmt.Errorf("cold: end bulk edge create %s: %w", idx, err)
			}
		}
	}
	v.wtAdd("bulk.create_edgeidx", 0, time.Since(t0), len(bulkEdgeIndexes))
	return nil
}

// healBulkSecondaryIndexes self-heals missing secondary indexes after crash.
func (v *Vault) healBulkSecondaryIndexes() error {
	all := append([]string{}, bulkParseIndexes...)
	all = append(all, bulkEdgeIndexes...)
	var cnt int
	var sb strings.Builder
	sb.WriteString(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name IN (`)
	for i, idx := range all {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(`'`)
		sb.WriteString(idx)
		sb.WriteString(`'`)
	}
	sb.WriteString(`)`)
	if err := v.db.QueryRow(sb.String()).Scan(&cnt); err != nil {
		return err
	}
	if cnt >= len(all) {
		return nil
	}
	return v.EndBulkParseLoad()
}
