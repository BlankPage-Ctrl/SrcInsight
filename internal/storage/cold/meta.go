package cold

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
)

// Meta keys owned by the vault itself. Orchestrator-level keys (extractor
// version, project root) live alongside under their own names.
const (
	MetaRoot       = "root"
	MetaExtractor  = "extractor"
	MetaTraceStats = "trace_stats"
)

// TraceStats summarizes vault shape so trace can size anchor breadth without
// aggregate scans at query time. Persisted under MetaTraceStats.
type TraceStats struct {
	Entries int `json:"entries"`
	Files   int `json:"files"`
	Dirs    int `json:"dirs"`
}

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
		// Trace stats describe content, so a wipe must not leave them stale.
		if _, err := tx.ExecContext(ctx, `DELETE FROM vault_meta WHERE key = ?`, MetaTraceStats); err != nil {
			return fmt.Errorf("cold: wipe: %w", err)
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

// dirOf strips the basename so entries in one folder share a bucket.
func dirOf(filePath string) string {
	if i := strings.LastIndexByte(filePath, '/'); i >= 0 {
		return filePath[:i]
	}
	return filePath
}

// ComputeTraceStats walks the entry set once, grouping by file so entries,
// files, and distinct directories all fall out of a single pass.
func (v *Vault) ComputeTraceStats(ctx context.Context) (TraceStats, error) {
	rows, err := v.db.QueryContext(ctx, `SELECT file_path, COUNT(*) FROM entries GROUP BY file_path`)
	if err != nil {
		return TraceStats{}, fmt.Errorf("cold: trace stats: %w", err)
	}
	defer rows.Close()
	var st TraceStats
	dirs := make(map[string]struct{})
	for rows.Next() {
		var file string
		var n int
		if err := rows.Scan(&file, &n); err != nil {
			return TraceStats{}, fmt.Errorf("cold: trace stats: %w", err)
		}
		st.Entries += n
		st.Files++
		dirs[dirOf(file)] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return TraceStats{}, fmt.Errorf("cold: trace stats: %w", err)
	}
	st.Dirs = len(dirs)
	return st, nil
}

// PutTraceStats persists the vault shape summary as JSON.
func (v *Vault) PutTraceStats(ctx context.Context, st TraceStats) error {
	b, err := json.Marshal(st)
	if err != nil {
		return fmt.Errorf("cold: put trace stats: %w", err)
	}
	return v.PutMeta(ctx, MetaTraceStats, string(b))
}

// GetTraceStats reads the summary; malformed or absent values report found=false.
func (v *Vault) GetTraceStats(ctx context.Context) (TraceStats, bool, error) {
	val, found, err := v.GetMeta(ctx, MetaTraceStats)
	if err != nil || !found {
		return TraceStats{}, false, err
	}
	var st TraceStats
	if err := json.Unmarshal([]byte(val), &st); err != nil {
		return TraceStats{}, false, nil
	}
	return st, true, nil
}
