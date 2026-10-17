package ts

import (
	"context"
	"sort"
	"strings"

	"srcinsight/internal/model"
	"srcinsight/internal/storage/cold"
	"srcinsight/services/resolve"
)

// Receiver-type resolution promotes recv.method loose ends once the type is known.
// Nearest heritage level wins; ambiguous matches stay loose.

const (
	recvWalkDepth = 8
)

// recvSolver carries the indexes one receiver-resolution run needs.
type recvSolver struct {
	store   resolve.Store
	pending resolve.Pending // file -> lower(name) -> rows
	byID    map[model.EntryID]cold.EntryRow
	lc      *resolve.LookupCache // shared vault-read memo for the run
	// Run-scoped memos; single-threaded use with no locks.
	heritageMemo map[model.EntryID][]cold.EntryRow
	returnMemo   map[model.EntryID]retMemo
	factoryMemo  map[string]model.EntryID
	usesMemo     map[model.EntryID][]cold.EntryRow
	walkMemo     map[string][]cold.EntryRow // roots+method -> matches

	bundleLinks map[model.EntryID][]cold.LinkRow // outgoing links by origin
	fileImports map[string][]cold.ImportRow
	fileBinds   map[string][]cold.RecvBindRow

	importCache map[string][]cold.ImportRow
	bindCache   map[string][]cold.RecvBindRow
	rowCache    map[model.EntryID]cold.EntryRow
	rowMiss     map[model.EntryID]bool
}

type retMemo struct {
	row cold.EntryRow
	ok  bool
}

func newRecvSolver(s resolve.Store, bundles []cold.OriginBundle, pending resolve.Pending, lc *resolve.LookupCache) *recvSolver {
	sv := &recvSolver{
		store:        s,
		pending:      pending,
		lc:           lc,
		heritageMemo: make(map[model.EntryID][]cold.EntryRow),
		returnMemo:   make(map[model.EntryID]retMemo),
		factoryMemo:  make(map[string]model.EntryID),
		usesMemo:     make(map[model.EntryID][]cold.EntryRow),
		walkMemo:     make(map[string][]cold.EntryRow),
		byID:         make(map[model.EntryID]cold.EntryRow),
		bundleLinks:  make(map[model.EntryID][]cold.LinkRow),
		fileImports:  make(map[string][]cold.ImportRow),
		fileBinds:    make(map[string][]cold.RecvBindRow),
		importCache:  make(map[string][]cold.ImportRow),
		bindCache:    make(map[string][]cold.RecvBindRow),
		rowCache:     make(map[model.EntryID]cold.EntryRow),
		rowMiss:      make(map[model.EntryID]bool),
	}
	for _, m := range pending {
		for _, rows := range m {
			for _, r := range rows {
				sv.byID[r.ID] = r
			}
		}
	}
	for _, b := range bundles {
		for _, l := range b.Links {
			sv.bundleLinks[l.Origin] = append(sv.bundleLinks[l.Origin], l)
		}
		if _, ok := sv.fileImports[b.Origin.Path]; !ok {
			sv.fileImports[b.Origin.Path] = b.Imports
		}
		if _, ok := sv.fileBinds[b.Origin.Path]; !ok {
			sv.fileBinds[b.Origin.Path] = b.RecvBinds
		}
	}
	return sv
}

func (s *recvSolver) importsOf(ctx context.Context, file string) ([]cold.ImportRow, error) {
	if imps, ok := s.fileImports[file]; ok {
		return imps, nil
	}
	if imps, ok := s.importCache[file]; ok {
		return imps, nil
	}
	imps, err := s.store.FetchImports(ctx, file)
	if err != nil {
		return nil, err
	}
	s.importCache[file] = imps
	return imps, nil
}

func (s *recvSolver) bindsOf(ctx context.Context, file string) ([]cold.RecvBindRow, error) {
	if binds, ok := s.fileBinds[file]; ok {
		return binds, nil
	}
	if binds, ok := s.bindCache[file]; ok {
		return binds, nil
	}
	binds, err := s.store.FetchRecvBindsByFile(ctx, file)
	if err != nil {
		return nil, err
	}
	s.bindCache[file] = binds
	return binds, nil
}

func (s *recvSolver) rowOf(ctx context.Context, id model.EntryID) (cold.EntryRow, bool, error) {
	if r, ok := s.byID[id]; ok {
		return r, true, nil
	}
	if r, ok := s.rowCache[id]; ok {
		return r, true, nil
	}
	if s.rowMiss[id] {
		return cold.EntryRow{}, false, nil
	}
	rows, err := s.store.FetchEntriesByIDs(ctx, []string{string(id)})
	if err != nil {
		return cold.EntryRow{}, false, err
	}
	if len(rows) == 0 {
		s.rowMiss[id] = true
		return cold.EntryRow{}, false, nil
	}
	s.rowCache[id] = rows[0]
	return rows[0], true, nil
}

// defining finds entries from one file following re-exports.
func (s *recvSolver) defining(ctx context.Context, file, name string, wantType bool, dialect model.Language) ([]cold.EntryRow, error) {
	return resolve.DefiningTargets(ctx, s.store, []string{file}, name, wantType, s.store.ResolverFor(dialect), s.fileImports, s.pending, s.lc)
}

// factoryID resolves a factory call to its single definition; ambiguous yields empty.
func (s *recvSolver) factoryID(ctx context.Context, file string, viaCall string, imps []cold.ImportRow, dialect model.Language) (model.EntryID, error) {
	key := file + "\x00" + viaCall
	if id, done := s.factoryMemo[key]; done {
		return id, nil
	}
	id, err := s.factoryIDUncached(ctx, file, viaCall, imps, dialect)
	if err == nil {
		s.factoryMemo[key] = id
	}
	return id, err
}

func (s *recvSolver) factoryIDUncached(ctx context.Context, file string, viaCall string, imps []cold.ImportRow, dialect model.Language) (model.EntryID, error) {
	// Chained factories resolve through their base factory.
	if i := strings.Index(viaCall, "("); i >= 0 {
		viaCall = viaCall[:i]
	}
	recvF, simpleF := resolve.SplitCrossRecv(viaCall)
	byLocal := func() map[string][]cold.ImportRow { return resolve.ProbeIndex(imps) }
	uniqueIn := func(rows []cold.EntryRow) (model.EntryID, bool) {
		if len(rows) == 1 {
			return rows[0].ID, true
		}
		return "", false
	}
	if recvF == "" {
		if rows, err := s.defining(ctx, file, simpleF, false, dialect); err != nil {
			return "", err
		} else if id, ok := uniqueIn(rows); ok {
			return id, nil
		}
		for _, imp := range byLocal()[simpleF] {
			rows, err := s.definingEach(ctx, file, imp.Source, simpleF, dialect)
			if err != nil {
				continue
			}
			if id, ok := uniqueIn(rows); ok {
				return id, nil
			}
		}
		return "", nil
	}
	for _, imp := range byLocal()[recvF] {
		rows, err := s.definingEach(ctx, file, imp.Source, simpleF, dialect)
		if err != nil {
			continue
		}
		if id, ok := uniqueIn(rows); ok {
			return id, nil
		}
	}
	return "", nil
}

// definingEach resolves one import source; callers demand uniqueness.
func (s *recvSolver) definingEach(ctx context.Context, file, source, name string, dialect model.Language) ([]cold.EntryRow, error) {
	resolver := s.store.ResolverFor(dialect)
	var out []cold.EntryRow
	for _, cand := range s.lc.CandidatePaths(resolver, file, source) {
		rows, err := s.defining(ctx, cand, name, false, dialect)
		if err != nil {
			continue
		}
		out = append(out, rows...)
		if len(out) > 1 {
			break
		}
	}
	return out, nil
}

func (s *recvSolver) returnTargetOf(ctx context.Context, factory model.EntryID) (cold.EntryRow, bool, error) {
	if m, done := s.returnMemo[factory]; done {
		return m.row, m.ok, nil
	}
	row, ok, err := s.returnTargetDepth(ctx, factory, 0)
	if err == nil {
		s.returnMemo[factory] = retMemo{row: row, ok: ok}
	}
	return row, ok, err
}

func (s *recvSolver) returnTargetDepth(ctx context.Context, factory model.EntryID, depth int) (cold.EntryRow, bool, error) {
	if depth > 2 {
		return cold.EntryRow{}, false, nil
	}
	for _, l := range s.bundleLinks[factory] {
		if l.Kind != model.EdgeReturn {
			continue
		}
		if r, ok, err := s.rowOf(ctx, l.Target); err != nil {
			return cold.EntryRow{}, false, err
		} else if ok {
			return r, true, nil
		}
	}
	nes, err := s.store.FetchNeighborEdges(ctx, factory, []string{model.EdgeReturn}, false)
	if err != nil {
		return cold.EntryRow{}, false, err
	}
	if len(nes) == 1 {
		r, ok, err := s.rowOf(ctx, nes[0].Ref.ID)
		if err != nil || !ok {
			return cold.EntryRow{}, false, err
		}
		return r, true, nil
	}
	if len(nes) != 0 {
		return cold.EntryRow{}, false, nil
	}
	// Const indirection reaches the factory via one callable Uses target.
	frow, ok, err := s.rowOf(ctx, factory)
	if err != nil || !ok || frow.Kind != model.KindConst {
		return cold.EntryRow{}, false, err
	}
	allUses, err := s.usesTargets(ctx, factory)
	if err != nil {
		return cold.EntryRow{}, false, err
	}
	var cands []cold.EntryRow
	for _, u := range allUses {
		if resolve.IsCallableKind(u.Kind) {
			cands = append(cands, u)
		}
	}
	if len(cands) != 1 {
		return cold.EntryRow{}, false, nil
	}
	return s.returnTargetDepth(ctx, cands[0].ID, depth+1)
}

// usesTargets lists one entry Uses neighbors.
func (s *recvSolver) usesTargets(ctx context.Context, id model.EntryID) ([]cold.EntryRow, error) {
	if out, done := s.usesMemo[id]; done {
		return out, nil
	}
	out, err := s.usesTargetsUncached(ctx, id)
	if err == nil {
		s.usesMemo[id] = out
	}
	return out, err
}

func (s *recvSolver) usesTargetsUncached(ctx context.Context, id model.EntryID) ([]cold.EntryRow, error) {
	var out []cold.EntryRow
	seen := make(map[model.EntryID]struct{})
	for _, l := range s.bundleLinks[id] {
		if l.Kind != model.EdgeUses {
			continue
		}
		if _, dup := seen[l.Target]; dup {
			continue
		}
		seen[l.Target] = struct{}{}
		if r, ok, err := s.rowOf(ctx, l.Target); err != nil {
			return nil, err
		} else if ok {
			out = append(out, r)
		}
	}
	nes, err := s.store.FetchNeighborEdges(ctx, id, []string{model.EdgeUses}, false)
	if err != nil {
		return nil, err
	}
	for _, ne := range nes {
		if _, dup := seen[ne.Ref.ID]; dup {
			continue
		}
		seen[ne.Ref.ID] = struct{}{}
		if r, ok, err := s.rowOf(ctx, ne.Ref.ID); err != nil {
			return nil, err
		} else if ok {
			out = append(out, r)
		}
	}
	return out, nil
}

// BindFor lists bindings governing recv: owner-scoped first, newest first.
func BindFor(binds []cold.RecvBindRow, qualifiedFrom, recv string) []cold.RecvBindRow {
	var owned, fileLevel []cold.RecvBindRow
	for _, b := range binds {
		if b.Recv != recv {
			continue
		}
		if b.Owner != "" && b.Owner == qualifiedFrom {
			owned = append(owned, b)
		} else if b.Owner == "" {
			fileLevel = append(fileLevel, b)
		}
	}
	reverse := func(rows []cold.RecvBindRow) []cold.RecvBindRow {
		for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
			rows[i], rows[j] = rows[j], rows[i]
		}
		return rows
	}
	return append(reverse(owned), reverse(fileLevel)...)
}

// factoryRoots resolves a factory binding to the return target row.
func (s *recvSolver) factoryRoots(ctx context.Context, file string, viaCall string, imps []cold.ImportRow, dialect model.Language) ([]cold.EntryRow, error) {
	fid, err := s.factoryID(ctx, file, viaCall, imps, dialect)
	if err != nil || fid == "" {
		return nil, err
	}
	ret, ok, err := s.returnTargetOf(ctx, fid)
	if err != nil || !ok || !resolve.IsTypeKind(ret.Kind) {
		return nil, err
	}
	return []cold.EntryRow{ret}, nil
}

// typeRoots finds type nodes visible from one file; spread roots yield nil.
func (s *recvSolver) typeRoots(ctx context.Context, file string, typeName string, imps []cold.ImportRow, dialect model.Language) ([]cold.EntryRow, error) {
	frontier := []string{file}
	byLocal := resolve.ProbeIndex(imps)
	resolver := s.store.ResolverFor(dialect)
	for _, imp := range byLocal[typeName] {
		frontier = append(frontier, s.lc.CandidatePaths(resolver, file, imp.Source)...)
	}
	var out []cold.EntryRow
	outFile := ""
	for _, f := range frontier {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		rows, err := resolve.DefiningTargets(ctx, s.store, []string{f}, typeName, true, resolver, s.fileImports, s.pending, s.lc)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			if outFile == "" {
				outFile = r.FilePath
			} else if r.FilePath != outFile {
				return nil, nil
			}
			out = append(out, r)
		}
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// heritageTargets lists extends and implements targets of one type node.
func (s *recvSolver) heritageTargets(ctx context.Context, id model.EntryID) ([]cold.EntryRow, error) {
	if out, done := s.heritageMemo[id]; done {
		return out, nil
	}
	out, err := s.heritageTargetsUncached(ctx, id)
	if err == nil {
		s.heritageMemo[id] = out
	}
	return out, err
}

func (s *recvSolver) heritageTargetsUncached(ctx context.Context, id model.EntryID) ([]cold.EntryRow, error) {
	var out []cold.EntryRow
	seen := make(map[model.EntryID]struct{})
	for _, l := range s.bundleLinks[id] {
		if l.Kind != model.EdgeExtends && l.Kind != model.EdgeImplements {
			continue
		}
		if _, dup := seen[l.Target]; dup {
			continue
		}
		seen[l.Target] = struct{}{}
		if r, ok, err := s.rowOf(ctx, l.Target); err != nil {
			return nil, err
		} else if ok {
			out = append(out, r)
		}
	}
	nes, err := s.store.FetchNeighborEdges(ctx, id, []string{model.EdgeExtends, model.EdgeImplements}, false)
	if err != nil {
		return nil, err
	}
	for _, ne := range nes {
		if _, dup := seen[ne.Ref.ID]; dup {
			continue
		}
		seen[ne.Ref.ID] = struct{}{}
		if r, ok, err := s.rowOf(ctx, ne.Ref.ID); err != nil {
			return nil, err
		} else if ok {
			out = append(out, r)
		}
	}
	return out, nil
}

// walkMethods finds callables on roots or heritage levels; nearest wins.
func (s *recvSolver) walkMethods(ctx context.Context, roots []cold.EntryRow, method string) ([]cold.EntryRow, error) {
	ids := make([]string, len(roots))
	for i, r := range roots {
		ids[i] = string(r.ID)
	}
	sort.Strings(ids)
	key := strings.Join(ids, "\x00") + "\x00" + method
	if out, done := s.walkMemo[key]; done {
		return out, nil
	}
	out, err := s.walkMethodsUncached(ctx, roots, method)
	if err == nil {
		s.walkMemo[key] = out
	}
	return out, err
}

func (s *recvSolver) walkMethodsUncached(ctx context.Context, roots []cold.EntryRow, method string) ([]cold.EntryRow, error) {
	visited := make(map[model.EntryID]struct{})
	level := roots
	for d := 0; d <= recvWalkDepth && len(level) > 0; d++ {
		var matches []cold.EntryRow
		matchFile := ""
		ambiguous := false
		var next []cold.EntryRow
		for _, t := range level {
			if _, seen := visited[t.ID]; seen {
				continue
			}
			visited[t.ID] = struct{}{}
			callables, err := s.defining(ctx, t.FilePath, method, false, t.Dialect)
			if err != nil {
				return nil, err
			}
			for _, c := range callables {
				if !resolve.IsCallableKind(c.Kind) {
					continue
				}
				if matchFile == "" {
					matchFile = c.FilePath
				} else if c.FilePath != matchFile {
					ambiguous = true
					break
				}
				matches = append(matches, c)
			}
			if ambiguous {
				break
			}
			parents, err := s.heritageTargets(ctx, t.ID)
			if err != nil {
				return nil, err
			}
			next = append(next, parents...)
		}
		if ambiguous {
			return nil, nil
		}
		if len(matches) > 0 {
			return matches, nil
		}
		level = next
	}
	return nil, nil
}

func (s *recvSolver) attemptOne(ctx context.Context, file string, imps []cold.ImportRow, binds []cold.RecvBindRow, from model.EntryID, qualifiedFrom, recv, simple string, dialect model.Language) ([]cold.EntryRow, error) {
	if recv == "super" {
		fromRow, ok, err := s.rowOf(ctx, from)
		if err != nil || !ok || fromRow.VesselID == "" {
			return nil, err
		}
		classRow, ok, err := s.rowOf(ctx, fromRow.VesselID)
		if err != nil || !ok {
			return nil, err
		}
		var roots []cold.EntryRow
		for _, l := range s.bundleLinks[classRow.ID] {
			if l.Kind != model.EdgeExtends {
				continue
			}
			if r, ok, err := s.rowOf(ctx, l.Target); err != nil {
				return nil, err
			} else if ok {
				roots = append(roots, r)
			}
		}
		nes, err := s.store.FetchNeighborEdges(ctx, classRow.ID, []string{model.EdgeExtends}, false)
		if err != nil {
			return nil, err
		}
		for _, ne := range nes {
			dup := false
			for _, r := range roots {
				if r.ID == ne.Ref.ID {
					dup = true
					break
				}
			}
			if dup {
				continue
			}
			if r, ok, err := s.rowOf(ctx, ne.Ref.ID); err != nil {
				return nil, err
			} else if ok {
				roots = append(roots, r)
			}
		}
		if len(roots) == 0 {
			return nil, nil
		}
		return s.walkMethods(ctx, roots, simple)
	}
	cands := BindFor(binds, qualifiedFrom, recv)
	for _, b := range cands {
		var roots []cold.EntryRow
		var rerr error
		if b.Type != "" {
			roots, rerr = s.typeRoots(ctx, file, b.Type, imps, dialect)
		} else if b.ViaCall != "" {
			roots, rerr = s.factoryRoots(ctx, file, b.ViaCall, imps, dialect)
		} else {
			continue
		}
		if rerr != nil {
			return nil, rerr
		}
		if len(roots) == 0 {
			continue
		}
		targets, err := s.walkMethods(ctx, roots, simple)
		if err != nil {
			return nil, err
		}
		if len(targets) > 0 {
			return targets, nil
		}
	}
	return nil, nil
}

// HasRecvCalls reports receiver-qualified calls the pass can promote.
func HasRecvCalls(loose []cold.LooseEndRow) bool {
	for _, le := range loose {
		if le.Kind != model.EdgeCalls {
			continue
		}
		if recv, _ := resolve.SplitCrossRecv(le.Name); recv != "" && recv != "this" && recv != "self" {
			return true
		}
	}
	return false
}

// ResolveRecvBatch runs the forward receiver pass inside the batch.
func ResolveRecvBatch(ctx context.Context, s resolve.Store, bundles []cold.OriginBundle, pending resolve.Pending, lc *resolve.LookupCache) error {
	if lc == nil {
		lc = resolve.NewLookupCache()
	}
	sv := newRecvSolver(s, bundles, pending, lc)
	for i := range bundles {
		if err := resolveRecvOne(ctx, sv, &bundles[i]); err != nil {
			return err
		}
	}
	return nil
}

// ResolveRecvShard resolves only idx bundles with a solver over all bundles.
// The solver needs whole-batch imports, binds, and links; memos stay worker-local.
func ResolveRecvShard(ctx context.Context, s resolve.Store, bundles []cold.OriginBundle, idx []int, pending resolve.Pending, lc *resolve.LookupCache) error {
	if lc == nil {
		lc = resolve.NewLookupCache()
	}
	sv := newRecvSolver(s, bundles, pending, lc)
	for _, i := range idx {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := resolveRecvOne(ctx, sv, &bundles[i]); err != nil {
			return err
		}
	}
	return nil
}

func resolveRecvOne(ctx context.Context, sv *recvSolver, b *cold.OriginBundle) error {
	if b.Origin.Dialect != model.LangTypeScript {
		return nil
	}
	if !HasRecvCalls(b.LooseEnds) {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	imps := b.Imports
	binds := b.RecvBinds
	seenLink := make(map[cold.LinkRow]struct{}, len(b.Links))
	for _, l := range b.Links {
		seenLink[l] = struct{}{}
	}
	quals := make(map[model.EntryID]string, len(b.LooseEnds))
	for _, le := range b.LooseEnds {
		if r, ok := sv.byID[le.From]; ok {
			quals[le.From] = r.Qualified
		}
	}
	var keepLoose []cold.LooseEndRow
	for _, le := range b.LooseEnds {
		if le.Kind != model.EdgeCalls {
			keepLoose = append(keepLoose, le)
			continue
		}
		recv, simple := resolve.SplitCrossRecv(le.Name)
		if recv == "" || recv == "this" || recv == "self" {
			keepLoose = append(keepLoose, le)
			continue
		}
		targets, err := sv.attemptOne(ctx, b.Origin.Path, imps, binds, le.From, quals[le.From], recv, simple, b.Origin.Dialect)
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
	return nil
}

// PromoteRecvReverse revisits loose ends bound to freshly indexed types.
func PromoteRecvReverse(ctx context.Context, s resolve.Store, bundles []cold.OriginBundle) error {
	if len(bundles) == 0 {
		return nil
	}
	exclude := make(map[string]struct{}, len(bundles))
	var typeNames []string
	seenType := make(map[string]struct{})
	for _, b := range bundles {
		exclude[b.Origin.Path] = struct{}{}
		if b.Origin.Dialect != model.LangTypeScript {
			continue
		}
		for _, e := range b.Entries {
			if !resolve.IsTypeKind(e.Kind) {
				continue
			}
			if _, ok := seenType[e.Name]; ok {
				continue
			}
			seenType[e.Name] = struct{}{}
			typeNames = append(typeNames, e.Name)
		}
	}
	if len(typeNames) == 0 {
		return nil
	}
	byFile := make(map[string][]cold.RecvBindRow)
	for _, name := range typeNames {
		if err := ctx.Err(); err != nil {
			return err
		}
		rows, err := s.FetchRecvBindsByType(ctx, name)
		if err != nil {
			return err
		}
		for _, r := range rows {
			if r.Type == "" {
				continue
			}
			if _, skip := exclude[r.FilePath]; skip {
				continue
			}
			byFile[r.FilePath] = append(byFile[r.FilePath], r)
		}
	}
	if len(byFile) == 0 {
		return nil
	}
	sv := newRecvSolver(s, nil, nil, resolve.NewLookupCache())
	type promo struct {
		link cold.LinkRow
		lose cold.LooseEndRow
	}
	var promos []promo
	for file, binds := range byFile {
		if err := ctx.Err(); err != nil {
			return err
		}
		loose, err := s.FetchLooseEnds(ctx, file)
		if err != nil {
			return err
		}
		if len(loose) == 0 {
			continue
		}
		var dialect model.Language
		for _, le := range loose {
			if le.Dialect != "" {
				dialect = le.Dialect
				break
			}
		}
		if dialect != model.LangTypeScript {
			continue
		}
		imps, err := sv.importsOf(ctx, file)
		if err != nil {
			return err
		}
		ids := make([]string, 0, len(loose))
		for _, le := range loose {
			ids = append(ids, string(le.From))
		}
		qualRows, err := s.FetchEntriesByIDs(ctx, ids)
		if err != nil {
			return err
		}
		quals := make(map[model.EntryID]string, len(qualRows))
		for _, r := range qualRows {
			quals[r.ID] = r.Qualified
		}
		for _, le := range loose {
			if le.Kind != model.EdgeCalls {
				continue
			}
			recv, simple := resolve.SplitCrossRecv(le.Name)
			if recv == "" || recv == "this" || recv == "self" {
				continue
			}
			targets, err := sv.attemptOne(ctx, file, imps, binds, le.From, quals[le.From], recv, simple, dialect)
			if err != nil {
				return err
			}
			for _, t := range targets {
				lr := cold.LinkRow{Origin: le.From, Target: t.ID, Kind: le.Kind, SiteStart: le.SiteStart, SiteEnd: le.SiteEnd, SiteStartCol: le.SiteStartCol, SiteEndCol: le.SiteEndCol, Cond: le.Cond}
				if lr.Valid() {
					promos = append(promos, promo{link: lr, lose: le})
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
