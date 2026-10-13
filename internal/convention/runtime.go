package convention

import "srcinsight/internal/model"

// TSBuiltins are TypeScript/global runtime names: JSON.parse and bare fetch
// both count. Mirrors the resolver's scope classification.
var TSBuiltins = map[string]struct{}{
	"JSON": {}, "Buffer": {}, "console": {}, "Math": {}, "Object": {},
	"Array": {}, "String": {}, "Number": {}, "Boolean": {}, "Promise": {},
	"process": {}, "require": {}, "module": {}, "exports": {}, "fetch": {},
	"setTimeout": {}, "setInterval": {}, "clearTimeout": {}, "clearInterval": {},
	"Map": {}, "Set": {}, "WeakMap": {}, "WeakSet": {}, "Symbol": {},
	"Reflect": {}, "Proxy": {}, "Error": {}, "RegExp": {}, "Date": {},
}

// GoBuiltins are Go predeclared identifiers, including types doubling as conversions like int(x).
// Without them conversions site as unresolved calls instead of external:runtime.
var GoBuiltins = map[string]struct{}{
	"len": {}, "cap": {}, "make": {}, "new": {}, "append": {},
	"copy": {}, "delete": {}, "panic": {}, "recover": {}, "print": {},
	"println": {}, "error": {}, "nil": {}, "true": {}, "false": {},
	"iota": {},
	"bool": {}, "string": {}, "int": {}, "int8": {}, "int16": {},
	"int32": {}, "int64": {}, "uint": {}, "uint8": {}, "uint16": {},
	"uint32": {}, "uint64": {}, "uintptr": {}, "float32": {}, "float64": {},
	"complex64": {}, "complex128": {}, "byte": {}, "rune": {},
	"any": {}, "comparable": {},
}

var PyBuiltins = map[string]struct{}{
	"print": {}, "len": {}, "range": {}, "str": {}, "int": {}, "float": {},
	"bool": {}, "list": {}, "dict": {}, "set": {}, "tuple": {}, "open": {},
	"super": {}, "isinstance": {}, "issubclass": {}, "type": {}, "enumerate": {},
	"zip": {}, "map": {}, "filter": {}, "sum": {}, "min": {}, "max": {},
	"abs": {}, "repr": {}, "sorted": {}, "reversed": {}, "any": {}, "all": {},
	"hasattr": {}, "getattr": {}, "setattr": {}, "callable": {}, "iter": {},
	"next": {}, "Exception": {}, "ValueError": {}, "TypeError": {},
}

// IsRuntimeBuiltin reports whether recv (or the bare name) is a language
// builtin. Either side matching is enough: "JSON.parse" and bare "fetch".
func IsRuntimeBuiltin(recv, probe string, dialect model.Language) bool {
	switch dialect {
	case model.LangPython:
		_, ok := PyBuiltins[probe]
		return ok
	case model.LangTypeScript:
		if _, ok := TSBuiltins[recv]; ok {
			return true
		}
		_, ok := TSBuiltins[probe]
		return ok
	case model.LangGo:
		if _, ok := GoBuiltins[recv]; ok {
			return true
		}
		_, ok := GoBuiltins[probe]
		return ok
	default:
		return false
	}
}
