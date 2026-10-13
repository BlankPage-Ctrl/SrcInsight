package frontend

import (
	"strings"
	"testing"

	"srcinsight/internal/model"
)

const optionsSFC = `<template>
  <div>
    <UserBadge :level="level" />
    <button @click="save">Save {{ label }}</button>
    <p v-if="visible">{{ full }}</p>
  </div>
</template>

<script lang="ts">
import UserBadge from './UserBadge.vue';
export default {
  methods: {
    save() { this.load(); },
    load() { return 1; }
  },
  computed: {
    full() { return 2; }
  }
}
</script>

<style>.x { color: red; }</style>
`

func TestSplitOptions(t *testing.T) {
	sfc := Split([]byte(optionsSFC))
	if len(sfc.Scripts) != 1 {
		t.Fatalf("scripts = %d, want 1", len(sfc.Scripts))
	}
	sb := sfc.Scripts[0]
	if sb.ParserExt != ".ts" {
		t.Errorf("ParserExt = %q, want .ts", sb.ParserExt)
	}
	if sb.Setup {
		t.Errorf("Setup = true, want false")
	}
	if !strings.Contains(string(sb.Content), "save()") {
		t.Errorf("script content missing methods")
	}
	if !sfc.HasTemplate {
		t.Fatalf("HasTemplate = false, want true")
	}
	if sfc.TemplateStartLine != 1 {
		t.Errorf("TemplateStartLine = %d, want 1", sfc.TemplateStartLine)
	}
	// Script content starts on the <script> line itself (line 9).
	if sb.StartLine != 9 {
		t.Errorf("Script StartLine = %d, want 9", sb.StartLine)
	}
}

func TestSplitSetup(t *testing.T) {
	src := "<template><button @click=\"go\">x</button></template>\n<script setup lang=\"tsx\">\nconst go = () => 1;\n</script>\n"
	sfc := Split([]byte(src))
	if len(sfc.Scripts) != 1 {
		t.Fatalf("scripts = %d, want 1", len(sfc.Scripts))
	}
	if !sfc.Scripts[0].Setup {
		t.Errorf("Setup = false, want true")
	}
	if sfc.Scripts[0].ParserExt != ".tsx" {
		t.Errorf("ParserExt = %q, want .tsx", sfc.Scripts[0].ParserExt)
	}
}

func TestTemplateRefsOptions(t *testing.T) {
	sfc := Split([]byte(optionsSFC))
	refs := harvestTemplate(sfc.Template, sfc.TemplateStartLine)
	byName := map[string]int{}
	for _, r := range refs {
		byName[r.Name] = r.Line
	}
	for _, want := range []string{"UserBadge", "save", "label", "visible", "full", "level"} {
		if _, ok := byName[want]; !ok {
			t.Errorf("missing template ref %q; got %v", want, refs)
		}
	}
	for _, native := range []string{"div", "button", "p"} {
		if _, ok := byName[native]; ok {
			t.Errorf("native tag %q must be skipped; got %v", native, refs)
		}
	}
	if line := byName["save"]; line != 4 {
		t.Errorf("save line = %d, want 4", line)
	}
}

func TestTemplateRefsKebab(t *testing.T) {
	refs := harvestTemplate(`<user-card :user="u" />`, 1)
	if len(refs) == 0 || refs[0].Name != "UserCard" {
		t.Errorf("kebab tag must map to UserCard; got %v", refs)
	}
}

func TestShiftAndAttach(t *testing.T) {
	fe := &model.FileExtract{
		Functions: []model.Function{
			{FunctionRef: model.FunctionRef{Name: "save", LineRange: model.Range{StartLine: 5, EndLine: 5}}, Kind: model.KindMethod},
			{FunctionRef: model.FunctionRef{Name: "helper", LineRange: model.Range{StartLine: 1, EndLine: 3}}, Kind: model.KindFunction},
		},
	}
	ShiftLines(fe, 9, "src/Card.vue")
	if fe.Functions[0].LineRange.StartLine != 14 {
		t.Errorf("shifted start = %d, want 14", fe.Functions[0].LineRange.StartLine)
	}
	if fe.Functions[0].FilePath != "src/Card.vue" {
		t.Errorf("FilePath = %q", fe.Functions[0].FilePath)
	}
	AttachComponent(fe, "Card")
	if fe.Functions[0].Container == nil || fe.Functions[0].Container.Name != "Card" {
		t.Errorf("method container = %+v, want Card", fe.Functions[0].Container)
	}
	if fe.Functions[0].QualifiedName() != "Card.save" {
		t.Errorf("qualified = %q, want Card.save", fe.Functions[0].QualifiedName())
	}
	if fe.Functions[1].Container != nil {
		t.Errorf("plain function must stay file-level, got %+v", fe.Functions[1].Container)
	}
}

func TestSyntheticRoot(t *testing.T) {
	root := SyntheticRoot("src/Card.vue", "Card", []model.FunctionRef{
		{Name: "save", SiteRange: model.Range{StartLine: 4, EndLine: 4}},
	})
	if root.Kind != model.KindClassDef || root.Name != "Card" {
		t.Errorf("root = %+v, want class Card", root)
	}
	if len(root.Tags) != 1 || root.Tags[0] != "vue:component" {
		t.Errorf("tags = %v, want [vue:component]", root.Tags)
	}
	if len(root.Calls) != 1 || root.Calls[0].Name != "save" {
		t.Errorf("calls = %+v, want [save]", root.Calls)
	}
}
