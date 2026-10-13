package convention

import "strings"

// Language stays typescript: no vault migration.
// Template bindings ride Calls on the synthetic component node.
var (
	VueFileSuffixes = []string{
		".vue",
	}
)

func init() {
	Register(Framework{
		Name:          "vue",
		FileSuffixes:  append([]string(nil), VueFileSuffixes...),
		ScriptExts:    append([]string(nil), VueFileSuffixes...),
		ComponentTags: []string{"vue:component"},
	})
}

func IsVueFile(p string) bool {
	fw, ok := Registered("vue")
	if !ok {
		return false
	}
	return hasAnySuffix(p, fw.FileSuffixes) || hasAnySuffix(p, fw.ScriptExts)
}

// VueComponentName derives the component name from a .vue path:
// "src/UserCard.vue" -> "UserCard". Non-vue paths yield "".
func VueComponentName(p string) string {
	if !IsVueFile(p) {
		return ""
	}
	base := p
	if i := strings.LastIndexAny(base, "/\\"); i >= 0 {
		base = base[i+1:]
	}
	name := strings.TrimSuffix(base, ".vue")
	name = strings.TrimSuffix(name, ".VUE")
	if name == "" || name == base {
		return ""
	}
	return name
}

// VueKebabToPascal maps template tag spellings to script names:
// "user-card" -> "UserCard". Already-Pascal input passes through.
func VueKebabToPascal(tag string) string {
	if tag == "" {
		return ""
	}
	if !strings.Contains(tag, "-") {
		return tag
	}
	var b strings.Builder
	upperNext := true
	for _, r := range tag {
		if r == '-' || r == '_' || r == ':' {
			upperNext = true
			continue
		}
		if upperNext {
			b.WriteString(strings.ToUpper(string(r)))
			upperNext = false
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
