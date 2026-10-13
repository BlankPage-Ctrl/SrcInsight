package indexer

import (
	"testing"

	"srcinsight/internal/model"
)

func TestIndexAssignsStableIDs(t *testing.T) {
	extracts := []model.FileExtract{
		{
			Path:     "src/auth/validator.ts",
			Language: model.LangTypeScript,
			Functions: []model.Function{
				{
					FunctionRef: model.FunctionRef{Name: "validateToken", FilePath: "src/auth/validator.ts", LineRange: model.Range{StartLine: 10, EndLine: 20}},
					Kind:        model.KindFunction,
				},
			},
		},
	}
	res := Index(extracts)
	if res.FilesScanned != 1 {
		t.Fatalf("FilesScanned = %d, want 1", res.FilesScanned)
	}
	if res.FunctionsIndexed != 1 {
		t.Fatalf("FunctionsIndexed = %d, want 1", res.FunctionsIndexed)
	}
	want := model.EntryID("typescript:src/auth/validator.ts:function:validateToken:1")
	if res.Functions[0].ID != want {
		t.Fatalf("ID = %q, want %q", res.Functions[0].ID, want)
	}
}

func TestIndexOrdinalDisambiguatesTwins(t *testing.T) {
	mk := func(name string, kind model.Kind, start int) model.Function {
		return model.Function{
			FunctionRef: model.FunctionRef{Name: name, FilePath: "a.ts", LineRange: model.Range{StartLine: start, EndLine: start + 2}},
			Kind:        kind,
		}
	}
	extracts := []model.FileExtract{
		{
			Path:     "a.ts",
			Language: model.LangTypeScript,
			Functions: []model.Function{
				mk("foo", model.KindFunction, 30),
				mk("foo", model.KindFunction, 10),
				mk("anonymous", model.KindArrow, 50),
				mk("anonymous", model.KindArrow, 40),
			},
		},
	}
	res := Index(extracts)
	// Same-name definitions get source-order ordinals regardless of input
	// order: the def at line 10 is :1 even though it was emitted second.
	want := []model.EntryID{
		"typescript:a.ts:function:foo:1",
		"typescript:a.ts:function:foo:2",
		"typescript:a.ts:arrow:anonymous:1",
		"typescript:a.ts:arrow:anonymous:2",
	}
	// Index preserves source order after the stable sort.
	if len(res.Functions) != len(want) {
		t.Fatalf("got %d functions, want %d", len(res.Functions), len(want))
	}
	for i, f := range res.Functions {
		if f.ID != want[i] {
			t.Fatalf("functions[%d].ID = %q, want %q", i, f.ID, want[i])
		}
	}
}

func TestIndexIDsImmuneToLineShifts(t *testing.T) {
	mk := func(start int) []model.FileExtract {
		return []model.FileExtract{
			{
				Path:     "a.ts",
				Language: model.LangTypeScript,
				Functions: []model.Function{
					{
						FunctionRef: model.FunctionRef{Name: "foo", FilePath: "a.ts", LineRange: model.Range{StartLine: start, EndLine: start + 5}},
						Kind:        model.KindFunction,
					},
				},
			},
		}
	}
	before := Index(mk(10)).Functions[0].ID
	after := Index(mk(25)).Functions[0].ID // same function, shifted down 15 lines
	if before != after {
		t.Fatalf("ID changed across line shift: %q vs %q", before, after)
	}
}
func TestIndexBackfillsContainers(t *testing.T) {
	extracts := []model.FileExtract{
		{
			Path:     "src/auth/validator.ts",
			Language: model.LangTypeScript,
			Functions: []model.Function{
				{
					FunctionRef: model.FunctionRef{Name: "AuthService", FilePath: "src/auth/validator.ts", LineRange: model.Range{StartLine: 2, EndLine: 30}},
					Kind:        model.KindClassDef,
				},
				{
					FunctionRef: model.FunctionRef{Name: "validateToken", FilePath: "src/auth/validator.ts", LineRange: model.Range{StartLine: 5, EndLine: 10}},
					Kind:        model.KindMethod,
					Container:   &model.Container{Kind: model.ContainerClass, Name: "AuthService"},
				},
				{
					FunctionRef: model.FunctionRef{Name: "requireUser", FilePath: "src/auth/validator.ts", LineRange: model.Range{StartLine: 40, EndLine: 45}},
					Kind:        model.KindFunction,
				},
			},
		},
	}
	res := Index(extracts)
	byName := make(map[string]*model.Function)
	for _, f := range res.Functions {
		byName[f.Name] = f
	}

	wantClassID := model.EntryID("typescript:src/auth/validator.ts:class:AuthService:1")
	if byName["AuthService"].ID != wantClassID {
		t.Fatalf("class ID = %q, want %q", byName["AuthService"].ID, wantClassID)
	}
	vt := byName["validateToken"]
	if vt.Container == nil || vt.Container.ID != wantClassID {
		t.Fatalf("validateToken container = %+v, want ID %s", vt.Container, wantClassID)
	}
	if vt.File.ID != "file:src/auth/validator.ts" || vt.File.Name != "validator.ts" {
		t.Fatalf("validateToken file = %+v", vt.File)
	}
	if ru := byName["requireUser"]; ru.Container != nil {
		t.Fatalf("requireUser container = %+v, want nil", ru.Container)
	}
}

func TestIndexCollectsImports(t *testing.T) {
	extracts := []model.FileExtract{
		{
			Path:      "services/caller.ts",
			Language:  model.LangTypeScript,
			Functions: []model.Function{},
			Imports:   []model.ImportStmt{{LocalName: "foo", Source: "./target", Kind: "named"}},
		},
	}
	res := Index(extracts)
	got := res.Imports["services/caller.ts"]
	if len(got) != 1 || got[0].LocalName != "foo" {
		t.Fatalf("imports = %+v", res.Imports)
	}
}
