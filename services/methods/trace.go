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

// TraceAsk tunes a trace over symbol names; From and To pin both ends.
type TraceAsk struct {
	Query     string   `json:"query"`
	From      string   `json:"from"`
	To        string   `json:"to"`
	MaxDepth  int      `json:"maxDepth"`
	MaxTrails int      `json:"maxTrails"`
	Kinds     []string `json:"kinds"`
}

// Trail is one call path with per-hop call-site ranges.
type Trail struct {
	Steps []model.EntryID `json:"steps"`
	Edges []model.Edge    `json:"edges"`
}

// UnresolvedRef is one unresolvable call with its scope label.
type UnresolvedRef struct {
	Name      string      `json:"name"`
	Kind      string      `json:"kind"`
	Scope     string      `json:"scope"`
	SiteRange model.Range `json:"siteRange,omitempty"`
}

// Ripple aggregates direct callers, files, and nearby tests.
type Ripple struct {
	Callers   int      `json:"callers"`
	Files     []string `json:"files,omitempty"`
	Tests     []string `json:"tests,omitempty"`
	FileTotal int      `json:"fileTotal,omitempty"`
	TestTotal int      `json:"testTotal,omitempty"`
	Truncated bool     `json:"truncated,omitempty"`
}

// TraceReport holds anchors, trails, nodes, edges, and context.
type TraceReport struct {
	Tokens  []string                      `json:"tokens"`
	Anchors map[string][]model.SearchHit  `json:"anchors"`
	Trails  []Trail                       `json:"trails"`
	Nodes   []model.Function              `json:"nodes"`
	Edges   []model.Edge                  `json:"edges"`
	Loose   map[string][]UnresolvedRef    `json:"loose,omitempty"`
	Imports map[string][]model.ImportStmt `json:"imports,omitempty"`
	Ripples map[string]Ripple             `json:"ripples,omitempty"`
	Stats   model.Stats                   `json:"stats"`
}

const (
	traceMaxTokens     = 16
	traceMaxPerToken   = 6
	traceMaxAnchors    = 40
	traceMaxSeeds      = 8
	traceMaxTrailsCap  = 4
	traceVisitCap      = 2000
	traceDirectedCap   = 8000
	traceDefaultDepth  = 7
	traceDirectedDepth = 12
	traceDepthCeiling  = 25
	traceCallKindOnly  = model.EdgeCalls

	traceTransportFanout  = 16
	traceTransportPreload = 5000

	// One non-call hop (type/parameter/use) may bridge a static-call break,
	// matching the "at most one unnamed bridge" rule. The probe budget keeps
	// the extra edge query bounded on wide graphs.
	traceBridgeProbeCap = 64
)

// traceBridgeKinds are the relationship edges allowed to bridge a call gap.
var traceBridgeKinds = []string{model.EdgeUses, model.EdgeParam, model.EdgeReturn}

func (a TraceAsk) withDefaults() TraceAsk {
	if a.MaxDepth <= 0 {
		if a.From != "" || a.To != "" {
			a.MaxDepth = traceDirectedDepth
		} else {
			a.MaxDepth = traceDefaultDepth
		}
	}
	if a.MaxDepth < 1 {
		a.MaxDepth = 1
	}
	if a.MaxDepth > traceDepthCeiling {
		a.MaxDepth = traceDepthCeiling
	}
	if a.MaxTrails <= 0 {
		a.MaxTrails = 1
	}
	if a.MaxTrails > traceMaxTrailsCap {
		a.MaxTrails = traceMaxTrailsCap
	}
	if len(a.Kinds) == 0 {
		a.Kinds = append([]string(nil), defaultGraphKinds...)
	}
	return a
}

func (a TraceAsk) validate() error {
	if (a.From == "") != (a.To == "") {
		return fmt.Errorf("from and to must be set together")
	}
	for _, k := range a.Kinds {
		if _, ok := validEdgeKinds[k]; !ok {
			return fmt.Errorf("unknown edge kind %q", k)
		}
	}
	return nil
}

// splitAsk keeps free text as capped symbol-shaped tokens.
func splitAsk(q string) []string {
	seen := make(map[string]struct{})
	var out []string
	for _, raw := range strings.FieldsFunc(q, func(r rune) bool {
		switch r {
		case ' ', '\t', '\n', '\r', ',', '(', ')', '[', ']':
			return true
		default:
			return false
		}
	}) {
		t := strings.Trim(raw, "{}<>\"'`")
		if t == "" {
			continue
		}
		t = stripAskExt(t)
		if len(t) < 3 || !isAskToken(t) {
			continue
		}
		fold := strings.ToLower(t)
		if _, ok := seen[fold]; ok {
			continue
		}
		seen[fold] = struct{}{}
		out = append(out, t)
		if len(out) >= traceMaxTokens {
			break
		}
	}
	return out
}

func isAskToken(t string) bool {
	for i, r := range t {
		if i == 0 {
			switch {
			case r == '_' || r == '$',
				r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z':
			default:
				return false
			}
			continue
		}
		switch {
		case r == '_' || r == '$' || r == '.' || r == ':' || r == '/',
			r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		default:
			return false
		}
	}
	return len(t) > 0
}

// anchorPolicy sizes per-token anchor breadth from the vault shape summary so
// the same recipe adapts across projects. search is the candidate fetch cap;
// ceiling is the per-token upper bound after coverage spreading.
type anchorPolicy struct {
	search  int
	ceiling int
}

// newAnchorPolicy scales the candidate pool with project size and the per-token
// ceiling with directory breadth, falling back to the hardcoded base floors
// when the vault has no stats yet.
func newAnchorPolicy(stats cold.TraceStats) anchorPolicy {
	search := traceMaxPerToken * 8
	if scaled := stats.Entries / 100; scaled > search {
		search = scaled
	}
	if search > cold.MaxSearchLimit {
		search = cold.MaxSearchLimit
	}
	ceiling := traceMaxPerToken
	if stats.Dirs > ceiling {
		ceiling = stats.Dirs
	}
	if ceiling > traceMaxAnchors {
		ceiling = traceMaxAnchors
	}
	return anchorPolicy{search: search, ceiling: ceiling}
}

// anchorModeRank orders search channels best-first.
func anchorModeRank(mode string) int {
	switch mode {
	case "exact":
		return 0
	case "prefix":
		return 1
	case "fts":
		return 2
	default:
		return 3
	}
}

// gatherAnchorRows unions every search channel for one token instead of
// stopping at the first non-empty one. Exact-first was why "discover" resolved
// only to an exact wrapper and never to discoverPlugins: ranking, not channel
// order, must decide.
func gatherAnchorRows(ctx context.Context, v *cold.Vault, token string, policy anchorPolicy) ([]cold.ScoredEntryRow, error) {
	var out []cold.ScoredEntryRow
	idx := make(map[model.EntryID]int)
	merge := func(got []cold.ScoredEntryRow) {
		for _, r := range got {
			if at, ok := idx[r.ID]; ok {
				if anchorModeRank(r.Mode) < anchorModeRank(out[at].Mode) {
					out[at] = r
				}
				continue
			}
			idx[r.ID] = len(out)
			out = append(out, r)
		}
	}
	for _, ch := range []struct {
		call func(context.Context, string, int) ([]cold.ScoredEntryRow, error)
	}{
		{v.SearchExact},
		{v.SearchPrefix},
		{v.SearchFTS},
	} {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		got, err := ch.call(ctx, token, policy.search)
		if err != nil {
			return nil, fmt.Errorf("methods: trace: resolve %q: %w", token, err)
		}
		merge(got)
	}
	// Substring is a full scan: only as a last resort for a sparse token.
	if len(out) < traceMaxPerToken {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		got, err := v.SearchSubstring(ctx, token, policy.search)
		if err != nil {
			return nil, fmt.Errorf("methods: trace: resolve %q: %w", token, err)
		}
		merge(got)
	}
	if len(out) > policy.search {
		sort.SliceStable(out, func(i, j int) bool {
			return anchorModeRank(out[i].Mode) < anchorModeRank(out[j].Mode)
		})
		out = out[:policy.search]
	}
	return out, nil
}

// anchorFacts carries the cross-token signals ranking needs.
type anchorFacts struct {
	// coLoc maps a file to the number of distinct query tokens resolved there.
	coLoc map[string]int
	// callers is the incoming calls count per candidate.
	callers map[model.EntryID]int
	// globalStrong reports whether any file in the whole ask is corroborated.
	globalStrong bool
}

// rankAnchorRows scores one token's candidates. A corroborated file (>=2 query
// tokens landing in it) outranks a lone namesake; centrality and body size
// then put a real definition ahead of an exact one-line wrapper.
func rankAnchorRows(ctx context.Context, v *cold.Vault, token string, directed bool, rows []cold.ScoredEntryRow, policy anchorPolicy, pool map[string]struct{}, typeToks []string, lcTokens map[string]struct{}, fileCache map[string]map[string]struct{}, facts anchorFacts) ([]cold.ScoredEntryRow, error) {
	if len(rows) == 0 {
		return nil, nil
	}
	precise := isAskSymbol(token)
	strong := make([]bool, len(rows))
	for i, r := range rows {
		s := precise || inAskTypedContext(r, typeToks) || vesselMentioned(r, pool) ||
			facts.coLoc[r.FilePath] >= 2
		// Bounded corroboration probe: only for a small bare-word candidate set.
		if !s && !precise && len(lcTokens) >= 2 && len(rows) <= traceMaxPerToken*2 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			s = corroboratedAskFile(ctx, v, token, r.FilePath, lcTokens, fileCache)
		}
		strong[i] = s
	}
	// Bare-word pruning: once the ask has a corroborated cluster, an
	// uncorroborated bare word ("folder" -> migrate.ts) is keyword noise, not
	// a seed. Shape-precise tokens and single-token asks are never pruned.
	if !directed && !precise && len(lcTokens) >= 2 && facts.globalStrong {
		kept := rows[:0]
		keptStrong := strong[:0]
		for i, r := range rows {
			if strong[i] {
				kept = append(kept, r)
				keptStrong = append(keptStrong, true)
			}
		}
		if len(kept) == 0 {
			return nil, nil
		}
		rows, strong = kept, keptStrong
	}
	fold := strings.ToLower(token)
	scored := make([]int, len(rows))
	worth := make([]float64, len(rows))
	typed := make([]bool, len(rows))
	bodies := make([]int, len(rows))
	callers := make([]int, len(rows))
	glue := make([]bool, len(rows))
	testy := make([]bool, len(rows))
	terms := make([]int, len(rows))
	for i, r := range rows {
		switch {
		case strings.EqualFold(r.Name, token):
			scored[i] = 0
		case strings.HasSuffix(strings.ToLower(r.Qualified), fold):
			scored[i] = 1
		default:
			scored[i] = 2
		}
		worth[i] = askRankScore(r)
		typed[i] = inAskTypedContext(r, typeToks)
		bodies[i] = r.EndLine - r.StartLine
		if bodies[i] < 0 {
			bodies[i] = 0
		}
		callers[i] = facts.callers[r.ID]
		glue[i] = isTraceGeneratedPath(r.FilePath)
		testy[i] = isTraceTestPath(r.FilePath)
		// Term hits are name-only: a local `manifestNotices` under
		// discoverPlugins must not inherit "discover" from its qualified path.
		hay := strings.ToLower(r.Name)
		for t := range lcTokens {
			if strings.Contains(hay, t) {
				terms[i]++
			}
		}
	}
	idx := make([]int, len(rows))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool {
		ia, ib := idx[a], idx[b]
		// Generated bindings (wailsjs/models) can have huge fan-in; they are
		// glue, so a hand-written symbol always outranks them.
		if glue[ia] != glue[ib] {
			return !glue[ia]
		}
		// Tests describe behavior; they are blast radius, not the thing asked
		// about, so source anchors come first.
		if testy[ia] != testy[ib] {
			return !testy[ia]
		}
		// Symbols whose own name carries several ask terms ("discoverPlugins"
		// for "plugin discover") are the intended target; a bare namesake
		// ("Discover") is weaker than the compound definition. Name terms
		// precede corroboration so a local `plugins` const inside discover()
		// cannot outrank discoverPlugins.
		if terms[ia] != terms[ib] {
			return terms[ia] > terms[ib]
		}
		if strong[ia] != strong[ib] {
			return strong[ia]
		}
		if scored[ia] != scored[ib] {
			return scored[ia] < scored[ib]
		}
		if callers[ia] != callers[ib] {
			return callers[ia] > callers[ib]
		}
		if bodies[ia] != bodies[ib] {
			return bodies[ia] > bodies[ib]
		}
		if typed[ia] != typed[ib] {
			return typed[ia] && !typed[ib]
		}
		if worth[ia] != worth[ib] {
			return worth[ia] > worth[ib]
		}
		if rows[ia].Score != rows[ib].Score {
			return rows[ia].Score < rows[ib].Score
		}
		return rows[ia].ID < rows[ib].ID
	})
	ordered := make([]cold.ScoredEntryRow, len(rows))
	for i, j := range idx {
		ordered[i] = rows[j]
	}
	rows = ordered
	max := anchorPerToken(rows, policy)
	if len(rows) > max {
		rows = spreadByBucket(rows, max)
	}
	// Rank is the final display order; the render layer treats rank 0 as the
	// confident anchor, so it must not stay at the search channel's rank.
	for i := range rows {
		rows[i].Rank = i
	}
	return rows, nil
}

// anchorBucketKey groups sibling definitions by their containing directory.
func anchorBucketKey(filePath string) string {
	if i := strings.LastIndexByte(filePath, '/'); i >= 0 {
		return filePath[:i]
	}
	return filePath
}

// distinctAnchorBuckets counts the directories the rows span.
func distinctAnchorBuckets(rows []cold.ScoredEntryRow) int {
	if len(rows) == 0 {
		return 0
	}
	seen := make(map[string]struct{}, len(rows))
	for _, r := range rows {
		seen[anchorBucketKey(r.FilePath)] = struct{}{}
	}
	return len(seen)
}

// anchorPerToken sizes the per-token cap coverage-first: every spanned
// directory gets a slot, floored at the base cap and bounded by the policy
// ceiling derived from vault shape.
func anchorPerToken(rows []cold.ScoredEntryRow, policy anchorPolicy) int {
	max := traceMaxPerToken
	if b := distinctAnchorBuckets(rows); b > max {
		max = b
	}
	if max > policy.ceiling {
		max = policy.ceiling
	}
	return max
}

// spreadByBucket covers every directory once in ranking order, then refills
// remaining slots by global rank so one folder cannot crowd out sibling areas.
func spreadByBucket(rows []cold.ScoredEntryRow, maxN int) []cold.ScoredEntryRow {
	if maxN <= 0 || len(rows) <= maxN {
		return rows
	}
	byKey := make(map[string][]int, len(rows))
	order := make([]string, 0, len(rows))
	for i, r := range rows {
		k := anchorBucketKey(r.FilePath)
		if _, ok := byKey[k]; !ok {
			order = append(order, k)
		}
		byKey[k] = append(byKey[k], i)
	}
	used := make([]bool, len(rows))
	out := make([]cold.ScoredEntryRow, 0, maxN)
	for _, k := range order {
		if len(out) >= maxN {
			break
		}
		ids := byKey[k]
		used[ids[0]] = true
		out = append(out, rows[ids[0]])
	}
	for i, r := range rows {
		if len(out) >= maxN {
			break
		}
		if used[i] {
			continue
		}
		used[i] = true
		out = append(out, r)
	}
	return out
}

// vesselMentioned reports whether the ask co-mentions the row vessel.
func vesselMentioned(r cold.ScoredEntryRow, pool map[string]struct{}) bool {
	if r.VesselName == "" {
		return false
	}
	_, ok := pool[strings.ToLower(r.VesselName)]
	return ok
}

func tokenSegments(tokens []string) map[string]struct{} {
	pool := make(map[string]struct{}, len(tokens)*2)
	for _, t := range tokens {
		for _, s := range strings.FieldsFunc(strings.ToLower(t), func(r rune) bool {
			return r == '.' || r == ':' || r == '/'
		}) {
			if s != "" {
				pool[s] = struct{}{}
			}
		}
	}
	return pool
}

type trailLink struct {
	prev model.EntryID
	edge model.Edge
}

// transportHop is one synthesized transport edge target plus its call-site.
type transportHop struct {
	to   model.EntryID
	site model.Range
}

type transportKey struct {
	id      model.EntryID
	reverse bool
}

// transportWalk resolves cross-transport neighbors for one Trace run from the
// transport hint tables preloaded once. Forward walks a client call owner to
// its matching server endpoints; reverse walks a server endpoint back to its
// callers. Owners without a resolved entry are dropped.
type transportWalk struct {
	calls     []cold.TransportCallRow
	endpoints []cold.TransportEndpointRow
	byCaller  map[model.EntryID][]cold.TransportCallRow
	byOwner   map[model.EntryID][]cold.TransportEndpointRow
	cache     map[transportKey][]transportHop
}

// newTransportWalk preloads every transport hint once. A vault without hints
// returns a nil walker so Trace skips transport entirely.
func newTransportWalk(ctx context.Context, v *cold.Vault) (*transportWalk, error) {
	calls, err := v.FetchTransportCallsByMethod(ctx, "", traceTransportPreload)
	if err != nil {
		return nil, err
	}
	endpoints, err := v.FetchTransportEndpointsByMethod(ctx, "", traceTransportPreload)
	if err != nil {
		return nil, err
	}
	if len(calls) == 0 && len(endpoints) == 0 {
		return nil, nil
	}
	w := &transportWalk{
		calls:     calls,
		endpoints: endpoints,
		byCaller:  make(map[model.EntryID][]cold.TransportCallRow),
		byOwner:   make(map[model.EntryID][]cold.TransportEndpointRow),
		cache:     make(map[transportKey][]transportHop),
	}
	for _, c := range calls {
		w.byCaller[c.Owner] = append(w.byCaller[c.Owner], c)
	}
	for _, e := range endpoints {
		w.byOwner[e.Owner] = append(w.byOwner[e.Owner], e)
	}
	return w, nil
}

// Neighbors returns transport-linked entries touching id, capped and sorted.
func (w *transportWalk) Neighbors(id model.EntryID, reverse bool) []transportHop {
	key := transportKey{id: id, reverse: reverse}
	if hops, ok := w.cache[key]; ok {
		return hops
	}
	var hops []transportHop
	seen := make(map[model.EntryID]struct{})
	add := func(to model.EntryID, site model.Range) {
		if to == "" {
			return
		}
		if _, ok := seen[to]; ok {
			return
		}
		seen[to] = struct{}{}
		hops = append(hops, transportHop{to: to, site: site})
	}
	if reverse {
		for _, ep := range w.byOwner[id] {
			for _, f := range matchEndpoint(ep, w.calls) {
				add(f.Caller, model.Range{StartLine: f.Call.SiteStart, EndLine: f.Call.SiteStart})
			}
		}
	} else {
		for _, c := range w.byCaller[id] {
			for _, f := range matchCall(c, w.endpoints) {
				add(f.Endpoint, model.Range{StartLine: f.Call.SiteStart, EndLine: f.Call.SiteStart})
			}
		}
	}
	sort.Slice(hops, func(i, j int) bool { return hops[i].to < hops[j].to })
	if len(hops) > traceTransportFanout {
		hops = hops[:traceTransportFanout]
	}
	w.cache[key] = hops
	return hops
}

// walkForward BFSes calls edges with at most one unnamed gap node; when tw is
// set, transport hops join as named nodes.
func walkForward(ctx context.Context, v *cold.Vault, tw *transportWalk, seed model.EntryID, named map[model.EntryID]struct{}, maxNodes int) (map[model.EntryID]trailLink, []model.EntryID, error) {
	parent := map[model.EntryID]trailLink{seed: {}}
	type item struct {
		id      model.EntryID
		depth   int
		gaps    int
		bridges int
	}
	queue := []item{{id: seed}}
	var reached []model.EntryID
	bridgeProbes := 0
	for head := 0; head < len(queue) && len(parent) < traceVisitCap; head++ {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		cur := queue[head]
		if cur.id != seed {
			if _, ok := named[cur.id]; ok {
				reached = append(reached, cur.id)
			}
		}
		if cur.depth >= maxNodes-1 {
			continue
		}
		nes, err := v.FetchNeighborEdges(ctx, cur.id, []string{traceCallKindOnly}, false)
		if err != nil {
			return nil, nil, err
		}
		push := func(nid model.EntryID, kind string, site model.Range, cond string, isCall bool) {
			if _, seen := parent[nid]; seen {
				return
			}
			gaps := cur.gaps
			bridges := cur.bridges
			if !isCall {
				bridges++
			}
			if bridges > 1 {
				return
			}
			if _, ok := named[nid]; ok {
				if isCall {
					gaps = 0
				}
			} else {
				gaps++
			}
			if gaps > 1 {
				return
			}
			parent[nid] = trailLink{prev: cur.id, edge: model.Edge{From: cur.id, To: nid, Kind: kind, SiteRange: site, Cond: cond}}
			queue = append(queue, item{id: nid, depth: cur.depth + 1, gaps: gaps, bridges: bridges})
		}
		sawNamed := false
		for _, ne := range nes {
			if _, ok := named[ne.Ref.ID]; ok {
				sawNamed = true
			}
			push(ne.Ref.ID, ne.Kind, ne.Ref.SiteRange, ne.Ref.Cond, true)
		}
		// A dead-end call branch may still reach a named symbol through a
		// typed relationship (uses/param/return). At most one such bridge per
		// trail, and a bounded number of probes per seed.
		if !sawNamed && cur.bridges == 0 && bridgeProbes < traceBridgeProbeCap {
			bridgeProbes++
			bes, err := v.FetchNeighborEdges(ctx, cur.id, traceBridgeKinds, false)
			if err != nil {
				return nil, nil, err
			}
			for _, ne := range bes {
				push(ne.Ref.ID, ne.Kind, ne.Ref.SiteRange, ne.Ref.Cond, false)
			}
		}
		if tw != nil {
			for _, h := range tw.Neighbors(cur.id, false) {
				push(h.to, model.EdgeTransport, h.site, "", true)
			}
		}
	}
	return parent, reached, nil
}

// layTrail rebuilds seed to target oldest hop first.
func layTrail(parent map[model.EntryID]trailLink, target model.EntryID) Trail {
	var ids []model.EntryID
	for cur := target; ; {
		ids = append(ids, cur)
		at := parent[cur]
		if at.prev == "" {
			break
		}
		cur = at.prev
	}
	for i, j := 0, len(ids)-1; i < j; i, j = i+1, j-1 {
		ids[i], ids[j] = ids[j], ids[i]
	}
	var edges []model.Edge
	for _, id := range ids[1:] {
		edges = append(edges, parent[id].edge)
	}
	return Trail{Steps: ids, Edges: edges}
}

// walkBoth searches from both ends and stitches at the first shared node.
func walkBoth(ctx context.Context, v *cold.Vault, tw *transportWalk, seed model.EntryID, sinks map[model.EntryID]struct{}, maxNodes int) (Trail, bool, error) {
	if _, ok := sinks[seed]; ok {
		return Trail{}, false, nil
	}
	forward := map[model.EntryID]trailLink{seed: {}}
	type backLink struct {
		next model.EntryID
		edge model.Edge
	}
	backward := make(map[model.EntryID]backLink)
	frontF := []model.EntryID{seed}
	var frontB []model.EntryID
	for id := range sinks {
		backward[id] = backLink{}
		frontB = append(frontB, id)
	}
	if len(frontB) == 0 {
		return Trail{}, false, nil
	}
	meet := func() (model.EntryID, bool) {
		for id := range forward {
			if _, ok := backward[id]; ok {
				return id, true
			}
		}
		return "", false
	}
	maxEdges := maxNodes - 1
	if maxEdges < 1 {
		maxEdges = 1
	}
	for laid := 0; laid < maxEdges; laid++ {
		if err := ctx.Err(); err != nil {
			return Trail{}, false, err
		}
		if len(forward)+len(backward) > traceDirectedCap {
			break
		}
		if len(frontF) <= len(frontB) {
			var next []model.EntryID
			for _, id := range frontF {
				nes, err := v.FetchNeighborEdges(ctx, id, []string{traceCallKindOnly}, false)
				if err != nil {
					return Trail{}, false, err
				}
				for _, ne := range nes {
					nid := ne.Ref.ID
					if _, seen := forward[nid]; seen {
						continue
					}
					forward[nid] = trailLink{prev: id, edge: model.Edge{From: id, To: nid, Kind: ne.Kind, SiteRange: ne.Ref.SiteRange, Cond: ne.Ref.Cond}}
					next = append(next, nid)
				}
				if tw != nil {
					for _, h := range tw.Neighbors(id, false) {
						if _, seen := forward[h.to]; seen {
							continue
						}
						forward[h.to] = trailLink{prev: id, edge: model.Edge{From: id, To: h.to, Kind: model.EdgeTransport, SiteRange: h.site}}
						next = append(next, h.to)
					}
				}
			}
			if len(next) == 0 {
				break
			}
			frontF = next
		} else {
			var next []model.EntryID
			for _, id := range frontB {
				nes, err := v.FetchNeighborEdges(ctx, id, []string{traceCallKindOnly}, true)
				if err != nil {
					return Trail{}, false, err
				}
				for _, ne := range nes {
					nid := ne.Ref.ID
					if _, seen := backward[nid]; seen {
						continue
					}
					backward[nid] = backLink{next: id, edge: model.Edge{From: nid, To: id, Kind: ne.Kind, SiteRange: ne.Ref.SiteRange, Cond: ne.Ref.Cond}}
					next = append(next, nid)
				}
				if tw != nil {
					for _, h := range tw.Neighbors(id, true) {
						if _, seen := backward[h.to]; seen {
							continue
						}
						backward[h.to] = backLink{next: id, edge: model.Edge{From: h.to, To: id, Kind: model.EdgeTransport, SiteRange: h.site}}
						next = append(next, h.to)
					}
				}
			}
			if len(next) == 0 {
				break
			}
			frontB = next
		}
		mid, ok := meet()
		if !ok {
			continue
		}
		fwd := layTrail(forward, mid)
		steps := append([]model.EntryID{}, fwd.Steps...)
		edges := append([]model.Edge{}, fwd.Edges...)
		for cur := mid; ; {
			bl := backward[cur]
			if bl.next == "" {
				break
			}
			steps = append(steps, bl.next)
			edges = append(edges, bl.edge)
			cur = bl.next
		}
		if len(steps) < 2 {
			return Trail{}, false, nil
		}
		if _, ok := sinks[steps[len(steps)-1]]; !ok {
			return Trail{}, false, nil
		}
		if len(steps) > maxNodes {
			return Trail{}, false, nil
		}
		return Trail{Steps: steps, Edges: edges}, true, nil
	}
	return Trail{}, false, nil
}

func Trace(ctx context.Context, v *cold.Vault, ask TraceAsk) (TraceReport, error) {
	ask = ask.withDefaults()
	if err := ask.validate(); err != nil {
		return TraceReport{}, fmt.Errorf("methods: trace: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return TraceReport{}, err
	}
	rep := TraceReport{
		Anchors: map[string][]model.SearchHit{},
		Loose:   map[string][]UnresolvedRef{},
		Imports: map[string][]model.ImportStmt{},
	}
	directed := ask.From != "" && ask.To != ""
	tokens := splitAsk(ask.Query)
	if directed {
		for _, extra := range []string{ask.From, ask.To} {
			found := false
			for _, t := range tokens {
				if strings.EqualFold(t, extra) {
					found = true
					break
				}
			}
			if !found {
				tokens = append(tokens, extra)
			}
		}
	}
	rep.Tokens = append([]string(nil), tokens...)
	if len(tokens) == 0 {
		return rep, nil
	}
	pool := tokenSegments(tokens)
	typeToks := askTypeTokens(tokens)
	lcTokens := make(map[string]struct{}, len(tokens))
	for _, t := range tokens {
		lcTokens[strings.ToLower(t)] = struct{}{}
	}
	fileCache := make(map[string]map[string]struct{})
	stats, _, err := v.GetTraceStats(ctx)
	if err != nil {
		return TraceReport{}, err
	}
	policy := newAnchorPolicy(stats)
	// Resolve every token before ranking any of them: co-location is a
	// whole-ask signal (how many query tokens land in one file).
	gathered := make(map[string][]cold.ScoredEntryRow, len(tokens))
	fileTokens := make(map[string]map[string]struct{}, len(tokens))
	// A file is corroborated when >=2 query tokens appear in its path or its
	// matched symbols. Path terms matter: discover.plugin.ts corroborates
	// "discover" + "plugin" even when only discoverPlugins was found there.
	fileSet := func(filePath string) map[string]struct{} {
		set := fileTokens[filePath]
		if set != nil {
			return set
		}
		set = make(map[string]struct{}, 2)
		lowerPath := strings.ToLower(filePath)
		for _, t := range tokens {
			if strings.Contains(lowerPath, strings.ToLower(t)) {
				set[t] = struct{}{}
			}
		}
		fileTokens[filePath] = set
		return set
	}
	var candidateIDs []model.EntryID
	seenCandidate := make(map[model.EntryID]struct{})
	for _, t := range tokens {
		if err := ctx.Err(); err != nil {
			return TraceReport{}, err
		}
		rows, err := gatherAnchorRows(ctx, v, t, policy)
		if err != nil {
			return TraceReport{}, err
		}
		gathered[t] = rows
		for _, r := range rows {
			fileSet(r.FilePath)[t] = struct{}{}
			if _, ok := seenCandidate[r.ID]; !ok {
				seenCandidate[r.ID] = struct{}{}
				candidateIDs = append(candidateIDs, r.ID)
			}
		}
	}
	coLoc := make(map[string]int, len(fileTokens))
	globalStrong := false
	for f, set := range fileTokens {
		coLoc[f] = len(set)
		if len(set) >= 2 {
			globalStrong = true
		}
	}
	callers, err := v.CountCallersBatch(ctx, candidateIDs)
	if err != nil {
		return TraceReport{}, err
	}
	facts := anchorFacts{coLoc: coLoc, callers: callers, globalStrong: globalStrong}

	anchorRows := make(map[string][]cold.ScoredEntryRow, len(tokens))
	named := make(map[model.EntryID]struct{})
	for _, t := range tokens {
		if err := ctx.Err(); err != nil {
			return TraceReport{}, err
		}
		rows, err := rankAnchorRows(ctx, v, t, directed, gathered[t], policy, pool, typeToks, lcTokens, fileCache, facts)
		if err != nil {
			return TraceReport{}, err
		}
		anchorRows[t] = rows
	}
buildHits:
	for _, t := range tokens {
		for _, r := range anchorRows[t] {
			named[r.ID] = struct{}{}
			hit := model.SearchHit{
				ID:        r.ID,
				Name:      r.Name,
				Qualified: r.Qualified,
				Kind:      r.Kind,
				FilePath:  r.FilePath,
				LineRange: model.Range{StartLine: r.StartLine, EndLine: r.EndLine},
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
			rep.Anchors[t] = append(rep.Anchors[t], hit)
			if len(named) >= traceMaxAnchors {
				break buildHits
			}
		}
	}
	if len(named) == 0 {
		return rep, nil
	}

	tw, err := newTransportWalk(ctx, v)
	if err != nil {
		return TraceReport{}, err
	}
	var trails []Trail
	if directed {
		// From/To may differ only in case from the query token that resolved
		// them; look both ends up case-insensitively.
		anchorByFold := make(map[string][]cold.ScoredEntryRow, len(anchorRows))
		for t, rows := range anchorRows {
			anchorByFold[strings.ToLower(t)] = rows
		}
		fromRows := anchorByFold[strings.ToLower(ask.From)]
		toRows := anchorByFold[strings.ToLower(ask.To)]
		toIDs := make(map[model.EntryID]struct{}, len(toRows))
		for _, r := range toRows {
			toIDs[r.ID] = struct{}{}
		}
		if len(fromRows) > 0 && len(toIDs) > 0 {
			for _, r := range fromRows {
				if err := ctx.Err(); err != nil {
					return TraceReport{}, err
				}
				if t, ok, err := walkBoth(ctx, v, tw, r.ID, toIDs, ask.MaxDepth); err != nil {
					return TraceReport{}, err
				} else if ok {
					trails = append(trails, t)
				}
			}
			sort.Slice(trails, func(i, j int) bool { return len(trails[i].Steps) < len(trails[j].Steps) })
		}
	} else if len(named) >= 2 {
		// Seed the walk from the best-ranked anchors, not alphabetically: a
		// dead one-line match must not spend the seed budget.
		seedRank := make(map[model.EntryID]int, len(named))
		for _, t := range tokens {
			for i, r := range anchorRows[t] {
				if _, ok := seedRank[r.ID]; !ok {
					seedRank[r.ID] = i
				}
			}
		}
		var seeds []model.EntryID
		for id := range named {
			seeds = append(seeds, id)
		}
		sort.Slice(seeds, func(i, j int) bool {
			if seedRank[seeds[i]] != seedRank[seeds[j]] {
				return seedRank[seeds[i]] < seedRank[seeds[j]]
			}
			if callers[seeds[i]] != callers[seeds[j]] {
				return callers[seeds[i]] > callers[seeds[j]]
			}
			return seeds[i] < seeds[j]
		})
		if len(seeds) > traceMaxSeeds {
			seeds = seeds[:traceMaxSeeds]
		}
		for _, s := range seeds {
			if err := ctx.Err(); err != nil {
				return TraceReport{}, err
			}
			parent, reached, err := walkForward(ctx, v, tw, s, named, ask.MaxDepth)
			if err != nil {
				return TraceReport{}, err
			}
			var deepest *Trail
			for _, id := range reached {
				t := layTrail(parent, id)
				if deepest == nil || len(t.Steps) > len(deepest.Steps) {
					cp := t
					deepest = &cp
				}
			}
			if deepest != nil {
				trails = append(trails, *deepest)
			}
		}
		sort.Slice(trails, func(i, j int) bool { return len(trails[i].Steps) > len(trails[j].Steps) })
	}
	// Prefer trails that contain the best anchor for some token; a chain of
	// local consts ("stat -> file") is reach, not the asked-about path.
	primary := make(map[model.EntryID]struct{}, len(tokens))
	for _, t := range tokens {
		if rows := anchorRows[t]; len(rows) > 0 {
			primary[rows[0].ID] = struct{}{}
		}
	}
	if len(primary) > 0 && len(trails) > 0 {
		kept := trails[:0]
		for _, tr := range trails {
			for _, id := range tr.Steps {
				if _, ok := primary[id]; ok {
					kept = append(kept, tr)
					break
				}
			}
		}
		if len(kept) > 0 {
			trails = kept
		}
	}
	rep.Trails = keepDistinctTrails(trails, ask.MaxTrails)

	// Ripples cover trail steps first, then anchors.
	headSet := make(map[model.EntryID]struct{})
	var heads []model.EntryID
	for _, t := range rep.Trails {
		for _, id := range t.Steps {
			if _, ok := headSet[id]; !ok {
				headSet[id] = struct{}{}
				heads = append(heads, id)
			}
		}
	}
	var anchorHeads []model.EntryID
	for id := range named {
		if _, ok := headSet[id]; !ok {
			anchorHeads = append(anchorHeads, id)
		}
	}
	sort.Slice(anchorHeads, func(i, j int) bool { return anchorHeads[i] < anchorHeads[j] })
	heads = append(heads, anchorHeads...)
	if len(heads) > rippleMaxHeads {
		heads = heads[:rippleMaxHeads]
	}
	if len(heads) > 0 {
		ripples, err := collectRipples(ctx, v, heads)
		if err != nil {
			return TraceReport{}, err
		}
		if len(ripples) > 0 {
			rep.Ripples = ripples
		}
	}

	// Enrich anchors and steps plus their 1-hop neighborhood.
	want := make(map[model.EntryID]struct{}, len(named)*2)
	for id := range named {
		want[id] = struct{}{}
	}
	for _, t := range rep.Trails {
		for _, id := range t.Steps {
			want[id] = struct{}{}
		}
	}
	var edgeList []model.Edge
	seenEdge := make(map[string]struct{})
	addEdge := func(e model.Edge) {
		key := string(e.From) + "\x00" + string(e.To) + "\x00" + e.Kind
		if _, ok := seenEdge[key]; ok {
			return
		}
		seenEdge[key] = struct{}{}
		edgeList = append(edgeList, e)
	}
	for _, t := range rep.Trails {
		for _, e := range t.Edges {
			addEdge(e)
		}
	}
	var order []model.EntryID
	for id := range want {
		order = append(order, id)
	}
	sort.Slice(order, func(i, j int) bool { return order[i] < order[j] })
	for _, id := range order {
		if err := ctx.Err(); err != nil {
			return TraceReport{}, err
		}
		for _, reverse := range []bool{false, true} {
			nes, err := v.FetchNeighborEdges(ctx, id, ask.Kinds, reverse)
			if err != nil {
				return TraceReport{}, err
			}
			for _, ne := range nes {
				var e model.Edge
				if reverse {
					e = model.Edge{From: ne.Ref.ID, To: id, Kind: ne.Kind, SiteRange: ne.Ref.SiteRange, Cond: ne.Ref.Cond}
				} else {
					e = model.Edge{From: id, To: ne.Ref.ID, Kind: ne.Kind, SiteRange: ne.Ref.SiteRange, Cond: ne.Ref.Cond}
				}
				addEdge(e)
				want[ne.Ref.ID] = struct{}{}
			}
		}
	}
	var ids []string
	for id := range want {
		ids = append(ids, string(id))
	}
	sort.Strings(ids)
	rows, err := v.FetchEntriesByIDs(ctx, ids)
	if err != nil {
		return TraceReport{}, err
	}
	refs := make(map[model.EntryID]model.FunctionRef, len(rows))
	nodes := make([]model.Function, 0, len(rows))
	for _, r := range rows {
		f := r.Model()
		if f.Calls == nil {
			f.Calls = []model.FunctionRef{}
		}
		if f.CalledBy == nil {
			f.CalledBy = []model.FunctionRef{}
		}
		nodes = append(nodes, f)
		refs[r.ID] = model.FunctionRef{
			ID:        r.ID,
			Name:      r.Name,
			FilePath:  r.FilePath,
			LineRange: model.Range{StartLine: r.StartLine, EndLine: r.EndLine},
			Scope:     model.InternalScope(r.FilePath),
		}
	}
	byID := make(map[model.EntryID]*model.Function, len(nodes))
	for i := range nodes {
		byID[nodes[i].ID] = &nodes[i]
	}
	for _, e := range edgeList {
		from, fok := byID[e.From]
		to, tok := byID[e.To]
		if !fok || !tok {
			continue
		}
		withSite := func(r model.FunctionRef, site model.Range) model.FunctionRef {
			r.SiteRange = site
			return r
		}
		switch e.Kind {
		case model.EdgeCalls:
			from.Calls = append(from.Calls, withSite(refs[e.To], e.SiteRange))
			to.CalledBy = append(to.CalledBy, withSite(refs[e.From], e.SiteRange))
		case model.EdgeExtends:
			from.Extends = append(from.Extends, refs[e.To])
		case model.EdgeImplements:
			from.Implements = append(from.Implements, refs[e.To])
		case model.EdgeParam:
			from.ParamTypes = append(from.ParamTypes, refs[e.To])
		case model.EdgeReturn:
			from.ReturnTypes = append(from.ReturnTypes, refs[e.To])
		case model.EdgeUses:
			from.Uses = append(from.Uses, refs[e.To])
		}
	}
	for i := range nodes {
		n := &nodes[i]
		sort.Slice(n.Calls, func(a, b int) bool { return n.Calls[a].ID < n.Calls[b].ID })
		sort.Slice(n.CalledBy, func(a, b int) bool { return n.CalledBy[a].ID < n.CalledBy[b].ID })
		sort.Slice(n.Extends, func(a, b int) bool { return n.Extends[a].ID < n.Extends[b].ID })
		sort.Slice(n.Implements, func(a, b int) bool { return n.Implements[a].ID < n.Implements[b].ID })
		sort.Slice(n.ParamTypes, func(a, b int) bool { return n.ParamTypes[a].ID < n.ParamTypes[b].ID })
		sort.Slice(n.ReturnTypes, func(a, b int) bool { return n.ReturnTypes[a].ID < n.ReturnTypes[b].ID })
		sort.Slice(n.Uses, func(a, b int) bool { return n.Uses[a].ID < n.Uses[b].ID })
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })
	sort.Slice(edgeList, func(i, j int) bool {
		if edgeList[i].From != edgeList[j].From {
			return edgeList[i].From < edgeList[j].From
		}
		if edgeList[i].To != edgeList[j].To {
			return edgeList[i].To < edgeList[j].To
		}
		if edgeList[i].Kind != edgeList[j].Kind {
			return edgeList[i].Kind < edgeList[j].Kind
		}
		return edgeList[i].SiteRange.StartLine < edgeList[j].SiteRange.StartLine
	})
	rep.Nodes = nodes
	rep.Edges = edgeList

	files := make(map[string]struct{}, len(nodes))
	for _, n := range nodes {
		files[n.FilePath] = struct{}{}
		if err := ctx.Err(); err != nil {
			return TraceReport{}, err
		}
		loose, err := v.FetchLooseEndsByOwner(ctx, n.ID)
		if err != nil {
			return TraceReport{}, err
		}
		if len(loose) > 0 {
			out := make([]UnresolvedRef, 0, len(loose))
			for _, l := range loose {
				out = append(out, UnresolvedRef{Name: l.Name, Kind: l.Kind, Scope: l.Scope, SiteRange: model.Range{StartLine: l.SiteStart, EndLine: l.SiteEnd}})
			}
			rep.Loose[string(n.ID)] = out
		}
	}
	for f := range files {
		if err := ctx.Err(); err != nil {
			return TraceReport{}, err
		}
		imps, err := v.FetchImportsByFile(ctx, v.DB(), f)
		if err != nil {
			return TraceReport{}, err
		}
		if len(imps) > 0 {
			out := make([]model.ImportStmt, 0, len(imps))
			for _, r := range imps {
				out = append(out, r.Model())
			}
			rep.Imports[f] = out
		}
	}
	if len(rep.Loose) == 0 {
		rep.Loose = nil
	}
	if len(rep.Imports) == 0 {
		rep.Imports = nil
	}

	entries, err := countIndexed(ctx, v)
	if err != nil {
		return TraceReport{}, err
	}
	tracked, err := countTracked(ctx, v)
	if err != nil {
		return TraceReport{}, err
	}
	rep.Stats = model.Stats{
		FilesScanned:     tracked,
		FunctionsIndexed: entries,
		NodesReturned:    len(rep.Nodes),
		EdgesReturned:    len(rep.Edges),
	}
	return rep, nil
}

// collectRipples maps each head to callers, files, and tests.
func collectRipples(ctx context.Context, v *cold.Vault, heads []model.EntryID) (map[string]Ripple, error) {
	out := make(map[string]Ripple, len(heads))
	for _, h := range heads {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		callers, err := v.CountCallers(ctx, h)
		if err != nil {
			return nil, err
		}
		r := Ripple{Callers: callers}
		seen := map[model.EntryID]struct{}{h: {}}
		frontier := []model.EntryID{h}
		files := map[string]struct{}{}
		tests := map[string]struct{}{}
		cut := false
		for depth := 1; depth <= rippleMaxDepth && len(frontier) > 0; depth++ {
			var next []model.EntryID
			for _, id := range frontier {
				nes, err := v.FetchNeighborEdges(ctx, id, []string{traceCallKindOnly}, true)
				if err != nil {
					return nil, err
				}
				for _, ne := range nes {
					nid := ne.Ref.ID
					if _, ok := seen[nid]; ok {
						continue
					}
					if len(seen) >= rippleMaxNodes {
						cut = true
						break
					}
					seen[nid] = struct{}{}
					if depth == 1 {
						files[ne.Ref.FilePath] = struct{}{}
					}
					if isTraceTestPath(ne.Ref.FilePath) {
						tests[ne.Ref.FilePath] = struct{}{}
					}
					next = append(next, nid)
				}
				if cut {
					break
				}
			}
			frontier = next
			if cut {
				break
			}
		}
		r.Files = cappedSortedKeys(files, rippleMaxFiles)
		r.Tests = cappedSortedKeys(tests, rippleMaxFiles)
		// Totals stay exact past the list caps.
		r.FileTotal = len(files)
		r.TestTotal = len(tests)
		if cut || len(files) > rippleMaxFiles || len(tests) > rippleMaxFiles {
			r.Truncated = true
		}
		out[string(h)] = r
	}
	return out, nil
}

// keepDistinctTrails drops duplicate and subsumed trails.
func keepDistinctTrails(trails []Trail, max int) []Trail {
	var kept []Trail
	var keys []string
	for _, t := range trails {
		parts := make([]string, len(t.Steps))
		for i, s := range t.Steps {
			parts[i] = string(s)
		}
		key := strings.Join(parts, ">")
		dup := false
		for _, other := range keys {
			if other == key || strings.Contains(other, key) {
				dup = true
				break
			}
		}
		if dup {
			continue
		}
		keys = append(keys, key)
		kept = append(kept, t)
		if len(kept) >= max {
			break
		}
	}
	if kept == nil {
		return []Trail{}
	}
	return kept
}

// HandleTrace adapts Trace to a stdio handler; blank asks yield empty.
func (s *Service) HandleTrace(ctx context.Context, raw json.RawMessage) (any, *stdio.RPCError) {
	var ask TraceAsk
	if len(raw) != 0 {
		if err := json.Unmarshal(raw, &ask); err != nil {
			return nil, &stdio.RPCError{Code: stdio.CodeInvalidParams, Message: fmt.Sprintf("trace: invalid params: %v", err)}
		}
	}
	ask = ask.withDefaults()
	if err := ask.validate(); err != nil {
		return nil, &stdio.RPCError{Code: stdio.CodeInvalidParams, Message: fmt.Sprintf("trace: %v", err)}
	}
	rep, err := Trace(ctx, s.vault, ask)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, &stdio.RPCError{Code: stdio.CodeInternalError, Message: "trace: cancelled"}
		}
		if strings.Contains(err.Error(), "methods: trace:") {
			return nil, &stdio.RPCError{Code: stdio.CodeInvalidParams, Message: err.Error()}
		}
		return nil, &stdio.RPCError{Code: stdio.CodeInternalError, Message: fmt.Sprintf("trace: %v", err)}
	}
	return rep, nil
}
