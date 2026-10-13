package cold

import (
	"context"
	"fmt"
	"strings"
)

// Search paging: LIMIT always applies inside SQL, so only matching rows
// cross the driver boundary.
const (
	// DefaultSearchLimit applies when the caller passes limit <= 0.
	DefaultSearchLimit = 20
	// MaxSearchLimit caps any caller-supplied limit.
	MaxSearchLimit = 200
)

func normSearchLimit(n int) int {
	if n <= 0 {
		return DefaultSearchLimit
	}
	if n > MaxSearchLimit {
		return MaxSearchLimit
	}
	return n
}

// SearchExact returns entries whose name or qualified name equals q,
// case-insensitively. Empty q matches nothing.
func (v *Vault) SearchExact(ctx context.Context, q string, limit int) ([]ScoredEntryRow, error) {
	if strings.TrimSpace(q) == "" {
		return nil, nil
	}
	rows, err := v.stmts.searchExact.QueryContext(ctx, q, q, normSearchLimit(limit))
	if err != nil {
		return nil, fmt.Errorf("cold: search exact %q: %w", q, err)
	}
	defer rows.Close()
	out, err := scanScoredEntries(rows)
	if err != nil {
		return nil, fmt.Errorf("cold: search exact %q: %w", q, err)
	}
	for i := range out {
		out[i].Mode = "exact"
		out[i].Rank = i
	}
	return out, nil
}

// SearchPrefix returns entries whose name starts with q. LIKE wildcards in q
// match literally; lower(name) scans ride idx_entries_lower_name.
func (v *Vault) SearchPrefix(ctx context.Context, q string, limit int) ([]ScoredEntryRow, error) {
	if strings.TrimSpace(q) == "" {
		return nil, nil
	}
	pat := escapeLikePattern(strings.ToLower(q)) + "%"
	rows, err := v.stmts.searchPrefix.QueryContext(ctx, pat, pat, normSearchLimit(limit))
	if err != nil {
		return nil, fmt.Errorf("cold: search prefix %q: %w", q, err)
	}
	defer rows.Close()
	out, err := scanScoredEntries(rows)
	if err != nil {
		return nil, fmt.Errorf("cold: search prefix %q: %w", q, err)
	}
	for i := range out {
		out[i].Mode = "prefix"
		out[i].Rank = i
	}
	return out, nil
}

// SearchSubstring returns entries whose name contains q. LIKE '%..%' cannot
// use the index, so this is a full scan; wildcards still match literally.
func (v *Vault) SearchSubstring(ctx context.Context, q string, limit int) ([]ScoredEntryRow, error) {
	if strings.TrimSpace(q) == "" {
		return nil, nil
	}
	pat := "%" + escapeLikePattern(strings.ToLower(q)) + "%"
	rows, err := v.stmts.searchSubstr.QueryContext(ctx, pat, pat, normSearchLimit(limit))
	if err != nil {
		return nil, fmt.Errorf("cold: search substring %q: %w", q, err)
	}
	defer rows.Close()
	out, err := scanScoredEntries(rows)
	if err != nil {
		return nil, fmt.Errorf("cold: search substring %q: %w", q, err)
	}
	for i := range out {
		out[i].Mode = "substring"
		out[i].Rank = i
	}
	return out, nil
}

// SearchFTS runs a multi-token prefix query over name plus doc, ranked by
// weighted bm25. Quoted tokens make user input injection-safe by construction.
func (v *Vault) SearchFTS(ctx context.Context, q string, limit int) ([]ScoredEntryRow, error) {
	match, ok := buildFTSMatch(q)
	if !ok {
		return nil, nil
	}
	rows, err := v.stmts.searchFTS.QueryContext(ctx, match, normSearchLimit(limit))
	if err != nil {
		return nil, fmt.Errorf("cold: search fts %q: %w", q, err)
	}
	defer rows.Close()
	out, err := scanScoredEntries(rows)
	if err != nil {
		return nil, fmt.Errorf("cold: search fts %q: %w", q, err)
	}
	for i := range out {
		out[i].Mode = "fts"
		out[i].Rank = i
	}
	return out, nil
}

// escapeLikePattern escapes LIKE metacharacters so input matches literally.
func escapeLikePattern(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch r {
		case '\\', '%', '_':
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// buildFTSMatch compiles text into a column-restricted FTS5 MATCH over name
// plus doc; ok is false when no usable token exists.
func buildFTSMatch(q string) (match string, ok bool) {
	var parts []string
	for _, tok := range strings.Fields(q) {
		if tok == "" {
			continue
		}
		parts = append(parts, `"`+strings.ReplaceAll(tok, `"`, `""`)+`"*`)
	}
	if len(parts) == 0 {
		return "", false
	}
	return `{name qualified_name doc} : ` + strings.Join(parts, " "), true
}
