package methods

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"

	"srcinsight/internal/model"
	"srcinsight/internal/storage/cold"
	"srcinsight/transport/stdio"
)

// SearchMode selects the backing cold primitive; auto falls back in order.
type SearchMode string

const (
	ModeAuto      SearchMode = "auto"
	ModeExact     SearchMode = "exact"
	ModePrefix    SearchMode = "prefix"
	ModeSubstring SearchMode = "substring"
	ModeFTS       SearchMode = "fts"
)

// MaxQueryTokens caps tokens per query, bounding SQLite round-trips.
const MaxQueryTokens = 32

func (m SearchMode) Validate() error {
	switch m {
	case "", ModeAuto, ModeExact, ModePrefix, ModeSubstring, ModeFTS:
		return nil
	default:
		return fmt.Errorf("unknown mode %q", string(m))
	}
}

// SearchOptions tunes a search over tokens; file and container narrow after the read.
type SearchOptions struct {
	Query     string     `json:"query"`
	Mode      SearchMode `json:"mode"`
	Limit     int        `json:"limit"`
	File      string     `json:"file"`
	Container string     `json:"container"`
}

// splitQueryTokens splits free text into capped search tokens.
func splitQueryTokens(q string) []string {
	cleaned := strings.Map(func(r rune) rune {
		if r == ',' || r == ';' {
			return ' '
		}
		return r
	}, q)
	toks := strings.Fields(cleaned)
	if len(toks) > MaxQueryTokens {
		toks = toks[:MaxQueryTokens]
	}
	return toks
}

func (o SearchOptions) normalize() SearchOptions {
	if o.Mode == "" {
		o.Mode = ModeAuto
	}
	if o.Limit <= 0 {
		o.Limit = cold.DefaultSearchLimit
	}
	if o.Limit > cold.MaxSearchLimit {
		o.Limit = cold.MaxSearchLimit
	}
	return o
}

// Search runs a flat vault search with no graph expansion.
func Search(ctx context.Context, v *cold.Vault, opts SearchOptions) (model.SearchResult, error) {
	opts = opts.normalize()
	res := model.SearchResult{Query: opts.Query, Hits: []model.SearchHit{}}
	if err := opts.Mode.Validate(); err != nil {
		return res, fmt.Errorf("methods: search: %w", err)
	}
	if len(splitQueryTokens(opts.Query)) == 0 {
		return res, nil
	}
	if err := ctx.Err(); err != nil {
		return res, err
	}

	rows, err := runSearch(ctx, v, opts)
	if err != nil {
		return res, err
	}
	rows = filterRows(rows, opts)
	if len(rows) > opts.Limit {
		rows = rows[:opts.Limit]
	}

	hits := make([]model.SearchHit, 0, len(rows))
	for _, r := range rows {
		hit := model.SearchHit{
			ID:        r.ID,
			Name:      r.Name,
			Qualified: r.Qualified,
			Kind:      r.Kind,
			FilePath:  r.FilePath,
			LineRange: model.Range{
				StartLine: r.StartLine,
				EndLine:   r.EndLine,
			},
			Dialect:   r.Dialect,
			Signature: r.Signature,
			Doc:       r.Doc,
			Tags:      append([]string(nil), r.Tags...),
			Score:     r.Score,
			Mode:      r.Mode,
			Rank:      r.Rank,
		}
		if r.VesselName != "" {
			hit.Vessel = &model.Container{
				Kind: r.VesselKind,
				ID:   r.VesselID,
				Name: r.VesselName,
			}
		}
		hits = append(hits, hit)
	}
	res.Hits = hits

	entries, err := countIndexed(ctx, v)
	if err != nil {
		return res, err
	}
	tracked, err := countTracked(ctx, v)
	if err != nil {
		return res, err
	}
	res.Stats = model.Stats{
		FilesScanned:     tracked,
		FunctionsIndexed: entries,
		NodesReturned:    len(hits),
	}
	return res, nil
}

// runSearch dispatches modes per token and unions pages; token order wins.
func runSearch(ctx context.Context, v *cold.Vault, opts SearchOptions) ([]cold.ScoredEntryRow, error) {
	tokens := splitQueryTokens(opts.Query)
	fetchLimit := opts.Limit
	if opts.File != "" || opts.Container != "" {
		fetchLimit = cold.MaxSearchLimit
	}
	one := func(mode SearchMode, token string) ([]cold.ScoredEntryRow, error) {
		switch mode {
		case ModeExact:
			return v.SearchExact(ctx, token, fetchLimit)
		case ModePrefix:
			return v.SearchPrefix(ctx, token, fetchLimit)
		case ModeSubstring:
			return v.SearchSubstring(ctx, token, fetchLimit)
		case ModeFTS:
			return v.SearchFTS(ctx, token, fetchLimit)
		default:
			return nil, fmt.Errorf("methods: search: unknown mode %q", string(mode))
		}
	}
	autoOne := func(token string) ([]cold.ScoredEntryRow, error) {
		for _, mode := range []SearchMode{ModeExact, ModePrefix, ModeFTS, ModeSubstring} {
			rows, err := one(mode, token)
			if err != nil {
				return nil, err
			}
			if len(rows) > 0 {
				return rows, nil
			}
		}
		return nil, nil
	}
	var out []cold.ScoredEntryRow
	seen := make(map[model.EntryID]struct{})
	// Explicit FTS keeps whole-query AND semantics.
	if opts.Mode == ModeFTS {
		return one(ModeFTS, opts.Query)
	}
	for _, tok := range tokens {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var rows []cold.ScoredEntryRow
		var err error
		if opts.Mode != ModeAuto {
			rows, err = one(opts.Mode, tok)
		} else {
			rows, err = autoOne(tok)
		}
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			if _, dup := seen[r.ID]; dup {
				continue
			}
			seen[r.ID] = struct{}{}
			out = append(out, r)
		}
	}
	return out, nil
}

// filterRows applies file and container narrowing in vault order.
func filterRows(rows []cold.ScoredEntryRow, opts SearchOptions) []cold.ScoredEntryRow {
	if opts.File == "" && opts.Container == "" {
		return rows
	}
	out := rows[:0]
	for _, r := range rows {
		if opts.File != "" && r.FilePath != opts.File {
			continue
		}
		if opts.Container != "" && r.VesselName != opts.Container && path.Base(r.FilePath) != opts.Container {
			continue
		}
		out = append(out, r)
	}
	return out
}

func countIndexed(ctx context.Context, v *cold.Vault) (int, error) {
	entries, _, err := v.Counts(ctx)
	if err != nil {
		return 0, fmt.Errorf("methods: search: %w", err)
	}
	return entries, nil
}

func countTracked(ctx context.Context, v *cold.Vault) (int, error) {
	tracked, err := v.FetchTracked(ctx)
	if err != nil {
		return 0, fmt.Errorf("methods: search: %w", err)
	}
	return len(tracked), nil
}

func (s *Service) HandleSearch(ctx context.Context, raw json.RawMessage) (any, *stdio.RPCError) {
	var opts SearchOptions
	if len(raw) != 0 {
		if err := json.Unmarshal(raw, &opts); err != nil {
			return nil, &stdio.RPCError{Code: stdio.CodeInvalidParams, Message: fmt.Sprintf("search: invalid params: %v", err)}
		}
	}
	if err := opts.Mode.Validate(); err != nil {
		return nil, &stdio.RPCError{Code: stdio.CodeInvalidParams, Message: fmt.Sprintf("search: %v", err)}
	}
	res, err := Search(ctx, s.vault, opts)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, &stdio.RPCError{Code: stdio.CodeInternalError, Message: "search: cancelled"}
		}
		return nil, &stdio.RPCError{Code: stdio.CodeInternalError, Message: fmt.Sprintf("search: %v", err)}
	}
	return res, nil
}
