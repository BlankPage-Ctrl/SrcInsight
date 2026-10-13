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

// GraphOptions takes an exact EntryID; depth and limit are ignored.
type GraphOptions struct {
	ID    string   `json:"id"`
	Kinds []string `json:"kinds"`
	// Deprecated: ignored.
	Depth         int `json:"depth"`
	CalledByDepth int `json:"calledByDepth"`
	Limit         int `json:"limit"`
}

var defaultGraphKinds = []string{
	model.EdgeCalls,
	model.EdgeExtends,
	model.EdgeImplements,
	model.EdgeParam,
	model.EdgeReturn,
	model.EdgeUses,
}

var validEdgeKinds = map[string]struct{}{
	model.EdgeCalls:      {},
	model.EdgeExtends:    {},
	model.EdgeImplements: {},
	model.EdgeParam:      {},
	model.EdgeReturn:     {},
	model.EdgeUses:       {},
}

func (o GraphOptions) normalize() GraphOptions {
	if len(o.Kinds) == 0 {
		o.Kinds = append([]string(nil), defaultGraphKinds...)
	}
	return o
}

func (o GraphOptions) validate() error {
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

// Graph returns the single-root graph for an exact EntryID with 1-hop edges.
func Graph(ctx context.Context, v *cold.Vault, opts GraphOptions) (model.Graph, error) {
	opts = opts.normalize()
	if err := opts.validate(); err != nil {
		return model.Graph{}, fmt.Errorf("methods: graph: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return model.Graph{}, err
	}

	rootRow, found, err := v.FetchEntryByID(ctx, opts.ID)
	if err != nil {
		return model.Graph{}, err
	}
	if !found {
		return model.Graph{}, fmt.Errorf("entry ID not found: %s", opts.ID)
	}
	root := rootRow.Model()
	if root.Calls == nil {
		root.Calls = []model.FunctionRef{}
	}
	if root.CalledBy == nil {
		root.CalledBy = []model.FunctionRef{}
	}

	edges := []model.Edge{}

	out, err := v.FetchNeighborEdges(ctx, root.ID, opts.Kinds, false)
	if err != nil {
		return model.Graph{}, err
	}
	for _, ne := range out {
		edges = append(edges, model.Edge{From: root.ID, To: ne.Ref.ID, Kind: ne.Kind, SiteRange: ne.Ref.SiteRange, Cond: ne.Ref.Cond})
		switch ne.Kind {
		case model.EdgeCalls:
			root.Calls = append(root.Calls, ne.Ref)
		case model.EdgeExtends:
			root.Extends = append(root.Extends, ne.Ref)
		case model.EdgeImplements:
			root.Implements = append(root.Implements, ne.Ref)
		case model.EdgeParam:
			root.ParamTypes = append(root.ParamTypes, ne.Ref)
		case model.EdgeReturn:
			root.ReturnTypes = append(root.ReturnTypes, ne.Ref)
		case model.EdgeUses:
			root.Uses = append(root.Uses, ne.Ref)
		}
	}

	in, err := v.FetchNeighborEdges(ctx, root.ID, opts.Kinds, true)
	if err != nil {
		return model.Graph{}, err
	}
	for _, ne := range in {
		edges = append(edges, model.Edge{From: ne.Ref.ID, To: root.ID, Kind: ne.Kind, SiteRange: ne.Ref.SiteRange, Cond: ne.Ref.Cond})
		// CalledBy covers reverse calls only.
		if ne.Kind == model.EdgeCalls {
			root.CalledBy = append(root.CalledBy, ne.Ref)
		} else if ne.Kind == model.EdgeUses {
		}
	}

	sort.Slice(root.Calls, func(i, j int) bool {
		if root.Calls[i].ID != root.Calls[j].ID {
			return root.Calls[i].ID < root.Calls[j].ID
		}
		return root.Calls[i].SiteRange.StartLine < root.Calls[j].SiteRange.StartLine
	})
	sort.Slice(root.CalledBy, func(i, j int) bool {
		if root.CalledBy[i].ID != root.CalledBy[j].ID {
			return root.CalledBy[i].ID < root.CalledBy[j].ID
		}
		return root.CalledBy[i].SiteRange.StartLine < root.CalledBy[j].SiteRange.StartLine
	})
	sort.Slice(root.Extends, func(i, j int) bool { return root.Extends[i].ID < root.Extends[j].ID })
	sort.Slice(root.Implements, func(i, j int) bool { return root.Implements[i].ID < root.Implements[j].ID })
	sort.Slice(root.ParamTypes, func(i, j int) bool { return root.ParamTypes[i].ID < root.ParamTypes[j].ID })
	sort.Slice(root.ReturnTypes, func(i, j int) bool { return root.ReturnTypes[i].ID < root.ReturnTypes[j].ID })
	sort.Slice(root.Uses, func(i, j int) bool { return root.Uses[i].ID < root.Uses[j].ID })
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
		return edges[i].SiteRange.StartLine < edges[j].SiteRange.StartLine
	})

	return model.Graph{Nodes: []model.Function{root}, Edges: edges}, nil
}

func (s *Service) HandleGraph(ctx context.Context, raw json.RawMessage) (any, *stdio.RPCError) {
	var opts GraphOptions
	if len(raw) != 0 {
		if err := json.Unmarshal(raw, &opts); err != nil {
			return nil, &stdio.RPCError{Code: stdio.CodeInvalidParams, Message: fmt.Sprintf("graph: invalid params: %v", err)}
		}
	}
	opts = opts.normalize()
	if err := opts.validate(); err != nil {
		return nil, &stdio.RPCError{Code: stdio.CodeInvalidParams, Message: fmt.Sprintf("graph: %v", err)}
	}
	res, err := Graph(ctx, s.vault, opts)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, &stdio.RPCError{Code: stdio.CodeInternalError, Message: "graph: cancelled"}
		}
		if strings.Contains(err.Error(), "entry ID not found") {
			return nil, &stdio.RPCError{Code: stdio.CodeInvalidParams, Message: err.Error()}
		}
		if strings.Contains(err.Error(), "methods: graph:") {
			return nil, &stdio.RPCError{Code: stdio.CodeInvalidParams, Message: err.Error()}
		}
		return nil, &stdio.RPCError{Code: stdio.CodeInternalError, Message: fmt.Sprintf("graph: %v", err)}
	}
	return res, nil
}
