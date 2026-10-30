package adapters

import (
	"context"
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"
	tstsg "github.com/tree-sitter/tree-sitter-typescript/bindings/go"

	"srcinsight/internal/convention"
	"srcinsight/internal/extract/frontend"
	tsextract "srcinsight/internal/extract/typescript"
	"srcinsight/internal/model"
	"srcinsight/internal/storage/cold"
	"srcinsight/services/resolve"
	"srcinsight/services/resolve/ts"
)

var tsLang = &Language{
	Dialect: model.LangTypeScript,
	Exts: []string{
		".ts", ".tsx", ".mts", ".cts",
		".js", ".jsx", ".mjs", ".cjs",
		".vue",
	},
	NewParser:       newTSParser,
	Grammar:         tsextract.LanguageForPath,
	Extract:         extractTS,
	Resolver:        resolverTS,
	InternalSource:  internalSourceTS,
	Invalidate:      invalidateTS,
	PrepareSameFile: prepareTS,
	SameFileCall:    sameFileCallTS,
	IsBuiltin: func(recv, probe string) bool {
		return convention.IsRuntimeBuiltin(recv, probe, model.LangTypeScript)
	},
	Forward: []ForwardPass{
		{Name: "di", Run: diForward},
		{Name: "recv", Enabled: ts.RecvForwardEnabled, Run: recvForward},
	},
	ForwardShard: []ForwardPass{
		{Name: "di", Run: diForward},
	},
	RecvShard: []ShardPass{
		{Name: "recv", Enabled: ts.RecvForwardEnabled, Run: recvShard},
	},
	Reverse: []ReversePass{
		{Name: "di", Stage: ReverseLanguage, Run: diReverse},
		{Name: "recv", Stage: ReverseRecv, Enabled: ts.RecvReverseEnabled, Run: recvReverse},
	},
}

func init() { register(tsLang) }

func newTSParser() (*sitter.Parser, error) {
	return newParserWith(sitter.NewLanguage(tstsg.LanguageTSX()))
}

// extractTS parses one TypeScript-family file, splitting Vue SFCs into scripts.
func extractTS(p *Pool, rel string, src []byte) (model.FileExtract, error) {
	if convention.IsVueFile(rel) {
		return extractVue(p, rel, src)
	}
	var fe model.FileExtract
	err := p.Parse(Get(model.LangTypeScript), rel, src, func(tree *sitter.Tree) error {
		var xerr error
		fe, xerr = tsextract.Extract(rel, src, tree)
		return xerr
	})
	if err != nil {
		return model.FileExtract{}, err
	}
	return fe, nil
}

// Each <script> block parses under a synthetic path so queries match the parser grammar.
func extractVue(p *Pool, rel string, src []byte) (model.FileExtract, error) {
	sfc := frontend.Split(src)
	comp := convention.VueComponentName(rel)
	var fe model.FileExtract
	fe.Path = rel
	fe.Language = model.LangTypeScript
	for _, sb := range sfc.Scripts {
		if len(sb.Content) == 0 {
			continue
		}
		synth := rel + sb.ParserExt
		err := p.Parse(Get(model.LangTypeScript), synth, sb.Content, func(tree *sitter.Tree) error {
			sfe, xerr := tsextract.Extract(synth, sb.Content, tree)
			if xerr != nil {
				return xerr
			}
			frontend.ShiftLines(&sfe, sb.StartLine-1, rel)
			fe.Functions = append(fe.Functions, sfe.Functions...)
			fe.Imports = append(fe.Imports, sfe.Imports...)
			fe.RecvBindings = append(fe.RecvBindings, sfe.RecvBindings...)
			fe.Endpoints = append(fe.Endpoints, sfe.Endpoints...)
			fe.Calls = append(fe.Calls, sfe.Calls...)
			return nil
		})
		if err != nil {
			return model.FileExtract{}, err
		}
	}
	fe.Imports = frontend.DedupeImports(fe.Imports)
	frontend.AttachComponent(&fe, comp)
	refs := templateRefsOf(rel, src)
	if len(refs) > 0 || len(fe.Functions) == 0 {
		if comp != "" {
			fe.Functions = append(fe.Functions, frontend.SyntheticRoot(rel, comp, refs))
		}
	}
	return fe, nil
}

// templateRefsOf reads template bindings through the matching plugin.
// Files without a Templater (plain scripts) yield nothing.
func templateRefsOf(rel string, src []byte) []model.FunctionRef {
	pl, ok := frontend.ForFile(rel)
	if !ok {
		return nil
	}
	tmpl, ok := pl.(frontend.Templater)
	if !ok {
		return nil
	}
	return tmpl.TemplateRefs(rel, src)
}

func resolverTS(rt *Runtime) resolve.ImportResolver {
	if rt == nil || rt.TS == nil {
		return ts.TSResolver{}
	}
	return ts.TSResolver{Alias: rt.TS.Alias, WS: rt.TS.WS}
}

func internalSourceTS(rt *Runtime, file, source string) bool {
	if strings.HasPrefix(source, ".") {
		return true
	}
	if rt == nil || rt.TS == nil {
		return false
	}
	if rt.TS.Alias != nil && rt.TS.Alias.IsInternalSource(file, source) {
		return true
	}
	return rt.TS.WS != nil && rt.TS.WS.IsInternal(source)
}

func invalidateTS(rt *Runtime) {
	if rt == nil || rt.TS == nil {
		return
	}
	rt.TS.Alias.InvalidateAll()
	rt.TS.WS.Invalidate()
}

func prepareTS(fns []*model.Function) Prep {
	return ts.BuildCtorBinds(fns)
}

// sameFileCallTS resolves this.field.method through constructor field types.
func sameFileCallTS(prep Prep, caller *model.Function, name string, byName map[string][]*model.Function) ([]*model.Function, bool) {
	field, method, ok := ts.SplitThisChain(name)
	if !ok {
		return nil, false
	}
	binds, _ := prep.(map[string]map[string]string)
	return ts.DIChainTargets(caller, byName, binds, field, method), true
}

func diForward(ctx context.Context, s resolve.Store, bundles []cold.OriginBundle, pending resolve.Pending, _ map[string][]cold.ImportRow, lc *resolve.LookupCache) error {
	return ts.ResolveDIBatch(ctx, s, bundles, pending, lc)
}

func recvForward(ctx context.Context, s resolve.Store, bundles []cold.OriginBundle, pending resolve.Pending, _ map[string][]cold.ImportRow, lc *resolve.LookupCache) error {
	return ts.ResolveRecvBatch(ctx, s, bundles, pending, lc)
}

func recvShard(ctx context.Context, s resolve.Store, bundles []cold.OriginBundle, idx []int, pending resolve.Pending, lc *resolve.LookupCache) error {
	return ts.ResolveRecvShard(ctx, s, bundles, idx, pending, lc)
}

func diReverse(ctx context.Context, s resolve.Store, bundles []cold.OriginBundle) error {
	return ts.PromoteDIReverse(ctx, s, bundles)
}

func recvReverse(ctx context.Context, s resolve.Store, bundles []cold.OriginBundle) error {
	return ts.PromoteRecvReverse(ctx, s, bundles)
}
