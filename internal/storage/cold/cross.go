package cold

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"srcinsight/internal/model"
)

// FetchTargetsByFileAndName returns matching entries in one file, filtered
// by file_path plus name.
func (v *Vault) FetchTargetsByFileAndName(ctx context.Context, q querier, file, name string) ([]EntryRow, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, kind, name, qualified_name, file_path, dialect, start_line, end_line, signature, doc, test_title, tags, vessel_kind, vessel_name, vessel_id FROM entries WHERE file_path = ? AND lower(name) = lower(?) ORDER BY id`, file, name)
	if err != nil {
		return nil, fmt.Errorf("cold: fetch targets %s %s: %w", file, name, err)
	}
	defer rows.Close()
	return scanEntries(rows)
}

// FetchTargetsBatch fetches entries for many (file, name) pairs, grouped by
// file. Returns map keyed by file plus name.
func (v *Vault) FetchTargetsBatch(ctx context.Context, q querier, pairs []struct{ File, Name string }) (map[string][]EntryRow, error) {
	out := make(map[string][]EntryRow, len(pairs))
	byFile := make(map[string]map[string]struct{}, len(pairs)/4+1)
	for _, p := range pairs {
		if p.File == "" || p.Name == "" {
			continue
		}
		if _, ok := byFile[p.File]; !ok {
			byFile[p.File] = make(map[string]struct{}, 4)
		}
		byFile[p.File][strings.ToLower(p.Name)] = struct{}{}
	}
	for file, nameSet := range byFile {
		names := make([]string, 0, len(nameSet))
		for n := range nameSet {
			names = append(names, n)
		}
		sort.Strings(names)
		// Fixed buckets keep one prepared statement per size; padding never matches.
		const maxChunk = 500
		for i := 0; i < len(names); i += maxChunk {
			end := i + maxChunk
			if end > len(names) {
				end = len(names)
			}
			sub := names[i:end]
			rows, err := v.fetchTargetsBucket(ctx, q, file, sub)
			if err != nil {
				return nil, fmt.Errorf("cold: batch fetch %s: %w", file, err)
			}
			for _, r := range rows {
				key := file + "\x00" + strings.ToLower(r.Name)
				out[key] = append(out[key], r)
			}
		}
	}
	return out, nil
}

// padName fills an IN-list slot that matches nothing: NUL cannot appear in
// an entry name, and lower() preserves it.
const padName = "\x00"

// fetchTargetsBucket runs one file plus names lookup, padded to a bucket
// so prepared statements stay reusable.
func (v *Vault) fetchTargetsBucket(ctx context.Context, q querier, file string, names []string) ([]EntryRow, error) {
	size := 0
	for _, b := range targetBucketSizes {
		if len(names) <= b {
			size = b
			break
		}
	}
	if size == 0 {
		size = targetBucketSizes[len(targetBucketSizes)-1]
	}
	if q == v.db {
		var stmt *sql.Stmt
		switch size {
		case 32:
			stmt = v.stmts.searchTargetsB32
		case 128:
			stmt = v.stmts.searchTargetsB128
		default:
			stmt = v.stmts.searchTargetsB500
		}
		args := make([]any, 0, size+1)
		args = append(args, file)
		for _, n := range names {
			args = append(args, n)
		}
		for len(args) < size+1 {
			args = append(args, padName)
		}
		rows, err := stmt.QueryContext(ctx, args...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		return scanEntries(rows)
	}
	place := strings.Repeat("?,", len(names))
	place = place[:len(place)-1]
	args := make([]any, 0, len(names)+1)
	args = append(args, file)
	for _, n := range names {
		args = append(args, n)
	}
	qStr := `SELECT id, kind, name, qualified_name, file_path, dialect, start_line, end_line, signature, doc, test_title, tags, vessel_kind, vessel_name, vessel_id FROM entries WHERE file_path = ? AND lower(name) IN (` + place + `) ORDER BY file_path, lower(name), id`
	rows, err := q.QueryContext(ctx, qStr, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEntries(rows)
}

// FetchImportsByFile returns imports for one file (filtered).
func (v *Vault) FetchImportsByFile(ctx context.Context, q querier, file string) ([]ImportRow, error) {
	var rows *sql.Rows
	var err error
	if q == v.db {
		rows, err = v.stmts.broughtInByFile.QueryContext(ctx, file)
	} else if tx, ok := q.(*sql.Tx); ok {
		rows, err = tx.StmtContext(ctx, v.stmts.broughtInByFile).QueryContext(ctx, file)
	} else {
		rows, err = q.QueryContext(ctx, `SELECT file_path, local_name, source, kind, alias FROM brought_in WHERE file_path = ? ORDER BY local_name`, file)
	}
	if err != nil {
		return nil, fmt.Errorf("cold: fetch imports %s: %w", file, err)
	}
	defer rows.Close()
	var out []ImportRow
	for rows.Next() {
		var r ImportRow
		if err := rows.Scan(&r.FilePath, &r.LocalName, &r.Source, &r.Kind, &r.Alias); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// FetchLooseByRefNames returns loose ends matching names with internal scope.
func (v *Vault) FetchLooseByRefNames(ctx context.Context, q querier, names []string, excludeFiles map[string]struct{}) ([]LooseEndRow, error) {
	if len(names) == 0 {
		return nil, nil
	}
	uniq := make(map[string]struct{})
	var lowers []string
	for _, n := range names {
		ln := strings.ToLower(n)
		if _, ok := uniq[ln]; !ok {
			uniq[ln] = struct{}{}
			lowers = append(lowers, n)
		}
	}
	var out []LooseEndRow
	const chunk = 200
	for i := 0; i < len(lowers); i += chunk {
		end := i + chunk
		if end > len(lowers) {
			end = len(lowers)
		}
		sub := lowers[i:end]
		place := strings.Repeat("?,", len(sub))
		place = place[:len(place)-1]
		lowerPlace := make([]string, len(sub))
		args := make([]any, 0, len(sub))
		for idx, n := range sub {
			lowerPlace[idx] = "?"
			args = append(args, strings.ToLower(n))
		}
		qStr := `SELECT from_entry, ref_name, ref_kind, scope, file_path, dialect, site_start, site_end, cond FROM loose_ends WHERE lower(ref_name) IN (` + strings.Join(lowerPlace, ",") + `) AND (scope = 'internal' OR scope LIKE 'internal:%')`
		if len(excludeFiles) > 0 {
			ex := make([]string, 0, len(excludeFiles))
			for f := range excludeFiles {
				ex = append(ex, f)
			}
			if len(ex) <= 100 {
				ph := strings.Repeat("?,", len(ex))
				ph = ph[:len(ph)-1]
				qStr += ` AND file_path NOT IN (` + ph + `)`
				for _, f := range ex {
					args = append(args, f)
				}
			}
		}
		qStr += ` ORDER BY file_path, from_entry`
		rows, err := q.QueryContext(ctx, qStr, args...)
		if err != nil {
			return nil, fmt.Errorf("cold: fetch loose by refs: %w", err)
		}
		for rows.Next() {
			var r LooseEndRow
			if err := rows.Scan(&r.From, &r.Name, &r.Kind, &r.Scope, &r.FilePath, &r.Dialect, &r.SiteStart, &r.SiteEnd, &r.Cond); err != nil {
				rows.Close()
				return nil, err
			}
			out = append(out, r)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return out, nil
}

// DeleteLooseEnd removes one loose-end row by key plus site. The site
// predicate keeps sibling call sites intact.
func (v *Vault) DeleteLooseEnd(ctx context.Context, q querier, from model.EntryID, refName, refKind string, siteStart, siteEnd int) error {
	var err error
	if tx, ok := q.(*sql.Tx); ok {
		_, err = tx.StmtContext(ctx, v.stmts.deleteLooseEnd).ExecContext(ctx, string(from), refName, refKind, siteStart, siteEnd)
	} else {
		_, _ = q.QueryContext(ctx, `SELECT 1`)
		_, err = v.db.ExecContext(ctx, `DELETE FROM loose_ends WHERE from_entry = ? AND ref_name = ? AND ref_kind = ? AND site_start = ? AND site_end = ?`, string(from), refName, refKind, siteStart, siteEnd)
	}
	if err != nil {
		return fmt.Errorf("cold: delete loose %s %s: %w", from, refName, err)
	}
	return nil
}
