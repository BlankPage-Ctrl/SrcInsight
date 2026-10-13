package convention

import "strings"

// React components are PascalCase; lowercase tags are runtime, not nodes.
var (
	// ReactFileSuffixes marks React layout files by name.
	ReactFileSuffixes = []string{
		".jsx",
		".tsx",
	}
	// Plain scripts may still hold JSX once parsed with the TSX grammar.
	ReactScriptSuffixes = []string{
		".js",
		".mjs",
		".cjs",
	}
)

func init() {
	Register(Framework{
		Name:          "react",
		FileSuffixes:  append([]string(nil), ReactFileSuffixes...),
		ScriptExts:    append(append([]string(nil), ReactFileSuffixes...), ReactScriptSuffixes...),
		HookPrefixes:  []string{"use"},
		ComponentTags: []string{"react:component"},
	})
}

func hasAnySuffix(p string, exts []string) bool {
	lower := strings.ToLower(p)
	for _, suf := range exts {
		if strings.HasSuffix(lower, suf) {
			return true
		}
	}
	return false
}

func IsReactFile(p string) bool {
	fw, ok := Registered("react")
	if !ok {
		return false
	}
	return hasAnySuffix(p, fw.FileSuffixes) || hasAnySuffix(p, fw.ScriptExts)
}

// Member chains check the head; lowercase heads are native tags.
func IsComponentName(name string) bool {
	if name == "" {
		return false
	}
	head := name
	if i := strings.IndexAny(name, ".:"); i >= 0 {
		head = name[:i]
	}
	if head == "" {
		return false
	}
	c := head[0]
	return c >= 'A' && c <= 'Z'
}

// Hook names need more than the prefix alone.
func IsHookName(name string) bool {
	for _, fw := range Frameworks() {
		for _, p := range fw.HookPrefixes {
			if len(name) > len(p) && strings.HasPrefix(name, p) {
				c := name[len(p)]
				if (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
					return true
				}
			}
		}
	}
	return false
}

func IsNativeJSXTag(name string) bool {
	if name == "" {
		return false
	}
	head := name
	if i := strings.IndexAny(name, ".:"); i >= 0 {
		head = name[:i]
	}
	if head == "" {
		return false
	}
	c := head[0]
	return (c >= 'a' && c <= 'z') || c == '-'
}
