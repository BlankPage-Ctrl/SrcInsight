package typescript

import (
	"strings"
	"testing"

	"srcinsight/internal/model"

	sitter "github.com/tree-sitter/go-tree-sitter"
)

func extractNames(t *testing.T, src string) map[string]model.Function {
	t.Helper()
	p := sitter.NewParser()
	defer p.Close()
	setPathLanguage(t, p, "src/x.ts")
	tree := p.Parse([]byte(src), nil)
	defer tree.Close()
	fe, _ := Extract("src/x.ts", []byte(src), tree)
	m := make(map[string]model.Function)
	for _, f := range fe.Functions {
		m[f.Name] = f
	}
	return m
}

func TestConstAllScopes(t *testing.T) {
	src := `
const GLOBAL = 1;
const MULTI_A = 1, MULTI_B = 2;
function outer() {
  const insideOuter = GLOBAL;
  const arrowConst = () => insideOuter;
  const cb = function() { const insideCb = 1; return insideCb; };
  for (const loopVar of GLOBAL_LIST) {
    const loopInner = loopVar;
  }
  try {
    const tryConst = 1;
  } catch (e) {
    const catchConst = e;
  } finally {
    const finallyConst = 1;
  }
  if (true) {
    const blockConst = 1;
  }
  const nestedArrow = (x) => {
    const innerArrowConst = x;
    return innerArrowConst;
  };
}
namespace MyNS {
  export const nsConst = 123;
}
module MyMod {
  export const modConst = 456;
}
class MyClass {
  myMethod() {
    const methodConst = 1;
  }
}
const topHelper = () => 42;
const usesOther = GLOBAL + topHelper();
const usesCall = topHelper();
`
	m := extractNames(t, src)
	cases := []struct {
		name     string
		wantKind model.Kind
		wantCont string // empty for top-level, else container name
		wantUses int    // check non-empty uses if >0
	}{
		{"GLOBAL", model.KindConst, "", 0},
		{"MULTI_A", model.KindConst, "", 0},
		{"MULTI_B", model.KindConst, "", 0},
		{"insideOuter", model.KindConst, "outer", 1},
		{"loopVar", model.KindConst, "outer", 1},
		{"loopInner", model.KindConst, "outer", 1},
		{"tryConst", model.KindConst, "outer", 0},
		{"catchConst", model.KindConst, "outer", 1},
		{"finallyConst", model.KindConst, "outer", 0},
		{"blockConst", model.KindConst, "outer", 0},
		{"innerArrowConst", model.KindConst, "nestedArrow", 1},
		{"nsConst", model.KindConst, "MyNS", 0},
		{"modConst", model.KindConst, "MyMod", 0},
		{"methodConst", model.KindConst, "myMethod", 0},
		{"usesOther", model.KindConst, "", 2},
		{"usesCall", model.KindConst, "", 1},
	}
	for _, c := range cases {
		f, ok := m[c.name]
		if !ok {
			t.Errorf("missing const %q: got %v", c.name, func() []string {
				var s []string
				for k := range m {
					s = append(s, k)
				}
				return s
			}())
			continue
		}
		if f.Kind != c.wantKind {
			t.Errorf("%q kind=%q want %q", c.name, f.Kind, c.wantKind)
		}
		gotCont := ""
		if f.Container != nil {
			gotCont = f.Container.Name
		}
		if gotCont != c.wantCont {
			t.Errorf("%q container=%q want %q (got kind %v)", c.name, gotCont, c.wantCont, f.Container)
		}
		if len(f.Uses) < c.wantUses {
			t.Errorf("%q uses=%v want at least %d", c.name, f.Uses, c.wantUses)
		}
		// Signature must contain const (or for loop)
		if f.Signature == "" || !strings.Contains(f.Signature, "const") {
			t.Errorf("%q signature=%q want const", c.name, f.Signature)
		}
	}
	// Ensure let/var not captured
	if _, ok := m["notConst"]; ok {
		t.Error("let should not be captured")
	}
	// Ensure function still indexed
	if _, ok := m["outer"]; !ok {
		t.Error("outer function missing")
	}
}
