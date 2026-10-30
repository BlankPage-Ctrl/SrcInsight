package adapters

import (
	"context"
	"path"
	"strings"

	"srcinsight/internal/model"
	"srcinsight/internal/storage/cold"
	"srcinsight/services/resolve"
)

// Go same-package scope shares one directory with no import; this pass keys on dir plus package.

func goPackageForward(ctx context.Context, s resolve.Store, bundles []cold.OriginBundle, pending resolve.Pending, bundleImports map[string][]cold.ImportRow, lc *resolve.LookupCache) error {
	return resolveGoPackageBatch(ctx, s, bundles, pending, bundleImports, lc)
}

func goPackageReverse(ctx context.Context, s resolve.Store, bundles []cold.OriginBundle) error {
	return promoteGoPackageReverse(ctx, s, bundles)
}

// goPackageOf returns the Go package clause, or empty when unknown.
func goPackageOf(imps []cold.ImportRow) string {
	for _, imp := range imps {
		if imp.Kind == model.ImportKindGoPackage {
			return imp.LocalName
		}
	}
	return ""
}

// goSiblingFiles returns known .go siblings sharing the bundle directory.
func goSiblingFiles(bundlePath string, exclude map[string]struct{}, known map[string]struct{}) []string {
	dir := path.Dir(bundlePath)
	var out []string
	for f := range known {
		if f == bundlePath {
			continue
		}
		if _, skip := exclude[f]; skip {
			continue
		}
		if path.Dir(f) != dir || !strings.HasSuffix(f, ".go") {
			continue
		}
		out = append(out, f)
	}
	return out
}

// goKindOK checks whether an entry kind can satisfy a loose-end kind.
func goKindOK(kind string, r cold.EntryRow) bool {
	switch kind {
	case model.EdgeCalls:
		return resolve.IsCallableKind(r.Kind)
	case model.EdgeExtends, model.EdgeImplements, model.EdgeParam, model.EdgeReturn:
		return resolve.IsTypeKind(r.Kind)
	default:
		return true
	}
}

// goLooseEligible checks for plain unresolved names owned by this pass.
func goLooseEligible(le cold.LooseEndRow) bool {
	if le.Scope != model.ScopeUnresolved && le.Scope != model.ScopeInternal {
		return false
	}
	recv, _ := resolve.SplitCrossRecv(le.Name)
	return recv == ""
}

// goPackageOK checks package equality; unknown on either side means false.
func goPackageOK(ctx context.Context, s resolve.Store, bundleImports map[string][]cold.ImportRow, lc *resolve.LookupCache, importerPkg, sib string) (bool, error) {
	if importerPkg == "" {
		return false, nil
	}
	imps, err := resolve.FileImports(ctx, s, bundleImports, lc, sib)
	if err != nil {
		return false, err
	}
	return goPackageOf(imps) == importerPkg, nil
}

// resolveGoPackageBatch runs the forward same-package pass on still-loose names.
func resolveGoPackageBatch(ctx context.Context, s resolve.Store, bundles []cold.OriginBundle, pending resolve.Pending, bundleImports map[string][]cold.ImportRow, lc *resolve.LookupCache) error {
	known, err := resolve.KnownFiles(ctx, s, bundles)
	if err != nil {
		return err
	}
	for i := range bundles {
		b := &bundles[i]
		if b.Origin.Dialect != model.LangGo || len(b.LooseEnds) == 0 {
			continue
		}
		importerPkg := goPackageOf(b.Imports)
		sibs := goSiblingFiles(b.Origin.Path, nil, known)
		if len(sibs) == 0 {
			continue
		}
		seenLink := make(map[cold.LinkRow]struct{}, len(b.Links))
		for _, l := range b.Links {
			seenLink[l] = struct{}{}
		}
		var keep []cold.LooseEndRow
		for _, le := range b.LooseEnds {
			if err := ctx.Err(); err != nil {
				return err
			}
			if !goLooseEligible(le) {
				keep = append(keep, le)
				continue
			}
			linked := false
			for _, sib := range sibs {
				ok, err := goPackageOK(ctx, s, bundleImports, lc, importerPkg, sib)
				if err != nil {
					return err
				}
				if !ok {
					continue
				}
				rows, err := resolve.FileRows(ctx, s, pending, lc, sib, le.Name)
				if err != nil {
					return err
				}
				rows = resolve.ExactCase(rows, le.Name)
				for _, r := range rows {
					if !goKindOK(le.Kind, r) {
						continue
					}
					lr := cold.LinkRow{Origin: le.From, Target: r.ID, Kind: le.Kind, SiteStart: le.SiteStart, SiteEnd: le.SiteEnd, SiteStartCol: le.SiteStartCol, SiteEndCol: le.SiteEndCol, Cond: le.Cond}
					if !lr.Valid() {
						continue
					}
					if _, dup := seenLink[lr]; dup {
						continue
					}
					seenLink[lr] = struct{}{}
					b.Links = append(b.Links, lr)
					linked = true
				}
				if linked {
					break
				}
			}
			if !linked {
				keep = append(keep, le)
			}
		}
		b.LooseEnds = keep
	}
	return nil
}

// promoteGoPackageReverse promotes siblings' loose refs when bundles add entries.
func promoteGoPackageReverse(ctx context.Context, s resolve.Store, bundles []cold.OriginBundle) error {
	known, err := resolve.KnownFiles(ctx, s, bundles)
	if err != nil {
		return err
	}
	bundleImports := make(map[string][]cold.ImportRow, len(bundles))
	for _, b := range bundles {
		bundleImports[b.Origin.Path] = b.Imports
	}
	lc := resolve.NewLookupCache()
	lc.SetKnown(known)
	type promo struct {
		link cold.LinkRow
		lose cold.LooseEndRow
	}
	var promos []promo
	for _, b := range bundles {
		if b.Origin.Dialect != model.LangGo || len(b.Entries) == 0 {
			continue
		}
		importerPkg := goPackageOf(b.Imports)
		if importerPkg == "" {
			continue
		}
		byName := make(map[string][]cold.EntryRow, len(b.Entries))
		for _, e := range b.Entries {
			byName[e.Name] = append(byName[e.Name], e)
		}
		for _, sib := range goSiblingFiles(b.Origin.Path, nil, known) {
			if err := ctx.Err(); err != nil {
				return err
			}
			ok, err := goPackageOK(ctx, s, bundleImports, lc, importerPkg, sib)
			if err != nil {
				return err
			}
			if !ok {
				continue
			}
			loose, err := s.FetchLooseEnds(ctx, sib)
			if err != nil {
				return err
			}
			for _, le := range loose {
				if !goLooseEligible(le) {
					continue
				}
				for _, e := range byName[le.Name] {
					if !goKindOK(le.Kind, e) {
						continue
					}
					lr := cold.LinkRow{Origin: le.From, Target: e.ID, Kind: le.Kind, SiteStart: le.SiteStart, SiteEnd: le.SiteEnd, SiteStartCol: le.SiteStartCol, SiteEndCol: le.SiteEndCol, Cond: le.Cond}
					if !lr.Valid() {
						continue
					}
					promos = append(promos, promo{link: lr, lose: le})
				}
			}
		}
	}
	if len(promos) == 0 {
		return nil
	}
	seen := make(map[cold.LinkRow]struct{}, len(promos))
	var links []cold.LinkRow
	var deletes []cold.LooseEndRow
	for _, pr := range promos {
		if _, dup := seen[pr.link]; dup {
			continue
		}
		seen[pr.link] = struct{}{}
		links = append(links, pr.link)
		deletes = append(deletes, pr.lose)
	}
	return s.PromoteLinks(ctx, links, deletes)
}
