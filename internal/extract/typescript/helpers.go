package typescript

import (
	"bytes"
	"sort"
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"

	"srcinsight/internal/convention"
	"srcinsight/internal/extract"
	"srcinsight/internal/model"
)

// condMaxLen caps a captured branch condition with truncation.
const condMaxLen = 120

// branchCond returns the nearest enclosing branch condition guarding the call.
// Nested function boundaries stop the walk; else branches negate.
func branchCond(src []byte, nameNode, def *sitter.Node) string {
	defID := def.Id()
	nameStart, nameEnd := nameNode.StartByte(), nameNode.EndByte()
	for n := nameNode.Parent(); n != nil; n = n.Parent() {
		if n.Id() == defID {
			break
		}
		switch n.Kind() {
		case "arrow_function", "function_expression", "function_declaration",
			"generator_function_declaration", "method_definition", "class_declaration":
			return ""
		case "if_statement", "ternary_expression":
			// handled below
		default:
			continue
		}
		condNode := n.ChildByFieldName("condition")
		if condNode == nil {
			continue
		}
		cond := extract.JoinInline(extract.Text(src, condNode))
		// Condition keeps its parens; strip one layer so when reads like the guard.
		cond = unwrapParens(cond)
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

// unwrapParens strips wrapping parens layers so conditions read like guards.
func unwrapParens(s string) string {
	for len(s) >= 2 && s[0] == '(' && s[len(s)-1] == ')' {
		depth := 0
		wraps := true
		for i := 0; i < len(s); i++ {
			switch s[i] {
			case '(':
				depth++
			case ')':
				depth--
				if depth == 0 && i != len(s)-1 {
					wraps = false
				}
			}
			if !wraps {
				break
			}
		}
		if !wraps || depth != 0 {
			break
		}
		s = strings.TrimSpace(s[1 : len(s)-1])
	}
	return s
}

// collectHeritage extracts extends and implements targets; refs stay unresolved for the resolver.
func collectHeritage(src []byte, def *sitter.Node) (extends, implements []model.FunctionRef) {
	for i := 0; i < int(def.NamedChildCount()); i++ {
		child := def.NamedChild(uint(i))
		switch child.Kind() {
		case "class_heritage":
			for j := 0; j < int(child.NamedChildCount()); j++ {
				switch clause := child.NamedChild(uint(j)); clause.Kind() {
				case "extends_clause":
					extends = append(extends, heritageTypes(src, clause)...)
				case "implements_clause":
					implements = append(implements, heritageTypes(src, clause)...)
				}
			}
		case "extends_type_clause":
			extends = append(extends, heritageTypes(src, child)...)
		}
	}
	return extends, implements
}

// heritageTypes walks a heritage clause; generic args are skipped.
func heritageTypes(src []byte, clause *sitter.Node) []model.FunctionRef {
	var out []model.FunctionRef
	for i := 0; i < int(clause.NamedChildCount()); i++ {
		collectHeritageType(src, clause.NamedChild(uint(i)), &out)
	}
	return out
}

func collectHeritageType(src []byte, n *sitter.Node, out *[]model.FunctionRef) {
	if n == nil {
		return
	}
	switch n.Kind() {
	case "type_arguments":
		return
	case "type_identifier", "identifier", "nested_type_identifier":
		*out = append(*out, model.FunctionRef{
			Name:      extract.Text(src, n),
			LineRange: extract.NodeRange(n),
		})
		return
	}
	for i := 0; i < int(n.NamedChildCount()); i++ {
		collectHeritageType(src, n.NamedChild(uint(i)), out)
	}
}

// comment is a collected comment block used for Doc capture.
type comment struct {
	endRow int // 0-indexed end row
	text   string
}

// functionName resolves the name for a definition node.
func functionName(src []byte, def *sitter.Node) string {
	switch def.Kind() {
	case "constructor":
		return "constructor"
	case "arrow_function":
		return arrowName(src, def)
	}
	if n := def.ChildByFieldName("name"); n != nil {
		return extract.Text(src, n)
	}
	if n := def.ChildByFieldName("property_identifier"); n != nil {
		return extract.Text(src, n)
	}
	return "anonymous"
}

// arrowName infers a name for an arrow function from its assignment context.
func arrowName(src []byte, arrow *sitter.Node) string {
	p := arrow.Parent()
	if p != nil && p.Kind() == "variable_declarator" {
		if n := p.ChildByFieldName("name"); n != nil {
			return extract.Text(src, n)
		}
	}
	// Class arrow properties live in a field definition, not a declarator.
	if p != nil && (p.Kind() == "public_field_definition" || p.Kind() == "field_definition") {
		for i := 0; i < int(p.NamedChildCount()); i++ {
			if ch := p.NamedChild(uint(i)); ch.Kind() == "property_identifier" || ch.Kind() == "identifier" {
				return extract.Text(src, ch)
			}
		}
	}
	if p != nil && p.Kind() == "assignment_expression" {
		if n := p.ChildByFieldName("left"); n != nil {
			return extract.Text(src, n)
		}
	}
	if p != nil && p.Kind() == "variable_assignment" {
		if n := p.ChildByFieldName("name"); n != nil {
			return extract.Text(src, n)
		}
	}
	if p != nil {
		if gp := p.Parent(); gp != nil && gp.Kind() == "pair" {
			if k := gp.ChildByFieldName("key"); k != nil {
				return extract.Text(src, k)
			}
		}
	}
	return "anonymous"
}

// testCallees aliases the central shape table for existing references.
var testCallees = convention.TestCallees

// maxTestTitle caps a captured title; the index stores labels, not prose.
const maxTestTitle = convention.MaxTestTitle

// isTestFilePath reports test/spec/fixture paths via the shared convention.
func isTestFilePath(path string) bool {
	return convention.IsTestPath(path)
}

// testTitle returns the case title when def is the direct callback of a test call.
// The def must be a non-first argument; anything else yields empty.
func testTitle(src []byte, path string, def *sitter.Node) string {
	if !isTestFilePath(path) {
		return ""
	}
	args := def.Parent()
	if args == nil || args.Kind() != "arguments" {
		return ""
	}
	call := args.Parent()
	if call == nil || call.Kind() != "call_expression" {
		return ""
	}
	fn := call.ChildByFieldName("function")
	if fn == nil || !isTestCallee(src, fn) {
		return ""
	}
	var idx = -1
	for i := 0; i < int(args.NamedChildCount()); i++ {
		// Byte ranges, not pointer identity: node wrappers copy.
		if ch := args.NamedChild(uint(i)); ch.StartByte() == def.StartByte() && ch.EndByte() == def.EndByte() {
			idx = i
			break
		}
	}
	if idx < 1 || int(args.NamedChildCount()) == 0 {
		return ""
	}
	first := args.NamedChild(0)
	switch first.Kind() {
	case "string":
		return trimQuotes(extract.Text(src, first))
	case "template_string":
		raw := extract.Text(src, first)
		if strings.Contains(raw, "${") {
			return ""
		}
		return trimQuotes(raw)
	default:
		return ""
	}
}

// isTestCallee reports whether a call function spells a test-style callee.
func isTestCallee(src []byte, fn *sitter.Node) bool {
	switch fn.Kind() {
	case "identifier":
		return convention.IsTestCalleeName(extract.Text(src, fn))
	case "member_expression":
		prop := fn.ChildByFieldName("property")
		if prop == nil {
			return false
		}
		return convention.IsTestCalleeName(extract.Text(src, prop))
	default:
		return false
	}
}

// collectDecoratorTags returns convention tags for decorators directly attached to def.
// Only known NestJS decorators map; unknown ones yield nothing.
func collectDecoratorTags(src []byte, def *sitter.Node) []string {
	parent := def.Parent()
	if parent == nil {
		return nil
	}
	// Walk backwards over contiguous decorators so stacked tags are kept.
	count := int(parent.NamedChildCount())
	idx := -1
	for i := 0; i < count; i++ {
		ch := parent.NamedChild(uint(i))
		if ch.StartByte() == def.StartByte() && ch.EndByte() == def.EndByte() {
			idx = i
			break
		}
	}
	if idx <= 0 {
		return nil
	}
	var names []string
	for i := idx - 1; i >= 0; i-- {
		ch := parent.NamedChild(uint(i))
		if ch.Kind() != "decorator" {
			break
		}
		if name := decoratorName(src, ch); name != "" && convention.IsNestDecor(name) {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return nil
	}
	// Reverse to source order, then map + dedupe.
	var out []string
	seen := make(map[string]struct{}, len(names))
	for i := len(names) - 1; i >= 0; i-- {
		tag := convention.NestTag(names[i])
		if _, ok := seen[tag]; ok {
			continue
		}
		seen[tag] = struct{}{}
		out = append(out, tag)
	}
	return out
}

// decoratorName resolves the decorator callee name; arguments never leak in.
func decoratorName(src []byte, deco *sitter.Node) string {
	for i := 0; i < int(deco.NamedChildCount()); i++ {
		ch := deco.NamedChild(uint(i))
		switch ch.Kind() {
		case "identifier", "type_identifier", "property_identifier":
			return extract.Text(src, ch)
		case "call_expression":
			if fn := ch.ChildByFieldName("function"); fn != nil {
				return extract.Text(src, fn)
			}
		case "member_expression":
			if prop := ch.ChildByFieldName("property"); prop != nil {
				return extract.Text(src, prop)
			}
		}
	}
	return ""
}

// trimQuotes strips one matching pair of quotes/backticks and caps length.
func trimQuotes(s string) string {
	if len(s) >= 2 {
		if c := s[0]; c == '"' || c == '\'' || c == '`' {
			if s[len(s)-1] == c {
				s = s[1 : len(s)-1]
			}
		}
	}
	s = strings.TrimSpace(s)
	if len(s) > maxTestTitle {
		s = s[:maxTestTitle]
	}
	return s
}

// renderSignature renders the one-line head of a definition without the body.
func renderSignature(src []byte, def *sitter.Node) string {
	end := def.EndByte()
	if body := def.ChildByFieldName("body"); body != nil {
		end = body.StartByte()
	}
	start := def.StartByte()
	if int(start) > int(end) || int(end) > len(src) {
		return ""
	}
	return strings.Join(strings.Fields(string(src[start:end])), " ")
}

// classContainer finds the nearest enclosing class; prefer enclosingContainer.
func classContainer(src []byte, def *sitter.Node) *model.Container {
	return enclosingContainer(src, def)
}

// enclosingContainer finds the nearest enclosing container for def.
// Precedence: class, then function, then namespace or module.
func enclosingContainer(src []byte, def *sitter.Node) *model.Container {
	for n := def.Parent(); n != nil; n = n.Parent() {
		switch n.Kind() {
		case "class_declaration", "abstract_class_declaration":
			if name := n.ChildByFieldName("name"); name != nil {
				return &model.Container{
					Kind: model.ContainerClass,
					Name: extract.Text(src, name),
				}
			}
		case "function_declaration", "generator_function_declaration", "function_expression":
			if name := n.ChildByFieldName("name"); name != nil {
				return &model.Container{
					Kind: model.ContainerFunction,
					Name: extract.Text(src, name),
				}
			}
		case "method_definition":
			if name := n.ChildByFieldName("name"); name != nil {
				return &model.Container{
					Kind: model.ContainerFunction,
					Name: extract.Text(src, name),
				}
			}
			if pid := extract.Text(src, n); pid != "" {
			}
		case "arrow_function":
			// Arrow containers use the inferred const name, else generic arrow.
			name := arrowName(src, n)
			if name != "" && name != "anonymous" {
				return &model.Container{
					Kind: model.ContainerFunction,
					Name: name,
				}
			}
			return &model.Container{
				Kind: model.ContainerFunction,
				Name: "arrow",
			}
		case "internal_module":
			// Namespace or module via internal_module.
			if name := n.ChildByFieldName("name"); name != nil {
				return &model.Container{
					Kind: model.ContainerNamespace,
					Name: extract.Text(src, name),
				}
			}
		case "module":
			if name := n.ChildByFieldName("name"); name != nil {
				return &model.Container{
					Kind: model.ContainerModule,
					Name: extract.Text(src, name),
				}
			}
		case "namespace_declaration":
			if name := n.ChildByFieldName("name"); name != nil {
				return &model.Container{
					Kind: model.ContainerNamespace,
					Name: extract.Text(src, name),
				}
			}
		}
	}
	return nil
}

// headBytes returns up to n bytes past leading whitespace for header checks.
func headBytes(src []byte, start, end uint, n int) []byte {
	if start >= end || int(start) >= len(src) {
		return nil
	}
	if int(end) > len(src) {
		end = uint(len(src))
	}
	b := src[start:end]
	for len(b) > 0 && (b[0] == ' ' || b[0] == '\t' || b[0] == '\n' || b[0] == '\r') {
		b = b[1:]
	}
	if len(b) > n {
		b = b[:n]
	}
	return b
}

// textCapped returns trimmed text without copying more than capBytes.
func textCapped(src []byte, start, end uint, capBytes int) string {
	if start >= end || int(start) >= len(src) {
		return ""
	}
	if int(end) > len(src) {
		end = uint(len(src))
	}
	// Skip leading whitespace by index, then cap the window.
	for start < end && (src[start] == ' ' || src[start] == '\t' || src[start] == '\n' || src[start] == '\r') {
		start++
	}
	if int(end-start) > capBytes {
		end = start + uint(capBytes)
	}
	return string(bytes.TrimSpace(src[start:end]))
}

func isConstLexical(src []byte, parent *sitter.Node) bool {
	if parent == nil || parent.Kind() != "lexical_declaration" {
		return false
	}
	if k := parent.ChildByFieldName("kind"); k != nil {
		if extract.Text(src, k) == "const" {
			return true
		}
	}
	// Fall back to first child text.
	if parent.ChildCount() > 0 {
		if c := parent.Child(0); c != nil && extract.Text(src, c) == "const" {
			return true
		}
	}
	// Fall back to a bounded header prefix check.
	return bytes.HasPrefix(headBytes(src, parent.StartByte(), parent.EndByte(), 16), []byte("const"))
}

// constName resolves the name for a const definition node.
func constName(src []byte, def *sitter.Node) string {
	switch def.Kind() {
	case "variable_declarator":
		if n := def.ChildByFieldName("name"); n != nil {
			if n.Kind() == "identifier" {
				return extract.Text(src, n)
			}
			// Destructured const falls back to trimmed pattern text.
			txt := extract.Text(src, n)
			// Keep short without splitting UTF-8 runes.
			if len([]rune(txt)) > 40 {
				r := []rune(txt)
				txt = string(r[:40])
			}
			return txt
		}
	case "identifier":
		return extract.Text(src, def)
	case "for_in_statement", "for_statement":
		if left := def.ChildByFieldName("left"); left != nil {
			if left.Kind() == "identifier" {
				return extract.Text(src, left)
			}
			return extract.Text(src, left)
		}
	}
	if n := def.ChildByFieldName("name"); n != nil {
		return extract.Text(src, n)
	}
	return extract.Text(src, def)
}

// renderConstSignature renders a const head without expanding the initializer.
func renderConstSignature(src []byte, def *sitter.Node) string {
	switch def.Kind() {
	case "variable_declarator":
		parent := def.Parent()
		var start uint
		if parent != nil && parent.Kind() == "lexical_declaration" {
			// Slice only this declarator plus the const prefix.
			if v := def.ChildByFieldName("value"); v != nil {
				valHead := textCapped(src, v.StartByte(), v.EndByte(), 512)
				// Collapse to 80 runes without cutting UTF-8.
				if len([]rune(valHead)) > 80 {
					r := []rune(valHead)
					valHead = strings.TrimSpace(string(r[:80])) + " ..."
				}
				// Repair any mid-rune cut.
				valHead = strings.ToValidUTF8(valHead, "")
				name := ""
				if n := def.ChildByFieldName("name"); n != nil {
					name = extract.Text(src, n)
				}
				return strings.Join(strings.Fields("const "+name+" = "+valHead), " ")
			}
			// Fall back to whole declarator with const prefix.
			decText := extract.Text(src, def)
			txt := "const " + decText
			return strings.Join(strings.Fields(txt), " ")
		}
		start = def.StartByte()
		end := def.EndByte()
		if int(start) > int(end) || int(end) > len(src) {
			return ""
		}
		return strings.Join(strings.Fields(string(src[start:end])), " ")
	case "identifier":
		// For-loop var renders from the parent clause.
		parent := def.Parent()
		if parent != nil && (parent.Kind() == "for_in_statement" || parent.Kind() == "for_statement") {
			txt := extract.Text(src, parent)
			// Trim body at closing paren.
			if idx := strings.Index(txt, ")"); idx >= 0 {
				txt = txt[:idx+1]
			}
			return strings.Join(strings.Fields(txt), " ")
		}
		return "const " + extract.Text(src, def)
	}
	end := def.EndByte()
	start := def.StartByte()
	if int(start) > int(end) || int(end) > len(src) {
		return ""
	}
	return strings.Join(strings.Fields(string(src[start:end])), " ")
}

// collectConstDefs returns const declarator nodes; only const is collected.
func collectConstDefs(q *dialectQueries, src []byte, root *sitter.Node) []fnDef {
	var out []fnDef
	qc := sitter.NewQueryCursor()
	defer qc.Close()
	matches := qc.Matches(q.constQuery, root, src)
	for m := matches.Next(); m != nil; m = matches.Next() {
		for _, c := range m.Captures {
			switch c.Index {
			case q.constDeclID:
				d := c.Node
				if isConstLexical(src, d.Parent()) {
					if nameNode := d.ChildByFieldName("name"); nameNode != nil && nameNode.Kind() == "identifier" {
						nc := d
						out = append(out, fnDef{node: &nc, kind: model.KindConst})
					} else if nameNode != nil && (nameNode.Kind() == "object_pattern" || nameNode.Kind() == "array_pattern") {
						// Destructuring const: expand each binding identifier as separate const entry
						collectPatternIdents(src, nameNode, &out)
					}
				}
			case q.constForinID:
				n := c.Node
				// `for (const x of Y)` or `for (const x in Y)`
				if k := n.ChildByFieldName("kind"); k != nil && extract.Text(src, k) == "const" {
					if left := n.ChildByFieldName("left"); left != nil && left.Kind() == "identifier" {
						out = append(out, fnDef{node: left, kind: model.KindConst})
					}
				} else if hasForConstHeader(src, &n) {
					if left := n.ChildByFieldName("left"); left != nil && left.Kind() == "identifier" {
						out = append(out, fnDef{node: left, kind: model.KindConst})
					}
				}
			}
		}
	}
	// Sort by byte range for deterministic emission order.
	sort.SliceStable(out, func(i, j int) bool {
		si, sj := out[i].node.StartByte(), out[j].node.StartByte()
		if si != sj {
			return si < sj
		}
		return out[i].node.EndByte() < out[j].node.EndByte()
	})
	return out
}

// hasForConstHeader reports a for-const header via a bounded slice.
func hasForConstHeader(src []byte, n *sitter.Node) bool {
	head := headBytes(src, n.StartByte(), n.EndByte(), 16)
	for _, sp := range [][]byte{[]byte(" "), []byte("\t"), []byte("\n"), []byte("\r")} {
		head = bytes.ReplaceAll(head, sp, []byte(""))
	}
	return bytes.HasPrefix(head, []byte("for(const"))
}

// collectPatternIdents expands destructured patterns into const entries.
func collectPatternIdents(src []byte, pattern *sitter.Node, out *[]fnDef) {
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if n.Kind() == "identifier" || n.Kind() == "shorthand_property_identifier" || n.Kind() == "property_identifier" {
			*out = append(*out, fnDef{node: n, kind: model.KindConst})
			return
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			walk(n.NamedChild(uint(i)))
		}
	}
	walk(pattern)
}

// collectConstUses extracts identifier dependencies from a const initializer.
func collectConstUses(src []byte, def *sitter.Node) []model.FunctionRef {
	var value *sitter.Node
	switch def.Kind() {
	case "variable_declarator":
		value = def.ChildByFieldName("value")
	case "identifier":
		// for-loop const: dependency is the right side of for_in
		if p := def.Parent(); p != nil && p.Kind() == "for_in_statement" {
			value = p.ChildByFieldName("right")
		}
	}
	if value == nil {
		return nil
	}
	// Leaf literals carry no references; skip the subtree walk.
	switch value.Kind() {
	case "number", "string", "true", "false", "null", "undefined", "this":
		return nil
	}
	seen := make(map[string]struct{})
	var out []model.FunctionRef
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		switch n.Kind() {
		case "identifier", "property_identifier", "type_identifier":
			// Skip object literal keys.
			if p := n.Parent(); p != nil && p.Kind() == "pair" {
				if key := p.ChildByFieldName("key"); key != nil && key.Equals(*n) {
					return
				}
			}
			name := extract.Text(src, n)
			if name == "" || name == "const" || name == "let" || name == "var" {
				break
			}
			if _, ok := seen[name]; ok {
				break
			}
			// Collect member chain for property position only.
			full := name
			if p := n.Parent(); p != nil && p.Kind() == "member_expression" {
				if prop := p.ChildByFieldName("property"); prop != nil && prop.Equals(*n) {
					if obj := p.ChildByFieldName("object"); obj != nil {
						objTxt := extract.Text(src, obj)
						if objTxt != "" && objTxt != name {
							full = objTxt + "." + name
						}
					}
				} else {
					full = name
				}
			}
			// Keep full chain for display; resolver splits recv.
			if _, ok := seen[full]; ok {
				break
			}
			seen[name] = struct{}{}
			seen[full] = struct{}{}
			out = append(out, model.FunctionRef{
				Name:      full,
				LineRange: extract.NodeRange(n),
				SiteRange: extract.SiteRange(n),
			})
			return
		case "call_expression", "binary_expression", "member_expression", "ternary_expression", "await_expression", "yield_expression", "assignment_expression", "arrow_function", "function_expression", "object", "array", "template_string", "subscript_expression":
		default:
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			walk(n.NamedChild(uint(i)))
		}
	}
	walk(value)
	// Dedupe by full name.
	uniq := make(map[string]struct{})
	var dedup []model.FunctionRef
	for _, r := range out {
		if _, ok := uniq[r.Name]; ok {
			continue
		}
		uniq[r.Name] = struct{}{}
		dedup = append(dedup, r)
	}
	return dedup
}

// asyncKind bakes async into the kind value.
func asyncKind(sig string, kind model.Kind) model.Kind {
	if !hasAsyncKeyword(sig) {
		return kind
	}
	switch kind {
	case model.KindFunction:
		return model.KindAsyncFunction
	case model.KindMethod:
		return model.KindAsyncMethod
	case model.KindArrow:
		return model.KindAsyncArrow
	}
	return kind
}

func hasAsyncKeyword(sig string) bool {
	// Signature is single-space collapsed, so a byte scan finds word tokens.
	for i := 0; i < len(sig); {
		for i < len(sig) && isSigSpace(sig[i]) {
			i++
		}
		start := i
		for i < len(sig) && !isSigSpace(sig[i]) {
			i++
		}
		if i-start == 5 && sig[start:i] == "async" {
			return true
		}
	}
	return false
}

func isSigSpace(c byte) bool {
	switch c {
	case ' ', '\t', '\n', '\r', '\v', '\f':
		return true
	}
	return false
}

// collectComments gathers all comment nodes in source order.
func collectComments(q *dialectQueries, src []byte, root *sitter.Node) []comment {
	var out []comment
	qc := sitter.NewQueryCursor()
	defer qc.Close()
	matches := qc.Matches(q.docQuery, root, src)
	for m := matches.Next(); m != nil; m = matches.Next() {
		for _, c := range m.Captures {
			if c.Index != q.docID {
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

// collectImports classifies each import by inspecting its import_clause node.
func collectImports(q *dialectQueries, src []byte, root *sitter.Node) []extract.ImportStmt {
	var out []extract.ImportStmt
	qc := sitter.NewQueryCursor()
	defer qc.Close()
	captureNames := q.importQuery.CaptureNames()
	matches := qc.Matches(q.importQuery, root, src)
	for m := matches.Next(); m != nil; m = matches.Next() {
		var stmtNode *sitter.Node
		source := ""
		for _, c := range m.Captures {
			var capName string
			if int(c.Index) < len(captureNames) {
				capName = captureNames[c.Index]
			}
			switch capName {
			case "import":
				nc := c.Node
				stmtNode = &nc
			case "import.source":
				source = strings.Trim(extract.Text(src, &c.Node), "'\"")
			}
		}
		if stmtNode == nil || source == "" {
			continue
		}
		out = append(out, classifyImport(src, stmtNode, source)...)
	}
	return out
}

// classifyImport inspects the clauses of a single import_statement.
func classifyImport(src []byte, stmt *sitter.Node, source string) []extract.ImportStmt {
	clause := childOfType(stmt, "import_clause")
	if clause == nil {
		return nil // side-effect import, unused by the resolver
	}
	first := clause.NamedChild(0)
	if first == nil {
		return nil
	}
	switch first.Kind() {
	case "named_imports":
		var stmts []extract.ImportStmt
		for i := 0; i < int(first.NamedChildCount()); i++ {
			spec := first.NamedChild(uint(i))
			if spec.Kind() != "import_specifier" {
				continue
			}
			name := extract.Text(src, spec.ChildByFieldName("name"))
			if name == "" {
				continue
			}
			// LocalName is the spelling used in this file; Alias keeps the
			// original exported name so resolution can find the definition.
			im := extract.ImportStmt{Source: source, Kind: "named", LocalName: name}
			if a := spec.ChildByFieldName("alias"); a != nil {
				im.Alias = name
				im.LocalName = extract.Text(src, a)
			}
			stmts = append(stmts, im)
		}
		return stmts
	case "namespace_import":
		// Namespace name feeds receiver probing; Alias keeps the star fallback.
		name := ""
		for i := 0; i < int(first.NamedChildCount()); i++ {
			if ch := first.NamedChild(uint(i)); ch.Kind() == "identifier" {
				name = extract.Text(src, ch)
				break
			}
		}
		if name == "" {
			return []extract.ImportStmt{{Source: source, Kind: "namespace", LocalName: "*"}}
		}
		return []extract.ImportStmt{{Source: source, Kind: "namespace", LocalName: name, Alias: "*"}}
	default:
		return []extract.ImportStmt{{Source: source, Kind: "default", LocalName: extract.Text(src, first)}}
	}
}

// collectReExports records export-from rows so the resolver follows barrel files.
func collectReExports(q *dialectQueries, src []byte, root *sitter.Node) []extract.ImportStmt {
	var out []extract.ImportStmt
	qc := sitter.NewQueryCursor()
	defer qc.Close()
	matches := qc.Matches(q.exportQuery, root, src)
	for m := matches.Next(); m != nil; m = matches.Next() {
		for _, c := range m.Captures {
			out = append(out, classifyExport(src, &c.Node)...)
		}
	}
	return out
}

// classifyExport turns one export_statement into re-export rows.
// Sourceless exports record under Kind export-alias.
func classifyExport(src []byte, stmt *sitter.Node) []extract.ImportStmt {
	var source string
	var clause *sitter.Node
	for i := 0; i < int(stmt.NamedChildCount()); i++ {
		switch ch := stmt.NamedChild(uint(i)); ch.Kind() {
		case "string":
			source = strings.Trim(extract.Text(src, ch), "'\"")
		case "export_clause":
			clause = ch
		}
	}
	if clause == nil {
		if source == "" {
			return nil
		}
		return []extract.ImportStmt{{Source: source, Kind: "re-export", LocalName: "*"}}
	}
	kind := "re-export"
	if source == "" {
		kind = "export-alias"
	}
	var out []extract.ImportStmt
	for i := 0; i < int(clause.NamedChildCount()); i++ {
		spec := clause.NamedChild(uint(i))
		if spec.Kind() != "export_specifier" {
			continue
		}
		kids := int(spec.NamedChildCount())
		if kids == 0 {
			continue
		}
		// Exported name is the last child.
		exported := extract.Text(src, spec.NamedChild(uint(kids-1)))
		if exported == "" {
			continue
		}
		im := extract.ImportStmt{Source: source, Kind: kind, LocalName: exported}
		if kids >= 2 {
			if local := extract.Text(src, spec.NamedChild(0)); local != "" && local != exported {
				im.Alias = local
			}
		}
		out = append(out, im)
	}
	return out
}

func childOfType(n *sitter.Node, typ string) *sitter.Node {
	for i := 0; i < int(n.ChildCount()); i++ {
		if c := n.Child(uint(i)); c != nil && c.Kind() == typ {
			return c
		}
	}
	return nil
}
