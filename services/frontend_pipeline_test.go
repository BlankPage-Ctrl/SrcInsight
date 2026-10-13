package services

import (
	"context"
	"strings"
	"testing"

	"srcinsight/internal/model"
)

// React (.tsx/.jsx) plus Vue SFCs: JSX/template links, tags, .vue dialect.
func TestFrontendFullScanEndToEnd(t *testing.T) {
	ctx := context.Background()
	p, _ := testPipeline(t, map[string]string{
		"src/UserCard.tsx": `export function UserCard(props: any) {
  return (<span>{props.title}</span>);
}
`,
		"src/Feed.tsx": `import UserCard from './UserCard';
import { useState } from 'react';
export function Feed() {
  const [x, setX] = useState(0);
  return (<div><UserCard title={x} /></div>);
}
export function useFetch(url: string) {
  return url;
}
`,
		"src/plain.jsx": `import { Feed } from './Feed';
export function Root() {
  return (<main><Feed /></main>);
}
`,
		"src/Card.vue": `<template>
  <div>
    <button @click="save">Save {{ label }}</button>
  </div>
</template>
<script lang="ts">
export default {
  methods: {
    save() { this.load(); },
    load() { return 1; }
  }
}
</script>
`,
		"src/Setup.vue": `<template>
  <button @click="go">Go</button>
</template>
<script setup lang="ts">
import { ref } from 'vue';
export function go() { return 1; }
const n = ref(0);
</script>
`,
	})
	defer p.Close()

	if _, err := p.FullScan(ctx, true); err != nil {
		t.Fatal(err)
	}

	links, err := p.Vault().FetchAllLinks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	has := func(fromSub, toSub, kind string) bool {
		for _, l := range links {
			if strings.Contains(string(l.Origin), fromSub) &&
				strings.Contains(string(l.Target), toSub) && l.Kind == kind {
				return true
			}
		}
		return false
	}
	if !has("Feed.tsx:function:Feed", "UserCard.tsx:function:UserCard", model.EdgeCalls) {
		t.Errorf("missing Feed→UserCard JSX link: %+v", links)
	}
	// External hook calls keep as loose ends, not links.
	loose, err := p.Vault().FetchAllLooseEnds(ctx)
	if err != nil {
		t.Fatal(err)
	}
	hookKept := false
	for _, l := range loose {
		if strings.Contains(string(l.From), "Feed.tsx:function:Feed") && l.Name == "useState" {
			hookKept = true
		}
	}
	if !hookKept {
		t.Errorf("missing Feed→useState loose end (external hook call): %+v", loose)
	}
	if !has("plain.jsx:function:Root", "Feed.tsx:function:Feed", model.EdgeCalls) {
		t.Errorf("missing Root→Feed JSX link: %+v", links)
	}
	if !has("Card.vue:class:Card", "Card.vue:method:Card.save", model.EdgeCalls) {
		t.Errorf("missing Card→Card.save template link: %+v", links)
	}
	if !has("Setup.vue:class:Setup", "Setup.vue:function:go", model.EdgeCalls) {
		t.Errorf("missing Setup→go template link: %+v", links)
	}
	if !has("Card.vue:method:Card.save", "Card.vue:method:Card.load", model.EdgeCalls) {
		t.Errorf("missing Card.save→Card.load link: %+v", links)
	}

	entries, err := p.Vault().FetchAllEntries(ctx)
	if err != nil {
		t.Fatal(err)
	}
	tags := map[string][]string{}
	dialect := map[string]model.Language{}
	for _, e := range entries {
		tags[string(e.ID)] = e.Tags
		dialect[string(e.ID)] = e.Dialect
	}
	wantTags := map[string]string{
		"typescript:src/Feed.tsx:function:Feed:1":     "react:component",
		"typescript:src/Feed.tsx:function:useFetch:1": "hook",
		"typescript:src/Card.vue:class:Card:1":        "vue:component",
		"typescript:src/Setup.vue:class:Setup:1":      "vue:component",
	}
	for id, want := range wantTags {
		got, ok := tags[id]
		if !ok {
			t.Errorf("missing entry %s; have %v", id, keysOf(tags))
			continue
		}
		found := false
		for _, g := range got {
			if g == want {
				found = true
			}
		}
		if !found {
			t.Errorf("entry %s tags = %v, want %q", id, got, want)
		}
	}
	// .vue entries keep the typescript dialect: no vault migration.
	for id, d := range dialect {
		if strings.Contains(id, ".vue:") && d != model.LangTypeScript {
			t.Errorf("entry %s dialect = %q, want typescript", id, d)
		}
	}
	if _, ok := tags["typescript:src/plain.jsx:function:Root:1"]; !ok {
		t.Errorf("missing Root entry; have %v", keysOf(tags))
	}
}

func keysOf(m map[string][]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
