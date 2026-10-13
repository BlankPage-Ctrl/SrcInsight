package cold

// baseSchema is the version-1 schema; later versions append in migrate.go.
// Cascades delete per file, UNIQUE identity dedups links, FTS triggers sync.
const baseSchema = `
-- Schema version tracking. Version 1 is this schema; later versions live in
-- migrate.go and must append here exactly once per version.
CREATE TABLE IF NOT EXISTS ledger_versions (
    version INTEGER PRIMARY KEY,
    applied_at INTEGER NOT NULL,
    note TEXT
);
INSERT OR IGNORE INTO ledger_versions (version, applied_at, note)
VALUES (1, (strftime('%s', 'now') * 1000), 'Initial cold schema');

-- Origins: tracked source files. fingerprint is the hex content hash used to
-- confirm real changes after the cheap size+mtime pre-filter in Reconcile.
CREATE TABLE IF NOT EXISTS origins (
    path TEXT PRIMARY KEY,
    fingerprint TEXT NOT NULL,
    dialect TEXT NOT NULL,
    bytes INTEGER NOT NULL,
    altered_at INTEGER NOT NULL,
    cataloged_at INTEGER NOT NULL,
    entry_count INTEGER NOT NULL DEFAULT 0,
    faults TEXT
);

-- Entries: indexed function-like definitions. vessel_* flattens the nearest
-- enclosing class/module container; empty when top-level. Calls, heritage
-- and reverse relations live in links/loose_ends, never here.
CREATE TABLE IF NOT EXISTS entries (
    id TEXT PRIMARY KEY,
    kind TEXT NOT NULL,
    name TEXT NOT NULL,
    qualified_name TEXT NOT NULL,
    file_path TEXT NOT NULL,
    dialect TEXT NOT NULL,
    start_line INTEGER NOT NULL,
    end_line INTEGER NOT NULL,
    signature TEXT NOT NULL DEFAULT '',
    doc TEXT NOT NULL DEFAULT '',
    vessel_kind TEXT NOT NULL DEFAULT '',
    vessel_name TEXT NOT NULL DEFAULT '',
    vessel_id TEXT NOT NULL DEFAULT ''
);

-- Links: resolved directed relationships between entries.
-- (v6-v11 append site_start/site_end: the 1-indexed call-site line range
-- inside origin; v14 appends cond: the branch condition. Kept out of this
-- v1 string per the never-edit rule above.)
CREATE TABLE IF NOT EXISTS links (
    id INTEGER PRIMARY KEY,
    origin TEXT NOT NULL,
    target TEXT NOT NULL,
    kind TEXT NOT NULL,
    FOREIGN KEY (origin) REFERENCES entries(id) ON DELETE CASCADE,
    FOREIGN KEY (target) REFERENCES entries(id) ON DELETE CASCADE
);

-- BroughtIn: per-file import statements, needed by whole-project
-- re-resolution on the incremental path.
CREATE TABLE IF NOT EXISTS brought_in (
    id INTEGER PRIMARY KEY,
    file_path TEXT NOT NULL,
    local_name TEXT NOT NULL,
    source TEXT NOT NULL,
    kind TEXT NOT NULL DEFAULT '',
    alias TEXT NOT NULL DEFAULT ''
);

-- LooseEnds: unresolved call/heritage references. scope preserves the
-- internal/external/runtime/unresolved classification used by stats and
-- display, so it must survive the round trip.
-- (v8-v9 append site_start/site_end like links above; v15 appends cond.)
CREATE TABLE IF NOT EXISTS loose_ends (
    id INTEGER PRIMARY KEY,
    from_entry TEXT NOT NULL,
    ref_name TEXT NOT NULL,
    ref_kind TEXT NOT NULL,
    scope TEXT NOT NULL DEFAULT '',
    file_path TEXT NOT NULL DEFAULT '',
    dialect TEXT NOT NULL DEFAULT '',
    FOREIGN KEY (from_entry) REFERENCES entries(id) ON DELETE CASCADE
);

-- VaultMeta: small key/value provenance (project root, extractor version).
CREATE TABLE IF NOT EXISTS vault_meta (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_entries_kind ON entries(kind);
CREATE INDEX IF NOT EXISTS idx_entries_name ON entries(name);
CREATE INDEX IF NOT EXISTS idx_entries_qualified ON entries(qualified_name);
CREATE INDEX IF NOT EXISTS idx_entries_file ON entries(file_path);
CREATE INDEX IF NOT EXISTS idx_entries_dialect ON entries(dialect);
CREATE INDEX IF NOT EXISTS idx_entries_file_line ON entries(file_path, start_line);
CREATE INDEX IF NOT EXISTS idx_entries_lower_name ON entries(lower(name));

-- Full-text index over the fields the query ranker scores.
CREATE VIRTUAL TABLE IF NOT EXISTS entries_fts USING fts5(
    id,
    name,
    qualified_name,
    signature,
    doc,
    content='entries',
    content_rowid='rowid'
);

CREATE TRIGGER IF NOT EXISTS entries_ai AFTER INSERT ON entries BEGIN
    INSERT INTO entries_fts(rowid, id, name, qualified_name, signature, doc)
    VALUES (NEW.rowid, NEW.id, NEW.name, NEW.qualified_name, NEW.signature, NEW.doc);
END;

CREATE TRIGGER IF NOT EXISTS entries_ad AFTER DELETE ON entries BEGIN
    INSERT INTO entries_fts(entries_fts, rowid, id, name, qualified_name, signature, doc)
    VALUES ('delete', OLD.rowid, OLD.id, OLD.name, OLD.qualified_name, OLD.signature, OLD.doc);
END;

CREATE TRIGGER IF NOT EXISTS entries_au AFTER UPDATE ON entries BEGIN
    INSERT INTO entries_fts(entries_fts, rowid, id, name, qualified_name, signature, doc)
    VALUES ('delete', OLD.rowid, OLD.id, OLD.name, OLD.qualified_name, OLD.signature, OLD.doc);
    INSERT INTO entries_fts(rowid, id, name, qualified_name, signature, doc)
    VALUES (NEW.rowid, NEW.id, NEW.name, NEW.qualified_name, NEW.signature, NEW.doc);
END;

-- Link composites cover narrow lookups via left-prefix scans.
CREATE INDEX IF NOT EXISTS idx_links_kind ON links(kind);
CREATE INDEX IF NOT EXISTS idx_links_origin_kind ON links(origin, kind);
CREATE INDEX IF NOT EXISTS idx_links_target_kind ON links(target, kind);

-- Link identity: what makes INSERT OR IGNORE dedup for real.
-- (v10-v11 widen this to per call site: origin, target, kind,
-- site_start, site_end.)
CREATE UNIQUE INDEX IF NOT EXISTS idx_links_identity
  ON links(origin, target, kind);

CREATE INDEX IF NOT EXISTS idx_broughtin_file ON brought_in(file_path);

CREATE INDEX IF NOT EXISTS idx_looseends_from ON loose_ends(from_entry);
CREATE INDEX IF NOT EXISTS idx_looseends_file ON loose_ends(file_path);

CREATE INDEX IF NOT EXISTS idx_origins_dialect ON origins(dialect);
CREATE INDEX IF NOT EXISTS idx_origins_altered ON origins(altered_at);

CREATE INDEX IF NOT EXISTS idx_broughtin_source ON brought_in(source);
CREATE INDEX IF NOT EXISTS idx_looseends_ref_scope ON loose_ends(ref_name, scope);
CREATE INDEX IF NOT EXISTS idx_looseends_ref ON loose_ends(ref_name);
`
