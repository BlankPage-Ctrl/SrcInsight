package resolve

import (
	"context"
	"strings"

	"srcinsight/internal/model"
	"srcinsight/internal/storage/cold"
)

// ResolveOutgoingCross promotes internal loose ends to links via filtered reads.
func ResolveOutgoingCross(ctx context.Context, s Store, b *cold.OriginBundle) error {
	if b == nil || len(b.LooseEnds) == 0 {
		return nil
	}
	dialect := b.Origin.Dialect
	resolver := s.ResolverFor(dialect)
	if resolver == nil {
		return nil
	}
	importByLocal := ProbeIndex(b.Imports)

	var keepLoose []cold.LooseEndRow
	var extraLinks []cold.LinkRow
	lc := NewLookupCache()

	seenLink := make(map[cold.LinkRow]struct{})
	for _, existing := range b.Links {
		seenLink[existing] = struct{}{}
	}

	for _, le := range b.LooseEnds {
		if le.Scope != model.ScopeInternal && !strings.HasPrefix(le.Scope, model.ScopeInternal+":") {
			keepLoose = append(keepLoose, le)
			continue
		}
		recv, simple := SplitCrossRecv(le.Name)
		probe := le.Name
		if recv != "" {
			probe = recv
		}
		candImps, ok := importByLocal[probe]
		if !ok || len(candImps) == 0 {
			keepLoose = append(keepLoose, le)
			continue
		}
		var candidateFiles []string
		for _, imp := range candImps {
			paths := lc.CandidatePaths(resolver, b.Origin.Path, imp.Source)
			candidateFiles = append(candidateFiles, paths...)
		}
		if len(candidateFiles) == 0 {
			keepLoose = append(keepLoose, le)
			continue
		}
		targetName := le.Name
		if recv != "" {
			targetName = simple
		}
		isHeritage := le.Kind == model.EdgeExtends || le.Kind == model.EdgeImplements
		isTypeRef := isHeritage || le.Kind == model.EdgeParam || le.Kind == model.EdgeReturn
		isUses := le.Kind == model.EdgeUses
		rows, err := DefiningTargets(ctx, s, candidateFiles, targetName, isTypeRef && !isUses, resolver, map[string][]cold.ImportRow{b.Origin.Path: b.Imports}, nil, lc)
		if err != nil {
			return err
		}
		for _, r := range rows {
			if isUses {
				// Uses allows any kind.
			}
			lr := cold.LinkRow{Origin: le.From, Target: r.ID, Kind: le.Kind, SiteStart: le.SiteStart, SiteEnd: le.SiteEnd, SiteStartCol: le.SiteStartCol, SiteEndCol: le.SiteEndCol, Cond: le.Cond}
			if !lr.Valid() {
				continue
			}
			if _, ok := seenLink[lr]; ok {
				continue
			}
			seenLink[lr] = struct{}{}
			extraLinks = append(extraLinks, lr)
		}
		if n := len(extraLinks); n > 0 && extraLinks[n-1].Origin == le.From {
			continue
		}
		keepLoose = append(keepLoose, le)
	}

	if len(extraLinks) > 0 {
		b.Links = append(b.Links, extraLinks...)
	}
	b.LooseEnds = keepLoose
	return nil
}

// ResolveOutgoingCrossWithPending adds in-batch visibility to the cross pass.
func ResolveOutgoingCrossWithPending(ctx context.Context, s Store, b *cold.OriginBundle, pending Pending, bundleImports map[string][]cold.ImportRow, lc *LookupCache) error {
	if b == nil || len(b.LooseEnds) == 0 {
		return nil
	}
	dialect := b.Origin.Dialect
	resolver := s.ResolverFor(dialect)
	if resolver == nil {
		return nil
	}
	importByLocal := ProbeIndex(b.Imports)
	keepLoose := make([]cold.LooseEndRow, 0, len(b.LooseEnds))
	extraLinks := make([]cold.LinkRow, 0, len(b.LooseEnds)/4+1)
	seenLink := make(map[cold.LinkRow]struct{}, len(b.Links)+len(b.LooseEnds)/4+1)
	for _, existing := range b.Links {
		seenLink[existing] = struct{}{}
	}
	for _, le := range b.LooseEnds {
		if le.Scope != model.ScopeInternal && !strings.HasPrefix(le.Scope, model.ScopeInternal+":") {
			keepLoose = append(keepLoose, le)
			continue
		}
		recv, simple := SplitCrossRecv(le.Name)
		probe := le.Name
		if recv != "" {
			probe = recv
		}
		candImps, ok := importByLocal[probe]
		if !ok || len(candImps) == 0 {
			keepLoose = append(keepLoose, le)
			continue
		}
		candidateFiles := make([]string, 0, len(candImps)*6)
		for _, imp := range candImps {
			paths := lc.CandidatePaths(resolver, b.Origin.Path, imp.Source)
			candidateFiles = append(candidateFiles, paths...)
		}
		if len(candidateFiles) == 0 {
			keepLoose = append(keepLoose, le)
			continue
		}
		targetName := le.Name
		if recv != "" {
			targetName = simple
		}
		isHeritage := le.Kind == model.EdgeExtends || le.Kind == model.EdgeImplements
		isTypeRef := isHeritage || le.Kind == model.EdgeParam || le.Kind == model.EdgeReturn
		rows, err := DefiningTargets(ctx, s, candidateFiles, targetName, isTypeRef, resolver, bundleImports, pending, lc)
		if err != nil {
			return err
		}
		for _, r := range rows {
			lr := cold.LinkRow{Origin: le.From, Target: r.ID, Kind: le.Kind, SiteStart: le.SiteStart, SiteEnd: le.SiteEnd, SiteStartCol: le.SiteStartCol, SiteEndCol: le.SiteEndCol, Cond: le.Cond}
			if !lr.Valid() {
				continue
			}
			if _, ok := seenLink[lr]; ok {
				continue
			}
			seenLink[lr] = struct{}{}
			extraLinks = append(extraLinks, lr)
		}
		if n := len(extraLinks); n > 0 && extraLinks[n-1].Origin == le.From {
			continue
		}
		keepLoose = append(keepLoose, le)
	}
	if len(extraLinks) > 0 {
		b.Links = append(b.Links, extraLinks...)
	}
	b.LooseEnds = keepLoose
	return nil
}

// PromoteReverseCross promotes other files loose ends to fresh entries.
func PromoteReverseCross(ctx context.Context, s Store, bundles []cold.OriginBundle) error {
	if len(bundles) == 0 {
		return nil
	}
	newByFile := make(map[string][]cold.EntryRow, len(bundles))
	newNamesSet := make(map[string]struct{})
	exclude := make(map[string]struct{}, len(bundles))
	for _, b := range bundles {
		exclude[b.Origin.Path] = struct{}{}
		for _, e := range b.Entries {
			newByFile[b.Origin.Path] = append(newByFile[b.Origin.Path], e)
			newNamesSet[strings.ToLower(e.Name)] = struct{}{}
		}
	}
	if len(newNamesSet) == 0 {
		return nil
	}
	names := make([]string, 0, len(newNamesSet))
	for n := range newNamesSet {
		names = append(names, n)
	}
	lowerToEntries := make(map[string][]cold.EntryRow, len(newNamesSet))
	for _, entries := range newByFile {
		for _, e := range entries {
			ln := strings.ToLower(e.Name)
			lowerToEntries[ln] = append(lowerToEntries[ln], e)
		}
	}

	candidates, err := s.FetchLooseByNames(ctx, names, exclude)
	if err != nil {
		return err
	}
	if len(candidates) == 0 {
		return nil
	}

	byFileLoose := make(map[string][]cold.LooseEndRow, len(candidates)/4+1)
	for _, le := range candidates {
		byFileLoose[le.FilePath] = append(byFileLoose[le.FilePath], le)
	}

	type promo struct {
		link cold.LinkRow
		lose cold.LooseEndRow
	}
	promos := make([]promo, 0, len(candidates)/4+1)
	importCache := make(map[string][]cold.ImportRow)
	lc := NewLookupCache()

	for file, looseList := range byFileLoose {
		var dialect model.Language
		if len(looseList) > 0 {
			dialect = looseList[0].Dialect
		}
		resolver := s.ResolverFor(dialect)
		if resolver == nil {
			continue
		}
		imps, ok := importCache[file]
		if !ok {
			fetched, ferr := s.FetchImports(ctx, file)
			if ferr != nil {
				continue
			}
			importCache[file] = fetched
			imps = fetched
		}
		byLocal := ProbeIndex(imps)
		for _, le := range looseList {
			recv, simple := SplitCrossRecv(le.Name)
			probe := le.Name
			if recv != "" {
				probe = recv
			}
			targetName := le.Name
			if recv != "" {
				targetName = simple
			}
			lowerTarget := strings.ToLower(targetName)
			entries, ok := lowerToEntries[lowerTarget]
			if !ok || len(entries) == 0 {
				continue
			}
			if exact := ExactCase(entries, targetName); len(exact) > 0 {
				entries = exact
			}
			candImps, ok := byLocal[probe]
			if !ok {
				continue
			}
			for _, entry := range entries {
				isHeritage := le.Kind == model.EdgeExtends || le.Kind == model.EdgeImplements
				isTypeRef := isHeritage || le.Kind == model.EdgeParam || le.Kind == model.EdgeReturn
				if isTypeRef && !IsTypeKind(entry.Kind) {
					continue
				}
				matched := false
				for _, imp := range candImps {
					paths := lc.CandidatePaths(resolver, file, imp.Source)
					for _, pf := range paths {
						if pf == entry.FilePath {
							matched = true
							break
						}
					}
					if matched {
						break
					}
				}
				if !matched {
					continue
				}
				promos = append(promos, promo{
					link: cold.LinkRow{Origin: le.From, Target: entry.ID, Kind: le.Kind, SiteStart: le.SiteStart, SiteEnd: le.SiteEnd, SiteStartCol: le.SiteStartCol, SiteEndCol: le.SiteEndCol, Cond: le.Cond},
					lose: le,
				})
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
