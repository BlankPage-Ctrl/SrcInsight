package typescript

import (
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"

	"srcinsight/internal/extract"
	"srcinsight/internal/model"
)

// collectRecvBindings binds declared names to types; annotations win, calls defer.
// Same-name repeats stay in source order for the resolver to try each.
func collectRecvBindings(q *dialectQueries, src []byte, root *sitter.Node) []model.RecvBinding {
	var out []model.RecvBinding
	seen := make(map[model.RecvBinding]struct{})
	keep := func(b model.RecvBinding) {
		if b.Recv == "" || b.Type == "" && b.ViaCall == "" {
			return
		}
		// Annotation and initializer each propose; annotation first.
		if _, dup := seen[b]; dup {
			return
		}
		seen[b] = struct{}{}
		out = append(out, b)
	}

	qc := sitter.NewQueryCursor()
	defer qc.Close()
	matches := qc.Matches(q.constQuery, root, src)
	for m := matches.Next(); m != nil; m = matches.Next() {
		for _, c := range m.Captures {
			if c.Index != q.constDeclID {
				continue
			}
			d := c.Node
			nameNode := d.ChildByFieldName("name")
			if nameNode == nil || nameNode.Kind() != "identifier" {
				continue
			}
			recv := extract.Text(src, nameNode)
			if recv == "" {
				continue
			}
			if t := d.ChildByFieldName("type"); t != nil {
				var refs []model.FunctionRef
				collectTypeRefs(src, t, &refs)
				if len(refs) > 0 {
					keep(model.RecvBinding{Recv: recv, Type: simpleType(refs[0].Name)})
				}
			}
			v := d.ChildByFieldName("value")
			if v == nil {
				continue
			}
			if typ, ok := unwrapValueType(src, v); ok {
				keep(model.RecvBinding{Recv: recv, Type: typ})
				continue
			}
			if fac := factoryCall(src, v); fac != "" {
				keep(model.RecvBinding{Recv: recv, ViaCall: fac})
			}
		}
	}
	return out
}

// unwrapValueType reads a direct type out of an initializer through wrappers.
func unwrapValueType(src []byte, v *sitter.Node) (string, bool) {
	for v != nil {
		switch v.Kind() {
		case "new_expression":
			if c := v.ChildByFieldName("constructor"); c != nil {
				if t := simpleType(extract.Text(src, c)); t != "" {
					return t, true
				}
			}
			for i := 0; i < int(v.NamedChildCount()); i++ {
				ch := v.NamedChild(uint(i))
				switch ch.Kind() {
				case "identifier", "type_identifier", "member_expression", "nested_type_identifier":
					if t := simpleType(extract.Text(src, ch)); t != "" {
						return t, true
					}
				}
			}
			return "", false
		case "as_expression", "satisfies_expression":
			// Type is the non-call named child.
			for i := 0; i < int(v.NamedChildCount()); i++ {
				ch := v.NamedChild(uint(i))
				switch ch.Kind() {
				case "call_expression", "arguments":
					continue
				}
				var refs []model.FunctionRef
				collectTypeRefs(src, ch, &refs)
				if len(refs) > 0 {
					return simpleType(refs[0].Name), true
				}
			}
			return "", false
		case "type_assertion":
			for i := 0; i < int(v.NamedChildCount()); i++ {
				ch := v.NamedChild(uint(i))
				var refs []model.FunctionRef
				collectTypeRefs(src, ch, &refs)
				if len(refs) > 0 {
					return simpleType(refs[0].Name), true
				}
			}
			return "", false
		case "await_expression", "parenthesized_expression", "non_null_expression":
			v = valueChild(v)
		default:
			return "", false
		}
	}
	return "", false
}

func valueChild(v *sitter.Node) *sitter.Node {
	for i := 0; i < int(v.NamedChildCount()); i++ {
		ch := v.NamedChild(uint(i))
		switch ch.Kind() {
		case "type_identifier", "type_arguments", "as", "satisfies":
			continue
		default:
			return ch
		}
	}
	return nil
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
			// Chained calls record the base factory.
			if i := strings.Index(callee, "("); i >= 0 {
				callee = callee[:i]
			}
			if i := strings.Index(callee, "<"); i >= 0 {
				callee = callee[:i]
			}
			callee = strings.ReplaceAll(callee, "?.", ".")
			callee = strings.TrimSpace(callee)
			if callee == "" || strings.ContainsAny(callee, " \t\n\r(){};,") {
				return ""
			}
			return callee
		case "await_expression", "parenthesized_expression", "non_null_expression":
			v = valueChild(v)
		default:
			return ""
		}
	}
	return ""
}

// collectParamBinds pairs each typed parameter with its first base type.
func collectParamBinds(src []byte, def *sitter.Node) []model.RecvBinding {
	params := functionForParams(def).ChildByFieldName("parameters")
	if params == nil {
		return nil
	}
	var out []model.RecvBinding
	for i := 0; i < int(params.NamedChildCount()); i++ {
		p := params.NamedChild(uint(i))
		n := p.ChildByFieldName("name")
		if n == nil {
			n = p.ChildByFieldName("pattern")
		}
		if n == nil {
			continue
		}
		// Rest patterns wrap the identifier.
		if n.Kind() == "rest_pattern" {
			found := false
			for j := 0; j < int(n.NamedChildCount()); j++ {
				if ch := n.NamedChild(uint(j)); ch.Kind() == "identifier" {
					n = ch
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}
		if n.Kind() != "identifier" {
			continue
		}
		t := p.ChildByFieldName("type")
		if t == nil {
			continue
		}
		var refs []model.FunctionRef
		collectTypeRefs(src, t, &refs)
		if len(refs) == 0 {
			continue
		}
		out = append(out, model.RecvBinding{Recv: extract.Text(src, n), Type: simpleType(refs[0].Name)})
	}
	return out
}

// simpleType reduces a qualified name to its last segment.
func simpleType(name string) string {
	if i := strings.LastIndex(name, "."); i >= 0 {
		name = name[i+1:]
	}
	name = strings.TrimSpace(name)
	if name == "" || isPrimitive(name) {
		return ""
	}
	return name
}
