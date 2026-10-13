package internal_test

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// moduleOf maps a path relative to internal/ to its owning module. The whole
// extract tree (extract, extract/python, extract/typescript) counts as one
// module, so intra-extractor imports are allowed; everything else is grouped
// by top-level directory (graph, indexer, query, resolve, storage, ...).
func moduleOf(rel string) string {
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) == 0 {
		return ""
	}
	if parts[0] == "extract" {
		return "extract"
	}
	return parts[0]
}

// TestSiblingIndependence enforces the architecture rule: sibling modules
// under internal/ must not depend on each other; composition happens at a
// higher layer (services/, cmd/). Every internal package may only import
// the shared leaves internal/model and internal/convention (plus stdlib
// and external libraries). Convention itself is leaf-only (stdlib + model),
// so sharing it introduces no coupling between siblings.
//
// Test files (*_test.go) are exempt so integration coverage can wire modules
// together; the rule applies to production code.
func TestSiblingIndependence(t *testing.T) {
	fset := token.NewFileSet()
	var checked int
	err := filepath.Walk(".", func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(".", path)
		importer := moduleOf(rel)
		if importer == "" {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		checked++
		for _, imp := range f.Imports {
			p, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				continue
			}
			if !strings.HasPrefix(p, "srcinsight/internal/") {
				continue
			}
			imported := moduleOf(strings.TrimPrefix(p, "srcinsight/internal/"))
			if imported == "" || imported == "model" || imported == "convention" || importer == imported {
				continue
			}
			t.Errorf("%s (module %q) must not import sibling module %q (%s); compose in services/ instead",
				rel, importer, imported, p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked == 0 {
		t.Fatal("no files checked; guard is misconfigured")
	}
	t.Logf("checked %d files", checked)
}
