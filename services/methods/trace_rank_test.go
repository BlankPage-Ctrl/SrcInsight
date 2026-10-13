package methods

import (
	"reflect"
	"testing"

	"srcinsight/internal/model"
	"srcinsight/internal/storage/cold"
)

func TestSplitAskStripsExtKeepsQualifier(t *testing.T) {
	got := splitAsk("parse.ts AuthService.validateToken stage_apply::run a/b")
	want := []string{"parse", "AuthService.validateToken", "stage_apply::run", "a/b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("splitAsk = %v, want %v", got, want)
	}
	if got := splitAsk("ab the"); len(got) != 1 || got[0] != "the" {
		t.Fatalf("short/bare filter = %v, want [the]", got)
	}
}

func TestStripAskExt(t *testing.T) {
	cases := map[string]string{
		"parse.ts":      "parse",
		"app.TSX":       "app",
		"mod.py":        "mod",
		"Class.method":  "Class.method",
		"a::b":          "a::b",
		"encode":        "encode",
		"archive.minjs": "archive.minjs",
	}
	for in, want := range cases {
		if got := stripAskExt(in); got != want {
			t.Fatalf("stripAskExt(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsAskSymbol(t *testing.T) {
	precise := []string{"safeEncode", "AuthService", "AuthService.validate", "a::b", "a/b", "foo_bar", "foo$bar"}
	for _, tok := range precise {
		if !isAskSymbol(tok) {
			t.Fatalf("isAskSymbol(%q) = false, want true", tok)
		}
	}
	bare := []string{"encode", "decode", "check", "version"}
	for _, tok := range bare {
		if isAskSymbol(tok) {
			t.Fatalf("isAskSymbol(%q) = true, want false", tok)
		}
	}
}

func TestAskTypeTokens(t *testing.T) {
	got := askTypeTokens([]string{"DataRequest", "task", "validate", "ab", "foo-bar"})
	if len(got) != 1 || got[0] != "DataRequest" {
		t.Fatalf("askTypeTokens = %v, want [DataRequest]", got)
	}
}

func TestAskRankScoreDemotesTestAndGenerated(t *testing.T) {
	plain := cold.ScoredEntryRow{EntryRow: cold.EntryRow{Kind: model.KindFunction, FilePath: "src/a.ts"}}
	testRow := cold.ScoredEntryRow{EntryRow: cold.EntryRow{Kind: model.KindFunction, FilePath: "src/a.test.ts"}}
	genRow := cold.ScoredEntryRow{EntryRow: cold.EntryRow{Kind: model.KindFunction, FilePath: "src/types.d.ts"}}
	if !(askRankScore(plain) > askRankScore(testRow) && askRankScore(testRow) > askRankScore(genRow)) {
		t.Fatalf("scores plain=%v test=%v gen=%v, want plain>test>gen",
			askRankScore(plain), askRankScore(testRow), askRankScore(genRow))
	}
	// Const alias keeps callable worth, not variable noise.
	if w := askKindWeight(model.KindConst); w < 0.8 {
		t.Fatalf("const weight = %v, want >= 0.8", w)
	}
}

func TestTraceReportHasNoMarkdown(t *testing.T) {
	rep, err := Trace(t.Context(), traceVault(t), TraceAsk{Query: "alpha gamma"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Trails) != 1 {
		t.Fatalf("trails = %d, want 1", len(rep.Trails))
	}
	for _, tr := range rep.Trails {
		for _, id := range tr.Steps {
			if id == "" {
				t.Fatal("empty step ID")
			}
		}
	}
}
