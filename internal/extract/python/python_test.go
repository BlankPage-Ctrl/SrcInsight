package python

import (
	"testing"

	sitter "github.com/tree-sitter/go-tree-sitter"
	py "github.com/tree-sitter/tree-sitter-python/bindings/go"

	"srcinsight/internal/extract"
	"srcinsight/internal/model"
)

func extractPY(t *testing.T, src string) extract.FileExtract {
	t.Helper()
	parser := sitter.NewParser()
	defer parser.Close()
	if err := parser.SetLanguage(sitter.NewLanguage(py.Language())); err != nil {
		t.Fatal(err)
	}
	tree := parser.Parse([]byte(src), nil)
	if tree == nil {
		t.Fatal("parse failed")
	}
	defer tree.Close()
	fe, err := Extract("app/mod.py", []byte(src), tree)
	if err != nil {
		t.Fatal(err)
	}
	return fe
}

func TestExtractFunctions(t *testing.T) {
	src := `
from .util import helper


class Store:
    def get_user(self, token):
        return self._load(token) or helper(token)

    def _load(self, key):
        return key


async def fetch():
    return await get()


def gen_values():
    yield 1
`
	fe := extractPY(t, src)

	byName := map[string]model.Function{}
	for _, f := range fe.Functions {
		byName[f.Name] = f
	}

	gu, ok := byName["get_user"]
	if !ok {
		t.Fatalf("get_user missing; got %v", funcNames(fe))
	}
	if gu.Kind != model.KindMethod {
		t.Errorf("kind = %s, want method", gu.Kind)
	}
	if gu.ContainerName() != "Store" {
		t.Errorf("container = %q, want Store", gu.ContainerName())
	}
	if gu.File.Kind != model.ContainerFile || gu.File.Name != "mod.py" {
		t.Errorf("file container = %+v", gu.File)
	}
	calls := map[string]bool{}
	for _, c := range gu.Calls {
		calls[c.Name] = true
	}
	if !calls["self._load"] {
		t.Errorf("missing self._load; got %v", calls)
	}
	if !calls["helper"] {
		t.Errorf("missing helper; got %v", calls)
	}

	if byName["fetch"].Kind != model.KindAsyncFunction {
		t.Errorf("fetch kind = %s, want async-function", byName["fetch"].Kind)
	}
	if byName["gen_values"].Kind != model.KindGenerator {
		t.Errorf("generator kind = %s", byName["gen_values"].Kind)
	}

	// import recorded
	if len(fe.Imports) == 0 || fe.Imports[0].LocalName != "helper" {
		t.Errorf("imports = %+v", fe.Imports)
	}
}

func funcNames(fe extract.FileExtract) []string {
	var out []string
	for _, f := range fe.Functions {
		out = append(out, f.Name)
	}
	return out
}

func TestExtractBranchCond(t *testing.T) {
	fe := extractPY(t, `
def flow(x, err):
    plain()
    if x > 0:
        positive()
    else:
        nonpositive()
    if err is None:
        nested = lambda: inside_lambda()
        return nested
`)
	var fn *model.Function
	for i := range fe.Functions {
		if fe.Functions[i].Name == "flow" {
			fn = &fe.Functions[i]
		}
	}
	if fn == nil {
		t.Fatal("flow missing")
	}
	condOf := map[string]string{}
	for _, c := range fn.Calls {
		condOf[c.Name] = c.Cond
	}
	if condOf["plain"] != "" {
		t.Errorf("plain cond = %q, want empty", condOf["plain"])
	}
	if condOf["positive"] != "x > 0" {
		t.Errorf("positive cond = %q", condOf["positive"])
	}
	if condOf["nonpositive"] != "!(x > 0)" {
		t.Errorf("nonpositive cond = %q", condOf["nonpositive"])
	}
	if condOf["inside_lambda"] != "" {
		t.Errorf("inside_lambda cond = %q, want empty (lambda boundary)", condOf["inside_lambda"])
	}
}
