package typescript

import (
	"testing"

	sitter "github.com/tree-sitter/go-tree-sitter"

	"srcinsight/internal/model"
)

func extractWithLang(t *testing.T, path, src string) []model.Function {
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
	return fe.Functions
}

func callsOf(funcs []model.Function, name string) []string {
	for _, f := range funcs {
		if f.Name == name {
			var out []string
			for _, c := range f.Calls {
				out = append(out, c.Name)
			}
			return out
		}
	}
	return nil
}

func tagsOf(funcs []model.Function, name string) []string {
	for _, f := range funcs {
		if f.Name == name {
			return f.Tags
		}
	}
	return nil
}

func hasCall(calls []string, want string) bool {
	for _, c := range calls {
		if c == want {
			return true
		}
	}
	return false
}

func hasTag(tags []string, want string) bool {
	for _, g := range tags {
		if g == want {
			return true
		}
	}
	return false
}

const jsxFeedSrc = `import UserCard from './UserCard';
import { useState } from 'react';
export function Feed() {
  const [x, setX] = useState(1);
  return (<div><UserCard title={x} /><Foo.Bar /><span/></div>);
}

export function useFetch(url: string) {
  const [data, setData] = useState(null);
  return data;
}

export function helper(x: number): number {
  return x + 1;
}
`

func TestExtractJSXComponent(t *testing.T) {
	for _, path := range []string{"src/Feed.tsx", "src/Feed.jsx", "src/Feed.js"} {
		funcs := extractWithLang(t, path, jsxFeedSrc)

		if tags := tagsOf(funcs, "Feed"); !hasTag(tags, "react:component") {
			t.Errorf("%s: Feed tags = %v, want react:component", path, tags)
		}
		calls := callsOf(funcs, "Feed")
		for _, want := range []string{"useState", "UserCard", "Foo.Bar"} {
			if !hasCall(calls, want) {
				t.Errorf("%s: Feed calls = %v, want %q", path, calls, want)
			}
		}
		for _, native := range []string{"div", "span"} {
			if hasCall(calls, native) {
				t.Errorf("%s: Feed calls = %v, native tag %q must be skipped", path, calls, native)
			}
		}

		if tags := tagsOf(funcs, "useFetch"); !hasTag(tags, "hook") {
			t.Errorf("%s: useFetch tags = %v, want hook", path, tags)
		}
		if tags := tagsOf(funcs, "helper"); hasTag(tags, "react:component") {
			t.Errorf("%s: helper tags = %v, plain function must stay untagged", path, tags)
		}
	}
}

func TestExtractTSHasNoJSX(t *testing.T) {
	if queriesForPath("src/x.ts").hasJSX {
		t.Errorf("ts queries must not carry a JSX query (grammar has no JSX nodes)")
	}
	if !queriesForPath("src/x.tsx").hasJSX {
		t.Errorf("tsx queries must carry a JSX query")
	}
}
