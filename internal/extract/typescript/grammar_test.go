package typescript

import (
	"testing"

	sitter "github.com/tree-sitter/go-tree-sitter"

	"srcinsight/internal/model"
)

// angleAssertionSrc is the minimal trigger for the hoppscotch AuthService
// loss: an fp-ts style `E.left(<T>{...})` angle-bracket assertion inside a
// method. Under the TSX grammar `<T>` mis-parses as a JSX opening element;
// the recovery cascade dissolves the enclosing class_declaration, so the
// class entry (and its tags) vanish while the methods survive.
const angleAssertionSrc = `import * as E from 'fp-ts/Either';
import { Injectable } from '@nestjs/common';
@Injectable()
export class Svc {
  m() {
    return E.left(<Err>{
      message: 1,
    });
  }
}
`

func extractWithPath(t *testing.T, path, src string) (extracted []model.Function, errs int) {
	t.Helper()
	parser := sitter.NewParser()
	defer parser.Close()
	setPathLanguage(t, parser, path)
	tree := parser.Parse([]byte(src), nil)
	if tree == nil {
		t.Fatal("parse failed")
	}
	defer tree.Close()
	errs = countErrs(tree.RootNode())
	fe, err := Extract(path, []byte(src), tree)
	if err != nil {
		t.Fatal(err)
	}
	return fe.Functions, errs
}

func countErrs(n *sitter.Node) int {
	c := 0
	var walk func(*sitter.Node)
	walk = func(x *sitter.Node) {
		if x.Kind() == "ERROR" {
			c++
			return
		}
		for i := 0; i < int(x.ChildCount()); i++ {
			walk(x.Child(uint(i)))
		}
	}
	walk(n)
	return c
}

func TestAngleAssertionKeepsClassInTS(t *testing.T) {
	for _, path := range []string{"src/svc.ts", "src/svc.mts", "src/svc.cts"} {
		funcs, errs := extractWithPath(t, path, angleAssertionSrc)
		if errs != 0 {
			t.Errorf("%s: ERROR nodes = %d, want 0", path, errs)
		}
		byName := map[string]model.Function{}
		for _, f := range funcs {
			byName[f.Name] = f
		}
		svc, ok := byName["Svc"]
		if !ok {
			t.Fatalf("%s: class Svc missing; got %v", path, funcNamesOf(funcs))
		}
		if svc.Kind != model.KindClassDef {
			t.Errorf("%s: Svc kind = %s, want class", path, svc.Kind)
		}
		if len(svc.Tags) != 1 || svc.Tags[0] != "injectable" {
			t.Errorf("%s: Svc tags = %v, want [injectable]", path, svc.Tags)
		}
		m, ok := byName["m"]
		if !ok {
			t.Fatalf("%s: method m missing", path)
		}
		if m.QualifiedName() != "Svc.m" {
			t.Errorf("%s: m qualified = %q, want Svc.m", path, m.QualifiedName())
		}
	}
}

// TestAngleAssertionCleanerInTS documents why .ts files must not use the
// TSX grammar: the same assertion parses with zero ERROR nodes under the
// TypeScript grammar but leaves error nodes under TSX (where `<T>` reads as
// a JSX opening element). On large real-world files that local confusion
// cascades and dissolves the enclosing class entirely (hoppscotch
// AuthService); the TS grammar keeps it clean regardless of file size.
func TestAngleAssertionCleanerInTS(t *testing.T) {
	_, tsErrs := extractWithPath(t, "src/svc.ts", angleAssertionSrc)
	if tsErrs != 0 {
		t.Errorf("ts: ERROR nodes = %d, want 0", tsErrs)
	}
	_, tsxErrs := extractWithPath(t, "src/svc.tsx", angleAssertionSrc)
	if tsxErrs == 0 {
		t.Errorf("tsx: ERROR nodes = 0, want >0 (angle assertion reads as JSX)")
	}
}

func funcNamesOf(funcs []model.Function) []string {
	out := make([]string, 0, len(funcs))
	for _, f := range funcs {
		out = append(out, f.Name)
	}
	return out
}
