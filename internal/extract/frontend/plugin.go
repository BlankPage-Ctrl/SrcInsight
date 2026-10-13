// Package frontend hosts one plugin per JS-world framework behind small
// interfaces, so a new framework registers itself without touching the
// TypeScript extractor or the pipeline.
//
// A framework implements the pieces it needs and nothing more: React tags
// definitions, Vue reads templates. Angular later adds a file here that
// registers both, following react.go and vue.go.
package frontend

import (
	sitter "github.com/tree-sitter/go-tree-sitter"

	"srcinsight/internal/model"
)

// Plugin is the shared root: identity plus file matching.
type Plugin interface {
	Name() string
	Match(path string) bool
}

// Tagger marks extracted definitions (components, hooks, routes).
type Tagger interface {
	Plugin
	TagsFor(path string, src []byte, def *sitter.Node, name string, kind model.Kind) []string
}

// Templater reads template bindings into call-shaped references.
type Templater interface {
	Plugin
	TemplateRefs(path string, src []byte) []model.FunctionRef
}

var (
	plugins    []Plugin
	taggers    []Tagger
	templaters []Templater
)

// Register adds a plugin; capability lists follow its interfaces.
func Register(p Plugin) {
	if p == nil || p.Name() == "" {
		return
	}
	plugins = append(plugins, p)
	if t, ok := p.(Tagger); ok {
		taggers = append(taggers, t)
	}
	if t, ok := p.(Templater); ok {
		templaters = append(templaters, t)
	}
}

// ForFile returns the first plugin matching path, if any.
func ForFile(path string) (Plugin, bool) {
	for _, p := range plugins {
		if p.Match(path) {
			return p, true
		}
	}
	return nil, false
}

// TagsFor fans definition tagging out to matching plugins.
func TagsFor(path string, src []byte, def *sitter.Node, name string, kind model.Kind) []string {
	var out []string
	for _, t := range taggers {
		if t.Match(path) {
			out = append(out, t.TagsFor(path, src, def, name, kind)...)
		}
	}
	return out
}
