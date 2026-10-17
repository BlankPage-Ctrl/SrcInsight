package methods

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"srcinsight/internal/model"
	"srcinsight/internal/storage/cold"
	"srcinsight/transport/stdio"
)

// LiveMode selects the live reference pass. It unmarshals from "auto"/"on"/
// "off" or the JSON booleans true/false; an empty value means auto.
type LiveMode string

const (
	LiveAuto LiveMode = "auto"
	LiveOn   LiveMode = "on"
	LiveOff  LiveMode = "off"
)

// UnmarshalJSON accepts a string mode or a boolean flag.
func (m *LiveMode) UnmarshalJSON(b []byte) error {
	s := strings.Trim(strings.TrimSpace(string(b)), `"`)
	switch strings.ToLower(s) {
	case "", "auto":
		*m = LiveAuto
	case "true", "on", "yes", "1":
		*m = LiveOn
	case "false", "off", "no", "0":
		*m = LiveOff
	default:
		return fmt.Errorf("unknown live mode %q", s)
	}
	return nil
}

func (m LiveMode) normalize() LiveMode {
	if m == "" {
		return LiveAuto
	}
	return m
}

// RefsOptions takes an exact EntryID; kinds default to calls. Live is ignored
// when the service has no live scanner.
type RefsOptions struct {
	ID    string   `json:"id"`
	Kinds []string `json:"kinds"`
	Live  LiveMode `json:"live"`
	Limit int      `json:"limit"`
}

// Refs caps bound output size and the loose-candidate probe.
const (
	refsDefaultLimit = 200
	refsMaxLimit     = 2000
	refsLooseCap     = 2000
)

// defaultRefsKinds covers both invocations and value references, since a rename
// must rewrite every place the symbol is named, not only where it is called.
var defaultRefsKinds = []string{model.EdgeCalls, model.EdgeUses}

func (o RefsOptions) normalize() RefsOptions {
	o.Live = o.Live.normalize()
	if len(o.Kinds) == 0 {
		o.Kinds = append([]string(nil), defaultRefsKinds...)
	}
	if o.Limit <= 0 {
		o.Limit = refsDefaultLimit
	}
	if o.Limit > refsMaxLimit {
		o.Limit = refsMaxLimit
	}
	return o
}

func (o RefsOptions) validate() error {
	if strings.TrimSpace(o.ID) == "" {
		return fmt.Errorf("id is required")
	}
	fid := model.EntryID(o.ID)
	if fid.Language() == "" || fid.FilePath() == "" || fid.Kind() == "" || fid.QualifiedName() == "" || fid.Ordinal() == 0 {
		return fmt.Errorf("invalid EntryID %q: want <lang>:<path>:<kind>:<qualifiedName>:<ordinal>", o.ID)
	}
	for _, k := range o.Kinds {
		if _, ok := validEdgeKinds[k]; !ok {
			return fmt.Errorf("unknown edge kind %q", k)
		}
	}
	return nil
}

// RefSite is one confirmed call site of the target.
type RefSite struct {
	Caller     model.EntryID `json:"caller"`
	CallerName string        `json:"callerName"`
	FilePath   string        `json:"filePath"`
	Kind       string        `json:"kind"`
	SiteRange  model.Range   `json:"siteRange"`
	Cond       string        `json:"cond,omitempty"`
	// Source reports where the site came from: "index" (resolved link) or
	// "live" (loose end confirmed through imports). Confidence follows.
	Source     string  `json:"source"`
	Confidence float64 `json:"confidence"`
}

// LooseRef is an unverified same-name reference that may or may not target
// the function. It is never counted as a call site.
type LooseRef struct {
	From      model.EntryID `json:"from"`
	FilePath  string        `json:"filePath"`
	Name      string        `json:"name"`
	Kind      string        `json:"kind"`
	Scope     string        `json:"scope,omitempty"`
	SiteRange model.Range   `json:"siteRange"`
	Cond      string        `json:"cond,omitempty"`
}

// RefsReport is the result of a refs query: the target plus every call site.
type RefsReport struct {
	Target model.Function `json:"target"`
	Sites  []RefSite      `json:"sites"`
	Loose  []LooseRef     `json:"loose,omitempty"`
	Stats  model.Stats    `json:"stats"`
}

// LiveRef is one loose reference the live scanner verified as a real call
// site through stored imports. It carries no caller name; refs derives it.
type LiveRef struct {
	Caller    model.EntryID
	FilePath  string
	Kind      string
	SiteRange model.Range
	Cond      string
}

// LiveScanner confirms loose references against a target through imports.
// Implementations must be read-only and keep no state after returning.
type LiveScanner interface {
	VerifyRefs(ctx context.Context, target cold.EntryRow, loose []cold.LooseEndRow) ([]LiveRef, error)
}

// siteKey identifies a reference for loose-confirmation matching.
func siteKey(caller model.EntryID, kind string, r model.Range) string {
	return fmt.Sprintf("%s\x00%s\x00%d:%d-%d:%d", caller, kind, r.StartLine, r.StartCol, r.EndLine, r.EndCol)
}

// sitePosKey collapses one physical expression to a single site.
func sitePosKey(file string, r model.Range) string {
	return fmt.Sprintf("%s\x00%d:%d-%d:%d", file, r.StartLine, r.StartCol, r.EndLine, r.EndCol)
}

// refKindRank orders edge kinds that share one source position; a real call
// outranks a value use, and anything else ranks last.
func refKindRank(kind string) int {
	switch kind {
	case model.EdgeCalls:
		return 0
	case model.EdgeUses:
		return 1
	default:
		return 2
	}
}

// refCand is a site plus the fields needed to pick one winner per position.
// The index emits several edges for one expression (nested callers, calls and
// uses); this keeps a single, most specific representative.
type refCand struct {
	site        RefSite
	callerStart int
	rank        int
}

// betterRef reports whether a beats b as the representative of one position.
func (a refCand) betterRef(b refCand) bool {
	if a.rank != b.rank {
		return a.rank < b.rank
	}
	an, bn := a.site.CallerName != "", b.site.CallerName != ""
	if an != bn {
		return an
	}
	if a.callerStart != b.callerStart {
		return a.callerStart > b.callerStart // the innermost enclosing caller wins
	}
	return a.site.Caller < b.site.Caller
}

// Refs returns every known call site of the target. It reads resolved links
// from the vault, then optionally confirms unresolved same-name references
// through the live scanner (import resolution only, no file reads).
func Refs(ctx context.Context, v *cold.Vault, live LiveScanner, opts RefsOptions) (RefsReport, error) {
	opts = opts.normalize()
	if err := opts.validate(); err != nil {
		return RefsReport{}, fmt.Errorf("methods: refs: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return RefsReport{}, err
	}

	targetRow, found, err := v.FetchEntryByID(ctx, opts.ID)
	if err != nil {
		return RefsReport{}, err
	}
	if !found {
		return RefsReport{}, fmt.Errorf("entry ID not found: %s", opts.ID)
	}
	target := targetRow.Model()
	rep := RefsReport{Target: target, Sites: []RefSite{}}

	sites := make(map[string]refCand)
	edges, err := v.FetchNeighborEdges(ctx, target.ID, opts.Kinds, true)
	if err != nil {
		return RefsReport{}, err
	}
	for _, ne := range edges {
		c := refCand{
			rank:        refKindRank(ne.Kind),
			callerStart: ne.Ref.LineRange.StartLine,
			site: RefSite{
				Caller:     ne.Ref.ID,
				CallerName: ne.Ref.Name,
				FilePath:   ne.Ref.FilePath,
				Kind:       ne.Kind,
				SiteRange:  ne.Ref.SiteRange,
				Cond:       ne.Ref.Cond,
				Source:     "index",
				Confidence: 1,
			},
		}
		key := sitePosKey(c.site.FilePath, c.site.SiteRange)
		if prev, ok := sites[key]; ok && !c.betterRef(prev) {
			continue
		}
		sites[key] = c
	}

	loose, err := v.FetchLooseByRefBasename(ctx, v.DB(), target.Name, refsLooseCap)
	if err != nil {
		return RefsReport{}, err
	}

	confirmed := make(map[string]struct{})
	if live != nil && len(loose) > 0 && opts.Live != LiveOff {
		refs, err := live.VerifyRefs(ctx, targetRow, loose)
		if err != nil {
			return RefsReport{}, err
		}
		for _, lr := range refs {
			c := refCand{
				rank: refKindRank(lr.Kind),
				site: RefSite{
					Caller:     lr.Caller,
					FilePath:   lr.FilePath,
					Kind:       lr.Kind,
					SiteRange:  lr.SiteRange,
					Cond:       lr.Cond,
					Source:     "live",
					Confidence: 0.9,
				},
			}
			key := sitePosKey(c.site.FilePath, c.site.SiteRange)
			if prev, ok := sites[key]; ok && !c.betterRef(prev) {
				continue
			}
			sites[key] = c
			confirmed[siteKey(lr.Caller, lr.Kind, lr.SiteRange)] = struct{}{}
		}
	}

	flat := make(map[string]RefSite, len(sites))
	for k, c := range sites {
		flat[k] = c.site
	}
	rep.Sites = sortSites(flat, opts.Limit)
	if err := fillCallerNames(ctx, v, rep.Sites); err != nil {
		return RefsReport{}, err
	}

	rep.Loose = collectLoose(loose, confirmed)

	entries, err := countIndexed(ctx, v)
	if err != nil {
		return RefsReport{}, err
	}
	tracked, err := countTracked(ctx, v)
	if err != nil {
		return RefsReport{}, err
	}
	rep.Stats = model.Stats{
		FilesScanned:     tracked,
		FunctionsIndexed: entries,
		NodesReturned:    1,
		EdgesReturned:    len(rep.Sites),
	}
	return rep, nil
}

// sortSites orders sites by file, line, then caller and applies the limit.
func sortSites(m map[string]RefSite, limit int) []RefSite {
	out := make([]RefSite, 0, len(m))
	for _, s := range m {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].FilePath != out[j].FilePath {
			return out[i].FilePath < out[j].FilePath
		}
		if out[i].SiteRange.StartLine != out[j].SiteRange.StartLine {
			return out[i].SiteRange.StartLine < out[j].SiteRange.StartLine
		}
		if out[i].SiteRange.StartCol != out[j].SiteRange.StartCol {
			return out[i].SiteRange.StartCol < out[j].SiteRange.StartCol
		}
		if out[i].Caller != out[j].Caller {
			return out[i].Caller < out[j].Caller
		}
		return out[i].Kind < out[j].Kind
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// fillCallerNames backfills the simple caller name for live sites, whose rows
// only carry the entry ID. Index sites already carry it.
func fillCallerNames(ctx context.Context, v *cold.Vault, sites []RefSite) error {
	want := make(map[string]struct{})
	var ids []string
	for i := range sites {
		if sites[i].CallerName != "" || sites[i].Caller == "" {
			continue
		}
		id := string(sites[i].Caller)
		if _, ok := want[id]; ok {
			continue
		}
		want[id] = struct{}{}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil
	}
	rows, err := v.FetchEntriesByIDs(ctx, ids)
	if err != nil {
		return err
	}
	names := make(map[string]string, len(rows))
	for _, r := range rows {
		names[string(r.ID)] = r.Name
	}
	for i := range sites {
		if sites[i].CallerName == "" {
			sites[i].CallerName = names[string(sites[i].Caller)]
		}
	}
	return nil
}

// collectLoose reports same-name references the live pass did not confirm.
func collectLoose(rows []cold.LooseEndRow, confirmed map[string]struct{}) []LooseRef {
	var out []LooseRef
	for _, le := range rows {
		r := model.Range{StartLine: le.SiteStart, EndLine: le.SiteEnd, StartCol: le.SiteStartCol, EndCol: le.SiteEndCol}
		if _, ok := confirmed[siteKey(le.From, le.Kind, r)]; ok {
			continue
		}
		out = append(out, LooseRef{
			From:      le.From,
			FilePath:  le.FilePath,
			Name:      le.Name,
			Kind:      le.Kind,
			Scope:     le.Scope,
			SiteRange: r,
			Cond:      le.Cond,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].FilePath != out[j].FilePath {
			return out[i].FilePath < out[j].FilePath
		}
		if out[i].SiteRange.StartLine != out[j].SiteRange.StartLine {
			return out[i].SiteRange.StartLine < out[j].SiteRange.StartLine
		}
		return out[i].From < out[j].From
	})
	return out
}

// HandleRefs adapts Refs to a stdio handler.
func (s *Service) HandleRefs(ctx context.Context, raw json.RawMessage) (any, *stdio.RPCError) {
	var opts RefsOptions
	if len(raw) != 0 {
		if err := json.Unmarshal(raw, &opts); err != nil {
			return nil, &stdio.RPCError{Code: stdio.CodeInvalidParams, Message: fmt.Sprintf("refs: invalid params: %v", err)}
		}
	}
	opts = opts.normalize()
	if err := opts.validate(); err != nil {
		return nil, &stdio.RPCError{Code: stdio.CodeInvalidParams, Message: fmt.Sprintf("refs: %v", err)}
	}
	res, err := Refs(ctx, s.vault, s.live, opts)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, &stdio.RPCError{Code: stdio.CodeInternalError, Message: "refs: cancelled"}
		}
		if strings.Contains(err.Error(), "entry ID not found") {
			return nil, &stdio.RPCError{Code: stdio.CodeInvalidParams, Message: err.Error()}
		}
		if strings.Contains(err.Error(), "methods: refs:") {
			return nil, &stdio.RPCError{Code: stdio.CodeInvalidParams, Message: err.Error()}
		}
		return nil, &stdio.RPCError{Code: stdio.CodeInternalError, Message: fmt.Sprintf("refs: %v", err)}
	}
	return res, nil
}
