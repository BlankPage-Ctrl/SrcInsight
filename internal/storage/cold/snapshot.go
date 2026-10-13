package cold

import (
	"context"
	"sort"

	"srcinsight/internal/model"
)

// AssembleSnapshot materializes the vault as a deterministic model.Graph.
//
// Deprecated: test-only full-table scans, never for production paths.
func (v *Vault) AssembleSnapshot(ctx context.Context) (model.Graph, error) {
	entries, err := v.FetchAllEntries(ctx)
	if err != nil {
		return model.Graph{}, err
	}
	links, err := v.FetchAllLinks(ctx)
	if err != nil {
		return model.Graph{}, err
	}
	loose, err := v.FetchAllLooseEnds(ctx)
	if err != nil {
		return model.Graph{}, err
	}

	nodes := make([]model.Function, 0, len(entries))
	byID := make(map[model.EntryID]*model.Function, len(entries))
	live := make(map[model.EntryID]struct{}, len(entries))
	for _, r := range entries {
		f := r.Model()
		nodes = append(nodes, f)
		byID[f.ID] = &nodes[len(nodes)-1]
		live[f.ID] = struct{}{}
	}

	edges := make([]model.Edge, 0, len(links))
	for _, l := range links {
		from, ok := byID[l.Origin]
		if !ok {
			continue
		}
		to, ok := byID[l.Target]
		if !ok {
			continue
		}
		ref := refOf(*to)
		ref.SiteRange = model.Range{StartLine: l.SiteStart, EndLine: l.SiteEnd}
		switch l.Kind {
		case model.EdgeCalls:
			from.Calls = append(from.Calls, ref)
		case model.EdgeExtends:
			from.Extends = append(from.Extends, ref)
		case model.EdgeImplements:
			from.Implements = append(from.Implements, ref)
		case model.EdgeParam:
			from.ParamTypes = append(from.ParamTypes, ref)
		case model.EdgeReturn:
			from.ReturnTypes = append(from.ReturnTypes, ref)
		case model.EdgeUses:
			from.Uses = append(from.Uses, ref)
		default:
			continue
		}
		edges = append(edges, model.Edge{From: l.Origin, To: l.Target, Kind: l.Kind, SiteRange: model.Range{StartLine: l.SiteStart, EndLine: l.SiteEnd}})
	}

	for _, l := range loose {
		from, ok := byID[l.From]
		if !ok {
			continue
		}
		ref := model.FunctionRef{Name: l.Name, Scope: l.Scope, SiteRange: model.Range{StartLine: l.SiteStart, EndLine: l.SiteEnd}}
		switch l.Kind {
		case model.EdgeCalls:
			from.Calls = append(from.Calls, ref)
		case model.EdgeExtends:
			from.Extends = append(from.Extends, ref)
		case model.EdgeImplements:
			from.Implements = append(from.Implements, ref)
		case model.EdgeParam:
			from.ParamTypes = append(from.ParamTypes, ref)
		case model.EdgeReturn:
			from.ReturnTypes = append(from.ReturnTypes, ref)
		case model.EdgeUses:
			from.Uses = append(from.Uses, ref)
		default:
			continue
		}
	}

	// CalledBy derives from calls links, never stored.
	for _, l := range links {
		if l.Kind != model.EdgeCalls {
			continue
		}
		to, ok := byID[l.Target]
		if !ok {
			continue
		}
		from, ok := byID[l.Origin]
		if !ok {
			continue
		}
		cr := refOf(*from)
		cr.SiteRange = model.Range{StartLine: l.SiteStart, EndLine: l.SiteEnd}
		to.CalledBy = append(to.CalledBy, cr)
	}
	for i := range nodes {
		nodes[i].CalledBy = sortedRefs(nodes[i].CalledBy)
		if nodes[i].Calls == nil {
			nodes[i].Calls = []model.FunctionRef{}
		}
	}

	sort.Slice(edges, func(i, j int) bool {
		if edges[i].From != edges[j].From {
			return edges[i].From < edges[j].From
		}
		if edges[i].To != edges[j].To {
			return edges[i].To < edges[j].To
		}
		if edges[i].Kind != edges[j].Kind {
			return edges[i].Kind < edges[j].Kind
		}
		if edges[i].SiteRange.StartLine != edges[j].SiteRange.StartLine {
			return edges[i].SiteRange.StartLine < edges[j].SiteRange.StartLine
		}
		return edges[i].SiteRange.EndLine < edges[j].SiteRange.EndLine
	})
	return model.Graph{Nodes: nodes, Edges: edges}, nil
}
