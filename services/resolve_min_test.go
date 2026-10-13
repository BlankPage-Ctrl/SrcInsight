package services

import (
	"testing"

	"srcinsight/internal/model"
	"srcinsight/internal/storage/cold"
)

func mkFn(id, name, file string, container string, calls ...string) *model.Function {
	f := &model.Function{
		FunctionRef: model.FunctionRef{
			ID:       model.EntryID(id),
			Name:     name,
			FilePath: file,
		},
		Kind: model.KindFunction,
	}
	if container != "" {
		f.Kind = model.KindMethod
		f.Container = &model.Container{Kind: model.ContainerClass, Name: container}
	}
	for _, c := range calls {
		f.Calls = append(f.Calls, model.FunctionRef{Name: c})
	}
	return f
}

func TestResolvePlainSameFile(t *testing.T) {
	caller := mkFn("a", "caller", "f.ts", "", "callee", "missing")
	callee := mkFn("b", "callee", "f.ts", "", "")
	links, loose := ResolveFile("f.ts", []*model.Function{caller, callee}, nil, model.LangTypeScript)
	if len(links) != 1 || links[0].Origin != "a" || links[0].Target != "b" || links[0].Kind != model.EdgeCalls {
		t.Fatalf("links = %+v", links)
	}
	if len(loose) != 1 || loose[0].Name != "missing" || loose[0].Scope != model.ScopeUnresolved {
		t.Fatalf("loose = %+v", loose)
	}
}

func TestResolveSelfPrefersContainer(t *testing.T) {
	caller := mkFn("a", "run", "f.ts", "A", "self.help")
	helpA := mkFn("b", "help", "f.ts", "A")
	helpB := mkFn("c", "help", "f.ts", "B")
	links, loose := ResolveFile("f.ts",
		[]*model.Function{caller, helpA, helpB}, nil, model.LangTypeScript)
	if len(loose) != 0 {
		t.Fatalf("loose = %+v", loose)
	}
	if len(links) != 1 || links[0].Target != "b" {
		t.Fatalf("links = %+v, want single link to same-container help", links)
	}
}

func TestResolveReceiverNeverGuessed(t *testing.T) {
	caller := mkFn("a", "caller", "f.ts", "", "obj.callee")
	callee := mkFn("b", "callee", "f.ts", "", "")
	links, loose := ResolveFile("f.ts", []*model.Function{caller, callee}, nil, model.LangTypeScript)
	if len(links) != 0 {
		t.Fatalf("links = %+v, cross-receiver must not resolve locally", links)
	}
	if len(loose) != 1 {
		t.Fatalf("loose = %+v", loose)
	}
}

func TestResolveUsesPrefersReceiverContainer(t *testing.T) {
	alias := mkFn("a", "objectType", "f.ts", "")
	alias.Kind = model.KindConst
	alias.Uses = []model.FunctionRef{{Name: "ZodObject.create"}}
	ctor := mkFn("b", "create", "f.ts", "ZodObject")
	other := mkFn("c", "create", "f.ts", "ZodArray")
	links, loose := ResolveFile("f.ts",
		[]*model.Function{alias, ctor, other}, nil, model.LangTypeScript)
	if len(loose) != 0 {
		t.Fatalf("loose = %+v", loose)
	}
	if len(links) != 1 || links[0].Target != "b" {
		t.Fatalf("links = %+v, want single link to ZodObject.create", links)
	}
}

func TestResolveUsesBackoffWithoutContainer(t *testing.T) {
	alias := mkFn("a", "objectType", "f.ts", "")
	alias.Kind = model.KindConst
	alias.Uses = []model.FunctionRef{{Name: "NS.create"}}
	ctor := mkFn("b", "create", "f.ts", "ZodObject")
	links, loose := ResolveFile("f.ts",
		[]*model.Function{alias, ctor}, nil, model.LangTypeScript)
	if len(loose) != 0 {
		t.Fatalf("loose = %+v", loose)
	}
	if len(links) != 1 || links[0].Target != "b" {
		t.Fatalf("links = %+v, want backoff link", links)
	}
}

func TestResolveScopeClassification(t *testing.T) {
	caller := mkFn("a", "caller", "f.ts", "", "sign", "fetch", "helper")
	callee := mkFn("b", "other", "f.ts", "", "")
	imports := []model.ImportStmt{
		{LocalName: "sign", Source: "./jwt", Kind: "named"},
		{LocalName: "helper", Source: "lodash", Kind: "named"},
	}
	_, loose := ResolveFile("f.ts", []*model.Function{caller, callee}, imports, model.LangTypeScript)
	byName := map[string]cold.LooseEndRow{}
	for _, l := range loose {
		byName[l.Name] = l
	}
	if byName["sign"].Scope != model.ScopeInternal {
		t.Errorf("sign scope = %q, want internal", byName["sign"].Scope)
	}
	if byName["fetch"].Scope != model.ScopeRuntime {
		t.Errorf("fetch scope = %q, want runtime", byName["fetch"].Scope)
	}
	if byName["helper"].Scope != "external:lodash" {
		t.Errorf("helper scope = %q, want external:lodash", byName["helper"].Scope)
	}
}

func TestResolveHeritageSameFile(t *testing.T) {
	base := &model.Function{
		FunctionRef: model.FunctionRef{ID: "b", Name: "Base", FilePath: "f.ts"},
		Kind:        model.KindClassDef,
	}
	child := &model.Function{
		FunctionRef: model.FunctionRef{ID: "c", Name: "Child", FilePath: "f.ts"},
		Kind:        model.KindClassDef,
		Extends:     []model.FunctionRef{{Name: "Base"}},
		Implements:  []model.FunctionRef{{Name: "Missing"}},
	}
	links, loose := ResolveFile("f.ts",
		[]*model.Function{base, child}, nil, model.LangTypeScript)
	if len(links) != 1 || links[0].Kind != model.EdgeExtends {
		t.Fatalf("links = %+v", links)
	}
	if len(loose) != 1 || loose[0].Kind != model.EdgeImplements {
		t.Fatalf("loose = %+v", loose)
	}
}
