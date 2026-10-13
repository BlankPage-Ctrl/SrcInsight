// Package golang extracts functions, methods, types, calls, and imports from Go sources.
// Structs map to classes, embedding maps to extends; literals and const/var nodes stay out.
package golang

import (
	"path/filepath"

	sitter "github.com/tree-sitter/go-tree-sitter"
	gots "github.com/tree-sitter/tree-sitter-go/bindings/go"

	"srcinsight/internal/extract"
	"srcinsight/internal/model"
)

const defsPattern = `
(function_declaration
  name: (identifier) @name) @def.function

(method_declaration
  name: (field_identifier) @name) @def.method

(type_spec
  name: (type_identifier) @name) @def.type

(type_alias
  name: (type_identifier) @name) @def.type
`

const callsPattern = `
(call_expression
  function: (identifier) @call.name) @call

(call_expression
  function: (selector_expression
    operand: (_) @call.receiver
    field: (field_identifier) @call.name)) @call
`

// importsPattern matches every import_spec; missing name means plain import.
const importsPattern = `(import_spec) @imp`

// commentPattern finds comment nodes for doc capture.
const commentPattern = `(comment) @doc`

const (
	capCallName = "call.name"
	capRecv     = "call.receiver"
)

var (
	Language  *sitter.Language
	defQuery  *sitter.Query
	callQuery *sitter.Query
	docQuery  *sitter.Query
	impQuery  *sitter.Query

	defKinds map[uint32]model.Kind
	callID   uint32
	recvID   uint32
	impID    uint32
	docID    uint32
)

func init() {
	Language = sitter.NewLanguage(gots.Language())
	var qerr *sitter.QueryError
	if defQuery, qerr = sitter.NewQuery(Language, defsPattern); qerr != nil {
		panic(qerr)
	}
	if callQuery, qerr = sitter.NewQuery(Language, callsPattern); qerr != nil {
		panic(qerr)
	}
	if docQuery, qerr = sitter.NewQuery(Language, commentPattern); qerr != nil {
		panic(qerr)
	}
	if impQuery, qerr = sitter.NewQuery(Language, importsPattern); qerr != nil {
		panic(qerr)
	}

	// type_alias shares def.type with type_spec; buildFunction refines by node kind.
	ck := map[string]model.Kind{
		"def.function": model.KindFunction,
		"def.method":   model.KindMethod,
		"def.type":     model.KindTypeAlias, // refined per node below
	}
	defKinds = map[uint32]model.Kind{}
	for name, k := range ck {
		if idx, ok := defQuery.CaptureIndexForName(name); ok {
			defKinds[uint32(idx)] = k
		}
	}
	if idx, ok := callQuery.CaptureIndexForName(capCallName); ok {
		callID = uint32(idx)
	}
	if idx, ok := callQuery.CaptureIndexForName(capRecv); ok {
		recvID = uint32(idx)
	}
	if idx, ok := impQuery.CaptureIndexForName("imp"); ok {
		impID = uint32(idx)
	}
	if idx, ok := docQuery.CaptureIndexForName("doc"); ok {
		docID = uint32(idx)
	}
}

// fnDef is a raw definition before graph assembly.
type fnDef struct {
	node *sitter.Node
	kind model.Kind
}

// Extract parses the tree and returns functions and imports.
func Extract(path string, src []byte, tree *sitter.Tree) (extract.FileExtract, error) {
	root := tree.RootNode()
	comments := collectComments(src, root)

	var fe extract.FileExtract
	fe.Path = path
	fe.Language = model.LangGo

	var defs []fnDef
	qc := sitter.NewQueryCursor()
	defer qc.Close()
	matches := qc.Matches(defQuery, root, src)
	for m := matches.Next(); m != nil; m = matches.Next() {
		var d fnDef
		for _, c := range m.Captures {
			if k, has := defKinds[c.Index]; has {
				d.kind = k
				nc := c.Node
				d.node = &nc
			}
		}
		if d.node != nil {
			defs = append(defs, d)
		}
	}

	allCalls := collectAllCalls(src, root)

	for _, d := range defs {
		fn := buildFunction(path, src, d.node, d.kind, comments)
		if d.kind == model.KindMethod || fn.Kind == model.KindMethod {
			fn.Calls = callsForDef(allCalls, src, d.node, ownReceiver(src, d.node))
		} else {
			fn.Calls = callsForDef(allCalls, src, d.node, "")
		}
		switch fn.Kind {
		case model.KindFunction, model.KindMethod:
			typeParams := typeParamNames(src, d.node)
			fn.ParamTypes = collectParamTypes(src, d.node, typeParams)
			fn.ReturnTypes = collectReturnTypes(src, d.node, typeParams)
			fn.RecvHints = collectFuncBinds(src, d.node, typeParams)
		case model.KindClassDef, model.KindInterface:
			fn.Extends = collectEmbeds(src, d.node)
		}
		fe.Functions = append(fe.Functions, fn)
	}
	fe.Imports = collectImports(src, root)
	fe.RecvBindings = collectRecvBindings(src, root)
	return fe, nil
}

func buildFunction(path string, src []byte, def *sitter.Node, kind model.Kind, comments []comment) model.Function {
	name := functionName(src, def)
	var container *model.Container
	if kind == model.KindMethod {
		if _, typ := receiverInfo(src, def); typ != "" {
			container = &model.Container{Kind: model.ContainerClass, Name: typ}
		}
	} else if kind == model.KindTypeAlias && def.Kind() == "type_spec" {
		// Refine the shared def.type capture by inner type.
		kind = specKind(src, def)
	}
	fn := model.Function{
		FunctionRef: model.FunctionRef{
			ID:        "",
			Name:      name,
			FilePath:  path,
			LineRange: extract.NodeRange(def),
		},
		Kind:      kind,
		Signature: renderSignature(src, def),
		File: model.Container{
			Kind: model.ContainerFile,
			Name: filepath.Base(path),
		},
		Container: container,
		Doc:       docFor(def, comments),
	}
	return fn
}

func functionName(src []byte, def *sitter.Node) string {
	if n := def.ChildByFieldName("name"); n != nil {
		return extract.Text(src, n)
	}
	return "anonymous"
}

// specKind refines type_spec by inner type: struct maps to class, interface to interface.
func specKind(src []byte, def *sitter.Node) model.Kind {
	t := def.ChildByFieldName("type")
	if t == nil {
		return model.KindTypeAlias
	}
	switch t.Kind() {
	case "struct_type":
		return model.KindClassDef
	case "interface_type":
		return model.KindInterface
	default:
		return model.KindTypeAlias
	}
}

// callSite is one file-wide call match; display name is precomputed for bucketing.
type callSite struct {
	start uint
	end   uint
	recv  string
	name  string
	site  model.Range
	node  sitter.Node
}

// collectAllCalls runs the call query once per file in source order.
func collectAllCalls(src []byte, root *sitter.Node) []callSite {
	var out []callSite
	qc := sitter.NewQueryCursor()
	defer qc.Close()
	matches := qc.Matches(callQuery, root, src)
	for m := matches.Next(); m != nil; m = matches.Next() {
		var name sitter.Node
		var hasName bool
		var recv string
		for _, c := range m.Captures {
			switch c.Index {
			case callID:
				name = c.Node
				hasName = true
			case recvID:
				recv = extract.Text(src, &c.Node)
			}
		}
		if !hasName {
			continue
		}
		out = append(out, callSite{
			start: name.StartByte(),
			end:   name.EndByte(),
			recv:  recv,
			name:  extract.Text(src, &name),
			site:  extract.SiteRange(&name),
			node:  name,
		})
	}
	return out
}

// callsForDef buckets file-wide calls into one def by byte containment.
// Calls on the own receiver rewrite to this.Method for shared resolution.
func callsForDef(all []callSite, src []byte, def *sitter.Node, ownRecv string) []model.FunctionRef {
	ds, de := def.StartByte(), def.EndByte()
	var out []model.FunctionRef
	for _, c := range all {
		if c.start < ds || c.end > de {
			continue
		}
		full := c.name
		switch {
		case c.recv == "":
			// plain call, unchanged
		case ownRecv != "" && c.recv == ownRecv:
			full = "this." + c.name
		default:
			full = c.recv + "." + c.name
		}
		node := c.node
		out = append(out, model.FunctionRef{Name: full, SiteRange: c.site, Cond: branchCond(src, &node, def)})
	}
	return out
}
