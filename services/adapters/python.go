package adapters

import (
	sitter "github.com/tree-sitter/go-tree-sitter"
	py "github.com/tree-sitter/tree-sitter-python/bindings/go"

	"srcinsight/internal/convention"
	pyextract "srcinsight/internal/extract/python"
	"srcinsight/internal/model"
	"srcinsight/services/resolve"
	pyresolve "srcinsight/services/resolve/py"
)

var pyLang = &Language{
	Dialect:   model.LangPython,
	Exts:      []string{".py"},
	NewParser: newPyParser,
	Extract:   extractPy,
	Resolver:  resolverPy,
	IsBuiltin: isBuiltinPy,
}

func init() { register(pyLang) }

func newPyParser() (*sitter.Parser, error) {
	return newParserWith(sitter.NewLanguage(py.Language()))
}

func extractPy(p *Pool, rel string, src []byte) (model.FileExtract, error) {
	var fe model.FileExtract
	err := p.Parse(Get(model.LangPython), rel, src, func(tree *sitter.Tree) error {
		var xerr error
		fe, xerr = pyextract.Extract(rel, src, tree)
		return xerr
	})
	if err != nil {
		return model.FileExtract{}, err
	}
	return fe, nil
}

func resolverPy(*Runtime) resolve.ImportResolver {
	return pyresolve.PyResolver{}
}

func isBuiltinPy(recv, probe string) bool {
	return convention.IsRuntimeBuiltin(recv, probe, model.LangPython)
}
