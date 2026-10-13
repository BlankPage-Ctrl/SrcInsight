package typescript

import (
	"testing"

	sitter "github.com/tree-sitter/go-tree-sitter"
)

func extractTSTest(t *testing.T, path, src string) []struct {
	Name  string
	Title string
} {
	t.Helper()
	parser := sitter.NewParser()
	defer parser.Close()
	setPathLanguage(t, parser, path)
	tree := parser.Parse([]byte(src), nil)
	if tree == nil {
		t.Fatal("parse failed")
	}
	defer tree.Close()
	fe, err := Extract(path, []byte(src), tree)
	if err != nil {
		t.Fatal(err)
	}
	var out []struct {
		Name  string
		Title string
	}
	for _, f := range fe.Functions {
		out = append(out, struct {
			Name  string
			Title string
		}{f.Name, f.TestTitle})
	}
	return out
}

func TestTestTitle(t *testing.T) {
	src := `
import { test, it } from "vitest";
import { parseAsync } from "./parse";
test("string async parse", async () => {
  await parseAsync("x");
});
it("works", async () => {
  parseAsync("y");
});
test.skip("skipped", () => {
  parseAsync("z");
});
const helper = () => parseAsync("w");
setTimeout(() => parseAsync("v"), 10);
test(` + "`clean ${'x'}`" + `, () => {
  parseAsync("u");
});
`
	got := extractTSTest(t, "src/a.test.ts", src)
	byName := map[string][]string{}
	for _, f := range got {
		byName[f.Name] = append(byName[f.Name], f.Title)
	}
	titles := byName["anonymous"]
	want := map[string]bool{"string async parse": true, "works": true, "skipped": true}
	for _, title := range titles {
		if title == "" {
			continue
		}
		if !want[title] {
			t.Fatalf("unexpected title %q in %v", title, titles)
		}
		delete(want, title)
	}
	for title := range want {
		t.Fatalf("missing title %q; got %v", title, titles)
	}
	// @ The const arrow and the nested timeout callback stay untitled.
	for _, title := range byName["helper"] {
		if title != "" {
			t.Fatalf("helper title = %q, want empty", title)
		}
	}
}

func TestTestTitleGatedToTestFiles(t *testing.T) {
	src := `
test("prod helper", async () => {
  await parseAsync("x");
});
`
	got := extractTSTest(t, "src/x.ts", src)
	for _, f := range got {
		if f.Title != "" {
			t.Fatalf("non-test file title = %q, want empty", f.Title)
		}
	}
}
