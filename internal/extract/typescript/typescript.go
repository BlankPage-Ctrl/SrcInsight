package typescript

import (
	"path/filepath"
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"
	tstsg "github.com/tree-sitter/tree-sitter-typescript/bindings/go"

	"srcinsight/internal/convention"
	"srcinsight/internal/extract"
	"srcinsight/internal/extract/frontend"
	"srcinsight/internal/model"
)

const defsPattern = `
(function_declaration
  name: (identifier) @name) @def.function

(generator_function_declaration
  name: (identifier) @name) @def.generator

(function_expression
  name: (identifier) @name) @def.function

(class_declaration
  name: (type_identifier) @name) @def.class

(abstract_class_declaration
  name: (type_identifier) @name) @def.class

(interface_declaration
  name: (type_identifier) @name) @def.interface

(type_alias_declaration
  name: (type_identifier) @name) @def.type

(enum_declaration
  name: (identifier) @name) @def.enum

(method_definition
  name: (property_identifier) @name) @def.method

(arrow_function) @arrow
`

const callsPattern = `
(call_expression
  function: (identifier) @call.name) @call

(call_expression
  function: (member_expression
    object: (_) @call.receiver
    property: (property_identifier) @call.name)) @call
`

// JSX usage rides Calls; lowercase tags filter in Go, fragments never match.
const jsxPattern = `
(jsx_opening_element
  name: (_) @jsx.name) @jsx.open

(jsx_self_closing_element
  name: (_) @jsx.name) @jsx.self
`

const importsPattern = `
(import_statement
  source: (string) @import.source) @import
`

// exportsPattern finds re-export statements so the resolver follows barrel files.
const exportsPattern = `
(export_statement) @export
`

// constsPattern finds const declarators and for-in loops for const collection.
const constsPattern = `
(variable_declarator) @const.decl

(for_in_statement) @const.forin
`

// commentPattern finds comment nodes for doc collection.
const commentPattern = `(comment) @doc`

const (
	capRecv     = "call.receiver"
	capCallName = "call.name"
)

// Compiled queries, one set per grammar; TS and TSX each need their own.
type dialectQueries struct {
	defQuery    *sitter.Query
	callQuery   *sitter.Query
	jsxQuery    *sitter.Query
	importQuery *sitter.Query
	exportQuery *sitter.Query
	constQuery  *sitter.Query
	docQuery    *sitter.Query

	// capture id lookups
	defKinds     map[uint32]model.Kind
	callID       uint32
	recvID       uint32
	jsxNameID    uint32
	hasJSX       bool
	constDeclID  uint32
	constForinID uint32
	docID        uint32
}

var (
	// Language is the TSX grammar; prefer LanguageForPath for parsing.
	Language *sitter.Language

	tsQueries  *dialectQueries
	tsxQueries *dialectQueries
)

// LanguageForPath returns the tree-sitter language for a source path.
// Plain .ts files use the TypeScript grammar; TSX files keep TSX.
func LanguageForPath(path string) *sitter.Language {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".ts", ".mts", ".cts":
		return sitter.NewLanguage(tstsg.LanguageTypescript())
	default:
		return sitter.NewLanguage(tstsg.LanguageTSX())
	}
}

// queriesForPath returns the query set matching LanguageForPath.
func queriesForPath(path string) *dialectQueries {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".ts", ".mts", ".cts":
		return tsQueries
	default:
		return tsxQueries
	}
}

func init() {
	Language = sitter.NewLanguage(tstsg.LanguageTSX())
	tsQueries = mustCompileQueries(sitter.NewLanguage(tstsg.LanguageTypescript()))
	tsxQueries = mustCompileQueries(sitter.NewLanguage(tstsg.LanguageTSX()))
}

// mustCompileQueries compiles every pattern against lang.
func mustCompileQueries(lang *sitter.Language) *dialectQueries {
	q := &dialectQueries{}
	var qerr *sitter.QueryError
	if q.defQuery, qerr = sitter.NewQuery(lang, defsPattern); qerr != nil {
		panic(qerr)
	}
	if q.callQuery, qerr = sitter.NewQuery(lang, callsPattern); qerr != nil {
		panic(qerr)
	}
	// The TS grammar has no JSX nodes; the query compiles under TSX only.
	if jq, jerr := sitter.NewQuery(lang, jsxPattern); jerr == nil {
		q.jsxQuery = jq
		if idx, ok := jq.CaptureIndexForName("jsx.name"); ok {
			q.jsxNameID = uint32(idx)
			q.hasJSX = true
		}
	}
	if q.importQuery, qerr = sitter.NewQuery(lang, importsPattern); qerr != nil {
		panic(qerr)
	}
	if q.exportQuery, qerr = sitter.NewQuery(lang, exportsPattern); qerr != nil {
		panic(qerr)
	}
	if q.constQuery, qerr = sitter.NewQuery(lang, constsPattern); qerr != nil {
		panic(qerr)
	}
	if q.docQuery, qerr = sitter.NewQuery(lang, commentPattern); qerr != nil {
		panic(qerr)
	}

	ck := map[string]model.Kind{
		"def.function":  model.KindFunction,
		"def.generator": model.KindGenerator,
		"def.class":     model.KindClassDef,
		"def.interface": model.KindInterface,
		"def.type":      model.KindTypeAlias,
		"def.enum":      model.KindEnum,
		"def.method":    model.KindMethod,
		"arrow":         model.KindArrow,
	}
	q.defKinds = map[uint32]model.Kind{}
	for name, k := range ck {
		if idx, ok := q.defQuery.CaptureIndexForName(name); ok {
			q.defKinds[uint32(idx)] = k
		}
	}
	if idx, ok := q.callQuery.CaptureIndexForName(capCallName); ok {
		q.callID = uint32(idx)
	}
	if idx, ok := q.callQuery.CaptureIndexForName(capRecv); ok {
		q.recvID = uint32(idx)
	}
	if idx, ok := q.constQuery.CaptureIndexForName("const.decl"); ok {
		q.constDeclID = uint32(idx)
	}
	if idx, ok := q.constQuery.CaptureIndexForName("const.forin"); ok {
		q.constForinID = uint32(idx)
	}
	if idx, ok := q.docQuery.CaptureIndexForName("doc"); ok {
		q.docID = uint32(idx)
	}
	return q
}

// fnDef is an extracted raw definition before graph assembly.
type fnDef struct {
	node *sitter.Node
	kind model.Kind
}

// Extract parses the tree and returns the functions and imports it contains.
func Extract(path string, src []byte, tree *sitter.Tree) (extract.FileExtract, error) {
	q := queriesForPath(path)
	root := tree.RootNode()
	comments := collectComments(q, src, root)

	var fe extract.FileExtract
	fe.Path = path
	fe.Language = model.LangTypeScript

	var defs []fnDef
	qc := sitter.NewQueryCursor()
	defer qc.Close()
	matches := qc.Matches(q.defQuery, root, src)
	for m := matches.Next(); m != nil; m = matches.Next() {
		var d fnDef
		for _, c := range m.Captures {
			if k, has := q.defKinds[c.Index]; has {
				d.kind = k
				nc := c.Node
				d.node = &nc
			}
		}
		if d.node != nil {
			defs = append(defs, d)
		}
	}
	// Const entries: walk tree for lexical `const` declarators and for-loop const vars.
	constDefs := collectConstDefs(q, src, root)
	defs = append(defs, constDefs...)

	allCalls := collectAllCalls(q, src, root)
	allCalls = append(allCalls, collectAllJSX(q, src, root)...)

	for _, d := range defs {
		fn := buildFunction(path, src, d.node, d.kind, comments)
		if d.kind == model.KindConst {
			// Const entries use Uses edges from the initializer; no calls or heritage.
			fn.Uses = collectConstUses(src, d.node)
		} else {
			fn.Calls = callsForDef(allCalls, src, d.node)
			fn.Extends, fn.Implements = collectHeritage(src, d.node)
			fn.ParamTypes = collectParamTypes(src, d.node)
			fn.ReturnTypes = collectReturnTypes(src, d.node)
			fn.RecvHints = collectParamBinds(src, d.node)
			fn.Tags = append(fn.Tags, frontend.TagsFor(path, src, d.node, fn.Name, fn.Kind)...)
		}
		fe.Functions = append(fe.Functions, fn)
	}
	fe.RecvBindings = collectRecvBindings(q, src, root)
	fe.Imports = collectImports(q, src, root)
	fe.Imports = append(fe.Imports, collectReExports(q, src, root)...)
	return fe, nil
}

// buildFunction assembles a model.Function from a definition node.
func buildFunction(path string, src []byte, def *sitter.Node, kind model.Kind, comments []comment) model.Function {
	var name, sig string
	if kind == model.KindConst {
		name = constName(src, def)
		sig = renderConstSignature(src, def)
	} else {
		name = functionName(src, def)
		sig = renderSignature(src, def)
		if kind == model.KindMethod && name == "constructor" {
			kind = model.KindConstructor
		}
	}
	// Titles ride anonymous test callbacks only.
	var title string
	if name == "anonymous" {
		title = testTitle(src, path, def)
	}
	fn := model.Function{
		FunctionRef: model.FunctionRef{
			ID:        "",
			Name:      name,
			FilePath:  path,
			LineRange: extract.NodeRange(def),
		},
		Kind:      kind,
		Signature: sig,
		File: model.Container{
			Kind: model.ContainerFile,
			Name: filepath.Base(path),
		},
		Container: enclosingContainer(src, def),
		Doc:       docFor(def, comments),
		TestTitle: title,
		Tags:      collectDecoratorTags(src, def),
	}
	// Bake async into the kind.
	if kind != model.KindConst {
		fn.Kind = asyncKind(sig, fn.Kind)
	}
	return fn
}

// callSite is one file-wide call match with precomputed display name.
type callSite struct {
	start uint
	end   uint
	full  string
	site  model.Range
	node  sitter.Node
}

// collectAllCalls runs the call query once per file in source order.
func collectAllCalls(q *dialectQueries, src []byte, root *sitter.Node) []callSite {
	var out []callSite
	qc := sitter.NewQueryCursor()
	defer qc.Close()
	matches := qc.Matches(q.callQuery, root, src)
	for m := matches.Next(); m != nil; m = matches.Next() {
		var name sitter.Node
		var hasName bool
		var recv string
		var start, end uint
		for _, c := range m.Captures {
			switch c.Index {
			case q.callID:
				name = c.Node
				hasName = true
			case q.recvID:
				recv = extract.Text(src, &c.Node)
			}
		}
		if !hasName {
			continue
		}
		// Byte range of the call name anchors the site to its enclosing def.
		start, end = name.StartByte(), name.EndByte()
		nm := extract.Text(src, &name)
		full := nm
		if recv == "this" {
			full = "this." + nm
		} else if recv != "" {
			full = recv + "." + nm
		}
		out = append(out, callSite{start: start, end: end, full: full, site: extract.SiteRange(&name), node: name})
	}
	return out
}

// Component JSX becomes call sites; lowercase tags are runtime, not nodes.
func collectAllJSX(q *dialectQueries, src []byte, root *sitter.Node) []callSite {
	if q == nil || !q.hasJSX || q.jsxQuery == nil {
		return nil
	}
	var out []callSite
	qc := sitter.NewQueryCursor()
	defer qc.Close()
	matches := qc.Matches(q.jsxQuery, root, src)
	for m := matches.Next(); m != nil; m = matches.Next() {
		var name sitter.Node
		var hasName bool
		for _, c := range m.Captures {
			if c.Index == q.jsxNameID {
				name = c.Node
				hasName = true
			}
		}
		if !hasName {
			continue
		}
		nm := extract.Text(src, &name)
		if nm == "" || convention.IsNativeJSXTag(nm) {
			continue
		}
		out = append(out, callSite{
			start: name.StartByte(),
			end:   name.EndByte(),
			full:  nm,
			site:  extract.SiteRange(&name),
			node:  name,
		})
	}
	return out
}

// callsForDef buckets file-wide call sites into one definition by byte containment.
func callsForDef(all []callSite, src []byte, def *sitter.Node) []model.FunctionRef {
	ds, de := def.StartByte(), def.EndByte()
	var out []model.FunctionRef
	for _, c := range all {
		if c.start < ds || c.end > de {
			continue
		}
		node := c.node
		out = append(out, model.FunctionRef{Name: c.full, SiteRange: c.site, Cond: branchCond(src, &node, def)})
	}
	return out
}

// collectCallsFor finds call sites within one def; file path uses the batched form.
func collectCallsFor(q *dialectQueries, src []byte, def *sitter.Node) []model.FunctionRef {
	var out []model.FunctionRef
	qc := sitter.NewQueryCursor()
	defer qc.Close()
	matches := qc.Matches(q.callQuery, def, src)
	for m := matches.Next(); m != nil; m = matches.Next() {
		var name sitter.Node
		var hasName bool
		var recv string
		for _, c := range m.Captures {
			switch c.Index {
			case q.callID:
				name = c.Node
				hasName = true
			case q.recvID:
				recv = extract.Text(src, &c.Node)
			}
		}
		if !hasName {
			continue
		}
		ref := model.FunctionRef{Name: extract.Text(src, &name), SiteRange: extract.SiteRange(&name), Cond: branchCond(src, &name, def)}
		if recv == "this" {
			ref.Name = "this." + ref.Name
		} else if recv != "" {
			ref.Name = recv + "." + ref.Name
		}
		out = append(out, ref)
	}
	return out
}
