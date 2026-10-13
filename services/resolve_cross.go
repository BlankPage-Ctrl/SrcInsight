package services

import (
	"strings"

	"srcinsight/internal/filesystem"
	"srcinsight/internal/model"
	"srcinsight/services/resolve"
	goresolve "srcinsight/services/resolve/go"
	pyresolve "srcinsight/services/resolve/py"
	tspkg "srcinsight/services/resolve/ts"
)

// ImportResolver wiring binds pipeline alias configs to per-language resolvers.

// ImportResolver maps an import source plus importer path to candidate files.
type ImportResolver = resolve.ImportResolver

// TSResolver is the canonical TypeScript resolver.
type TSResolver = tspkg.TSResolver

// Workspace is the canonical pnpm-workspace mapper.
type Workspace = tspkg.Workspace

// PyResolver is the canonical Python resolver.
type PyResolver = pyresolve.PyResolver

// GoResolver is the canonical go.mod-aware Go resolver.
type GoResolver = goresolve.GoResolver

// NewWorkspace builds a workspace mapper bound to root.
func NewWorkspace(root *filesystem.Root) *Workspace {
	return tspkg.NewWorkspace(root)
}

// relativeCandidates expands legacy relative-only candidates.
func relativeCandidates(currentFile, source string) []string {
	return tspkg.RelativeCandidates(currentFile, source)
}

// expandBaseCandidates expands one alias base into file candidates.
func expandBaseCandidates(base string) []string {
	return tspkg.ExpandBaseCandidates(base)
}

// resolverFor returns the stateless resolver for a dialect, or nil.
func resolverFor(lang model.Language) ImportResolver {
	switch lang {
	case model.LangTypeScript:
		return TSResolver{}
	case model.LangPython:
		return PyResolver{}
	case model.LangGo:
		return GoResolver{}
	default:
		return nil
	}
}

// resolverFor returns the alias-bound resolver, or stateless when unconfigured.
func (p *Pipeline) resolverFor(lang model.Language) ImportResolver {
	switch lang {
	case model.LangTypeScript:
		if p == nil {
			return TSResolver{}
		}
		return TSResolver{Alias: p.tsAlias, WS: p.ws}
	case model.LangPython:
		return PyResolver{}
	case model.LangGo:
		if p == nil {
			return GoResolver{}
		}
		return GoResolver{Mods: p.goMods}
	default:
		return nil
	}
}

// isInternalSource returns a per-file internal-source check; nil-safe.
func (p *Pipeline) isInternalSource(file string) func(source string) bool {
	if p == nil || (p.tsAlias == nil && p.ws == nil && p.goMods == nil) {
		return nil
	}
	alias, ws := p.tsAlias, p.ws
	goMods := p.goMods
	// Scope the Go module check to Go importers to avoid bare-import collisions.
	isGoImporter := false
	if lang, ok := LanguageOf(file); ok && lang == model.LangGo {
		isGoImporter = true
	}
	return func(source string) bool {
		if strings.HasPrefix(source, ".") {
			return true
		}
		if alias != nil && alias.IsInternalSource(file, source) {
			return true
		}
		if ws != nil && ws.IsInternal(source) {
			return true
		}
		return isGoImporter && goMods != nil && goresolve.GoResolver{Mods: goMods}.IsInternalSource(file, source)
	}
}
