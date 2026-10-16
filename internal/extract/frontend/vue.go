package frontend

import (
	"path/filepath"
	"regexp"
	"strings"

	"srcinsight/internal/convention"
	"srcinsight/internal/model"
)

// vuePlugin reads Vue single-file components: lexical template bindings
// plus script-block splitting for the TypeScript extractor.
type vuePlugin struct{}

func init() { Register(vuePlugin{}) }

func (vuePlugin) Name() string { return "vue" }

func (vuePlugin) Match(path string) bool { return convention.IsVueFile(path) }

// TemplateRefs converts template bindings into call-shaped references.
func (vuePlugin) TemplateRefs(_ string, src []byte) []model.FunctionRef {
	sfc := Split(src)
	if !sfc.HasTemplate {
		return nil
	}
	refs := harvestTemplate(sfc.Template, sfc.TemplateStartLine)
	out := make([]model.FunctionRef, 0, len(refs))
	for _, r := range refs {
		out = append(out, model.FunctionRef{
			Name:      r.Name,
			SiteRange: model.Range{StartLine: r.Line, EndLine: r.Line},
		})
	}
	return out
}

// componentTags reads the tag from the registry, with a literal fallback.
func componentTags() []string {
	if fw, ok := convention.Registered("vue"); ok && len(fw.ComponentTags) > 0 {
		return append([]string(nil), fw.ComponentTags...)
	}
	return []string{"vue:component"}
}

// ScriptBlock is one <script> section with its original line offset.
type ScriptBlock struct {
	Content []byte
	// StartLine is the 1-indexed original line of the block's first content byte.
	StartLine int
	Setup     bool
	// ParserExt selects the grammar: ".ts" for lang=ts, ".tsx" otherwise.
	ParserExt string
}

// SFC is a split single-file component.
type SFC struct {
	Scripts           []ScriptBlock
	Template          string
	TemplateStartLine int
	HasTemplate       bool
}

var (
	blockOpenRe = regexp.MustCompile(`(?i)<(script|template|style)\b([^>]*)>`)
	langAttrRe  = regexp.MustCompile(`(?i)\blang\s*=\s*("([^"]*)"|'([^']*)'|([^\s"'` + "`" + `>]+))`)
	setupAttrRe = regexp.MustCompile(`(?i)(?:^|\s)setup(?:\s|=|>|/|$)`)
	commentRe   = regexp.MustCompile(`(?s)<!--.*?-->`)
	mustacheRe  = regexp.MustCompile(`\{\{([\s\S]*?)\}\}`)
	// attrBindRe groups: 1 = quoted value, 2/3 = double/single-quoted inner.
	attrBindRe = regexp.MustCompile(`(?i)(?:@[\w:.-]+|v-on:[\w:.-]+|:[\w:.-]+|v-bind:[\w:.-]+|v-(?:if|show|for|model|slot)[\w:.-]*)\s*=\s*("([^"]*)"|'([^']*)')`)
	tagRe      = regexp.MustCompile(`<[A-Za-z][\w:.-]*`)
	identRe    = regexp.MustCompile(`[A-Za-z_$][\w$]*(?:\.[A-Za-z_$][\w$]*)*`)
)

var jsKeywords = map[string]struct{}{
	"true": {}, "false": {}, "null": {}, "undefined": {}, "this": {},
	"in": {}, "of": {}, "new": {}, "typeof": {}, "void": {},
	"delete": {}, "as": {}, "from": {}, "if": {}, "else": {},
	"for": {}, "while": {}, "return": {}, "function": {}, "await": {},
	"async": {}, "var": {}, "let": {}, "const": {},
}

// Split is lexical (no new grammar); unclosed blocks are ignored.
func Split(src []byte) SFC {
	var out SFC
	text := string(src)
	for _, loc := range blockOpenRe.FindAllStringSubmatchIndex(text, -1) {
		kind := strings.ToLower(text[loc[2]:loc[3]])
		attrs := text[loc[4]:loc[5]]
		contentStart := loc[1]
		rest := text[contentStart:]
		closeRe := regexp.MustCompile(sprintfClose(kind))
		cloc := closeRe.FindStringIndex(rest)
		if cloc == nil {
			continue
		}
		content := rest[:cloc[0]]
		startLine := 1 + strings.Count(text[:contentStart], "\n")
		switch kind {
		case "script":
			out.Scripts = append(out.Scripts, ScriptBlock{
				Content:   []byte(content),
				StartLine: startLine,
				Setup:     setupAttrRe.MatchString(" " + attrs + " "),
				ParserExt: parserExtFor(attrs),
			})
		case "template":
			if !out.HasTemplate {
				out.Template = content
				out.TemplateStartLine = startLine
				out.HasTemplate = true
			}
		}
	}
	return out
}

func sprintfClose(kind string) string {
	return `(?i)</` + kind + `\s*>`
}

func parserExtFor(attrs string) string {
	m := langAttrRe.FindStringSubmatch(attrs)
	lang := ""
	if m != nil {
		for _, g := range m[2:] {
			if g != "" {
				lang = strings.ToLower(strings.TrimSpace(g))
				break
			}
		}
	}
	switch lang {
	case "ts", "typescript":
		return ".ts"
	default:
		return ".tsx"
	}
}

type TemplateRef struct {
	Name string
	Line int
}

// Native tags are skipped; kebab-case maps to PascalCase script names.
func harvestTemplate(template string, startLine int) []TemplateRef {
	clean := commentRe.ReplaceAllString(template, " ")
	var out []TemplateRef
	seen := map[string]struct{}{}

	push := func(name string, line int) {
		name = strings.TrimSpace(name)
		if name == "" {
			return
		}
		// Attribute handlers may trail call args: save(item) -> save.
		if i := strings.Index(name, "("); i >= 0 {
			name = strings.TrimSpace(name[:i])
		}
		if name == "" {
			return
		}
		if _, dup := seen[name]; dup {
			return
		}
		seen[name] = struct{}{}
		out = append(out, TemplateRef{Name: name, Line: line})
	}

	lineOf := func(idx int) int {
		return startLine + strings.Count(clean[:idx], "\n")
	}

	// Closing tags repeat the name; dedupe keeps the opening line.
	for _, loc := range tagRe.FindAllStringIndex(clean, -1) {
		tag := clean[loc[0]+1 : loc[1]]
		if convention.IsNativeJSXTag(tag) && !strings.Contains(tag, "-") {
			continue
		}
		push(convention.VueKebabToPascal(tag), lineOf(loc[0]))
	}

	for _, loc := range mustacheRe.FindAllStringSubmatchIndex(clean, -1) {
		expr := clean[loc[2]:loc[3]]
		line := lineOf(loc[0])
		for _, id := range harvestIdents(expr) {
			push(id, line)
		}
	}

	for _, loc := range attrBindRe.FindAllStringSubmatchIndex(clean, -1) {
		val := ""
		if loc[6] >= 0 {
			val = clean[loc[6]:loc[7]]
		} else if loc[4] >= 0 {
			val = clean[loc[4]:loc[5]]
		}
		if val == "" {
			continue
		}
		line := lineOf(loc[0])
		for _, id := range harvestIdents(val) {
			push(id, line)
		}
	}
	return out
}

// Dotted identifier chains, minus JS keywords; unresolved names are fine.
func harvestIdents(expr string) []string {
	var out []string
	seen := map[string]struct{}{}
	for _, id := range identRe.FindAllString(expr, -1) {
		head := id
		if i := strings.Index(id, "."); i >= 0 {
			head = id[:i]
		}
		if _, kw := jsKeywords[head]; kw {
			continue
		}
		if _, kw := jsKeywords[id]; kw {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

// ShiftLines moves extractor ranges from block-relative to file-absolute
// and repoints containers at the real path.
func ShiftLines(fe *model.FileExtract, delta int, realPath string) {
	shift := func(r *model.Range) {
		r.StartLine += delta
		r.EndLine += delta
	}
	for i := range fe.Functions {
		f := &fe.Functions[i]
		f.FilePath = realPath
		f.File = model.Container{Kind: model.ContainerFile, Name: filepath.Base(realPath)}
		shift(&f.LineRange)
		for j := range f.Calls {
			shift(&f.Calls[j].SiteRange)
			shift(&f.Calls[j].LineRange)
		}
		for j := range f.Uses {
			shift(&f.Uses[j].SiteRange)
			shift(&f.Uses[j].LineRange)
		}
		for _, refs := range [][]model.FunctionRef{f.Extends, f.Implements, f.ParamTypes, f.ReturnTypes} {
			for j := range refs {
				shift(&refs[j].LineRange)
				shift(&refs[j].SiteRange)
			}
		}
	}
	for i := range fe.Endpoints {
		fe.Endpoints[i].Line += delta
	}
	for i := range fe.Calls {
		fe.Calls[i].Line += delta
	}
}

// Container-less methods bind to the component, so names read Card.save.
func AttachComponent(fe *model.FileExtract, comp string) {
	if comp == "" {
		return
	}
	for i := range fe.Functions {
		f := &fe.Functions[i]
		if f.Container != nil {
			continue
		}
		switch f.Kind {
		case model.KindMethod, model.KindAsyncMethod, model.KindConstructor:
			f.Container = &model.Container{Kind: model.ContainerClass, Name: comp}
		}
	}
}

// Kind class gives methods a backfill target; the tag marks identity.
func SyntheticRoot(realPath, comp string, calls []model.FunctionRef) model.Function {
	return model.Function{
		FunctionRef: model.FunctionRef{
			Name:      comp,
			FilePath:  realPath,
			LineRange: model.Range{StartLine: 1, EndLine: 1},
		},
		Kind:      model.KindClassDef,
		Signature: "vue component " + comp + " (" + filepath.Base(realPath) + ")",
		File:      model.Container{Kind: model.ContainerFile, Name: filepath.Base(realPath)},
		Calls:     calls,
		Tags:      componentTags(),
	}
}

func DedupeImports(imps []model.ImportStmt) []model.ImportStmt {
	seen := map[model.ImportStmt]struct{}{}
	out := imps[:0]
	for _, im := range imps {
		if _, dup := seen[im]; dup {
			continue
		}
		seen[im] = struct{}{}
		out = append(out, im)
	}
	return out
}
