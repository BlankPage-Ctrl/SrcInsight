// Package cold is the durable embedded storage layer for srcinsight.
// Hot stays the live read model; cold is the vault on disk for partial updates.
package cold

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// DBFileName is the database file name inside the state directory.
const DBFileName = "srcinsight.db"

// StateDirName is the state directory name inside a project root. The file
// watcher ignores it, so cold writes never retrigger a sync.
const StateDirName = ".srcinsight"

// DefaultPath returns the conventional database path for a project root,
// creating the state directory if needed.
func DefaultPath(root string) (string, error) {
	dir := filepath.Join(root, StateDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("cold: create state dir: %w", err)
	}
	return filepath.Join(dir, DBFileName), nil
}

// ResolveDBPath returns the database file path for a project root plus an
// optional override. Relative overrides resolve against root, not cwd.
func ResolveDBPath(root, dbOverride string) (string, error) {
	trimmed := strings.TrimSpace(dbOverride)
	if trimmed == "" {
		return DefaultPath(root)
	}
	// SQLite DSN passthrough (no dir creation): :memory: and file: URIs.
	if trimmed == ":memory:" || strings.HasPrefix(trimmed, "file:") {
		return trimmed, nil
	}
	// Remote URLs used to pass through to the libSQL driver; fail loudly now.
	if strings.Contains(trimmed, "://") {
		return "", fmt.Errorf("cold: remote database URL %q is not supported (local paths only)", trimmed)
	}
	// Relative resolves against root, not cwd.
	candidate := trimmed
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(root, candidate)
	}
	candidate = filepath.Clean(candidate)

	// Existing path: disambiguate dir vs file via Stat.
	if info, err := os.Stat(candidate); err == nil {
		if info.IsDir() {
			dbFile := filepath.Join(candidate, DBFileName)
			if _, err := os.Stat(dbFile); err == nil {
				return dbFile, os.MkdirAll(candidate, 0o755)
			}
			if err := os.MkdirAll(candidate, 0o755); err != nil {
				return "", fmt.Errorf("cold: create db dir: %w", err)
			}
			return dbFile, nil
		}
		// Existing file: use as-is, ensure parent exists.
		if err := os.MkdirAll(filepath.Dir(candidate), 0o755); err != nil {
			return "", fmt.Errorf("cold: create db parent dir: %w", err)
		}
		return candidate, nil
	}

	// Non-existing path: heuristic by extension.
	ext := strings.ToLower(filepath.Ext(candidate))
	isFileExt := ext == ".db" || ext == ".sqlite" || ext == ".sqlite3" || ext == ".libsql"
	// Also treat explicit DBFileName basename as file even without ext check (defensive).
	if isFileExt || strings.EqualFold(filepath.Base(candidate), DBFileName) {
		if err := os.MkdirAll(filepath.Dir(candidate), 0o755); err != nil {
			return "", fmt.Errorf("cold: create db parent dir: %w", err)
		}
		return candidate, nil
	}
	// Treat as directory that will contain srcinsight.db
	if err := os.MkdirAll(candidate, 0o755); err != nil {
		return "", fmt.Errorf("cold: create db dir: %w", err)
	}
	return filepath.Join(candidate, DBFileName), nil
}

// Vault is a durable, file-backed function graph store. Open with Open.
// Callers serialize writers: at most one sync or replace runs at a time.
type Vault struct {
	db   *sql.DB
	path string

	ident fileIdent // file identity at open time; see replacedOnDisk.

	stmts *statements

	wt *writeTimings // write-stage breakdown; only filled when timings env is set.

	savedCacheSpill int  // cache_spill value before the bulk window.
	hasCacheSpill   bool // whether savedCacheSpill was captured.
}

// Open opens the vault at dbPath and runs pending migrations.
// Bare paths become file: URLs for the embedded driver.
func Open(dbPath string) (*Vault, error) {
	if strings.Contains(dbPath, "://") && !strings.HasPrefix(strings.TrimSpace(dbPath), "file:") {
		return nil, fmt.Errorf("cold: remote database URL %q is not supported (local paths only)", dbPath)
	}
	db, err := sql.Open("sqlite", dbURL(dbPath))
	if err != nil {
		return nil, fmt.Errorf("cold: open %s: %w", dbPath, err)
	}
	// Single pooled connection matches the single-writer discipline.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(5 * time.Minute)
	db.SetConnMaxIdleTime(1 * time.Minute)

	v := &Vault{db: db, path: dbPath}
	if err := v.configure(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := v.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	stmts, err := prepareAll(db)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	v.stmts = stmts
	// Heal a bulk-load window left open by a crash.
	if err := v.healBulkLoad(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := v.healBulkSecondaryIndexes(); err != nil {
		_ = db.Close()
		return nil, err
	}
	// sql.Open is lazy: the file may only exist now, after migrations wrote.
	v.ident = captureFileIdent(dbPath)
	return v, nil
}

// Path returns the database path this vault was opened with.
func (v *Vault) Path() string { return v.path }

// DB returns the underlying *sql.DB for filtered reads outside transactions.
func (v *Vault) DB() *sql.DB { return v.db }

// Close folds WAL pages back and closes the vault. Idempotent.
func (v *Vault) Close() error {
	if v.db == nil {
		return nil
	}
	// Fold WAL pages back and refresh the planner statistics on a graceful
	// shutdown so the next open starts warm.
	_, _ = v.db.Exec(`PRAGMA wal_checkpoint(PASSIVE)`)
	_, _ = v.db.Exec(`PRAGMA optimize`)
	err := v.db.Close()
	v.db = nil
	return err
}

// Transact runs fn inside one transaction, rolling back on error.
func (v *Vault) Transact(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := v.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("cold: begin: %w", err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("cold: commit: %w", err)
	}
	return nil
}

// configure applies the connection recipe. busy_timeout runs first so a
// locked open waits instead of failing; pragmas drain rows some drivers return.
func (v *Vault) configure() error {
	pragmas := []string{
		`PRAGMA busy_timeout = 5000`,
		`PRAGMA foreign_keys = ON`,
		`PRAGMA journal_mode = WAL`,
		`PRAGMA synchronous = NORMAL`,
		`PRAGMA cache_size = -64000`,
		`PRAGMA temp_store = MEMORY`,
		`PRAGMA mmap_size = 268435456`,
		`PRAGMA journal_size_limit = 67108864`,
	}
	for _, p := range pragmas {
		if err := v.drain(p); err != nil {
			return fmt.Errorf("cold: %s: %w", p, err)
		}
	}
	return nil
}

// dbURL normalizes a vault path: bare paths become file: URLs.
func dbURL(path string) string {
	if path == ":memory:" || strings.HasPrefix(path, "file:") {
		return path
	}
	return "file:" + path
}

// drain runs a statement that may or may not produce rows and discards them.
func (v *Vault) drain(query string, args ...any) error {
	rows, err := v.db.Query(query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
	}
	return rows.Err()
}

// DrainPragma runs a PRAGMA statement outside the prepared set.
func (v *Vault) DrainPragma(query string) error { return v.drain(query) }

// GetWalSizeBytes returns the WAL file size in bytes (0 if absent).
func (v *Vault) GetWalSizeBytes() int64 {
	if v.path == ":memory:" {
		return 0
	}
	st, err := os.Stat(v.path + "-wal")
	if err != nil {
		return 0
	}
	return st.Size()
}

// GetDbFileSizeBytes returns the main DB file size in bytes (0 if absent).
func (v *Vault) GetDbFileSizeBytes() int64 {
	if v.path == ":memory:" {
		return 0
	}
	st, err := os.Stat(v.path)
	if err != nil {
		return 0
	}
	return st.Size()
}

func (v *Vault) GetWalAutocheckpoint() int {
	var n int
	_ = v.db.QueryRow(`PRAGMA wal_autocheckpoint`).Scan(&n)
	return n
}

func (v *Vault) SetWalAutocheckpoint(n int) error {
	return v.drain(fmt.Sprintf(`PRAGMA wal_autocheckpoint = %d`, n))
}

func (v *Vault) GetJournalMode() string {
	var m string
	_ = v.db.QueryRow(`PRAGMA journal_mode`).Scan(&m)
	return m
}

// CheckpointWalPassive runs wal_checkpoint(PASSIVE) without blocking the writer.
func (v *Vault) CheckpointWalPassive() error {
	// Tolerates the row-returning driver quirk.
	return v.drain(`PRAGMA wal_checkpoint(PASSIVE)`)
}

// CheckpointWalTruncate runs a wal_checkpoint(TRUNCATE) at a parked barrier.
func (v *Vault) CheckpointWalTruncate() error {
	return v.drain(`PRAGMA wal_checkpoint(TRUNCATE)`)
}

// replacedOnDisk reports whether the open file was replaced on disk.
// Identity is dev+ino on Unix and volume+file-index on Windows.
func (v *Vault) replacedOnDisk() bool {
	if v.path == ":memory:" || v.ident.invalid() {
		return false
	}
	cur := captureFileIdent(v.path)
	if cur.invalid() {
		return true
	}
	return !v.ident.sameFile(cur)
}

func nowMilli() int64 { return time.Now().UnixMilli() }
