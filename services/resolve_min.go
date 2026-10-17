package services

import (
	"strings"

	"srcinsight/internal/convention"
	"srcinsight/internal/model"
	"srcinsight/internal/storage/cold"
	tspkg "srcinsight/services/resolve/ts"
)

// ResolveFile splits one file's refs into same-file links vs loose ends.
// It reads no vault state, so writes stay proportional to changed files.
func ResolveFile(
	path string,
	fns []*model.Function,
	imports []model.ImportStmt,
	dialect model.Language,
) (links []cold.LinkRow, loose []cold.LooseEndRow) {
	return ResolveFileWithAlias(path, fns, imports, dialect, nil)
}

// ResolveFileWithAlias labels alias-internal sources as internal via isInternal.
func ResolveFileWithAlias(
	path string,
	fns []*model.Function,
	imports []model.ImportStmt,
	dialect model.Language,
	isInternal func(source string) bool,
) (links []cold.LinkRow, loose []cold.LooseEndRow) {
	byName := make(map[string][]*model.Function, len(fns))
	for _, f := range fns {
		if f == nil || f.ID == "" {
			continue
		}
		byName[f.Name] = append(byName[f.Name], f)
	}
	// Index imports once so scope classification stays O(1) per ref.
	byLocal := indexImports(imports)

	// ctorBinds maps constructor param types so this-chains skip self matches.
	ctorBinds := tspkg.BuildCtorBinds(fns)

	seenLink := make(map[cold.LinkRow]struct{}, len(fns))
	links = make([]cold.LinkRow, 0, len(fns))
	loose = make([]cold.LooseEndRow, 0, len(fns))
	addLink := func(origin model.EntryID, target model.EntryID, kind string, site model.Range, cond string) {
		r := cold.LinkRow{Origin: origin, Target: target, Kind: kind, SiteStart: site.StartLine, SiteEnd: site.EndLine, SiteStartCol: site.StartCol, SiteEndCol: site.EndCol, Cond: cond}
		if !r.Valid() {
			return
		}
		if _, ok := seenLink[r]; ok {
			return
		}
		seenLink[r] = struct{}{}
		links = append(links, r)
	}
	addLoose := func(from model.EntryID, name, kind, scope string, site model.Range, cond string) {
		if from == "" || name == "" || kind == "" {
			return
		}
		loose = append(loose, cold.LooseEndRow{
			From:         from,
			Name:         name,
			Kind:         kind,
			Scope:        scope,
			FilePath:     path,
			Dialect:      dialect,
			SiteStart:    site.StartLine,
			SiteEnd:      site.EndLine,
			SiteStartCol: site.StartCol,
			SiteEndCol:   site.EndCol,
			Cond:         cond,
		})
	}

	for _, f := range fns {
		if f == nil || f.ID == "" {
			continue
		}
		for _, c := range f.Calls {
			recv, simple := splitRecv(c.Name)
			switch {
			case recv == "":
				targets := byName[simple]
				if len(targets) == 0 {
					addLoose(f.ID, c.Name, model.EdgeCalls, classifyScopeIndexed(c.Name, recv, imports, byLocal, dialect, isInternal), c.SiteRange, c.Cond)
					continue
				}
				for _, t := range targets {
					addLink(f.ID, t.ID, model.EdgeCalls, c.SiteRange, c.Cond)
				}
			case recv == "self" || recv == "this":
				// Chained this-calls never fall back to the caller's container.
				if dialect == model.LangTypeScript {
					if field, method, ok := tspkg.SplitThisChain(c.Name); ok {
						targets := tspkg.DIChainTargets(f, byName, ctorBinds, field, method)
						if len(targets) == 0 {
							addLoose(f.ID, c.Name, model.EdgeCalls, model.ScopeUnresolved, c.SiteRange, c.Cond)
							continue
						}
						for _, t := range targets {
							addLink(f.ID, t.ID, model.EdgeCalls, c.SiteRange, c.Cond)
						}
						continue
					}
				}
				targets := sameContainer(f, byName[simple])
				if len(targets) == 0 {
					targets = byName[simple]
				}
				if len(targets) == 0 {
					addLoose(f.ID, c.Name, model.EdgeCalls, model.ScopeUnresolved, c.SiteRange, c.Cond)
					continue
				}
				for _, t := range targets {
					addLink(f.ID, t.ID, model.EdgeCalls, c.SiteRange, c.Cond)
				}
			default:
				addLoose(f.ID, c.Name, model.EdgeCalls, classifyScopeIndexed(c.Name, recv, imports, byLocal, dialect, isInternal), c.SiteRange, c.Cond)
			}
		}
		for _, h := range f.Extends {
			resolveHeritageIndexed(f.ID, h.Name, model.EdgeExtends, path, dialect, byName, imports, byLocal, isInternal, addLink, addLoose, siteOf(h))
		}
		for _, h := range f.Implements {
			resolveHeritageIndexed(f.ID, h.Name, model.EdgeImplements, path, dialect, byName, imports, byLocal, isInternal, addLink, addLoose, siteOf(h))
		}
		for _, p := range f.ParamTypes {
			resolveTypeRefIndexed(f.ID, p.Name, model.EdgeParam, path, dialect, byName, imports, byLocal, isInternal, addLink, addLoose, siteOf(p))
		}
		for _, r := range f.ReturnTypes {
			resolveTypeRefIndexed(f.ID, r.Name, model.EdgeReturn, path, dialect, byName, imports, byLocal, isInternal, addLink, addLoose, siteOf(r))
		}
		for _, u := range f.Uses {
			resolveUsesIndexed(f.ID, u.Name, model.EdgeUses, path, dialect, byName, imports, byLocal, isInternal, addLink, addLoose, siteOf(u))
		}
	}
	return links, loose
}

// siteOf returns the call-site range; zero means unknown.
func siteOf(ref model.FunctionRef) model.Range {
	if ref.SiteRange.StartLine != 0 || ref.SiteRange.EndLine != 0 {
		return ref.SiteRange
	}
	return ref.LineRange
}

// splitRecv splits "recv.simple"; plain names return empty recv.
func splitRecv(name string) (recv, simple string) {
	if i := strings.Index(name, "."); i >= 0 {
		recv = name[:i]
		simple = name[i+1:]
		if j := strings.LastIndex(simple, "."); j >= 0 {
			simple = simple[j+1:]
		}
		return recv, simple
	}
	return "", name
}

// sameContainer prefers targets sharing the caller's container name.
func sameContainer(caller *model.Function, cands []*model.Function) []*model.Function {
	if caller.Container == nil || caller.Container.Name == "" {
		return nil
	}
	var out []*model.Function
	for _, t := range cands {
		if t.Container != nil && t.Container.Name == caller.Container.Name {
			out = append(out, t)
		}
	}
	return out
}

// resolveHeritage links heritage refs to same-file type nodes or loose ends.
func resolveHeritage(
	from model.EntryID,
	name, kind, path string,
	dialect model.Language,
	byName map[string][]*model.Function,
	imports []model.ImportStmt,
	addLink func(model.EntryID, model.EntryID, string, model.Range, string),
	addLoose func(model.EntryID, string, string, string, model.Range, string),
) {
	resolveHeritageWithAlias(from, name, kind, path, dialect, byName, imports, nil, addLink, addLoose, model.Range{})
}

// resolveHeritageWithAlias threads alias awareness into scope classification.
func resolveHeritageWithAlias(
	from model.EntryID,
	name, kind, path string,
	dialect model.Language,
	byName map[string][]*model.Function,
	imports []model.ImportStmt,
	isInternal func(source string) bool,
	addLink func(model.EntryID, model.EntryID, string, model.Range, string),
	addLoose func(model.EntryID, string, string, string, model.Range, string),
	site model.Range,
) {
	resolveHeritageIndexed(from, name, kind, path, dialect, byName, imports, nil, isInternal, addLink, addLoose, site)
}

// resolveHeritageIndexed uses a prebuilt import index when non-nil.
func resolveHeritageIndexed(
	from model.EntryID,
	name, kind, path string,
	dialect model.Language,
	byName map[string][]*model.Function,
	imports []model.ImportStmt,
	byLocal map[string]indexedImp,
	isInternal func(source string) bool,
	addLink func(model.EntryID, model.EntryID, string, model.Range, string),
	addLoose func(model.EntryID, string, string, string, model.Range, string),
	site model.Range,
) {
	_, simple := splitRecv(name)
	var targets []*model.Function
	for _, t := range byName[simple] {
		switch t.Kind {
		case model.KindClassDef, model.KindInterface, model.KindTypeAlias, model.KindEnum:
			targets = append(targets, t)
		}
	}
	if len(targets) == 0 {
		recv, _ := splitRecv(name)
		addLoose(from, name, kind, classifyScopeIndexed(name, recv, imports, byLocal, dialect, isInternal), site, "")
		return
	}
	for _, t := range targets {
		addLink(from, t.ID, kind, site, "")
	}
}

// resolveTypeRef links type refs to same-file type nodes or loose ends.
func resolveTypeRef(
	from model.EntryID,
	name, kind, path string,
	dialect model.Language,
	byName map[string][]*model.Function,
	imports []model.ImportStmt,
	addLink func(model.EntryID, model.EntryID, string, model.Range, string),
	addLoose func(model.EntryID, string, string, string, model.Range, string),
) {
	resolveTypeRefWithAlias(from, name, kind, path, dialect, byName, imports, nil, addLink, addLoose, model.Range{})
}

func resolveTypeRefWithAlias(
	from model.EntryID,
	name, kind, path string,
	dialect model.Language,
	byName map[string][]*model.Function,
	imports []model.ImportStmt,
	isInternal func(source string) bool,
	addLink func(model.EntryID, model.EntryID, string, model.Range, string),
	addLoose func(model.EntryID, string, string, string, model.Range, string),
	site model.Range,
) {
	resolveTypeRefIndexed(from, name, kind, path, dialect, byName, imports, nil, isInternal, addLink, addLoose, site)
}

// resolveTypeRefIndexed uses a prebuilt import index when non-nil.
func resolveTypeRefIndexed(
	from model.EntryID,
	name, kind, path string,
	dialect model.Language,
	byName map[string][]*model.Function,
	imports []model.ImportStmt,
	byLocal map[string]indexedImp,
	isInternal func(source string) bool,
	addLink func(model.EntryID, model.EntryID, string, model.Range, string),
	addLoose func(model.EntryID, string, string, string, model.Range, string),
	site model.Range,
) {
	_, simple := splitRecv(name)
	var targets []*model.Function
	for _, t := range byName[simple] {
		switch t.Kind {
		case model.KindClassDef, model.KindInterface, model.KindTypeAlias, model.KindEnum:
			targets = append(targets, t)
		}
	}
	if len(targets) == 0 {
		recv, _ := splitRecv(name)
		addLoose(from, name, kind, classifyScopeIndexed(name, recv, imports, byLocal, dialect, isInternal), site, "")
		return
	}
	for _, t := range targets {
		addLink(from, t.ID, kind, site, "")
	}
}

// resolveUsesWithAlias links const deps to any same-file node or loose ends.
func resolveUsesWithAlias(
	from model.EntryID,
	name, kind, path string,
	dialect model.Language,
	byName map[string][]*model.Function,
	imports []model.ImportStmt,
	isInternal func(source string) bool,
	addLink func(model.EntryID, model.EntryID, string, model.Range, string),
	addLoose func(model.EntryID, string, string, string, model.Range, string),
	site model.Range,
) {
	resolveUsesIndexed(from, name, kind, path, dialect, byName, imports, nil, isInternal, addLink, addLoose, site)
}

// resolveUsesIndexed uses a prebuilt import index when non-nil.
func resolveUsesIndexed(
	from model.EntryID,
	name, kind, path string,
	dialect model.Language,
	byName map[string][]*model.Function,
	imports []model.ImportStmt,
	byLocal map[string]indexedImp,
	isInternal func(source string) bool,
	addLink func(model.EntryID, model.EntryID, string, model.Range, string),
	addLoose func(model.EntryID, string, string, string, model.Range, string),
	site model.Range,
) {
	recv, simple := splitRecv(name)
	targets := byName[simple]
	// Prefer receiver-container targets, else keep every same-file match.
	if recv != "" && recv != "self" && recv != "this" && len(targets) > 1 {
		var scoped []*model.Function
		for _, t := range targets {
			if t.Container != nil && t.Container.Name == recv {
				scoped = append(scoped, t)
			}
		}
		if len(scoped) > 0 {
			targets = scoped
		}
	}
	if len(targets) == 0 {
		recv, _ := splitRecv(name)
		addLoose(from, name, kind, classifyScopeIndexed(name, recv, imports, byLocal, dialect, isInternal), site, "")
		return
	}
	for _, t := range targets {
		addLink(from, t.ID, kind, site, "")
	}
}

// classifyScope labels unresolved refs without touching the vault.
func classifyScope(fullName, recv string, imports []model.ImportStmt, dialect model.Language) string {
	return classifyScopeWithAlias(fullName, recv, imports, dialect, nil)
}

// classifyScopeWithAlias labels alias-internal sources as internal.
func classifyScopeWithAlias(fullName, recv string, imports []model.ImportStmt, dialect model.Language, isInternal func(source string) bool) string {
	return classifyScopeIndexed(fullName, recv, imports, nil, dialect, isInternal)
}

// indexedImp pairs an import with its file order.
type indexedImp struct {
	imp model.ImportStmt
	pos int
}

// indexImports keys imports by local name once per file for O(1) lookup.
func indexImports(imports []model.ImportStmt) map[string]indexedImp {
	if len(imports) == 0 {
		return nil
	}
	byLocal := make(map[string]indexedImp, len(imports)+1)
	for i, imp := range imports {
		if imp.Kind == "export-alias" || imp.Kind == model.ImportKindGoPackage {
			continue
		}
		for _, key := range []string{imp.LocalName, imp.Alias} {
			if key == "" {
				continue
			}
			if _, ok := byLocal[key]; !ok {
				byLocal[key] = indexedImp{imp: imp, pos: i}
			}
		}
	}
	return byLocal
}

// classifyScopeIndexed classifies with a prebuilt index when non-nil.
func classifyScopeIndexed(fullName, recv string, imports []model.ImportStmt, byLocal map[string]indexedImp, dialect model.Language, isInternal func(source string) bool) string {
	probe := fullName
	if recv != "" {
		probe = recv
	}
	if byLocal != nil {
		best, ok := byLocal[probe]
		if star, sok := byLocal["*"]; sok && (!ok || star.pos < best.pos) {
			best, ok = star, true
		}
		if ok {
			imp := best.imp
			if strings.HasPrefix(imp.Source, ".") {
				return model.ScopeInternal
			}
			if isInternal != nil && isInternal(imp.Source) {
				return model.ScopeInternal
			}
			return model.ExternalScope(moduleOf(imp.Source))
		}
	} else {
		for _, imp := range imports {
			local := imp.LocalName
			if local == "" {
				local = imp.Alias
			}
			if local == "" || (local != probe && local != "*") {
				continue
			}
			if strings.HasPrefix(imp.Source, ".") {
				return model.ScopeInternal
			}
			if isInternal != nil && isInternal(imp.Source) {
				return model.ScopeInternal
			}
			return model.ExternalScope(moduleOf(imp.Source))
		}
	}
	if isRuntimeBuiltin(recv, probe, dialect) {
		return model.ScopeRuntime
	}
	return model.ScopeUnresolved
}

// moduleOf reduces an import source to its top-level module.
func moduleOf(source string) string {
	if strings.HasPrefix(source, "@") {
		parts := strings.Split(source, "/")
		if len(parts) >= 2 {
			return parts[0] + "/" + parts[1]
		}
		return source
	}
	if i := strings.IndexAny(source, "./"); i >= 0 {
		if i == 0 {
			return source
		}
		return source[:i]
	}
	return source
}

var pyBuiltins = convention.PyBuiltins

var tsBuiltins = convention.TSBuiltins

// isRuntimeBuiltin checks the shared builtin convention.
func isRuntimeBuiltin(recv, probe string, dialect model.Language) bool {
	return convention.IsRuntimeBuiltin(recv, probe, dialect)
}
