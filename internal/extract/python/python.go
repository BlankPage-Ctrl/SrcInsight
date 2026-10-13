// Package python extracts functions, calls, signatures, docs, and imports from Python sources.
package python

import (
	"path/filepath"
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"
	py "github.com/tree-sitter/tree-sitter-python/bindings/go"

	"srcinsight/internal/extract"
	"srcinsight/internal/model"
)

const defsPattern = `
(function_definition
  name: (identifier) @name) @def.function

(class_definition
  name: (identifier) @name) @def.class
`

const callsPattern = `
(call
  function: (identifier) @call.name) @call

(call
  function: (attribute
    object: (_) @call.receiver
    attribute: (identifier) @call.name)) @call
`

// commentPattern finds comment nodes for doc collection.
const commentPattern = `(comment) @doc`

// importsPattern finds import statements for import collection.
const importsPattern = `
(import_statement) @imp.stmt

(import_from_statement) @imp.from
`

const (
	capName     = "name"
	capRecv     = "call.receiver"
	capCallName = "call.name"
)

var (
	Language    *sitter.Language
	defQuery    *sitter.Query
	callQuery   *sitter.Query
	yieldQuery  *sitter.Query
	docQuery    *sitter.Query
	importQuery *sitter.Query

	defKinds map[uint32]model.Kind
	nameID   uint32
	callID   uint32
	recvID   uint32

	docID     uint32
	impStmtID uint32
	impFromID uint32
)

func init() {
	Language = sitter.NewLanguage(py.Language())
	var qerr *sitter.QueryError
	if defQuery, qerr = sitter.NewQuery(Language, defsPattern); qerr != nil {
		panic(qerr)
	}
	if callQuery, qerr = sitter.NewQuery(Language, callsPattern); qerr != nil {
		panic(qerr)
	}
	if yieldQuery, qerr = sitter.NewQuery(Language, `(yield) @yield`); qerr != nil {
		panic(qerr)
	}
	if docQuery, qerr = sitter.NewQuery(Language, commentPattern); qerr != nil {
		panic(qerr)
	}
	if importQuery, qerr = sitter.NewQuery(Language, importsPattern); qerr != nil {
		panic(qerr)
	}

	ck := map[string]model.Kind{
		"def.function": model.KindFunction,
		"def.class":    model.KindClassDef,
	}
	defKinds = map[uint32]model.Kind{}
	for name, k := range ck {
		if idx, ok := defQuery.CaptureIndexForName(name); ok {
			defKinds[uint32(idx)] = k
		}
	}
	if idx, ok := defQuery.CaptureIndexForName(capName); ok {
		nameID = uint32(idx)
	}
	_ = nameID
	if idx, ok := callQuery.CaptureIndexForName(capCallName); ok {
		callID = uint32(idx)
	}
	if idx, ok := callQuery.CaptureIndexForName(capRecv); ok {
		recvID = uint32(idx)
	}
	if idx, ok := docQuery.CaptureIndexForName("doc"); ok {
		docID = uint32(idx)
	}
	if idx, ok := importQuery.CaptureIndexForName("imp.stmt"); ok {
		impStmtID = uint32(idx)
	}
	if idx, ok := importQuery.CaptureIndexForName("imp.from"); ok {
		impFromID = uint32(idx)
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
	fe.Language = model.LangPython

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
	allYields := collectAllYields(root)

	for _, d := range defs {
		fn := buildFunction(path, src, d.node, d.kind, comments, allYields)
		fn.Calls = callsForDef(allCalls, src, d.node)
		fe.Functions = append(fe.Functions, fn)
	}
	fe.Imports = collectImports(src, root)
	return fe, nil
}

func buildFunction(path string, src []byte, def *sitter.Node, kind model.Kind, comments []comment, yields []yieldSite) model.Function {
	name := functionName(src, def)
	container := classContainer(src, def)
	containerName := ""
	if container != nil {
		containerName = container.Name
	}
	sig := renderSignature(src, def)
	if containerName != "" && kind == model.KindFunction {
		kind = model.KindMethod
	}
	if strings.HasPrefix(sig, "async ") {
		if kind == model.KindMethod {
			kind = model.KindAsyncMethod
		} else {
			kind = model.KindAsyncFunction
		}
	} else if hasYieldIn(def, yields) && kind != model.KindMethod {
		kind = model.KindGenerator
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
		Container: classContainer(src, def),
		Doc:       docFor(def, comments),
	}
	return fn
}

// yieldSite is one yield match byte range, collected once per file.
type yieldSite struct{ start, end uint }

func collectAllYields(root *sitter.Node) []yieldSite {
	var out []yieldSite
	qc := sitter.NewQueryCursor()
	defer qc.Close()
	matches := qc.Matches(yieldQuery, root, nil)
	for m := matches.Next(); m != nil; m = matches.Next() {
		for _, c := range m.Captures {
			out = append(out, yieldSite{start: c.Node.StartByte(), end: c.Node.EndByte()})
		}
	}
	return out
}

func hasYieldIn(def *sitter.Node, yields []yieldSite) bool {
	ds, de := def.StartByte(), def.EndByte()
	for _, y := range yields {
		if y.start >= ds && y.end <= de {
			return true
		}
	}
	return false
}

// hasYield kept for tests; prefer hasYieldIn batch.
func hasYield(src []byte, def *sitter.Node) bool {
	_ = src
	qc := sitter.NewQueryCursor()
	defer qc.Close()
	matches := qc.Matches(yieldQuery, def, src)
	return matches.Next() != nil
}

func functionName(src []byte, def *sitter.Node) string {
	if n := def.ChildByFieldName("name"); n != nil {
		return extract.Text(src, n)
	}
	return "anonymous"
}

// classContainer walks ancestry to find an enclosing class_definition.
func classContainer(src []byte, def *sitter.Node) *model.Container {
	for n := def.Parent(); n != nil; n = n.Parent() {
		if n.Kind() == "class_definition" {
			if name := n.ChildByFieldName("name"); name != nil {
				return &model.Container{
					Kind: model.ContainerClass,
					Name: extract.Text(src, name),
				}
			}
		}
	}
	return nil
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
		// Receiver-prefixed name is baked once per file.
		nm := extract.Text(src, &name)
		full := nm
		if recv == "self" {
			full = "self." + nm
		} else if recv != "" {
			full = recv + "." + nm
		}
		out = append(out, callSite{start: name.StartByte(), end: name.EndByte(), full: full, site: extract.SiteRange(&name), node: name})
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

func collectCallsFor(src []byte, def *sitter.Node) []model.FunctionRef {
	var out []model.FunctionRef
	qc := sitter.NewQueryCursor()
	defer qc.Close()
	matches := qc.Matches(callQuery, def, src)
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
		ref := model.FunctionRef{Name: extract.Text(src, &name), SiteRange: extract.SiteRange(&name), Cond: branchCond(src, &name, def)}
		if recv == "self" {
			ref.Name = "self." + ref.Name
		} else if recv != "" {
			ref.Name = recv + "." + ref.Name
		}
		out = append(out, ref)
	}
	return out
}
