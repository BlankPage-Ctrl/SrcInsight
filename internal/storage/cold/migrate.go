package cold

import (
	"database/sql"
	"fmt"
	"strings"
)

// CurrentLedgerVersion is the newest schema version this code understands.
// Version 1 is baseSchema itself; later versions append a Migration below.
const CurrentLedgerVersion = 23

// Migration is one versioned schema step. Up must be idempotent so a
// half-applied migration re-runs cleanly.
type Migration struct {
	Version int
	Note    string
	Up      string
}

// migrations lists every step after version 1. Each Up holds exactly one
// statement; multi-statement steps split across versions.
var migrations = []Migration{
	{
		Version: 2,
		Note:    "cross-file indexes: brought_in(source), loose_ends(ref_name,scope)",
		Up: `
CREATE INDEX IF NOT EXISTS idx_broughtin_source ON brought_in(source);
CREATE INDEX IF NOT EXISTS idx_looseends_ref_scope ON loose_ends(ref_name, scope);
CREATE INDEX IF NOT EXISTS idx_looseends_ref ON loose_ends(ref_name);
`,
	},
	{
		Version: 3,
		Note:    "receiver bindings: recv_binds per-file receiver to type map",
		Up: `
CREATE TABLE IF NOT EXISTS recv_binds (
    id INTEGER PRIMARY KEY,
    file_path TEXT NOT NULL,
    owner TEXT NOT NULL DEFAULT '',
    recv TEXT NOT NULL,
    type_name TEXT NOT NULL DEFAULT '',
    via_call TEXT NOT NULL DEFAULT ''
);
`,
	},
	{
		Version: 4,
		Note:    "receiver bindings by-file index",
		Up:      `CREATE INDEX IF NOT EXISTS idx_recvbinds_file ON recv_binds(file_path)`,
	},
	{
		Version: 5,
		Note:    "receiver bindings by-type index for reverse promotion",
		Up:      `CREATE INDEX IF NOT EXISTS idx_recvbinds_type ON recv_binds(type_name)`,
	},
	{
		Version: 6,
		Note:    "call-site tracking: links.site_start (0 = unknown, no backfill; reindex for accuracy)",
		Up:      `ALTER TABLE links ADD COLUMN site_start INTEGER NOT NULL DEFAULT 0`,
	},
	{
		Version: 7,
		Note:    "call-site tracking: links.site_end",
		Up:      `ALTER TABLE links ADD COLUMN site_end INTEGER NOT NULL DEFAULT 0`,
	},
	{
		Version: 8,
		Note:    "call-site tracking: loose_ends.site_start",
		Up:      `ALTER TABLE loose_ends ADD COLUMN site_start INTEGER NOT NULL DEFAULT 0`,
	},
	{
		Version: 9,
		Note:    "call-site tracking: loose_ends.site_end",
		Up:      `ALTER TABLE loose_ends ADD COLUMN site_end INTEGER NOT NULL DEFAULT 0`,
	},
	{
		Version: 10,
		Note:    "call-site tracking: drop pair-level link identity",
		Up:      `DROP INDEX IF EXISTS idx_links_identity`,
	},
	{
		Version: 11,
		Note:    "call-site tracking: per-site link identity (origin,target,kind,site_start,site_end)",
		Up:      `CREATE UNIQUE INDEX IF NOT EXISTS idx_links_identity ON links(origin, target, kind, site_start, site_end)`,
	},
	{
		Version: 12,
		Note:    "test-case titles for anonymous test callbacks (entries.test_title, empty until reindex)",
		Up:      `ALTER TABLE entries ADD COLUMN test_title TEXT NOT NULL DEFAULT ''`,
	},
	{
		Version: 13,
		Note:    "entry-level convention tags for framework marks (entries.tags, empty until reindex)",
		Up:      `ALTER TABLE entries ADD COLUMN tags TEXT NOT NULL DEFAULT ''`,
	},
	{
		Version: 14,
		Note:    "branch conditions on calls: links.cond (empty until reindex)",
		Up:      `ALTER TABLE links ADD COLUMN cond TEXT NOT NULL DEFAULT ''`,
	},
	{
		Version: 15,
		Note:    "branch conditions on calls: loose_ends.cond (empty until reindex)",
		Up:      `ALTER TABLE loose_ends ADD COLUMN cond TEXT NOT NULL DEFAULT ''`,
	},
	{
		Version: 16,
		Note:    "transport flow hints: endpoints table (empty until reindex)",
		Up: `
CREATE TABLE IF NOT EXISTS transport_endpoints (
    id INTEGER PRIMARY KEY,
    file_path TEXT NOT NULL,
    owner TEXT NOT NULL DEFAULT '',
    transport TEXT NOT NULL DEFAULT 'http',
    method TEXT NOT NULL DEFAULT '',
    path TEXT NOT NULL DEFAULT '',
    operation TEXT NOT NULL DEFAULT '',
    framework TEXT NOT NULL DEFAULT '',
    site_start INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_tendpoints_method_path ON transport_endpoints(method, path);
CREATE INDEX IF NOT EXISTS idx_tendpoints_operation ON transport_endpoints(operation);
CREATE INDEX IF NOT EXISTS idx_tendpoints_file ON transport_endpoints(file_path);
`,
	},
	{
		Version: 17,
		Note:    "transport flow hints: calls table (empty until reindex)",
		Up: `
CREATE TABLE IF NOT EXISTS transport_calls (
    id INTEGER PRIMARY KEY,
    file_path TEXT NOT NULL,
    owner TEXT NOT NULL DEFAULT '',
    transport TEXT NOT NULL DEFAULT 'http',
    method TEXT NOT NULL DEFAULT '',
    url TEXT NOT NULL DEFAULT '',
    operation TEXT NOT NULL DEFAULT '',
    lib TEXT NOT NULL DEFAULT '',
    site_start INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_tcalls_method ON transport_calls(method);
CREATE INDEX IF NOT EXISTS idx_tcalls_operation ON transport_calls(operation);
CREATE INDEX IF NOT EXISTS idx_tcalls_file ON transport_calls(file_path);
CREATE INDEX IF NOT EXISTS idx_tcalls_owner ON transport_calls(owner);
`,
	},
	{
		Version: 18,
		Note:    "transport endpoints by owner for trace transport hops",
		Up:      `CREATE INDEX IF NOT EXISTS idx_tendpoints_owner ON transport_endpoints(owner)`,
	},
	{
		Version: 19,
		Note:    "call-site identifier columns: links.site_start_col (0 until reindex)",
		Up:      `ALTER TABLE links ADD COLUMN site_start_col INTEGER NOT NULL DEFAULT 0`,
	},
	{
		Version: 20,
		Note:    "call-site identifier columns: links.site_end_col",
		Up:      `ALTER TABLE links ADD COLUMN site_end_col INTEGER NOT NULL DEFAULT 0`,
	},
	{
		Version: 21,
		Note:    "reference identifier columns: loose_ends.site_start_col (0 until reindex)",
		Up:      `ALTER TABLE loose_ends ADD COLUMN site_start_col INTEGER NOT NULL DEFAULT 0`,
	},
	{
		Version: 22,
		Note:    "reference identifier columns: loose_ends.site_end_col",
		Up:      `ALTER TABLE loose_ends ADD COLUMN site_end_col INTEGER NOT NULL DEFAULT 0`,
	},
	{
		Version: 23,
		Note:    "link identity widens to per-identifier site: add site_start_col,site_end_col",
		Up: `
DROP INDEX IF EXISTS idx_links_identity;
CREATE UNIQUE INDEX IF NOT EXISTS idx_links_identity
  ON links(origin, target, kind, site_start, site_end, site_start_col, site_end_col);
`,
	},
}

// migrate brings the database to CurrentLedgerVersion. The splitter stays
// trigger-aware: semicolons inside CREATE TRIGGER bodies must not split.
func (v *Vault) migrate() error {
	for _, stmt := range splitStatements(baseSchema) {
		if _, err := v.db.Exec(stmt); err != nil {
			return fmt.Errorf("cold: apply schema: %w", err)
		}
	}
	current, err := v.ledgerVersion()
	if err != nil {
		return err
	}
	for _, m := range migrations {
		if m.Version <= current {
			continue
		}
		tx, err := v.db.Begin()
		if err != nil {
			return fmt.Errorf("cold: migrate to %d: %w", m.Version, err)
		}
		if _, err := tx.Exec(m.Up); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("cold: migrate to %d (%s): %w", m.Version, m.Note, err)
		}
		if _, err := tx.Exec(
			`INSERT INTO ledger_versions (version, applied_at, note) VALUES (?, ?, ?)`,
			m.Version, nowMilli(), m.Note,
		); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("cold: record migration %d: %w", m.Version, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("cold: commit migration %d: %w", m.Version, err)
		}
		current = m.Version
	}
	if current < CurrentLedgerVersion {
		return fmt.Errorf("cold: schema at v%d, code wants v%d", current, CurrentLedgerVersion)
	}
	return nil
}

// splitStatements cuts a schema script on statement boundaries, keeping
// CREATE TRIGGER bodies (which contain interior semicolons) whole.
func splitStatements(script string) []string {
	var out []string
	var cur strings.Builder
	depth := 0
	flush := func() {
		if s := strings.TrimSpace(cur.String()); s != "" {
			out = append(out, s)
		}
		cur.Reset()
	}
	for _, line := range strings.Split(script, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(strings.ToUpper(trimmed), "CREATE TRIGGER") {
			depth++
		}
		cur.WriteString(line)
		cur.WriteByte('\n')
		if !strings.HasSuffix(trimmed, ";") {
			continue
		}
		if depth > 0 {
			if strings.ToUpper(trimmed) == "END;" {
				depth--
				flush()
			}
			continue
		}
		flush()
	}
	flush()
	return out
}

// ledgerVersion returns the newest recorded schema version, or 0 on a bare
// database that predates the ledger table.
func (v *Vault) ledgerVersion() (int, error) {
	var version sql.NullInt64
	if err := v.db.QueryRow(`SELECT MAX(version) FROM ledger_versions`).Scan(&version); err != nil {
		return 0, fmt.Errorf("cold: read ledger version: %w", err)
	}
	if !version.Valid {
		return 0, nil
	}
	return int(version.Int64), nil
}

// VaultVersion reports the recorded schema version for staleness checks.
func (v *Vault) VaultVersion() (int, error) { return v.ledgerVersion() }
