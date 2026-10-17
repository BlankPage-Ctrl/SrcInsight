// Package ts resolves constructor-injected this.field.method calls in two passes.
package ts

import (
	"context"
	"strings"

	"srcinsight/internal/model"
	"srcinsight/internal/storage/cold"
	"srcinsight/services/resolve"
)

// DIChainTarget resolves one DI loose end via constructor binds and imports.
func DIChainTarget(
	ctx context.Context,
	s resolve.Store,
	b *cold.OriginBundle,
	byID map[model.EntryID]cold.EntryRow,
	importByLocal map[string][]cold.ImportRow,
	resolver resolve.ImportResolver,
	pending resolve.Pending,
	lc *resolve.LookupCache,
	le cold.LooseEndRow,
) ([]cold.EntryRow, error) {
	if le.Kind != model.EdgeCalls || le.Dialect != model.LangTypeScript {
		return nil, nil
	}
	field, method, ok := SplitThisChain(le.Name)
	if !ok {
		return nil, nil
	}
	owner, ok := byID[le.From]
	if !ok || owner.VesselName == "" {
		return nil, nil
	}
	cls := owner.VesselName
	// Constructor overloads union their binds.
	bindTypes := make(map[string]string)
	for _, e := range b.Entries {
		if e.Kind != model.KindConstructor || e.VesselName != cls {
			continue
		}
		for _, bd := range b.RecvBinds {
			if bd.Owner == e.Qualified && bd.Recv != "" && bd.Type != "" {
				if _, dup := bindTypes[bd.Recv]; !dup {
					bindTypes[bd.Recv] = bd.Type
				}
			}
		}
	}
	typ, ok := bindTypes[field]
	if !ok || typ == "" {
		return nil, nil
	}
	candImps, ok := importByLocal[typ]
	if !ok || len(candImps) == 0 {
		return nil, nil
	}
	// Matches in exactly one file win; spread matches stay loose.
	var hits []cold.EntryRow
	hitFile := ""
	ambiguous := false
	for _, imp := range candImps {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for _, f := range lc.CandidatePaths(resolver, b.Origin.Path, imp.Source) {
			rows, err := resolve.FileRows(ctx, s, pending, lc, f, method)
			if err != nil {
				return nil, err
			}
			if exact := resolve.ExactCase(rows, method); len(exact) > 0 {
				rows = exact
			}
			var mine []cold.EntryRow
			for _, r := range rows {
				if r.VesselName == typ && resolve.IsCallableKind(r.Kind) {
					mine = append(mine, r)
				}
			}
			if len(mine) == 0 {
				continue
			}
			if hitFile != "" && hitFile != f {
				ambiguous = true
				break
			}
			hitFile = f
			hits = append(hits, mine...)
		}
		if ambiguous {
			break
		}
	}
	if ambiguous || len(hits) == 0 {
		return nil, nil
	}
	return hits, nil
}

// ResolveDIBatch runs the forward DI pass inside the batch.
func ResolveDIBatch(
	ctx context.Context,
	s resolve.Store,
	bundles []cold.OriginBundle,
	pending resolve.Pending,
	lc *resolve.LookupCache,
) error {
	for i := range bundles {
		b := &bundles[i]
		if b.Origin.Dialect != model.LangTypeScript || len(b.LooseEnds) == 0 {
			continue
		}
		hasChain := false
		for _, le := range b.LooseEnds {
			if le.Kind != model.EdgeCalls {
				continue
			}
			if _, _, ok := SplitThisChain(le.Name); ok {
				hasChain = true
				break
			}
		}
		if !hasChain {
			continue
		}
		resolver := s.ResolverFor(b.Origin.Dialect)
		if resolver == nil {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		byID := make(map[model.EntryID]cold.EntryRow, len(b.Entries))
		for _, e := range b.Entries {
			byID[e.ID] = e
		}
		importByLocal := resolve.ProbeIndex(b.Imports)
		seenLink := make(map[cold.LinkRow]struct{}, len(b.Links))
		for _, l := range b.Links {
			seenLink[l] = struct{}{}
		}
		var keepLoose []cold.LooseEndRow
		for _, le := range b.LooseEnds {
			targets, err := DIChainTarget(ctx, s, b, byID, importByLocal, resolver, pending, lc, le)
			if err != nil {
				return err
			}
			if len(targets) == 0 {
				keepLoose = append(keepLoose, le)
				continue
			}
			for _, t := range targets {
				lr := cold.LinkRow{Origin: le.From, Target: t.ID, Kind: le.Kind, SiteStart: le.SiteStart, SiteEnd: le.SiteEnd, SiteStartCol: le.SiteStartCol, SiteEndCol: le.SiteEndCol, Cond: le.Cond}
				if !lr.Valid() {
					continue
				}
				if _, dup := seenLink[lr]; dup {
					continue
				}
				seenLink[lr] = struct{}{}
				b.Links = append(b.Links, lr)
			}
		}
		b.LooseEnds = keepLoose
	}
	return nil
}

// PromoteDIReverse revisits other files DI chains bound to fresh types.
func PromoteDIReverse(ctx context.Context, s resolve.Store, bundles []cold.OriginBundle) error {
	if len(bundles) == 0 {
		return nil
	}
	type freshMethod struct {
		id     model.EntryID
		name   string
		vessel string
	}
	freshByType := make(map[string][]freshMethod)
	exclude := make(map[string]struct{}, len(bundles))
	for _, b := range bundles {
		exclude[b.Origin.Path] = struct{}{}
		if b.Origin.Dialect != model.LangTypeScript {
			continue
		}
		for _, e := range b.Entries {
			if e.VesselName == "" || !resolve.IsCallableKind(e.Kind) || resolve.IsTypeKind(e.Kind) {
				continue
			}
			freshByType[e.VesselName] = append(freshByType[e.VesselName], freshMethod{id: e.ID, name: e.Name, vessel: e.VesselName})
		}
	}
	if len(freshByType) == 0 {
		return nil
	}
	type promo struct {
		link cold.LinkRow
		lose cold.LooseEndRow
	}
	var promos []promo
	for typ, methods := range freshByType {
		if err := ctx.Err(); err != nil {
			return err
		}
		binds, err := s.FetchRecvBindsByType(ctx, typ)
		if err != nil {
			return err
		}
		byFile := make(map[string][]cold.RecvBindRow)
		for _, bd := range binds {
			if bd.Type != typ || bd.Recv == "" {
				continue
			}
			if _, skip := exclude[bd.FilePath]; skip {
				continue
			}
			byFile[bd.FilePath] = append(byFile[bd.FilePath], bd)
		}
		for file, fbinds := range byFile {
			if err := ctx.Err(); err != nil {
				return err
			}
			entries, err := s.FetchEntries(ctx, file)
			if err != nil {
				return err
			}
			qualVessel := make(map[string]string, len(entries))
			idVessel := make(map[model.EntryID]string, len(entries))
			for _, e := range entries {
				if _, dup := qualVessel[e.Qualified]; !dup {
					qualVessel[e.Qualified] = e.VesselName
				}
				idVessel[e.ID] = e.VesselName
			}
			loose, err := s.FetchLooseEnds(ctx, file)
			if err != nil {
				return err
			}
			for _, le := range loose {
				if le.Kind != model.EdgeCalls {
					continue
				}
				field, method, ok := SplitThisChain(le.Name)
				if !ok {
					continue
				}
				bound := false
				for _, bd := range fbinds {
					if bd.Recv != field {
						continue
					}
					// Bind owner must vessel the loose owner class.
					if cls := qualVessel[bd.Owner]; cls != "" && cls == idVessel[le.From] {
						bound = true
						break
					}
				}
				if !bound {
					continue
				}
				for _, m := range methods {
					if !strings.EqualFold(m.name, method) {
						continue
					}
					if m.name != method {
						exact := false
						for _, m2 := range methods {
							if m2.name == method {
								exact = true
								break
							}
						}
						if exact {
							continue
						}
					}
					promos = append(promos, promo{
						link: cold.LinkRow{Origin: le.From, Target: m.id, Kind: le.Kind, SiteStart: le.SiteStart, SiteEnd: le.SiteEnd, SiteStartCol: le.SiteStartCol, SiteEndCol: le.SiteEndCol, Cond: le.Cond},
						lose: le,
					})
				}
			}
		}
	}
	if len(promos) == 0 {
		return nil
	}
	seen := make(map[cold.LinkRow]struct{})
	var uniqueLinks []cold.LinkRow
	var toDelete []cold.LooseEndRow
	for _, pr := range promos {
		if _, ok := seen[pr.link]; ok {
			continue
		}
		seen[pr.link] = struct{}{}
		uniqueLinks = append(uniqueLinks, pr.link)
		toDelete = append(toDelete, pr.lose)
	}
	return s.PromoteLinks(ctx, uniqueLinks, toDelete)
}
