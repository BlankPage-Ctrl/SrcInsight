package typescript

import (
	"reflect"
	"testing"
)

func TestSplitPackage(t *testing.T) {
	cases := []struct{ in, pkg, sub string }{
		{"zod", "zod", "."},
		{"zod/mini", "zod", "./mini"},
		{"@org/pkg/sub", "@org/pkg", "./sub"},
		{"@org/pkg", "@org/pkg", "."},
	}
	for _, c := range cases {
		pkg, sub := SplitPackage(c.in)
		if pkg != c.pkg || sub != c.sub {
			t.Errorf("SplitPackage(%q) = (%q,%q), want (%q,%q)", c.in, pkg, sub, c.pkg, c.sub)
		}
	}
}

func TestParseWorkspaceGlobs(t *testing.T) {
	got := ParseWorkspaceGlobs("packages:\n  - 'packages/*'\n  - \"apps/*\" # comment\n")
	if !reflect.DeepEqual(got, []string{"packages/*", "apps/*"}) {
		t.Fatalf("globs = %v", got)
	}
}

func TestExpandWorkspaceCandidates(t *testing.T) {
	got := ExpandWorkspaceCandidates("packages/pkg", map[string]any{".": "./index.js"}, "pkg")
	if len(got) == 0 || got[0] != "packages/pkg/src/index.ts" {
		t.Fatalf("candidates = %v, want packages/pkg/src/index.ts first", got)
	}
	got = ExpandWorkspaceCandidates("packages/pkg", nil, "pkg/mini")
	found := false
	for _, c := range got {
		if c == "packages/pkg/src/mini.ts" {
			found = true
		}
	}
	if !found {
		t.Fatalf("subpath candidates = %v, want packages/pkg/src/mini.ts", got)
	}
}

func TestIsConfigRel(t *testing.T) {
	for _, p := range []string{"tsconfig.json", "a/tsconfig.json", "jsconfig.json", "tsconfig.base.json"} {
		if !IsConfigRel(p) {
			t.Errorf("%s should be config", p)
		}
	}
	if IsConfigRel("src/app.ts") {
		t.Error("app.ts must not be config")
	}
}
