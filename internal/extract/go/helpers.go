package golang

import (
	"regexp"
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"

	"srcinsight/internal/extract"
	"srcinsight/internal/model"
)

// comment is a collected comment block used for Doc capture.
type comment struct {
	endRow int // 0-indexed end row
	text   string
}

// renderSignature renders the one-line head of a definition, stopping at the body.
// Type decls keep only the first line with a type prefix.
func renderSignature(src []byte, def *sitter.Node) string {
	end := def.EndByte()
	body := def.ChildByFieldName("body")
	if body != nil {
		end = body.StartByte()
	}
	start := def.StartByte()
	if int(start) > int(end) || int(end) > len(src) {
		return ""
	}
	head := string(src[start:end])
	if body == nil {
		if i := strings.IndexByte(head, '\n'); i >= 0 {
			head = head[:i]
		}
		head = "type " + head
	}
	return strings.Join(strings.Fields(head), " ")
}

// collectComments gathers all comment nodes in source order.
func collectComments(src []byte, root *sitter.Node) []comment {
	var out []comment
	qc := sitter.NewQueryCursor()
	defer qc.Close()
	matches := qc.Matches(docQuery, root, src)
	for m := matches.Next(); m != nil; m = matches.Next() {
		for _, c := range m.Captures {
			if c.Index != docID {
				continue
			}
			n := c.Node
			out = append(out, comment{
				endRow: int(n.EndPosition().Row),
				text:   extract.Text(src, &n),
			})
		}
	}
	return out
}

// docFor returns the contiguous comment block immediately preceding def.
func docFor(def *sitter.Node, comments []comment) string {
	if len(comments) == 0 {
		return ""
	}
	start := int(def.StartPosition().Row)
	var lines []string
	for i := len(comments) - 1; i >= 0; i-- {
		c := comments[i]
		if len(lines) == 0 {
			if c.endRow+1 == start || c.endRow+1 == start-1 {
				lines = append([]string{c.text}, lines...)
				start = c.endRow
				continue
			}
			break
		}
		if c.endRow+1 == start {
			lines = append([]string{c.text}, lines...)
			start = c.endRow
			continue
		}
		break
	}
	return strings.Join(lines, "\n")
}

// packageName returns the file package clause name, or empty when absent.
func packageName(src []byte, root *sitter.Node) string {
	qc := sitter.NewQueryCursor()
	defer qc.Close()
	for i := 0; i < int(root.NamedChildCount()); i++ {
		if ch := root.NamedChild(uint(i)); ch.Kind() == "package_clause" {
			for j := 0; j < int(ch.NamedChildCount()); j++ {
				if id := ch.NamedChild(uint(j)); id.Kind() == "package_identifier" {
					return extract.Text(src, id)
				}
			}
		}
	}
	return ""
}

// receiverInfo returns the receiver name and base type for a method def.
// Pointer, generic, and package wrappers unwrap to the base name.
func receiverInfo(src []byte, def *sitter.Node) (recv, typ string) {
	recvList := def.ChildByFieldName("receiver")
	if recvList == nil {
		return "", ""
	}
	for i := 0; i < int(recvList.NamedChildCount()); i++ {
		rd := recvList.NamedChild(uint(i))
		if rd.Kind() != "parameter_declaration" {
			continue
		}
		t := rd.ChildByFieldName("type")
		if t == nil {
			continue
		}
		if typ = baseTypeName(src, t, nil); typ == "" {
			continue
		}
		// Blank receivers bind the type with no variable for call rewriting.
		names := paramNames(src, rd)
		if len(names) == 0 {
			return "", typ
		}
		return names[0], typ
	}
	return "", ""
}

func ownReceiver(src []byte, def *sitter.Node) string {
	recv, _ := receiverInfo(src, def)
	return recv
}

// goPrimitives lists predeclared types that never wire to user nodes.
var goPrimitives = map[string]struct{}{
	"bool": {}, "string": {}, "int": {}, "int8": {}, "int16": {},
	"int32": {}, "int64": {}, "uint": {}, "uint8": {}, "uint16": {},
	"uint32": {}, "uint64": {}, "uintptr": {}, "float32": {}, "float64": {},
	"complex64": {}, "complex128": {}, "byte": {}, "rune": {},
	"any": {}, "comparable": {}, "error": {},
}

func isGoPrimitive(name string) bool {
	_, ok := goPrimitives[name]
	return ok
}

// typeParamNames collects generic parameter names skipped as call-site skolems.
func typeParamNames(src []byte, def *sitter.Node) map[string]struct{} {
	tp := def.ChildByFieldName("type_parameters")
	if tp == nil {
		return nil
	}
	var out map[string]struct{}
	for i := 0; i < int(tp.NamedChildCount()); i++ {
		d := tp.NamedChild(uint(i))
		if d.Kind() != "type_parameter_declaration" {
			continue
		}
		for j := 0; j < int(d.NamedChildCount()); j++ {
			if ch := d.NamedChild(uint(j)); ch.Kind() == "identifier" {
				if v := extract.Text(src, ch); v != "" {
					if out == nil {
						out = make(map[string]struct{})
					}
					out[v] = struct{}{}
				}
			}
		}
	}
	return out
}

// paramNames returns every declared name of one parameter_declaration.
func paramNames(src []byte, decl *sitter.Node) []string {
	var out []string
	// First name lives in the name field; later names are bare identifier siblings.
	for i := 0; i < int(decl.NamedChildCount()); i++ {
		if ch := decl.NamedChild(uint(i)); ch.Kind() == "identifier" {
			if v := extract.Text(src, ch); v != "" {
				out = append(out, v)
			}
		}
	}
	return out
}

// baseTypeName reduces a type node to its user-defined base name, unwrapping wrappers.
// Primitives, type params, and anonymous composites yield empty.
func baseTypeName(src []byte, t *sitter.Node, typeParams map[string]struct{}) string {
	return baseTypeNameQualified(src, t, typeParams, false)
}

// qualifiedTypeName keeps package qualification for import-aware resolution.
func qualifiedTypeName(src []byte, t *sitter.Node, typeParams map[string]struct{}) string {
	return baseTypeNameQualified(src, t, typeParams, true)
}

func baseTypeNameQualified(src []byte, t *sitter.Node, typeParams map[string]struct{}, qualified bool) string {
	for t != nil {
		switch t.Kind() {
		case "type_identifier":
			name := extract.Text(src, t)
			if name == "" || isGoPrimitive(name) {
				return ""
			}
			if typeParams != nil {
				if _, skip := typeParams[name]; skip {
					return ""
				}
			}
			return name
		case "qualified_type":
			pkg, name := "", ""
			if p := t.ChildByFieldName("package"); p != nil {
				pkg = extract.Text(src, p)
			}
			if n := t.ChildByFieldName("name"); n != nil {
				name = extract.Text(src, n)
			}
			if name == "" || isGoPrimitive(name) {
				return ""
			}
			if qualified && pkg != "" {
				return pkg + "." + name
			}
			return name
		case "generic_type":
			// Only the base wires up; type arguments are skipped.
			t = t.ChildByFieldName("type")
			continue
		case "pointer_type", "slice_type", "array_type":
			t = firstTypeChild(t)
			continue
		case "map_type":
			// Map prefers the value type as the likely user type.
			v := typeChildByField(t, "value")
			if v == nil {
				v = firstTypeChild(t)
			}
			t = v
			continue
		case "channel_type":
			t = typeChildByField(t, "value")
			continue
		case "parenthesized_type":
			t = firstTypeChild(t)
			continue
		case "function_type", "struct_type", "interface_type":
			// Anonymous composite: no named target.
			return ""
		default:
			return ""
		}
	}
	return ""
}

func firstTypeChild(t *sitter.Node) *sitter.Node {
	for i := 0; i < int(t.NamedChildCount()); i++ {
		return t.NamedChild(uint(i))
	}
	return nil
}

func typeChildByField(t *sitter.Node, field string) *sitter.Node {
	if c := t.ChildByFieldName(field); c != nil {
		return c
	}
	return nil
}

// collectTypeRefs appends one FunctionRef per distinct base type under list.
func collectTypeRefs(src []byte, list *sitter.Node, typeParams map[string]struct{}, seen map[string]struct{}, out *[]model.FunctionRef) {
	if list == nil {
		return
	}
	// A lone type node unwraps directly when it is not a parameter list.
	if list.Kind() != "parameter_list" {
		if name := qualifiedTypeName(src, list, typeParams); name != "" {
			if _, dup := seen[name]; !dup {
				seen[name] = struct{}{}
				*out = append(*out, model.FunctionRef{Name: name, LineRange: extract.NodeRange(list)})
			}
		}
		return
	}
	for i := 0; i < int(list.NamedChildCount()); i++ {
		d := list.NamedChild(uint(i))
		if d.Kind() != "parameter_declaration" && d.Kind() != "variadic_parameter_declaration" {
			continue
		}
		t := d.ChildByFieldName("type")
		if t == nil {
			continue
		}
		if name := qualifiedTypeName(src, t, typeParams); name != "" {
			if _, dup := seen[name]; dup {
				continue
			}
			seen[name] = struct{}{}
			*out = append(*out, model.FunctionRef{Name: name, LineRange: extract.NodeRange(t)})
		}
	}
}

// collectParamTypes extracts base type refs from a func/method's parameters.
func collectParamTypes(src []byte, def *sitter.Node, typeParams map[string]struct{}) []model.FunctionRef {
	var out []model.FunctionRef
	seen := make(map[string]struct{})
	collectTypeRefs(src, def.ChildByFieldName("parameters"), typeParams, seen, &out)
	return out
}

// collectReturnTypes extracts base type refs from a func/method result.
func collectReturnTypes(src []byte, def *sitter.Node, typeParams map[string]struct{}) []model.FunctionRef {
	res := def.ChildByFieldName("result")
	if res == nil {
		return nil
	}
	var out []model.FunctionRef
	collectTypeRefs(src, res, typeParams, make(map[string]struct{}), &out)
	return out
}

// collectEmbeds extracts Go embedding as heritage refs; union members are skipped.
func collectEmbeds(src []byte, def *sitter.Node) []model.FunctionRef {
	inner := def.ChildByFieldName("type")
	if inner == nil {
		return nil
	}
	var out []model.FunctionRef
	switch inner.Kind() {
	case "struct_type":
		// Anonymous fields are embeddings; descend explicitly per grammar version.
		var fds []*sitter.Node
		for i := 0; i < int(inner.NamedChildCount()); i++ {
			ch := inner.NamedChild(uint(i))
			switch ch.Kind() {
			case "field_declaration":
				fds = append(fds, ch)
			case "field_declaration_list":
				for j := 0; j < int(ch.NamedChildCount()); j++ {
					if fd := ch.NamedChild(uint(j)); fd.Kind() == "field_declaration" {
						fds = append(fds, fd)
					}
				}
			}
		}
		for _, fd := range fds {
			if fd.ChildByFieldName("name") != nil {
				continue
			}
			t := fd.ChildByFieldName("type")
			if t == nil {
				continue
			}
			if name := baseTypeName(src, t, nil); name != "" {
				out = append(out, model.FunctionRef{Name: name, LineRange: extract.NodeRange(t)})
			}
		}
	case "interface_type":
		for i := 0; i < int(inner.NamedChildCount()); i++ {
			el := inner.NamedChild(uint(i))
			switch el.Kind() {
			case "type_elem":
				// Embedded interface has one plain child; unions carry negated_type children.
				if el.NamedChildCount() != 1 {
					continue
				}
				ch := el.NamedChild(uint(0))
				if ch.Kind() != "type_identifier" && ch.Kind() != "qualified_type" {
					continue
				}
				if name := baseTypeName(src, ch, nil); name != "" {
					out = append(out, model.FunctionRef{Name: name, LineRange: extract.NodeRange(ch)})
				}
			case "method_elem":
			}
		}
	}
	return out
}

// collectFuncBinds pairs each typed name of one func/method with its base type.
func collectFuncBinds(src []byte, def *sitter.Node, typeParams map[string]struct{}) []model.RecvBinding {
	var out []model.RecvBinding
	seen := make(map[string]struct{})
	keep := func(recv, typ string) {
		if recv == "" || recv == "_" || typ == "" {
			return
		}
		if _, dup := seen[recv+"\x00"+typ]; dup {
			return
		}
		seen[recv+"\x00"+typ] = struct{}{}
		out = append(out, model.RecvBinding{Recv: recv, Type: typ})
	}
	if recv, typ := receiverInfo(src, def); recv != "" {
		keep(recv, typ)
	}
	params := def.ChildByFieldName("parameters")
	if params == nil {
		return out
	}
	for i := 0; i < int(params.NamedChildCount()); i++ {
		d := params.NamedChild(uint(i))
		if d.Kind() != "parameter_declaration" && d.Kind() != "variadic_parameter_declaration" {
			continue
		}
		t := d.ChildByFieldName("type")
		if t == nil {
			continue
		}
		typ := baseTypeName(src, t, typeParams)
		if typ == "" {
			continue
		}
		for _, n := range paramNames(src, d) {
			keep(n, typ)
		}
	}
	return out
}

// collectRecvBindings binds file-level declared names to types; tuple values bind every name.
func collectRecvBindings(src []byte, root *sitter.Node) []model.RecvBinding {
	var out []model.RecvBinding
	seen := make(map[model.RecvBinding]struct{})
	keep := func(b model.RecvBinding) {
		if b.Recv == "" || b.Recv == "_" || (b.Type == "" && b.ViaCall == "") {
			return
		}
		if _, dup := seen[b]; dup {
			return
		}
		seen[b] = struct{}{}
		out = append(out, b)
	}
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		switch n.Kind() {
		case "var_spec":
			names := specNames(src, n)
			if t := n.ChildByFieldName("type"); t != nil {
				if typ := baseTypeName(src, t, nil); typ != "" {
					for _, v := range names {
						keep(model.RecvBinding{Recv: v, Type: typ})
					}
				}
			}
			if v := n.ChildByFieldName("value"); v != nil {
				bindInitializer(src, v, names, keep)
			}
		case "short_var_declaration":
			var names []string
			if l := n.ChildByFieldName("left"); l != nil {
				for i := 0; i < int(l.NamedChildCount()); i++ {
					if ch := l.NamedChild(uint(i)); ch.Kind() == "identifier" {
						if v := extract.Text(src, ch); v != "" {
							names = append(names, v)
						}
					}
				}
			}
			if r := n.ChildByFieldName("right"); r != nil {
				bindInitializer(src, r, names, keep)
			}
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			walk(n.NamedChild(uint(i)))
		}
	}
	walk(root)
	return out
}

// specNames returns the declared names of a var_spec/const_spec.
func specNames(src []byte, spec *sitter.Node) []string {
	var out []string
	for i := 0; i < int(spec.NamedChildCount()); i++ {
		if ch := spec.NamedChild(uint(i)); ch.Kind() == "identifier" {
			if v := extract.Text(src, ch); v != "" {
				out = append(out, v)
			}
		}
	}
	return out
}

// bindInitializer proposes one binding per name from an initializer list.
func bindInitializer(src []byte, values *sitter.Node, names []string, keep func(model.RecvBinding)) {
	if len(names) == 0 {
		return
	}
	// Values arrive as an expression_list; a lone expression is handled identically.
	var exprs []*sitter.Node
	if values.Kind() == "expression_list" {
		for i := 0; i < int(values.NamedChildCount()); i++ {
			exprs = append(exprs, values.NamedChild(uint(i)))
		}
	} else {
		exprs = []*sitter.Node{values}
	}
	for _, e := range exprs {
		if typ, ok := unwrapCompositeType(src, e); ok {
			for _, v := range names {
				keep(model.RecvBinding{Recv: v, Type: typ})
			}
			continue
		}
		if fac := factoryCall(src, e); fac != "" {
			for _, v := range names {
				keep(model.RecvBinding{Recv: v, ViaCall: fac})
			}
		}
	}
}

// unwrapCompositeType reads a type out of a composite literal through wrapper layers.
func unwrapCompositeType(src []byte, v *sitter.Node) (string, bool) {
	for v != nil {
		switch v.Kind() {
		case "composite_literal":
			if t := v.ChildByFieldName("type"); t != nil {
				if name := baseTypeName(src, t, nil); name != "" {
					return name, true
				}
			}
			return "", false
		case "unary_expression", "parenthesized_expression":
			v = firstTypeChild(v)
		default:
			return "", false
		}
	}
	return "", false
}

// factoryCall returns the callee text of a call initializer for deferred resolution.
func factoryCall(src []byte, v *sitter.Node) string {
	for v != nil {
		switch v.Kind() {
		case "call_expression":
			fn := v.ChildByFieldName("function")
			if fn == nil {
				return ""
			}
			callee := extract.Text(src, fn)
			if i := strings.Index(callee, "["); i >= 0 {
				callee = callee[:i]
			}
			callee = strings.TrimSpace(callee)
			if callee == "" || strings.ContainsAny(callee, " \t\n\r(){};,") {
				return ""
			}
			return callee
		case "parenthesized_expression":
			v = firstTypeChild(v)
		default:
			return ""
		}
	}
	return ""
}

// condMaxLen caps a captured branch condition with truncation.
const condMaxLen = 120

var versionSuffix = regexp.MustCompile(`\.v[0-9]+$`)

// branchCond returns the nearest enclosing branch condition guarding the call.
// Nested func literals bound the walk; calls in an if-initializer are unguarded.
func branchCond(src []byte, nameNode, def *sitter.Node) string {
	defID := def.Id()
	nameStart, nameEnd := nameNode.StartByte(), nameNode.EndByte()
	for n := nameNode.Parent(); n != nil; n = n.Parent() {
		if n.Id() == defID {
			break
		}
		switch n.Kind() {
		case "func_literal":
			return ""
		case "if_statement":
			// handled below
		default:
			continue
		}
		condNode := n.ChildByFieldName("condition")
		if condNode == nil {
			continue
		}
		// Calls in the if-initializer run before the guard; keep walking outward.
		if init := n.ChildByFieldName("initializer"); init != nil &&
			init.StartByte() <= nameStart && nameEnd <= init.EndByte() {
			continue
		}
		cond := extract.JoinInline(extract.Text(src, condNode))
		if cond == "" {
			return ""
		}
		if len(cond) > condMaxLen {
			cond = cond[:condMaxLen-3] + "..."
		}
		if alt := n.ChildByFieldName("alternative"); alt != nil &&
			alt.StartByte() <= nameStart && nameEnd <= alt.EndByte() {
			return "!(" + cond + ")"
		}
		return cond
	}
	return ""
}

// majorSuffix matches a trailing Go-major-version path segment.
var majorSuffix = regexp.MustCompile(`^v[0-9]+$`)

// importBase guesses the package local name from an import path.
// Trailing major versions and version suffixes fall back per convention.
func importBase(path string) string {
	path = strings.Trim(path, "/")
	segs := strings.Split(path, "/")
	base := segs[len(segs)-1]
	if majorSuffix.MatchString(base) && len(segs) >= 2 {
		base = segs[len(segs)-2]
	}
	base = versionSuffix.ReplaceAllString(base, "")
	if base == "" {
		return path
	}
	return base
}

// unquoteImport strips Go string quotes around an import path.
func unquoteImport(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '`' && s[len(s)-1] == '`') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// collectImports finds every import_spec and classifies each; blank imports are skipped.
// One synthetic row per file carries the package clause for the same-package pass.
func collectImports(src []byte, root *sitter.Node) []extract.ImportStmt {
	var out []extract.ImportStmt
	qc := sitter.NewQueryCursor()
	defer qc.Close()
	matches := qc.Matches(impQuery, root, src)
	for m := matches.Next(); m != nil; m = matches.Next() {
		for _, c := range m.Captures {
			if c.Index != impID {
				continue
			}
			n := c.Node
			pathNode := n.ChildByFieldName("path")
			if pathNode == nil {
				continue
			}
			path := unquoteImport(extract.Text(src, pathNode))
			if path == "" {
				continue
			}
			nameNode := n.ChildByFieldName("name")
			if nameNode == nil {
				out = append(out, extract.ImportStmt{
					LocalName: importBase(path),
					Source:    path,
					Kind:      "namespace",
				})
				continue
			}
			switch nameNode.Kind() {
			case "blank_identifier":
				continue
			case "dot":
				out = append(out, extract.ImportStmt{
					LocalName: "*",
					Source:    path,
					Kind:      "named",
				})
			default: // package_identifier: explicit alias
				if alias := extract.Text(src, nameNode); alias != "" {
					out = append(out, extract.ImportStmt{
						LocalName: alias,
						Source:    path,
						Kind:      "named",
					})
				}
			}
		}
	}
	if pkg := packageName(src, root); pkg != "" {
		out = append(out, extract.ImportStmt{
			LocalName: pkg,
			Source:    "",
			Kind:      model.ImportKindGoPackage,
		})
	}
	return out
}
