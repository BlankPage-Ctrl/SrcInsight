package typescript

import (
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"

	"srcinsight/internal/convention"
	"srcinsight/internal/extract"
	"srcinsight/internal/model"
)

// controllerPrefixes maps class names to their @Controller prefix in one file.
func controllerPrefixes(src []byte, root *sitter.Node) map[string]string {
	out := map[string]string{}
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if n.Kind() == "class_declaration" || n.Kind() == "abstract_class_declaration" {
			name := ""
			if id := n.ChildByFieldName("name"); id != nil {
				name = extract.Text(src, id)
			}
			if name != "" {
				if prefix, ok := classDecoratorArg(src, n, "Controller"); ok {
					out[name] = prefix
				}
			}
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			walk(n.NamedChild(uint(i)))
		}
	}
	walk(root)
	return out
}

// classDecoratorArg finds @Name('arg') on a class node.
func classDecoratorArg(src []byte, classNode *sitter.Node, want string) (string, bool) {
	parent := classNode.Parent()
	if parent == nil {
		return "", false
	}
	count := int(parent.NamedChildCount())
	idx := -1
	for i := 0; i < count; i++ {
		ch := parent.NamedChild(uint(i))
		if ch.StartByte() == classNode.StartByte() && ch.EndByte() == classNode.EndByte() {
			idx = i
			break
		}
	}
	if idx <= 0 {
		return "", false
	}
	for i := idx - 1; i >= 0; i-- {
		ch := parent.NamedChild(uint(i))
		if ch.Kind() != "decorator" {
			break
		}
		if decoratorName(src, ch) != want {
			continue
		}
		return decoratorStringArg(src, ch), true
	}
	return "", false
}

// decoratorStringArg returns the first string argument of a decorator call.
func decoratorStringArg(src []byte, deco *sitter.Node) string {
	var call *sitter.Node
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if call != nil {
			return
		}
		if n.Kind() == "call_expression" {
			call = n
			return
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			walk(n.NamedChild(uint(i)))
		}
	}
	walk(deco)
	if call == nil {
		return ""
	}
	args := call.ChildByFieldName("arguments")
	if args == nil {
		return ""
	}
	for i := 0; i < int(args.NamedChildCount()); i++ {
		if lit := stringLiteral(src, args.NamedChild(uint(i))); lit != "" {
			return lit
		}
	}
	return ""
}

// stringLiteral unwraps string and simple template literals; dynamic templates yield "".
func stringLiteral(src []byte, n *sitter.Node) string {
	switch n.Kind() {
	case "string":
		raw := extract.Text(src, n)
		return trimQuotes(raw)
	case "template_string":
		raw := extract.Text(src, n)
		if strings.Contains(raw, "${") {
			return templateToPattern(raw)
		}
		return trimQuotes(raw)
	default:
		return ""
	}
}

// templateToPattern converts `/api/u/${id}` into `/api/u/{param}`.
func templateToPattern(raw string) string {
	s := trimQuotes(raw)
	var b strings.Builder
	for i := 0; i < len(s); {
		if i+1 < len(s) && s[i] == '$' && s[i+1] == '{' {
			depth := 1
			j := i + 2
			for j < len(s) && depth > 0 {
				switch s[j] {
				case '{':
					depth++
				case '}':
					depth--
				}
				j++
			}
			b.WriteString("{param}")
			i = j
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// urlPattern extracts a matchable URL from fetch-style arguments. Plain and
// template literals stay as-is; `+` chains fold dynamic operands into
// {param} so "/cats/" + id yields "/cats/{param}".
func urlPattern(src []byte, n *sitter.Node) string {
	if lit := stringLiteral(src, n); lit != "" {
		return lit
	}
	if n.Kind() != "binary_expression" {
		return ""
	}
	left := urlPattern(src, n.ChildByFieldName("left"))
	right := urlPattern(src, n.ChildByFieldName("right"))
	switch {
	case left != "" && right != "":
		return left + right
	case left != "":
		return left + "{param}"
	case right != "":
		return "{param}" + right
	default:
		return ""
	}
}

// transportEndpointsForDef builds server hints for one method definition.
func transportEndpointsForDef(src []byte, def *sitter.Node, fn model.Function, prefixes map[string]string) []model.TransportEndpoint {
	parent := def.Parent()
	if parent == nil {
		return nil
	}
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
	container := ""
	if fn.Container != nil {
		container = fn.Container.Name
	}
	var out []model.TransportEndpoint
	for i := idx - 1; i >= 0; i-- {
		ch := parent.NamedChild(uint(i))
		if ch.Kind() != "decorator" {
			break
		}
		name := decoratorName(src, ch)
		if convention.IsNestMethodDecor(name) {
			arg := decoratorStringArg(src, ch)
			out = append(out, model.TransportEndpoint{
				Transport: "http",
				Method:    name,
				Path:      convention.JoinPrefix(prefixes[container], arg),
				Framework: "nestjs",
				Func:      fn.QualifiedName(),
				Line:      fn.LineRange.StartLine,
			})
			continue
		}
		if convention.IsNestGQLMethodDecor(name) {
			op := decoratorStringArg(src, ch)
			if op == "" {
				op = fn.Name
			}
			out = append(out, model.TransportEndpoint{
				Transport: "graphql",
				Operation: op,
				Framework: "nestjs-gql",
				Func:      fn.QualifiedName(),
				Line:      fn.LineRange.StartLine,
			})
		}
	}
	return out
}

// transportCallsForDef walks one def body for client fetch hints.
func transportCallsForDef(src []byte, def *sitter.Node, fn model.Function, byLocal map[string]string) []model.TransportCall {
	var out []model.TransportCall
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if n.Kind() == "call_expression" {
			if c := classifyCall(src, n, fn, byLocal); c != nil {
				out = append(out, *c)
			}
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			walk(n.NamedChild(uint(i)))
		}
	}
	walk(def)
	return out
}

// classifyCall maps one call_expression to a client hint, or nil when not a transport call.
func classifyCall(src []byte, call *sitter.Node, fn model.Function, byLocal map[string]string) *model.TransportCall {
	fnNode := call.ChildByFieldName("function")
	if fnNode == nil {
		return nil
	}
	var recv, name string
	switch fnNode.Kind() {
	case "identifier":
		name = extract.Text(src, fnNode)
	case "member_expression":
		if obj := fnNode.ChildByFieldName("object"); obj != nil {
			recv = extract.Text(src, obj)
		}
		if prop := fnNode.ChildByFieldName("property"); prop != nil {
			name = extract.Text(src, prop)
		}
	default:
		return nil
	}
	lib := convention.ClientLib(recv, name, byLocal)
	if lib == "" {
		if recv == "router" || recv == "app" {
			return expressEndpoint(src, call, fn, recv, name)
		}
		return nil
	}
	args := call.ChildByFieldName("arguments")
	if args == nil || int(args.NamedChildCount()) == 0 {
		return nil
	}
	url := ""
	for i := 0; i < int(args.NamedChildCount()); i++ {
		if lit := urlPattern(src, args.NamedChild(uint(i))); lit != "" {
			url = lit
			break
		}
	}
	if url == "" {
		return nil
	}
	method := ""
	switch lib {
	case "bare-fetch", "nuxt-fetch":
		method = fetchInitMethod(src, args)
	case "axios":
		method = convention.AxiosMethod(name)
	}
	kind := "http"
	op := ""
	if lib == "gql-client" {
		kind = "graphql"
		op = url
		url = ""
	}
	return &model.TransportCall{
		Transport: string(kind),
		Method:    method,
		URL:       url,
		Operation: op,
		Lib:       lib,
		Func:      fn.QualifiedName(),
		Line:      int(call.StartPosition().Row) + 1,
	}
}

// expressEndpoint handles router.get('/path', handler) as a server hint.
func expressEndpoint(src []byte, call *sitter.Node, fn model.Function, recv, name string) *model.TransportCall {
	method, ok := convention.ExpressMethod(name)
	if !ok {
		return nil
	}
	args := call.ChildByFieldName("arguments")
	if args == nil || int(args.NamedChildCount()) == 0 {
		return nil
	}
	path := stringLiteral(src, args.NamedChild(0))
	if path == "" {
		return nil
	}
	return &model.TransportCall{
		Transport: "http",
		Method:    method,
		URL:       "__endpoint__:" + convention.NormalizePath(path),
		Lib:       "express-route",
		Func:      fn.QualifiedName(),
		Line:      int(call.StartPosition().Row) + 1,
	}
}

// fileLevelExpressRoutes catches router.get('/path') at module scope.
func fileLevelExpressRoutes(src []byte, root *sitter.Node) []model.TransportEndpoint {
	var out []model.TransportEndpoint
	var walk func(n *sitter.Node, depth int)
	walk = func(n *sitter.Node, depth int) {
		if n.Kind() == "call_expression" && depth <= 3 {
			if ep := expressFileEndpoint(src, n); ep != nil {
				out = append(out, *ep)
			}
		}
		if n.Kind() == "function_declaration" || n.Kind() == "method_definition" || n.Kind() == "arrow_function" {
			return
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			walk(n.NamedChild(uint(i)), depth+1)
		}
	}
	walk(root, 0)
	return out
}

// expressFileEndpoint maps a file-scope router call to an endpoint hint.
func expressFileEndpoint(src []byte, call *sitter.Node) *model.TransportEndpoint {
	fnNode := call.ChildByFieldName("function")
	if fnNode == nil || fnNode.Kind() != "member_expression" {
		return nil
	}
	obj := fnNode.ChildByFieldName("object")
	prop := fnNode.ChildByFieldName("property")
	if obj == nil || prop == nil {
		return nil
	}
	recv := extract.Text(src, obj)
	if recv != "router" && recv != "app" {
		return nil
	}
	method, ok := convention.ExpressMethod(extract.Text(src, prop))
	if !ok {
		return nil
	}
	args := call.ChildByFieldName("arguments")
	if args == nil || int(args.NamedChildCount()) == 0 {
		return nil
	}
	path := stringLiteral(src, args.NamedChild(0))
	if path == "" {
		return nil
	}
	return &model.TransportEndpoint{
		Transport: "http",
		Method:    method,
		Path:      convention.NormalizePath(path),
		Framework: "express",
		Line:      int(call.StartPosition().Row) + 1,
	}
}

// fetchInitMethod reads {method: 'POST'} from fetch's second argument.
func fetchInitMethod(src []byte, args *sitter.Node) string {
	if int(args.NamedChildCount()) < 2 {
		return ""
	}
	init := args.NamedChild(1)
	if init == nil || init.Kind() != "object" {
		return ""
	}
	for i := 0; i < int(init.NamedChildCount()); i++ {
		pair := init.NamedChild(uint(i))
		if pair.Kind() != "pair" {
			continue
		}
		key := pair.ChildByFieldName("key")
		val := pair.ChildByFieldName("value")
		if key == nil || val == nil {
			continue
		}
		if extract.Text(src, key) != "method" {
			continue
		}
		if lit := stringLiteral(src, val); lit != "" {
			return lit
		}
	}
	return ""
}
