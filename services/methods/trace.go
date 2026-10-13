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
	traceResolveLimit  = 50
	traceDefaultDepth  = 7
	traceDirectedDepth = 12
	traceDepthCeiling  = 25
	traceCallKindOnly  = model.EdgeCalls
)

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

// anchorToken resolves one token exact-first with ranked fallback.
func anchorToken(ctx context.Context, v *cold.Vault, token string, directed bool, pool map[string]struct{}, typeToks []string, lcTokens map[string]struct{}, fileCache map[string]map[string]struct{}) ([]cold.ScoredEntryRow, error) {
	var rows []cold.ScoredEntryRow
	for _, mode := range []struct {
		name string
		call func(context.Context, string, int) ([]cold.ScoredEntryRow, error)
	}{
		{"exact", v.SearchExact},
		{"prefix", v.SearchPrefix},
		{"fts", v.SearchFTS},
		{"substr", v.SearchSubstring},
	} {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		got, err := mode.call(ctx, token, traceResolveLimit)
		if err != nil {
			return nil, fmt.Errorf("methods: trace: resolve %q: %w", token, err)
		}
		if len(got) > 0 {
			rows = got
			break
		}
	}
	if len(rows) == 0 {
		return nil, nil
	}
	fold := strings.ToLower(token)
	scored := make([]int, len(rows))
	worth := make([]float64, len(rows))
	typed := make([]bool, len(rows))
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
	}
	idx := make([]int, len(rows))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool {
		ia, ib := idx[a], idx[b]
		if scored[ia] != scored[ib] {
			return scored[ia] < scored[ib]
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
	if !directed && len(rows) > 3 {
		var kept []cold.ScoredEntryRow
		for _, r := range rows {
			if vesselMentioned(r, pool) || inAskTypedContext(r, typeToks) {
				kept = append(kept, r)
			}
		}
		if len(kept) > 0 {
			rows = kept
		}
	}
	if !directed && !isAskSymbol(token) {
		var kept []cold.ScoredEntryRow
		for _, r := range rows {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if corroboratedAskFile(ctx, v, token, r.FilePath, lcTokens, fileCache) {
				kept = append(kept, r)
			}
		}
		if len(kept) > 0 {
			rows = kept
		}
	}
	if len(rows) > traceMaxPerToken {
		rows = rows[:traceMaxPerToken]
	}
	return rows, nil
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

// walkForward BFSes calls edges with at most one unnamed gap node.
func walkForward(ctx context.Context, v *cold.Vault, seed model.EntryID, named map[model.EntryID]struct{}, maxNodes int) (map[model.EntryID]trailLink, []model.EntryID, error) {
	parent := map[model.EntryID]trailLink{seed: {}}
	type item struct {
		id    model.EntryID
		depth int
		gaps  int
	}
	queue := []item{{id: seed}}
	var reached []model.EntryID
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
		for _, ne := range nes {
			nid := ne.Ref.ID
			if _, seen := parent[nid]; seen {
				continue
			}
			gaps := cur.gaps
			if _, ok := named[nid]; ok {
				gaps = 0
			} else {
				gaps++
			}
			if gaps > 1 {
				continue
			}
			parent[nid] = trailLink{prev: cur.id, edge: model.Edge{From: cur.id, To: nid, Kind: ne.Kind, SiteRange: ne.Ref.SiteRange, Cond: ne.Ref.Cond}}
			queue = append(queue, item{id: nid, depth: cur.depth + 1, gaps: gaps})
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
func walkBoth(ctx context.Context, v *cold.Vault, seed model.EntryID, sinks map[model.EntryID]struct{}, maxNodes int) (Trail, bool, error) {
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
	anchorRows := make(map[string][]cold.ScoredEntryRow, len(tokens))
	named := make(map[model.EntryID]struct{})
	for _, t := range tokens {
		if err := ctx.Err(); err != nil {
			return TraceReport{}, err
		}
		rows, err := anchorToken(ctx, v, t, directed, pool, typeToks, lcTokens, fileCache)
		if err != nil {
			return TraceReport{}, err
		}
		anchorRows[t] = rows
		for _, r := range rows {
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
				break
			}
		}
	}
	if len(named) == 0 {
		return rep, nil
	}

	var trails []Trail
	if directed {
		fromRows := anchorRows[ask.From]
		toIDs := make(map[model.EntryID]struct{}, len(anchorRows[ask.To]))
		for _, r := range anchorRows[ask.To] {
			toIDs[r.ID] = struct{}{}
		}
		if len(fromRows) > 0 && len(toIDs) > 0 {
			for _, r := range fromRows {
				if err := ctx.Err(); err != nil {
					return TraceReport{}, err
				}
				if t, ok, err := walkBoth(ctx, v, r.ID, toIDs, ask.MaxDepth); err != nil {
					return TraceReport{}, err
				} else if ok {
					trails = append(trails, t)
				}
			}
			sort.Slice(trails, func(i, j int) bool { return len(trails[i].Steps) < len(trails[j].Steps) })
		}
	} else if len(named) >= 2 {
		var seeds []model.EntryID
		for id := range named {
			seeds = append(seeds, id)
		}
		sort.Slice(seeds, func(i, j int) bool { return seeds[i] < seeds[j] })
		if len(seeds) > traceMaxSeeds {
			seeds = seeds[:traceMaxSeeds]
		}
		for _, s := range seeds {
			if err := ctx.Err(); err != nil {
				return TraceReport{}, err
			}
			parent, reached, err := walkForward(ctx, v, s, named, ask.MaxDepth)
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
