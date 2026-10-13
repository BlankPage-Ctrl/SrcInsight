package ts

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"srcinsight/internal/filesystem"
)

func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	abs := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newResolver(t *testing.T, files map[string]string) *Resolver {
	t.Helper()
	dir := t.TempDir()
	for rel, src := range files {
		writeFile(t, dir, rel, src)
	}
	root, err := filesystem.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	return New(root)
}

func TestSiblingConfigs(t *testing.T) {
	r := newResolver(t, map[string]string{
		"frontend/tsconfig.json": `{"compilerOptions": {"baseUrl": ".", "paths": {"@/*": ["src/*"]}}}`,
		"frontend/src/api.ts":    `export function fetchData(): number { return 1; }`,
		"backend/tsconfig.json":  `{"compilerOptions": {"baseUrl": ".", "paths": {"@/*": ["lib/*"]}}}`,
	})
	got := r.CandidateBases("frontend/src/app.ts", "@/api")
	if !reflect.DeepEqual(got, []string{"frontend/src/api"}) {
		t.Fatalf("frontend expand = %+v", got)
	}
	got = r.CandidateBases("backend/src/app.ts", "@/api")
	if !reflect.DeepEqual(got, []string{"backend/lib/api"}) {
		t.Fatalf("backend expand = %+v", got)
	}
	if !r.IsInternalSource("frontend/src/app.ts", "@/api") {
		t.Fatal("alias should be internal")
	}
	if r.IsInternalSource("frontend/src/app.ts", "lodash") {
		t.Fatal("bare package must not be internal")
	}
}

func TestNestedUILayout(t *testing.T) {
	r := newResolver(t, map[string]string{
		"tsconfig.json":    `{"compilerOptions": {"baseUrl": ".", "paths": {"@root/*": ["shared/*"]}}}`,
		"ui/tsconfig.json": `{"compilerOptions": {"baseUrl": ".", "paths": {"@/*": ["src/*"]}}}`,
	})
	if got := r.CandidateBases("ui/src/app.ts", "@/x"); !reflect.DeepEqual(got, []string{"ui/src/x"}) {
		t.Fatalf("nested ui must win over root: %+v", got)
	}
	if got := r.CandidateBases("other/a.ts", "@root/x"); !reflect.DeepEqual(got, []string{"shared/x"}) {
		t.Fatalf("root fallback = %+v", got)
	}
}

func TestExtendsRelative(t *testing.T) {
	r := newResolver(t, map[string]string{
		"tsconfig.base.json":     `{"compilerOptions": {"paths": {"@shared/*": ["shared/*"]}}}`,
		"frontend/tsconfig.json": `{"extends": "../tsconfig.base.json", "compilerOptions": {"baseUrl": ".", "paths": {"@/*": ["src/*"]}}}`,
	})
	got := r.CandidateBases("frontend/src/a.ts", "@shared/u")
	// parent targets keep parent base ("" -> root), not child's frontend.
	if len(got) != 1 || got[0] != "shared/u" {
		t.Fatalf("extends-relative = %v, want [shared/u] (per-parent base)", got)
	}
	if got := r.CandidateBases("frontend/src/a.ts", "@/u"); len(got) != 1 || got[0] != "frontend/src/u" {
		t.Fatalf("child paths = %v", got)
	}
}

func TestExtendsCycleAndMissingSilent(t *testing.T) {
	r := newResolver(t, map[string]string{
		"a/tsconfig.json": `{"extends": "../b/tsconfig.json", "compilerOptions": {"paths": {"@/*": ["src/*"]}}}`,
		"b/tsconfig.json": `{"extends": "../a/tsconfig.json"}`,
		"c/tsconfig.json": `{"extends": "./missing.json", "compilerOptions": {"baseUrl": ".", "paths": {"@/*": ["src/*"]}}}`,
	})
	if got := r.CandidateBases("a/x.ts", "@/v"); len(got) != 1 {
		t.Fatalf("cycle must not hang, got %v", got)
	}
	if got := r.CandidateBases("c/x.ts", "@/v"); len(got) != 1 || got[0] != "c/src/v" {
		t.Fatalf("missing extends must fall back to own file: %+v", got)
	}
}

func TestJsconfigFallback(t *testing.T) {
	r := newResolver(t, map[string]string{
		"frontend/jsconfig.json": `{"compilerOptions": {"baseUrl": ".", "paths": {"@/*": ["src/*"]}}}`,
	})
	if got := r.CandidateBases("frontend/app.ts", "@/v"); len(got) != 1 || got[0] != "frontend/src/v" {
		t.Fatalf("jsconfig = %+v", got)
	}
}

func TestInvalidate(t *testing.T) {
	r := newResolver(t, map[string]string{
		"frontend/tsconfig.json": `{"compilerOptions": {"baseUrl": ".", "paths": {"@/*": ["src/*"]}}}`,
	})
	if len(r.CandidateBases("frontend/a.ts", "@/v")) != 1 {
		t.Fatal("warm cache first")
	}
	r.Invalidate("frontend/tsconfig.json")
	if len(r.CandidateBases("frontend/a.ts", "@/v")) != 1 {
		t.Fatal("reload after invalidate")
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
