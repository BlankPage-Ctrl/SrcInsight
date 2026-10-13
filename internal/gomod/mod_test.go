package gomod

import (
	"reflect"
	"testing"
)

func TestParseModule(t *testing.T) {
	f := Parse([]byte("module example.com/demo\n\ngo 1.21\n"))
	if f.Module != "example.com/demo" {
		t.Errorf("module = %q", f.Module)
	}
	if len(f.Replaces) != 0 {
		t.Errorf("replaces = %+v", f.Replaces)
	}
}

func TestParseReplaceForms(t *testing.T) {
	f := Parse([]byte(`module example.com/demo

require example.com/other v1.0.0

replace example.com/other => ./local/other

replace (
	example.com/gen => ../gen
	example.com/pinned v1.2.3 => example.com/fork v1.2.4
)
`))
	if len(f.Replaces) != 3 {
		t.Fatalf("replaces = %+v", f.Replaces)
	}
	if !f.Replaces[0].Local || f.Replaces[0].New != "./local/other" {
		t.Errorf("single replace = %+v", f.Replaces[0])
	}
	if f.Replaces[2].Local {
		t.Errorf("module→module replace must not be local: %+v", f.Replaces[2])
	}
}

func TestReplaceDir(t *testing.T) {
	f := &File{
		Module: "example.com/demo",
		Replaces: []Replacement{
			{Old: "example.com/demo", New: "./", Local: true},
			{Old: "example.com/gen", New: "../gen", Local: true},
			{Old: "example.com/ext", New: "example.com/fork", Local: false},
		},
	}
	cases := map[string]string{
		"example.com/gen/sub": "../gen/sub",
		"example.com/gen":     "../gen",
		"example.com/ext/sub": "",
		"example.com/other/x": "",
		"golang.org/x/tools":  "",
	}
	for src, want := range cases {
		if got := f.ReplaceDir(src); got != want {
			t.Errorf("ReplaceDir(%q) = %q, want %q", src, got, want)
		}
	}
}

func TestModuleDir(t *testing.T) {
	f := &File{Module: "example.com/demo"}
	cases := map[string]string{
		"example.com/demo":     "",
		"example.com/demo/pkg": "pkg",
		"example.com/demo/a/b": "a/b",
		"example.com/other":    "",
		"example.com/demo2":    "",
		"fmt":                  "",
	}
	for src, wantRel := range cases {
		rel, ok := f.ModuleDir(src)
		wantOK := src == "example.com/demo" || len(src) > len("example.com/demo/") && src[:len("example.com/demo/")] == "example.com/demo/"
		if ok != wantOK || (ok && rel != wantRel) {
			t.Errorf("ModuleDir(%q) = (%q, %v)", src, rel, ok)
		}
	}
}

func TestParseWork(t *testing.T) {
	w := ParseWork([]byte("go 1.21\n\nuse (\n\t./api\n\t./web\n)\n"))
	if !reflect.DeepEqual(w.Uses, []string{"./api", "./web"}) {
		t.Errorf("uses = %+v", w.Uses)
	}
	w2 := ParseWork([]byte("go 1.21\nuse ./only\n"))
	if !reflect.DeepEqual(w2.Uses, []string{"./only"}) {
		t.Errorf("single use = %+v", w2.Uses)
	}
}
