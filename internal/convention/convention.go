// Package convention holds non-native knowledge: test paths, generated shapes, builtins, and framework patterns.
// It uses stdlib and model only, with no I/O, so extractors, resolvers, and rankers share one value.
//
// Framework support grows via Register; call sites never hardcode framework names.
package convention

// Framework describes one framework's extract-relevant shapes; later phases may add routes without changing call sites.
type Framework struct {
	Name          string
	FileSuffixes  []string
	ClassDecors   []string
	MethodDecors  []string
	ParamDecors   []string
	ScriptExts    []string
	HookPrefixes  []string
	ComponentTags []string
}

var registry = map[string]Framework{}

// Register adds or replaces a framework entry. Later calls win so tests
// and future framework packages can override the built-in stub.
func Register(f Framework) {
	if f.Name == "" {
		return
	}
	registry[f.Name] = f
}

// Registered returns the framework entry by name, if present.
func Registered(name string) (Framework, bool) {
	f, ok := registry[name]
	return f, ok
}

// Frameworks returns all registered frameworks in deterministic order.
func Frameworks() []Framework {
	names := make([]string, 0, len(registry))
	for n := range registry {
		names = append(names, n)
	}
	for i := 1; i < len(names); i++ {
		for j := i; j > 0 && names[j] < names[j-1]; j-- {
			names[j], names[j-1] = names[j-1], names[j]
		}
	}
	out := make([]Framework, 0, len(names))
	for _, n := range names {
		out = append(out, registry[n])
	}
	return out
}
