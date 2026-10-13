package cold

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// Multi-row chunk sizes: rows x cols must stay under the SQLite 999-variable
// limit per statement.
const (
	bulkEntryChunk = 50  // 50 x 13 cols = 650 vars
	bulkLinkChunk  = 200 // 200 x 3 cols = 600 vars
	bulkLooseChunk = 150 // 150 x 6 cols = 900 vars
)

// bulkImportChunkSafe covers 5 cols: 150 x 5 = 750 vars.
const bulkImportChunkSafe = 150

// ReplaceOriginsBatch writes bundles in one transaction with ReplaceOrigin
// semantics. Pair with the bulk-load window on the fresh-index path.
func (v *Vault) ReplaceOriginsBatch(ctx context.Context, bundles []OriginBundle) error {
	if len(bundles) == 0 {
		return nil
	}
	if len(bundles) == 1 {
		return v.ReplaceOrigin(ctx, bundles[0])
	}
	return v.Transact(ctx, func(tx *sql.Tx) error {
		return v.replaceBundlesTx(ctx, tx, bundles, nil)
	})
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
			if err := v.insertEntriesBatchTx(ctx, tx, chunk); err != nil {
				return err
			}
			if err := v.insertRestBatchTx(ctx, tx, chunk); err != nil {
				return err
			}
			return v.upsertOriginsBatchTx(ctx, tx, chunk)
		}); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	// Links last: all entry IDs are now visible for the existence check.
	return v.Transact(ctx, func(tx *sql.Tx) error {
		return v.insertLinksBatchTx(ctx, tx, bundles)
	})
}

// replaceBundlesTx runs the batch body inside tx. Snapshots come from
// preSnapshot when non-nil, else they are taken before the first delete.
func (v *Vault) replaceBundlesTx(ctx context.Context, tx *sql.Tx, bundles []OriginBundle, preSnapshot map[string][]incomingLink) error {
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
	if err := v.insertEntriesBatchTx(ctx, tx, bundles); err != nil {
		return err
	}
	if err := v.insertLinksBatchTx(ctx, tx, bundles); err != nil {
		return err
	}
	if err := v.insertRestBatchTx(ctx, tx, bundles); err != nil {
		return err
	}
	for _, b := range bundles {
		if err := v.reattachIncomingTx(ctx, tx, b.Origin.Path, snaps[b.Origin.Path]); err != nil {
			return err
		}
	}
	if err := v.upsertOriginsBatchTx(ctx, tx, bundles); err != nil {
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
func (v *Vault) insertEntriesBatchTx(ctx context.Context, tx *sql.Tx, bundles []OriginBundle) error {
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
	for i := 0; i < len(rows); i += bulkEntryChunk {
		end := i + bulkEntryChunk
		if end > len(rows) {
			end = len(rows)
		}
		chunk := rows[i:end]
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
		if _, err := tx.ExecContext(ctx, sb.String(), args...); err != nil {
			return fmt.Errorf("cold: batch entries [%d:%d]: %w", i, end, err)
		}
	}
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
func (v *Vault) insertLinksBatchTx(ctx context.Context, tx *sql.Tx, bundles []OriginBundle) error {
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
	present, err := v.existingIDs(ctx, tx, linkEndpoints(all))
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
	for i := 0; i < len(keep); i += bulkLinkChunk {
		end := i + bulkLinkChunk
		if end > len(keep) {
			end = len(keep)
		}
		chunk := keep[i:end]
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
		if _, err := tx.ExecContext(ctx, sb.String(), args...); err != nil {
			return fmt.Errorf("cold: batch links [%d:%d]: %w", i, end, err)
		}
	}
	return nil
}

// insertRestBatchTx writes all imports + loose ends + recv binds multi-row.
func (v *Vault) insertRestBatchTx(ctx context.Context, tx *sql.Tx, bundles []OriginBundle) error {
	totalImps, totalLoose, totalBinds := 0, 0, 0
	for _, b := range bundles {
		totalImps += len(b.Imports)
		totalLoose += len(b.LooseEnds)
		totalBinds += len(b.RecvBinds)
	}
	imps := make([]ImportRow, 0, totalImps)
	loose := make([]LooseEndRow, 0, totalLoose)
	binds := make([]RecvBindRow, 0, totalBinds)
	for _, b := range bundles {
		imps = append(imps, b.Imports...)
		for _, r := range b.LooseEnds {
			if r.From == "" || r.Name == "" {
				continue
			}
			loose = append(loose, r)
		}
		for _, r := range b.RecvBinds {
			if r.Valid() {
				binds = append(binds, r)
			}
		}
	}
	for i := 0; i < len(imps); i += bulkImportChunkSafe {
		end := i + bulkImportChunkSafe
		if end > len(imps) {
			end = len(imps)
		}
		chunk := imps[i:end]
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
		if _, err := tx.ExecContext(ctx, sb.String(), args...); err != nil {
			return fmt.Errorf("cold: batch imports [%d:%d]: %w", i, end, err)
		}
	}
	for i := 0; i < len(loose); i += bulkLooseChunk {
		end := i + bulkLooseChunk
		if end > len(loose) {
			end = len(loose)
		}
		chunk := loose[i:end]
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
		if _, err := tx.ExecContext(ctx, sb.String(), args...); err != nil {
			return fmt.Errorf("cold: batch loose ends [%d:%d]: %w", i, end, err)
		}
	}
	// Recv binds: 5 cols x 150 = 750 vars per chunk, same budget as imports.
	for i := 0; i < len(binds); i += bulkImportChunkSafe {
		end := i + bulkImportChunkSafe
		if end > len(binds) {
			end = len(binds)
		}
		chunk := binds[i:end]
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
		if _, err := tx.ExecContext(ctx, sb.String(), args...); err != nil {
			return fmt.Errorf("cold: batch recv binds [%d:%d]: %w", i, end, err)
		}
	}
	return nil
}

// upsertOriginsBatchTx records origin rows multi-row (chunked: 100 x 8).
func (v *Vault) upsertOriginsBatchTx(ctx context.Context, tx *sql.Tx, bundles []OriginBundle) error {
	if len(bundles) == 0 {
		return nil
	}
	const chunk = 100
	for i := 0; i < len(bundles); i += chunk {
		end := i + chunk
		if end > len(bundles) {
			end = len(bundles)
		}
		part := bundles[i:end]
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
		if _, err := tx.ExecContext(ctx, sb.String(), args...); err != nil {
			return fmt.Errorf("cold: batch origins [%d:%d]: %w", i, end, err)
		}
	}
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
	for _, t := range ftsTriggerNames {
		if _, err := v.db.Exec(`DROP TRIGGER IF EXISTS ` + t); err != nil {
			return fmt.Errorf("cold: begin bulk drop %s: %w", t, err)
		}
	}
	return nil
}

// EndBulkLoad rebuilds the FTS index in one pass, then recreates triggers.
func (v *Vault) EndBulkLoad() error {
	if _, err := v.db.Exec(`INSERT INTO entries_fts(entries_fts) VALUES('rebuild')`); err != nil {
		return fmt.Errorf("cold: end bulk rebuild fts: %w", err)
	}
	for _, ddl := range ftsTriggerDDLs {
		if _, err := v.db.Exec(ddl); err != nil {
			return fmt.Errorf("cold: end bulk recreate trigger: %w", err)
		}
	}
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
	if on {
		if err := v.drain(`PRAGMA wal_autocheckpoint = 0`); err != nil {
			return fmt.Errorf("cold: bulk autocheckpoint off: %w", err)
		}
		if err := v.drain(`PRAGMA synchronous = OFF`); err != nil {
			return fmt.Errorf("cold: bulk synchronous off: %w", err)
		}
		return nil
	}
	if err := v.drain(`PRAGMA synchronous = NORMAL`); err != nil {
		return fmt.Errorf("cold: bulk synchronous restore: %w", err)
	}
	if err := v.drain(`PRAGMA wal_autocheckpoint = 1000`); err != nil {
		return fmt.Errorf("cold: bulk autocheckpoint restore: %w", err)
	}
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
	for _, idx := range bulkParseIndexes {
		if _, err := v.db.Exec(`DROP INDEX IF EXISTS ` + idx); err != nil {
			return fmt.Errorf("cold: begin bulk drop %s: %w", idx, err)
		}
	}
	// Also drop edge indexes (keep UNIQUE identity).
	return v.BeginBulkEdgeLoad()
}

// EndBulkParseLoad recreates parse indexes.
func (v *Vault) EndBulkParseLoad() error {
	for _, idx := range bulkParseIndexes {
		if ddl, ok := bulkIndexDDLs[idx]; ok {
			if _, err := v.db.Exec(ddl); err != nil {
				return fmt.Errorf("cold: end bulk create %s: %w", idx, err)
			}
		}
	}
	return v.EndBulkEdgeLoad()
}

// BeginBulkEdgeLoad drops edge secondary indexes during resolution bulk.
func (v *Vault) BeginBulkEdgeLoad() error {
	for _, idx := range bulkEdgeIndexes {
		if _, err := v.db.Exec(`DROP INDEX IF EXISTS ` + idx); err != nil {
			return fmt.Errorf("cold: begin bulk edge drop %s: %w", idx, err)
		}
	}
	return nil
}

// EndBulkEdgeLoad recreates edge indexes.
func (v *Vault) EndBulkEdgeLoad() error {
	for _, idx := range bulkEdgeIndexes {
		if ddl, ok := bulkIndexDDLs[idx]; ok {
			if _, err := v.db.Exec(ddl); err != nil {
				return fmt.Errorf("cold: end bulk edge create %s: %w", idx, err)
			}
		}
	}
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
