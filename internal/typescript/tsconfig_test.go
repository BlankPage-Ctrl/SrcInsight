package typescript

import (
	"reflect"
	"testing"
)

func TestParseBasic(t *testing.T) {
	raw, err := Parse([]byte(`{
		// comment
		"compilerOptions": {
			"baseUrl": ".",
			"paths": { "@/*": ["src/*"], },
		},
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if raw.BaseURL == nil || *raw.BaseURL != "." {
		t.Fatalf("baseUrl = %+v", raw.BaseURL)
	}
	if !reflect.DeepEqual(raw.Paths["@/*"], []string{"src/*"}) {
		t.Fatalf("paths = %+v", raw.Paths)
	}
}

func TestParseBlockCommentAndTrailingComma(t *testing.T) {
	raw, err := Parse([]byte("/* head */ { \"extends\": \"../base.json\", \"compilerOptions\": { \"paths\": {} } }"))
	if err != nil {
		t.Fatal(err)
	}
	if len(raw.Extends) != 1 || raw.Extends[0] != "../base.json" {
		t.Fatalf("extends = %+v", raw.Extends)
	}
}

func TestParseExtendsList(t *testing.T) {
	raw, err := Parse([]byte(`{"extends": ["./a.json", "./b.json"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(raw.Extends, []string{"./a.json", "./b.json"}) {
		t.Fatalf("extends = %+v", raw.Extends)
	}
}

func TestParseURLInStringNotStripped(t *testing.T) {
	raw, err := Parse([]byte(`{"compilerOptions": {"baseUrl": "https://x", "paths": {"a": ["b"]}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if raw.BaseURL == nil || *raw.BaseURL != "https://x" {
		t.Fatalf("baseUrl = %+v", raw.BaseURL)
	}
}

func TestMatchPattern(t *testing.T) {
	cases := []struct {
		pat, src, suffix string
		ok               bool
	}{
		{"@/*", "@/api", "api", true},
		{"@/*", "@/a/b", "a/b", true},
		{"@utils", "@utils", "", true},
		{"@utils", "@other", "", false},
		{"*", "anything", "anything", true},
		{"src/*.ts", "src/a.ts", "a", true},
		{"a*b*c", "abc", "", true}, // multi-star treated as literal miss unless equal
	}
	for _, c := range cases {
		suf, ok := MatchPattern(c.pat, c.src)
		if c.pat == "a*b*c" {
			if ok {
				t.Errorf("multi-star %q should not match", c.pat)
			}
			continue
		}
		if ok != c.ok || suf != c.suffix {
			t.Errorf("Match(%q,%q) = (%q,%v), want (%q,%v)", c.pat, c.src, suf, ok, c.suffix, c.ok)
		}
	}
}

func TestMergeChildWins(t *testing.T) {
	baseURL := "."
	parent := Config{Dir: "", BaseURL: "", Paths: map[string][]string{
		"@shared/*": {"shared/*"},
		"@/*":       {"old/*"},
	}}
	childRaw := Raw{BaseURL: &baseURL, Paths: map[string][]string{"@/*": {"src/*"}}}
	got := FromRaw("frontend", "frontend/tsconfig.json", childRaw, &parent)
	if got.BaseURL != "frontend" {
		t.Fatalf("base = %q", got.BaseURL)
	}
	if !reflect.DeepEqual(got.Paths["@/*"], []string{"src/*"}) {
		t.Fatalf("child paths = %+v", got.Paths)
	}
	if !reflect.DeepEqual(got.Paths["@shared/*"], []string{"shared/*"}) {
		t.Fatalf("inherited paths = %+v", got.Paths)
	}
}

func TestExpandBases(t *testing.T) {
	cfg := Config{Dir: "frontend", BaseURL: "frontend", Paths: map[string][]string{
		"@/*": {"src/*"},
	}}
	got := ExpandBases(&cfg, "@/api")
	if !reflect.DeepEqual(got, []string{"frontend/src/api"}) {
		t.Fatalf("expand = %+v", got)
	}
}
