package adapters

import (
	"path"
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"
	gots "github.com/tree-sitter/tree-sitter-go/bindings/go"

	"srcinsight/internal/convention"
	goextract "srcinsight/internal/extract/go"
	"srcinsight/internal/model"
	"srcinsight/internal/storage/cold"
	"srcinsight/services/resolve"
	goresolve "srcinsight/services/resolve/go"
)

var goLang = &Language{
	Dialect:          model.LangGo,
	Exts:             []string{".go"},
	NewParser:        newGoParser,
	Extract:          extractGo,
	Resolver:         resolverGo,
	InternalSource:   internalSourceGo,
	Invalidate:       invalidateGo,
	IsBuiltin:        isBuiltinGo,
	RefWithoutImport: refWithoutImportGo,
	Forward: []ForwardPass{
		{Name: "go-package", Run: goPackageForward},
	},
	ForwardShard: []ForwardPass{
		{Name: "go-package", Run: goPackageForward},
	},
	Reverse: []ReversePass{
		{Name: "go-package", Stage: ReverseCross, Run: goPackageReverse},
	},
}

func init() { register(goLang) }

func newGoParser() (*sitter.Parser, error) {
	return newParserWith(sitter.NewLanguage(gots.Language()))
}

func extractGo(p *Pool, rel string, src []byte) (model.FileExtract, error) {
	var fe model.FileExtract
	err := p.Parse(Get(model.LangGo), rel, src, func(tree *sitter.Tree) error {
		var xerr error
		fe, xerr = goextract.Extract(rel, src, tree)
		return xerr
	})
	if err != nil {
		return model.FileExtract{}, err
	}
	return fe, nil
}

func resolverGo(rt *Runtime) resolve.ImportResolver {
	if rt == nil || rt.Go == nil {
		return goresolve.GoResolver{}
	}
	return goresolve.GoResolver{Mods: rt.Go.Mods}
}

// internalSourceGo scopes go.mod lookups to Go importers so bare imports from
// other dialects never collide.
func internalSourceGo(rt *Runtime, file, source string) bool {
	if l, ok := Lookup(file); !ok || l.Dialect != model.LangGo {
		return false
	}
	if strings.HasPrefix(source, ".") {
		return true
	}
	if rt == nil || rt.Go == nil || rt.Go.Mods == nil {
		return false
	}
	return goresolve.GoResolver{Mods: rt.Go.Mods}.IsInternalSource(file, source)
}

func invalidateGo(rt *Runtime) {
	if rt == nil || rt.Go == nil {
		return
	}
	rt.Go.Mods.InvalidateAll()
}

func isBuiltinGo(recv, probe string) bool {
	return convention.IsRuntimeBuiltin(recv, probe, model.LangGo)
}

// refWithoutImportGo reports the same-package case: plain names need only a
// matching directory, not an import edge.
func refWithoutImportGo(targetFile string, le cold.LooseEndRow, recv string) (handled, ok bool) {
	if recv != "" {
		return false, false
	}
	return true, path.Dir(le.FilePath) == path.Dir(targetFile)
}
