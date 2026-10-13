package convention

import (
	"testing"

	"srcinsight/internal/model"
)

func TestIsTestPath(t *testing.T) {
	yes := []string{
		"src/a.test.ts",
		"src/a.spec.ts",
		"__tests__/a.ts",
		"src/test/x.ts",
		"testdata/zod.ts",
		"src/mocks/svc.ts",
	}
	for _, p := range yes {
		if !IsTestPath(p) {
			t.Fatalf("IsTestPath(%q) = false, want true", p)
		}
	}
	no := []string{"src/a.ts", "src/testing-util.ts", "src/latest.ts"}
	for _, p := range no {
		if IsTestPath(p) {
			t.Fatalf("IsTestPath(%q) = true, want false", p)
		}
	}
	if !IsTestPath("pkg/tea_test.go") {
		t.Fatal("IsTestPath(pkg/tea_test.go) = false, want true (Go test suffix)")
	}
	if IsTestPath("pkg/latest.go") {
		t.Fatal("IsTestPath(pkg/latest.go) = true, want false")
	}
}

func TestIsGeneratedPath(t *testing.T) {
	for _, p := range []string{"src/types.d.ts", "gen/foo.pb.go", "a.min.js", "x/__generated__/y.ts"} {
		if !IsGeneratedPath(p) {
			t.Fatalf("IsGeneratedPath(%q) = false, want true", p)
		}
	}
	if IsGeneratedPath("src/a.ts") {
		t.Fatal("IsGeneratedPath(src/a.ts) = true, want false")
	}
}

func TestIsRuntimeBuiltin(t *testing.T) {
	if !IsRuntimeBuiltin("JSON", "JSON.parse", model.LangTypeScript) {
		t.Fatal("JSON.parse should be runtime")
	}
	if !IsRuntimeBuiltin("", "fetch", model.LangTypeScript) {
		t.Fatal("fetch should be runtime")
	}
	if !IsRuntimeBuiltin("", "print", model.LangPython) {
		t.Fatal("print should be runtime")
	}
	if IsRuntimeBuiltin("", "parseAsync", model.LangTypeScript) {
		t.Fatal("parseAsync must not be runtime")
	}
	// Go predeclared conversions double as runtime calls.
	for _, n := range []string{"len", "string", "int", "float64", "bool", "byte"} {
		if !IsRuntimeBuiltin("", n, model.LangGo) {
			t.Fatalf("%s should be a Go runtime builtin", n)
		}
	}
	if IsRuntimeBuiltin("", "NewServer", model.LangGo) {
		t.Fatal("NewServer must not be runtime")
	}
}

func TestTestCallee(t *testing.T) {
	for _, n := range []string{"test", "it", "each", "only"} {
		if !IsTestCalleeName(n) {
			t.Fatalf("IsTestCalleeName(%q) = false", n)
		}
	}
	if IsTestCalleeName("parseAsync") {
		t.Fatal("parseAsync must not be a test callee")
	}
}

func TestStripAskExt(t *testing.T) {
	if got := StripAskExt("parse.ts"); got != "parse" {
		t.Fatalf("StripAskExt(parse.ts) = %q", got)
	}
	if got := StripAskExt("Class.method"); got != "Class.method" {
		t.Fatalf("StripAskExt(Class.method) = %q", got)
	}
}

func TestNestJS(t *testing.T) {
	if !IsNestClassDecor("Controller") || !IsNestClassDecor("Injectable") || !IsNestClassDecor("Module") {
		t.Fatal("class decors missing")
	}
	for _, m := range []string{"Get", "Post", "Put", "Delete", "Patch", "Options", "Head", "All"} {
		if !IsNestMethodDecor(m) {
			t.Fatalf("method decor %s missing", m)
		}
	}
	if IsNestDecor("Param") {
		t.Fatal("Param must stay out of v1 scope")
	}
	if !IsNestFile("cats.controller.ts") || !IsNestFile("cats.service.ts") || !IsNestFile("cats.module.ts") {
		t.Fatal("nest file suffix missing")
	}
	if !IsNestFile("shortcode.resolver.ts") {
		t.Fatal("resolver file suffix missing")
	}
	if !IsNestGQLClassDecor("Resolver") || !IsNestGQLDecor("Resolver") {
		t.Fatal("GQL class decor missing")
	}
	for _, m := range []string{"Query", "Mutation", "Subscription", "ResolveField"} {
		if !IsNestGQLMethodDecor(m) || !IsNestGQLDecor(m) || !IsNestDecor(m) {
			t.Fatalf("GQL method decor %s missing", m)
		}
	}
	for _, m := range []string{"ObjectType", "InputType", "Field", "Args"} {
		if IsNestDecor(m) {
			t.Fatalf("%s must stay out of v2 scope", m)
		}
	}
	if got := NestTag("Get"); got != "route:Get" {
		t.Fatalf("NestTag(Get) = %q", got)
	}
	if got := NestTag("Controller"); got != "controller" {
		t.Fatalf("NestTag(Controller) = %q", got)
	}
	if got := NestTag("Resolver"); got != "resolver" {
		t.Fatalf("NestTag(Resolver) = %q", got)
	}
	for decor, want := range map[string]string{
		"Query": "query", "Mutation": "mutation",
		"Subscription": "subscription", "ResolveField": "resolve-field",
	} {
		if got := NestTag(decor); got != want {
			t.Fatalf("NestTag(%s) = %q, want %q", decor, got, want)
		}
	}
	if _, ok := Registered("nestjs"); !ok {
		t.Fatal("nestjs not registered")
	}
}
