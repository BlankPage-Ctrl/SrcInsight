package services

import (
	"context"
	"strings"

	"srcinsight/internal/model"
	"srcinsight/internal/storage/cold"
	"srcinsight/services/adapters"
	"srcinsight/services/methods"
	"srcinsight/services/resolve"
)

// Pipeline satisfies the refs live scanner contract.
var _ methods.LiveScanner = (*Pipeline)(nil)

// VerifyRefs confirms that unresolved references naming the target actually
// reach it, using only stored imports plus the dialect resolver. It never
// parses a file: candidate files are narrowed by import resolution, so the
// pass is RAM-only and discarded once the answer is built.
func (p *Pipeline) VerifyRefs(ctx context.Context, target cold.EntryRow, loose []cold.LooseEndRow) ([]methods.LiveRef, error) {
	if len(loose) == 0 {
		return nil, nil
	}
	resolver := p.langs.ResolverFor(target.Dialect)
	if resolver == nil {
		return nil, nil
	}
	lc := resolve.NewLookupCache()
	byLocalByFile := make(map[string]map[string][]cold.ImportRow)
	var out []methods.LiveRef
	for _, le := range loose {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if le.Kind != model.EdgeCalls {
			continue
		}
		recv, simple := resolve.SplitCrossRecv(le.Name)
		if !strings.EqualFold(simple, target.Name) {
			continue
		}
		byLocal, ok := byLocalByFile[le.FilePath]
		if !ok {
			imps, err := p.vault.FetchImportsByFile(ctx, p.vault.DB(), le.FilePath)
			if err != nil {
				return nil, err
			}
			byLocal = resolve.ProbeIndex(imps)
			byLocalByFile[le.FilePath] = byLocal
		}
		if !refImportsTarget(target, le, recv, byLocal, resolver, lc) {
			continue
		}
		out = append(out, methods.LiveRef{
			Caller:    le.From,
			FilePath:  le.FilePath,
			Kind:      le.Kind,
			SiteRange: model.Range{StartLine: le.SiteStart, EndLine: le.SiteEnd, StartCol: le.SiteStartCol, EndCol: le.SiteEndCol},
			Cond:      le.Cond,
		})
	}
	return out, nil
}

// refImportsTarget reports whether the loose end's file imports the target
// file. Go plain names are same-package and need a matching directory instead
// of an import.
func refImportsTarget(target cold.EntryRow, le cold.LooseEndRow, recv string, byLocal map[string][]cold.ImportRow, resolver resolve.ImportResolver, lc *resolve.LookupCache) bool {
	if l := adapters.Get(target.Dialect); l != nil && l.RefWithoutImport != nil {
		if handled, ok := l.RefWithoutImport(target.FilePath, le, recv); handled {
			return ok
		}
	}
	probe := le.Name
	if recv != "" {
		probe = recv
	}
	imps := byLocal[probe]
	if star, ok := byLocal["*"]; ok {
		imps = append(append([]cold.ImportRow(nil), imps...), star...)
	}
	for _, imp := range imps {
		for _, f := range lc.CandidatePaths(resolver, le.FilePath, imp.Source) {
			if f == target.FilePath {
				return true
			}
		}
	}
	return false
}
