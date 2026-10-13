package goresolve

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

func newGoResolver(t *testing.T, files map[string]string) GoResolver {
	t.Helper()
	dir := t.TempDir()
	for rel, src := range files {
		writeFile(t, dir, rel, src)
	}
	root, err := filesystem.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	return GoResolver{Mods: New(root)}
}

func TestModulePrefixResolve(t *testing.T) {
	g := newGoResolver(t, map[string]string{
		"go.mod":                  "module example.com/demo\n\ngo 1.21\n",
		"pkg/util/util.go":        "package util\n",
		"pkg/util/helper_test.go": "package util\n",
		"cmd/main.go":             "package main\n",
	})
	got := g.CandidatePaths("cmd/main.go", "example.com/demo/pkg/util")
	if !reflect.DeepEqual(got, []string{"pkg/util/util.go"}) {
		t.Fatalf("module candidates = %v (test files excluded)", got)
	}
	if !g.IsInternalSource("cmd/main.go", "example.com/demo/pkg/util") {
		t.Fatal("module import must be internal")
	}
	if g.IsInternalSource("cmd/main.go", "fmt") {
		t.Fatal("stdlib must not be internal")
	}
	if g.IsInternalSource("cmd/main.go", "golang.org/x/tools") {
		t.Fatal("external module must not be internal")
	}
	if got := g.CandidatePaths("cmd/main.go", "fmt"); len(got) != 0 {
		t.Fatalf("stdlib candidates = %v", got)
	}
}

func TestReplaceLocalResolve(t *testing.T) {
	g := newGoResolver(t, map[string]string{
		"go.mod":               "module example.com/demo\n\ngo 1.21\n\nreplace example.com/shared => ./internal/shared\n",
		"internal/shared/s.go": "package shared\n",
		"cmd/main.go":          "package main\n",
	})
	got := g.CandidatePaths("cmd/main.go", "example.com/shared")
	if !reflect.DeepEqual(got, []string{"internal/shared/s.go"}) {
		t.Fatalf("replace candidates = %v", got)
	}
	if !g.IsInternalSource("cmd/main.go", "example.com/shared/sub") {
		t.Fatal("replaced subtree must be internal")
	}
}

func TestVendorNeverResolves(t *testing.T) {
	g := newGoResolver(t, map[string]string{
		"go.mod":            "module example.com/demo\n\ngo 1.21\n",
		"vendor/fmt/fmt.go": "package fmt\n",
		"pkg/util/util.go":  "package util\n",
	})
	if got := g.CandidatePaths("pkg/util/util.go", "example.com/demo/vendor/fmt"); len(got) != 0 {
		t.Fatalf("vendor candidates = %v", got)
	}
}

func TestNoModuleSameFileOnly(t *testing.T) {
	g := newGoResolver(t, map[string]string{
		"main.go": "package main\n",
	})
	if got := g.CandidatePaths("main.go", "example.com/demo/pkg"); len(got) != 0 {
		t.Fatalf("module-less candidates = %v", got)
	}
	// Relative imports stay internal (scope) even without a module.
	if !g.IsInternalSource("a/main.go", "./sub") {
		t.Fatal("relative import must be internal")
	}
}

func TestGoWorkMembers(t *testing.T) {
	g := newGoResolver(t, map[string]string{
		"go.work":        "go 1.21\n\nuse ./api\n",
		"api/go.mod":     "module example.com/api\n\ngo 1.21\n",
		"api/handler.go": "package api\n",
		"web/go.mod":     "module example.com/web\n\ngo 1.21\n",
		"web/main.go":    "package main\n",
	})
	got := g.CandidatePaths("web/main.go", "example.com/api")
	if !reflect.DeepEqual(got, []string{"api/handler.go"}) {
		t.Fatalf("workspace candidates = %v", got)
	}
	if !g.IsInternalSource("web/main.go", "example.com/api") {
		t.Fatal("workspace member must be internal")
	}
}

func TestPackageFilesSorting(t *testing.T) {
	g := newGoResolver(t, map[string]string{
		"go.mod":    "module example.com/demo\n",
		"pkg/b.go":  "package pkg\n",
		"pkg/a.go":  "package pkg\n",
		"pkg/z.txt": "x",
	})
	got := g.Mods.PackageFiles("pkg", false)
	if !reflect.DeepEqual(got, []string{"pkg/a.go", "pkg/b.go"}) {
		t.Fatalf("package files = %v", got)
	}
	got = g.Mods.PackageFiles("pkg", true)
	if len(got) != 2 {
		t.Fatalf("includeTests without tests = %v", got)
	}
}

func TestModuleRootPackageResolves(t *testing.T) {
	// A replace to the module root ("../") must list the ROOT directory:
	// PackageFiles("") is the project root, not nothing.
	g := newGoResolver(t, map[string]string{
		"go.mod":      "module example.com/demo\n\ngo 1.21\n",
		"tea.go":      "package tea\n",
		"sub/go.mod":  "module example.com/sub\n\ngo 1.21\n\nreplace example.com/demo => ../\n",
		"sub/main.go": "package main\n",
	})
	got := g.CandidatePaths("sub/main.go", "example.com/demo")
	if !reflect.DeepEqual(got, []string{"tea.go"}) {
		t.Fatalf("root replace candidates = %v", got)
	}
	if !g.IsInternalSource("sub/main.go", "example.com/demo") {
		t.Fatal("root replace must be internal")
	}
}
