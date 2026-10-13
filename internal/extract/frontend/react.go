package frontend

import (
	sitter "github.com/tree-sitter/go-tree-sitter"
	tstsg "github.com/tree-sitter/tree-sitter-typescript/bindings/go"

	"srcinsight/internal/convention"
	"srcinsight/internal/extract"
	"srcinsight/internal/model"
)

// reactTagger marks components and hooks. JSX usage itself rides the
// TypeScript extractor, so only tagging lives here.
type reactTagger struct{}

func init() { Register(reactTagger{}) }

func (reactTagger) Name() string { return "react" }

func (reactTagger) Match(path string) bool { return convention.IsReactFile(path) }

// Own TSX query: the twin in the typescript package can't be shared
// without an import cycle.
const reactJSXPattern = `
(jsx_opening_element
  name: (_) @jsx.name) @jsx.open

(jsx_self_closing_element
  name: (_) @jsx.name) @jsx.self
`

var (
	reactTSXLang  = sitter.NewLanguage(tstsg.LanguageTSX())
	reactJSXQuery = mustCompileJSX()
	reactJSXName  uint32
)

func mustCompileJSX() *sitter.Query {
	q, qerr := sitter.NewQuery(reactTSXLang, reactJSXPattern)
	if qerr != nil {
		panic(qerr)
	}
	if idx, ok := q.CaptureIndexForName("jsx.name"); ok {
		reactJSXName = uint32(idx)
	}
	return q
}

// Components need PascalCase plus rendered JSX; anonymous arrows stay untagged.
func (reactTagger) TagsFor(path string, src []byte, def *sitter.Node, name string, kind model.Kind) []string {
	if !convention.IsReactFile(path) || name == "" || name == "anonymous" {
		return nil
	}
	if convention.IsHookName(name) {
		return []string{"hook"}
	}
	if !convention.IsComponentName(name) {
		return nil
	}
	switch kind {
	case model.KindFunction, model.KindAsyncFunction,
		model.KindArrow, model.KindAsyncArrow,
		model.KindMethod, model.KindAsyncMethod:
	default:
		return nil
	}
	if rendersJSX(src, def) {
		if fw, ok := convention.Registered("react"); ok && len(fw.ComponentTags) > 0 {
			return append([]string(nil), fw.ComponentTags...)
		}
		return []string{"react:component"}
	}
	return nil
}

func rendersJSX(src []byte, def *sitter.Node) bool {
	if def == nil || reactJSXQuery == nil {
		return false
	}
	qc := sitter.NewQueryCursor()
	defer qc.Close()
	matches := qc.Matches(reactJSXQuery, def, src)
	for m := matches.Next(); m != nil; m = matches.Next() {
		for _, c := range m.Captures {
			if c.Index == reactJSXName {
				if nm := extract.Text(src, &c.Node); nm != "" && !convention.IsNativeJSXTag(nm) {
					return true
				}
			}
		}
	}
	return false
}
