package methods

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"srcinsight/internal/convention"
	"srcinsight/internal/model"
	"srcinsight/internal/storage/cold"
	"srcinsight/internal/transport"
)

// TransportAsk queries flows by route or operation. Method and Path target
// REST endpoints; Operation targets GraphQL/WS. Limit caps returned flows.
type TransportAsk struct {
	Method    string `json:"method"`
	Path      string `json:"path"`
	Operation string `json:"operation"`
	Limit     int    `json:"limit"`
}

// TransportFlow is one matched client call to server endpoint pair.
type TransportFlow struct {
	Score    float64                   `json:"score"`
	Caller   model.EntryID             `json:"caller,omitempty"`
	Endpoint model.EntryID             `json:"endpoint,omitempty"`
	Call     cold.TransportCallRow     `json:"call"`
	Route    cold.TransportEndpointRow `json:"route"`
}

// TransportReport holds matched flows plus their node and edge sets.
type TransportReport struct {
	Flows []TransportFlow  `json:"flows"`
	Nodes []model.Function `json:"nodes"`
	Edges []model.Edge     `json:"edges"`
	Stats model.Stats      `json:"stats"`
}

const (
	transportDefaultLimit = 20
	transportMaxLimit     = 50
	transportCandCap      = 500
)

func (a TransportAsk) withDefaults() TransportAsk {
	a.Method = convention.NormalizeMethod(a.Method)
	a.Path = convention.NormalizePath(a.Path)
	if a.Limit <= 0 {
		a.Limit = transportDefaultLimit
	}
	if a.Limit > transportMaxLimit {
		a.Limit = transportMaxLimit
	}
	return a
}

// matchHTTP pairs calls with endpoints on normalized paths, scoring
// method agreement. Candidates arrive pre-filtered by verb.
func matchHTTP(calls []cold.TransportCallRow, endpoints []cold.TransportEndpointRow) []TransportFlow {
	var out []TransportFlow
	for _, c := range calls {
		if c.URL == "" {
			continue
		}
		for _, e := range endpoints {
			if e.Path == "" || !transport.MatchPath(c.URL, e.Path) {
				continue
			}
			score := 0.7
			if c.Method != "" && e.Method != "" {
				if c.Method != e.Method {
					continue
				}
				score = 1
			}
			out = append(out, TransportFlow{
				Score:    score,
				Caller:   c.Owner,
				Endpoint: e.Owner,
				Call:     c,
				Route:    e,
			})
		}
	}
	return out
}

// matchOperation pairs GraphQL/WS hints on case-insensitive operation names.
func matchOperation(calls []cold.TransportCallRow, endpoints []cold.TransportEndpointRow) []TransportFlow {
	byOp := make(map[string][]cold.TransportEndpointRow)
	for _, e := range endpoints {
		if e.Operation == "" {
			continue
		}
		byOp[strings.ToLower(e.Operation)] = append(byOp[strings.ToLower(e.Operation)], e)
	}
	var out []TransportFlow
	for _, c := range calls {
		if c.Operation == "" {
			continue
		}
		for _, e := range byOp[strings.ToLower(c.Operation)] {
			out = append(out, TransportFlow{
				Score:    1,
				Caller:   c.Owner,
				Endpoint: e.Owner,
				Call:     c,
				Route:    e,
			})
		}
	}
	return out
}

// Transport resolves flows for one route or operation query.
func Transport(ctx context.Context, v *cold.Vault, ask TransportAsk) (TransportReport, error) {
	ask = ask.withDefaults()
	rep := TransportReport{}
	if ask.Operation == "" && ask.Path == "" {
		return rep, fmt.Errorf("methods: transport: path or operation is required")
	}
	if err := ctx.Err(); err != nil {
		return rep, err
	}
	var flows []TransportFlow
	if ask.Operation != "" {
		endpoints, err := v.FetchTransportEndpointsByOperation(ctx, ask.Operation, transportCandCap)
		if err != nil {
			return rep, err
		}
		calls, err := v.FetchTransportCallsByOperation(ctx, ask.Operation, transportCandCap)
		if err != nil {
			return rep, err
		}
		flows = matchOperation(calls, endpoints)
	} else {
		endpoints, err := v.FetchTransportEndpointsByMethod(ctx, ask.Method, transportCandCap)
		if err != nil {
			return rep, err
		}
		var kept []cold.TransportEndpointRow
		for _, e := range endpoints {
			if e.Path == "" {
				continue
			}
			if ask.Path == "" || transport.MatchPath(ask.Path, e.Path) || transport.MatchPath(e.Path, ask.Path) {
				kept = append(kept, e)
			}
		}
		var calls []cold.TransportCallRow
		if ask.Method != "" {
			got, err := v.FetchTransportCallsByMethod(ctx, ask.Method, transportCandCap)
			if err != nil {
				return rep, err
			}
			calls = append(calls, got...)
		}
		got, err := v.FetchTransportCallsByMethod(ctx, "", transportCandCap)
		if err != nil {
			return rep, err
		}
		calls = append(calls, got...)
		flows = matchHTTP(calls, kept)
	}
	sort.Slice(flows, func(i, j int) bool {
		if flows[i].Score != flows[j].Score {
			return flows[i].Score > flows[j].Score
		}
		if flows[i].Caller != flows[j].Caller {
			return flows[i].Caller < flows[j].Caller
		}
		return flows[i].Endpoint < flows[j].Endpoint
	})
	if len(flows) > ask.Limit {
		flows = flows[:ask.Limit]
	}
	rep.Flows = flows
	nodes, edges, err := transportNodesEdges(ctx, v, flows)
	if err != nil {
		return rep, err
	}
	rep.Nodes = nodes
	rep.Edges = edges
	entries, err := countIndexed(ctx, v)
	if err != nil {
		return rep, err
	}
	tracked, err := countTracked(ctx, v)
	if err != nil {
		return rep, err
	}
	rep.Stats = model.Stats{
		FilesScanned:     tracked,
		FunctionsIndexed: entries,
		NodesReturned:    len(rep.Nodes),
		EdgesReturned:    len(rep.Edges),
	}
	return rep, nil
}

// transportNodesEdges hydrates flow owners into nodes plus transport edges.
func transportNodesEdges(ctx context.Context, v *cold.Vault, flows []TransportFlow) ([]model.Function, []model.Edge, error) {
	want := make(map[model.EntryID]struct{})
	for _, f := range flows {
		if f.Caller != "" {
			want[f.Caller] = struct{}{}
		}
		if f.Endpoint != "" {
			want[f.Endpoint] = struct{}{}
		}
	}
	var ids []string
	for id := range want {
		ids = append(ids, string(id))
	}
	sort.Strings(ids)
	rows, err := v.FetchEntriesByIDs(ctx, ids)
	if err != nil {
		return nil, nil, err
	}
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
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })
	var edges []model.Edge
	seen := make(map[string]struct{})
	for _, f := range flows {
		if f.Caller == "" || f.Endpoint == "" {
			continue
		}
		key := string(f.Caller) + "\x00" + string(f.Endpoint)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		edges = append(edges, model.Edge{
			From: f.Caller,
			To:   f.Endpoint,
			Kind: model.EdgeTransport,
			SiteRange: model.Range{
				StartLine: f.Call.SiteStart,
				EndLine:   f.Call.SiteStart,
			},
		})
	}
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].From != edges[j].From {
			return edges[i].From < edges[j].From
		}
		return edges[i].To < edges[j].To
	})
	return nodes, edges, nil
}

// matchCall pairs one call row against endpoint candidates.
func matchCall(c cold.TransportCallRow, endpoints []cold.TransportEndpointRow) []TransportFlow {
	if c.Operation != "" {
		return matchOperation([]cold.TransportCallRow{c}, endpoints)
	}
	return matchHTTP([]cold.TransportCallRow{c}, endpoints)
}

// matchEndpoint pairs one endpoint row against call candidates.
func matchEndpoint(e cold.TransportEndpointRow, calls []cold.TransportCallRow) []TransportFlow {
	if e.Operation != "" {
		return matchOperation(calls, []cold.TransportEndpointRow{e})
	}
	return matchHTTP(calls, []cold.TransportEndpointRow{e})
}
