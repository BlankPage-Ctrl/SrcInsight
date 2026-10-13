package cold

import (
	"database/sql"
	"fmt"
)

// statements holds every prepared statement, prepared once at Open and
// reused for the vault lifetime.
type statements struct {
	upsertOrigin          *sql.Stmt
	deleteOrigin          *sql.Stmt
	originByPath          *sql.Stmt
	allOrigins            *sql.Stmt
	insertEntry           *sql.Stmt
	entriesByFile         *sql.Stmt
	allEntries            *sql.Stmt
	deleteEntriesByFile   *sql.Stmt
	entryByID             *sql.Stmt
	insertLink            *sql.Stmt
	allLinks              *sql.Stmt
	incomingLinks         *sql.Stmt
	targetByKindName      *sql.Stmt
	targetByFileAndName   *sql.Stmt
	insertBroughtIn       *sql.Stmt
	deleteBroughtInByFile *sql.Stmt
	allBroughtIn          *sql.Stmt
	broughtInByFile       *sql.Stmt
	insertRecvBind        *sql.Stmt
	deleteRecvBindByFile  *sql.Stmt
	recvBindsByFile       *sql.Stmt
	recvBindsByType       *sql.Stmt
	looseEndsByFile       *sql.Stmt
	insertLooseEnd        *sql.Stmt
	allLooseEnds          *sql.Stmt
	deleteLooseEnd        *sql.Stmt
	looseEndsByOwner      *sql.Stmt
	putMeta               *sql.Stmt
	getMeta               *sql.Stmt
	countEntries          *sql.Stmt
	countLinks            *sql.Stmt
	searchExact           *sql.Stmt
	searchPrefix          *sql.Stmt
	searchSubstr          *sql.Stmt
	searchFTS             *sql.Stmt
	// Fixed-bucket file plus names lookups: one prepared statement per size.
	searchTargetsB32  *sql.Stmt
	searchTargetsB128 *sql.Stmt
	searchTargetsB500 *sql.Stmt
}

// targetBucketSizes are the IN-list capacities. Max vars stays under the
// SQLite 999 limit.
var targetBucketSizes = []int{32, 128, 500}

// targetBucketSQL builds the file+names lookup for exactly n name slots.
func targetBucketSQL(n int) string {
	return `SELECT id, kind, name, qualified_name, file_path, dialect, start_line, end_line, signature, doc, test_title, tags, vessel_kind, vessel_name, vessel_id FROM entries WHERE file_path = ? AND lower(name) IN (` + placeholdersCache[n] + `) ORDER BY file_path, lower(name), id`
}

func prepareAll(db *sql.DB) (*statements, error) {
	s := &statements{}
	defs := []struct {
		dest **sql.Stmt
		sql  string
	}{
		{&s.upsertOrigin, `INSERT INTO origins (path, fingerprint, dialect, bytes, altered_at, cataloged_at, entry_count, faults)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(path) DO UPDATE SET fingerprint=excluded.fingerprint, dialect=excluded.dialect,
				bytes=excluded.bytes, altered_at=excluded.altered_at, cataloged_at=excluded.cataloged_at,
				entry_count=excluded.entry_count, faults=excluded.faults`},
		{&s.deleteOrigin, `DELETE FROM origins WHERE path = ?`},
		{&s.originByPath, `SELECT path, fingerprint, dialect, bytes, altered_at, cataloged_at, entry_count, faults FROM origins WHERE path = ?`},
		{&s.allOrigins, `SELECT path, fingerprint, dialect, bytes, altered_at, cataloged_at, entry_count, faults FROM origins ORDER BY path`},
		{&s.insertEntry, `INSERT OR REPLACE INTO entries (id, kind, name, qualified_name, file_path, dialect, start_line, end_line, signature, doc, test_title, tags, vessel_kind, vessel_name, vessel_id)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`},
		{&s.entriesByFile, `SELECT id, kind, name, qualified_name, file_path, dialect, start_line, end_line, signature, doc, test_title, tags, vessel_kind, vessel_name, vessel_id FROM entries WHERE file_path = ? ORDER BY id`},
		{&s.allEntries, `SELECT id, kind, name, qualified_name, file_path, dialect, start_line, end_line, signature, doc, test_title, tags, vessel_kind, vessel_name, vessel_id FROM entries ORDER BY id`},
		{&s.deleteEntriesByFile, `DELETE FROM entries WHERE file_path = ?`},
		{&s.entryByID, `SELECT id, kind, name, qualified_name, file_path, dialect, start_line, end_line, signature, doc, test_title, tags, vessel_kind, vessel_name, vessel_id FROM entries WHERE id = ?`},
		{&s.insertLink, `INSERT OR IGNORE INTO links (origin, target, kind, site_start, site_end, cond) VALUES (?, ?, ?, ?, ?, ?)`},
		{&s.allLinks, `SELECT origin, target, kind, site_start, site_end, cond FROM links ORDER BY origin, target, kind, site_start, site_end`},
		// Incoming cross-file links carry the (kind, name) re-resolution key.
		{&s.incomingLinks, `SELECT l.origin, l.kind, t.kind, t.name, l.site_start, l.site_end, l.cond FROM links l
			JOIN entries t ON t.id = l.target
			JOIN entries o ON o.id = l.origin
			WHERE t.file_path = ? AND o.file_path <> ?`},
		{&s.targetByKindName, `SELECT id FROM entries WHERE file_path = ? AND kind = ? AND name = ? ORDER BY id LIMIT 1`},
		{&s.targetByFileAndName, `SELECT id, kind FROM entries WHERE file_path = ? AND lower(name) = lower(?) ORDER BY id`},
		{&s.insertBroughtIn, `INSERT INTO brought_in (file_path, local_name, source, kind, alias) VALUES (?, ?, ?, ?, ?)`},
		{&s.deleteBroughtInByFile, `DELETE FROM brought_in WHERE file_path = ?`},
		{&s.allBroughtIn, `SELECT file_path, local_name, source, kind, alias FROM brought_in ORDER BY file_path, local_name`},
		{&s.broughtInByFile, `SELECT file_path, local_name, source, kind, alias FROM brought_in WHERE file_path = ? ORDER BY local_name`},
		{&s.insertRecvBind, `INSERT INTO recv_binds (file_path, owner, recv, type_name, via_call) VALUES (?, ?, ?, ?, ?)`},
		{&s.deleteRecvBindByFile, `DELETE FROM recv_binds WHERE file_path = ?`},
		{&s.recvBindsByFile, `SELECT file_path, owner, recv, type_name, via_call FROM recv_binds WHERE file_path = ? ORDER BY id`},
		{&s.recvBindsByType, `SELECT file_path, owner, recv, type_name, via_call FROM recv_binds WHERE type_name = ? ORDER BY file_path`},
		{&s.looseEndsByFile, `SELECT from_entry, ref_name, ref_kind, scope, file_path, dialect, site_start, site_end, cond FROM loose_ends WHERE file_path = ? ORDER BY from_entry, ref_name, site_start, site_end`},
		{&s.insertLooseEnd, `INSERT INTO loose_ends (from_entry, ref_name, ref_kind, scope, file_path, dialect, site_start, site_end, cond) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`},
		{&s.allLooseEnds, `SELECT from_entry, ref_name, ref_kind, scope, file_path, dialect, site_start, site_end, cond FROM loose_ends ORDER BY from_entry, ref_name, site_start, site_end`},
		{&s.deleteLooseEnd, `DELETE FROM loose_ends WHERE from_entry = ? AND ref_name = ? AND ref_kind = ? AND site_start = ? AND site_end = ?`},
		{&s.looseEndsByOwner, `SELECT from_entry, ref_name, ref_kind, scope, file_path, dialect, site_start, site_end, cond FROM loose_ends WHERE from_entry = ? ORDER BY ref_name, ref_kind, site_start, site_end`},
		{&s.putMeta, `INSERT INTO vault_meta (key, value, updated_at) VALUES (?, ?, ?)
			ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at`},
		{&s.getMeta, `SELECT value FROM vault_meta WHERE key = ?`},
		{&s.countEntries, `SELECT COUNT(*) FROM entries`},
		{&s.countLinks, `SELECT COUNT(*) FROM links`},
		// LIKE patterns arrive pre-escaped; lower(name) rides idx_entries_lower_name.
		{&s.searchExact, `SELECT id, kind, name, qualified_name, file_path, dialect, start_line, end_line, signature, doc, test_title, tags, vessel_kind, vessel_name, vessel_id, 0.0 FROM entries
			WHERE lower(name) = lower(?) OR lower(qualified_name) = lower(?) ORDER BY id LIMIT ?`},
		{&s.searchPrefix, `SELECT id, kind, name, qualified_name, file_path, dialect, start_line, end_line, signature, doc, test_title, tags, vessel_kind, vessel_name, vessel_id, 0.0 FROM entries
			WHERE lower(name) LIKE ? ESCAPE '\' OR lower(qualified_name) LIKE ? ESCAPE '\' ORDER BY id LIMIT ?`},
		{&s.searchSubstr, `SELECT id, kind, name, qualified_name, file_path, dialect, start_line, end_line, signature, doc, test_title, tags, vessel_kind, vessel_name, vessel_id, 0.0 FROM entries
			WHERE lower(name) LIKE ? ESCAPE '\' OR lower(qualified_name) LIKE ? ESCAPE '\' ORDER BY id LIMIT ?`},
		// FTS weights: name 20, qualified 10, doc 3; bm25 negative, smaller first.
		{&s.searchFTS, `SELECT e.id, e.kind, e.name, e.qualified_name, e.file_path, e.dialect, e.start_line, e.end_line, e.signature, e.doc, e.test_title, e.tags, e.vessel_kind, e.vessel_name, e.vessel_id,
			bm25(entries_fts, 0, 20, 10, 0, 3)
			FROM entries_fts JOIN entries e ON e.rowid = entries_fts.rowid
			WHERE entries_fts MATCH ? ORDER BY bm25(entries_fts, 0, 20, 10, 0, 3), e.id LIMIT ?`},
		{&s.searchTargetsB32, targetBucketSQL(32)},
		{&s.searchTargetsB128, targetBucketSQL(128)},
		{&s.searchTargetsB500, targetBucketSQL(500)},
	}
	for _, d := range defs {
		stmt, err := db.Prepare(d.sql)
		if err != nil {
			return nil, fmt.Errorf("cold: prepare statement: %w", err)
		}
		*d.dest = stmt
	}
	return s, nil
}
