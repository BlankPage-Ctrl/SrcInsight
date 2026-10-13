package typescript

import (
	sitter "github.com/tree-sitter/go-tree-sitter"

	"srcinsight/internal/extract"
	"srcinsight/internal/model"
)

// utilityTypes lists wrapper types unwrapped recursively to base types.
var utilityTypes = map[string]struct{}{
	"Pick":                  {},
	"Omit":                  {},
	"Partial":               {},
	"Required":              {},
	"Readonly":              {},
	"Record":                {},
	"Exclude":               {},
	"Extract":               {},
	"NonNullable":           {},
	"ReturnType":            {},
	"Parameters":            {},
	"ConstructorParameters": {},
	"InstanceType":          {},
	"Awaited":               {},
	"Promise":               {},
	"Array":                 {},
	"ReadonlyArray":         {},
}

// primitiveTypes lists built-in types that never wire to user nodes.
var primitiveTypes = map[string]struct{}{
	"string":    {},
	"number":    {},
	"boolean":   {},
	"void":      {},
	"any":       {},
	"unknown":   {},
	"never":     {},
	"null":      {},
	"undefined": {},
	"bigint":    {},
	"symbol":    {},
	"object":    {},
	"true":      {},
	"false":     {},
}

func isUtility(name string) bool {
	_, ok := utilityTypes[name]
	return ok
}

func isPrimitive(name string) bool {
	_, ok := primitiveTypes[name]
	return ok
}

// collectParamTypes extracts base type refs from params; wrappers collapse inward.
// Arrow-valued methods read types off the inner arrow.
func collectParamTypes(src []byte, def *sitter.Node) []model.FunctionRef {
	params := functionForParams(def).ChildByFieldName("parameters")
	if params == nil {
		return nil
	}
	var out []model.FunctionRef
	seen := make(map[string]struct{})
	for i := 0; i < int(params.NamedChildCount()); i++ {
		param := params.NamedChild(uint(i))
		// Type lives in the type field.
		typeNode := param.ChildByFieldName("type")
		if typeNode == nil {
			continue
		}
		var refs []model.FunctionRef
		collectTypeRefs(src, typeNode, &refs)
		for _, r := range refs {
			if _, ok := seen[r.Name]; ok {
				continue
			}
			seen[r.Name] = struct{}{}
			out = append(out, r)
		}
	}
	return out
}

// collectReturnTypes extracts base type refs from the return annotation.
func collectReturnTypes(src []byte, def *sitter.Node) []model.FunctionRef {
	rt := functionForParams(def).ChildByFieldName("return_type")
	if rt == nil {
		return nil
	}
	var out []model.FunctionRef
	collectTypeRefs(src, rt, &out)
	seen := make(map[string]struct{})
	var dedup []model.FunctionRef
	for _, r := range out {
		if _, ok := seen[r.Name]; ok {
			continue
		}
		seen[r.Name] = struct{}{}
		dedup = append(dedup, r)
	}
	return dedup
}

// functionForParams returns the node holding params; arrow values descend inward.
func functionForParams(def *sitter.Node) *sitter.Node {
	if def == nil {
		return nil
	}
	if def.ChildByFieldName("parameters") != nil || def.ChildByFieldName("return_type") != nil {
		return def
	}
	if v := def.ChildByFieldName("value"); v != nil && (v.Kind() == "arrow_function" || v.Kind() == "function_expression") {
		return v
	}
	return def
}

// collectTypeRefs walks a type node; wrappers unwrap, primitives skip.
func collectTypeRefs(src []byte, n *sitter.Node, out *[]model.FunctionRef) {
	if n == nil {
		return
	}
	switch n.Kind() {
	case "type_identifier", "identifier", "nested_type_identifier":
		name := extract.Text(src, n)
		// Nested names stay full; the resolver splits recv.
		if isPrimitive(name) {
			return
		}
		*out = append(*out, model.FunctionRef{
			Name:      name,
			LineRange: extract.NodeRange(n),
		})
		return
	case "generic_type":
		nameNode := n.ChildByFieldName("name")
		baseName := ""
		if nameNode != nil {
			baseName = extract.Text(src, nameNode)
		}
		args := n.ChildByFieldName("type_arguments")
		if isUtility(baseName) {
			if args != nil {
				for i := 0; i < int(args.NamedChildCount()); i++ {
					collectTypeRefs(src, args.NamedChild(uint(i)), out)
				}
			}
			return
		}
		// Non-utility generics emit the outer name only.
		if baseName != "" && !isPrimitive(baseName) && !isUtility(baseName) {
			*out = append(*out, model.FunctionRef{
				Name:      baseName,
				LineRange: extract.NodeRange(nameNode),
			})
		}
		return
	case "array_type":
		for i := 0; i < int(n.NamedChildCount()); i++ {
			collectTypeRefs(src, n.NamedChild(uint(i)), out)
		}
		return
	case "union_type", "intersection_type", "tuple_type", "conditional_type":
		for i := 0; i < int(n.NamedChildCount()); i++ {
			collectTypeRefs(src, n.NamedChild(uint(i)), out)
		}
		return
	case "type_annotation", "optional_type", "rest_type", "readonly_type":
		for i := 0; i < int(n.NamedChildCount()); i++ {
			collectTypeRefs(src, n.NamedChild(uint(i)), out)
		}
		return
	case "predefined_type":
		txt := extract.Text(src, n)
		if isPrimitive(txt) {
			return
		}
		if txt != "" {
			*out = append(*out, model.FunctionRef{
				Name:      txt,
				LineRange: extract.NodeRange(n),
			})
		}
		return
	case "literal_type", "template_literal", "mapped_type", "indexed_access_type", "type_query", "infer_type", "type_predicate", "constructor_type", "function_type":
		return
	default:
		// Fall back to recursing named children.
		for i := 0; i < int(n.NamedChildCount()); i++ {
			collectTypeRefs(src, n.NamedChild(uint(i)), out)
		}
	}
}
