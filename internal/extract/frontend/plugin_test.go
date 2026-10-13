package frontend

import (
	"testing"

	sitter "github.com/tree-sitter/go-tree-sitter"
	tstsg "github.com/tree-sitter/tree-sitter-typescript/bindings/go"

	"srcinsight/internal/extract"
	"srcinsight/internal/model"
)

func TestForFileRoutes(t *testing.T) {
	pl, ok := ForFile("src/Feed.tsx")
	if !ok || pl.Name() != "react" {
		t.Errorf("ForFile(Feed.tsx) = %v, %v; want react", pl, ok)
	}
	pl, ok = ForFile("src/Card.vue")
	if !ok || pl.Name() != "vue" {
		t.Errorf("ForFile(Card.vue) = %v, %v; want vue", pl, ok)
	}
	if _, ok := ForFile("src/x.ts"); ok {
		t.Errorf("ForFile(x.ts) matched, want none")
	}
	if _, ok := ForFile("src/x.py"); ok {
		t.Errorf("ForFile(x.py) matched, want none")
	}
}

func TestCapabilitiesSplit(t *testing.T) {
	pl, _ := ForFile("src/Feed.tsx")
	if _, ok := pl.(Tagger); !ok {
		t.Errorf("react plugin must tag")
	}
	if _, ok := pl.(Templater); ok {
		t.Errorf("react plugin must not read templates (JSX rides the extractor)")
	}
	pl, _ = ForFile("src/Card.vue")
	tmpl, ok := pl.(Templater)
	if !ok {
		t.Fatalf("vue plugin must read templates")
	}
	refs := tmpl.TemplateRefs("src/Card.vue", []byte("<template><button @click=\"save\">x</button></template>\n"))
	if len(refs) != 1 || refs[0].Name != "save" || refs[0].SiteRange.StartLine != 1 {
		t.Errorf("TemplateRefs = %+v, want [save @1]", refs)
	}
	if _, ok := pl.(Tagger); ok {
		t.Errorf("vue plugin must not tag definitions (root carries the tag)")
	}
}

func TestTagsForFansOut(t *testing.T) {
	src := "export function Feed() { return (<div><UserCard /></div>); }\n"
	parser := sitter.NewParser()
	defer parser.Close()
	if err := parser.SetLanguage(sitter.NewLanguage(tstsg.LanguageTSX())); err != nil {
		t.Fatal(err)
	}
	tree := parser.Parse([]byte(src), nil)
	if tree == nil {
		t.Fatal("parse failed")
	}
	defer tree.Close()
	def := findFunc([]byte(src), tree.RootNode(), "Feed")
	if def == nil {
		t.Fatal("Feed def missing")
	}
	tags := TagsFor("src/Feed.tsx", []byte(src), def, "Feed", model.KindFunction)
	if len(tags) != 1 || tags[0] != "react:component" {
		t.Errorf("TagsFor = %v, want [react:component]", tags)
	}
	if tags := TagsFor("src/x.ts", []byte(src), def, "Feed", model.KindFunction); len(tags) != 0 {
		t.Errorf("TagsFor(x.ts) = %v, want none", tags)
	}
}

func findFunc(src []byte, root *sitter.Node, name string) *sitter.Node {
	var out *sitter.Node
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if out != nil {
			return
		}
		if n.Kind() == "function_declaration" {
			if id := n.ChildByFieldName("name"); id != nil && extract.Text(src, id) == name {
				out = n
				return
			}
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			walk(n.NamedChild(uint(i)))
		}
	}
	walk(root)
	return out
}
