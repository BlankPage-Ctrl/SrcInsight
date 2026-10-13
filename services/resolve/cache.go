package resolve

import (
	"context"
	"strings"

	"srcinsight/internal/model"
	"srcinsight/internal/storage/cold"
)

// maxFollowDepth bounds re-export following; chains must terminate.
const maxFollowDepth = 4

// LookupCache memoizes vault reads within one resolve run.
// Pending rows are not cached; the caller checks pending first.
type LookupCache struct {
	pairs   map[string][]cold.EntryRow
	imports map[string][]cold.ImportRow
	// paths memoizes CandidatePaths per file and source.
	paths map[[2]string][]string
	// known gates candidate paths; nil disables gating.
	known map[string]struct{}
}

// NewLookupCache builds an empty run-scoped cache.
func NewLookupCache() *LookupCache {
	return &LookupCache{
		pairs:   make(map[string][]cold.EntryRow),
		imports: make(map[string][]cold.ImportRow),
		paths:   make(map[[2]string][]string),
	}
}

// SetKnown installs the known-files gate; nil disables it.
func (lc *LookupCache) SetKnown(known map[string]struct{}) {
	if lc != nil {
		lc.known = known
	}
}

// CandidatePaths returns memoized candidate paths; nil-safe.
func (lc *LookupCache) CandidatePaths(r ImportResolver, file, source string) []string {
	if lc == nil {
		return r.CandidatePaths(file, source)
	}
	key := [2]string{file, source}
	if out, ok := lc.paths[key]; ok {
		return out
	}
	out := r.CandidatePaths(file, source)
	lc.paths[key] = out
	return out
}

func PairKey(file, name string) string {
	return file + "\x00" + strings.ToLower(name)
}

// KnownFiles returns files that can hold entries this run.
func KnownFiles(ctx context.Context, s Store, bundles []cold.OriginBundle) (map[string]struct{}, error) {
	tracked, err := s.FetchTracked(ctx)
	if err != nil {
		return nil, err
	}
	known := make(map[string]struct{}, len(tracked)+len(bundles))
	for _, t := range tracked {
		known[t.Path] = struct{}{}
	}
	for _, b := range bundles {
		known[b.Origin.Path] = struct{}{}
	}
	return known, nil
}

// PrimeLookup pre-warms the lookup cache for a batch via bulk fetches.
func PrimeLookup(ctx context.Context, s Store, bundles []cold.OriginBundle, bundleImports map[string][]cold.ImportRow, pending Pending, lc *LookupCache) error {
	// Files covered by pending or outside the known set skip the query.
	wanted := make(map[string]map[string]struct{})
	addWant := func(file, name string) {
		if file == "" || name == "" {
			return
		}
		if lc != nil && lc.known != nil {
			if _, ok := lc.known[file]; !ok {
				return
			}
		}
		m, ok := wanted[file]
		if !ok {
			m = make(map[string]struct{})
			wanted[file] = m
		}
		m[name] = struct{}{}
	}
	dialects := make(map[string]model.Language, len(bundles))
	for _, b := range bundles {
		dialects[b.Origin.Path] = b.Origin.Dialect
		if len(b.LooseEnds) == 0 {
			continue
		}
		resolver := s.ResolverFor(b.Origin.Dialect)
		if resolver == nil {
			continue
		}
		importByLocal := ProbeIndex(b.Imports)
		for _, le := range b.LooseEnds {
			if le.Scope != model.ScopeInternal && !strings.HasPrefix(le.Scope, model.ScopeInternal+":") {
				continue
			}
			recv, simple := SplitCrossRecv(le.Name)
			probe := le.Name
			if recv != "" {
				probe = recv
			}
			target := le.Name
			if recv != "" {
				target = simple
			}
			for _, imp := range importByLocal[probe] {
				for _, f := range lc.CandidatePaths(resolver, b.Origin.Path, imp.Source) {
					addWant(f, target)
				}
			}
		}
	}
	resolverFor := func(file string) ImportResolver {
		d := dialects[file]
		if d == "" && strings.HasSuffix(file, ".py") {
			d = model.LangPython
		}
		return s.ResolverFor(d)
	}
	followed := make(map[string]struct{})
	for depth := 0; depth <= maxFollowDepth; depth++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		// Pending rows first, then one chunked query serves the rest.
		var miss []FileName
		for file, names := range wanted {
			for n := range names {
				if hasPair(lc, file, n) {
					continue
				}
				if pm, ok := pending[file]; ok {
					if rows, ok := pm[strings.ToLower(n)]; ok && len(rows) > 0 {
						lc.pairs[PairKey(file, n)] = rows
						continue
					}
				}
				miss = append(miss, FileName{File: file, Name: n})
			}
		}
		if len(miss) > 0 {
			batch, err := s.FetchTargetsBatch(ctx, miss)
			if err != nil {
				return err
			}
			for _, m := range miss {
				lc.pairs[PairKey(m.File, m.Name)] = batch[PairKey(m.File, m.Name)]
			}
		}
		grew := false
		for file, names := range wanted {
			if _, done := followed[file]; done {
				continue
			}
			followed[file] = struct{}{}
			imps, err := FileImports(ctx, s, bundleImports, lc, file)
			if err != nil {
				return err
			}
			resolver := resolverFor(file)
			if resolver == nil {
				continue
			}
			for n := range names {
				for _, imp := range imps {
					if imp.Kind != "re-export" {
						continue
					}
					if imp.LocalName != n && imp.LocalName != "*" {
						continue
					}
					for _, f := range lc.CandidatePaths(resolver, file, imp.Source) {
						m, ok := wanted[f]
						if !ok {
							m = make(map[string]struct{})
							wanted[f] = m
						}
						if _, dup := m[n]; !dup {
							m[n] = struct{}{}
							grew = true
						}
					}
				}
			}
		}
		if !grew {
			break
		}
	}
	return nil
}

func hasPair(lc *LookupCache, file, name string) bool {
	if lc == nil {
		return false
	}
	_, ok := lc.pairs[PairKey(file, name)]
	return ok
}

// FileImports returns a file imports from bundle, cache, then vault.
func FileImports(ctx context.Context, s Store, bundleImports map[string][]cold.ImportRow, lc *LookupCache, file string) ([]cold.ImportRow, error) {
	if imps, ok := bundleImports[file]; ok {
		return imps, nil
	}
	if lc != nil {
		if imps, ok := lc.imports[file]; ok {
			return imps, nil
		}
	}
	imps, err := s.FetchImports(ctx, file)
	if err != nil {
		return nil, err
	}
	if lc != nil {
		lc.imports[file] = imps
	}
	return imps, nil
}

// ProbeIndex keys imports by local name; export-alias and gopackage rows skip.
func ProbeIndex(imps []cold.ImportRow) map[string][]cold.ImportRow {
	m := make(map[string][]cold.ImportRow, len(imps))
	for _, imp := range imps {
		if imp.Kind == "export-alias" || imp.Kind == model.ImportKindGoPackage {
			continue
		}
		m[imp.LocalName] = append(m[imp.LocalName], imp)
		if imp.Alias != "" && imp.Alias != imp.LocalName {
			m[imp.Alias] = append(m[imp.Alias], imp)
		}
	}
	return m
}

// FileRows finds entries in one file via cache, pending, then vault.
func FileRows(ctx context.Context, s Store, pending Pending, lc *LookupCache, file, name string) ([]cold.EntryRow, error) {
	key := PairKey(file, name)
	if lc != nil {
		if rows, ok := lc.pairs[key]; ok {
			return rows, nil
		}
	}
	if pm, ok := pending[file]; ok {
		if rows, ok := pm[strings.ToLower(name)]; ok && len(rows) > 0 {
			if lc != nil {
				lc.pairs[key] = rows
			}
			return rows, nil
		}
	}
	rows, err := s.FetchTargets(ctx, file, name)
	if err != nil {
		return nil, err
	}
	if lc != nil {
		lc.pairs[key] = rows
	}
	return rows, nil
}

// AliasLocal maps an exported alias back to its local spelling.
func AliasLocal(imps []cold.ImportRow, name string) string {
	for _, imp := range imps {
		if imp.Kind == "export-alias" && imp.LocalName == name && imp.Alias != "" {
			return imp.Alias
		}
	}
	return ""
}

// ExactCase keeps exact-case rows; the vault lookup folds case.
func ExactCase(rows []cold.EntryRow, name string) []cold.EntryRow {
	var out []cold.EntryRow
	for _, r := range rows {
		if r.Name == name {
			out = append(out, r)
		}
	}
	return out
}

// DefiningTargets finds entries from frontier files following re-exports.
// First file with matches wins; wantType restricts to type nodes.
func DefiningTargets(ctx context.Context, s Store, frontier []string, name string, wantType bool, resolver ImportResolver, bundleImports map[string][]cold.ImportRow, pending Pending, lc *LookupCache) ([]cold.EntryRow, error) {
	if lc == nil {
		lc = NewLookupCache()
	}
	visited := make(map[string]struct{}, len(frontier)*2)
	queue := append([]string(nil), frontier...)
	for depth := 0; depth <= maxFollowDepth && len(queue) > 0; depth++ {
		// One chunked query serves every missing frontier file.
		var miss []FileName
		for _, file := range queue {
			if _, seen := visited[file]; seen {
				continue
			}
			if len(lc.known) > 0 {
				if _, ok := lc.known[file]; !ok {
					visited[file] = struct{}{}
					continue
				}
			}
			key := PairKey(file, name)
			if pm, ok := pending[file]; ok {
				if rows, ok := pm[strings.ToLower(name)]; ok && len(rows) > 0 {
					lc.pairs[key] = rows
					continue
				}
			}
			if _, ok := lc.pairs[key]; ok {
				continue
			}
			miss = append(miss, FileName{File: file, Name: name})
		}
		if len(miss) > 0 {
			batch, err := s.FetchTargetsBatch(ctx, miss)
			if err != nil {
				return nil, err
			}
			for _, m := range miss {
				lc.pairs[PairKey(m.File, m.Name)] = batch[PairKey(m.File, m.Name)]
			}
		}
		var next []string
		for _, file := range queue {
			if _, seen := visited[file]; seen {
				continue
			}
			visited[file] = struct{}{}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			rows := lc.pairs[PairKey(file, name)]
			// Exact-case hits displace case-folded collisions.
			if exact := ExactCase(rows, name); len(exact) > 0 {
				rows = exact
			}
			if wantType {
				var typed []cold.EntryRow
				for _, r := range rows {
					if IsTypeKind(r.Kind) {
						typed = append(typed, r)
					}
				}
				rows = typed
			}
			if len(rows) > 0 {
				return rows, nil
			}
			imps, ok := bundleImports[file]
			if !ok {
				var ok2 bool
				if imps, ok2 = lc.imports[file]; !ok2 {
					fetched, err := s.FetchImports(ctx, file)
					if err != nil {
						continue
					}
					imps = fetched
					lc.imports[file] = fetched
				}
			}
			// Export aliases resolve without a file hop.
			if aliased := AliasLocal(imps, name); aliased != "" {
				rows, err := FileRows(ctx, s, pending, lc, file, aliased)
				if err != nil {
					return nil, err
				}
				if wantType {
					var typed []cold.EntryRow
					for _, r := range rows {
						if IsTypeKind(r.Kind) {
							typed = append(typed, r)
						}
					}
					rows = typed
				}
				if len(rows) > 0 {
					return rows, nil
				}
			}
			for _, imp := range imps {
				if imp.Kind != "re-export" {
					continue
				}
				if imp.LocalName != name && imp.LocalName != "*" {
					continue
				}
				next = append(next, lc.CandidatePaths(resolver, file, imp.Source)...)
			}
		}
		queue = next
	}
	return nil, nil
}
