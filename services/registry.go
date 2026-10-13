// Package services threads model data through filesystem, extract, indexer, and vault.
package services

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"
	gots "github.com/tree-sitter/tree-sitter-go/bindings/go"
	py "github.com/tree-sitter/tree-sitter-python/bindings/go"
	tstsg "github.com/tree-sitter/tree-sitter-typescript/bindings/go"

	tsextract "srcinsight/internal/extract/typescript"
	"srcinsight/internal/model"
)

// JS and Vue SFCs share the typescript dialect.
// Framework identity rides path suffix plus tags, not the language value.
var extToLang = map[string]model.Language{
	".ts":  model.LangTypeScript,
	".tsx": model.LangTypeScript,
	".mts": model.LangTypeScript,
	".cts": model.LangTypeScript,
	".js":  model.LangTypeScript,
	".jsx": model.LangTypeScript,
	".mjs": model.LangTypeScript,
	".cjs": model.LangTypeScript,
	".vue": model.LangTypeScript,
	".py":  model.LangPython,
	".go":  model.LangGo,
}

// LanguageOf returns the language for a root-relative path.
func LanguageOf(rel string) (model.Language, bool) {
	lang, ok := extToLang[strings.ToLower(filepath.Ext(rel))]
	return lang, ok
}

// IsSourceFile checks for an indexable source file; watcher scope uses it too.
func IsSourceFile(rel string) bool {
	_, ok := LanguageOf(rel)
	return ok
}

// maxParseWorkers caps the parse pool; commits bottleneck beyond ~8 workers.
const maxParseWorkers = 8

// ParseWorkers resolves parse parallelism from env or CPU count.
func ParseWorkers() int {
	if raw := strings.TrimSpace(os.Getenv("SRCINSIGHT_PARSE_WORKERS")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			return n
		}
	}
	n := runtime.NumCPU() - 1
	if n < 1 {
		return 1
	}
	if n > maxParseWorkers {
		return maxParseWorkers
	}
	return n
}

// Registry pools one parser channel per language; parses lease and return one.
type Registry struct {
	size int
	ts   chan *sitter.Parser
	py   chan *sitter.Parser
	go_  chan *sitter.Parser
}

// NewRegistry builds the parser pool with ParseWorkers slots per language.
func NewRegistry() *Registry {
	return NewRegistrySize(ParseWorkers())
}

// NewRegistrySize builds the pool with n slots per language.
func NewRegistrySize(n int) *Registry {
	if n < 1 {
		n = 1
	}
	r := &Registry{
		size: n,
		ts:   make(chan *sitter.Parser, n),
		py:   make(chan *sitter.Parser, n),
		go_:  make(chan *sitter.Parser, n),
	}
	for i := 0; i < n; i++ {
		p := sitter.NewParser()
		if err := p.SetLanguage(sitter.NewLanguage(tstsg.LanguageTSX())); err != nil {
			panic(err)
		}
		r.ts <- p
		q := sitter.NewParser()
		if err := q.SetLanguage(sitter.NewLanguage(py.Language())); err != nil {
			panic(err)
		}
		r.py <- q
		g := sitter.NewParser()
		if err := g.SetLanguage(sitter.NewLanguage(gots.Language())); err != nil {
			panic(err)
		}
		r.go_ <- g
	}
	return r
}

// Size returns the per-language pool depth.
func (r *Registry) Size() int { return r.size }

// Close releases pooled parsers; trees and cursors already close per parse.
func (r *Registry) Close() {
	for {
		select {
		case p := <-r.ts:
			p.Close()
		default:
			goto drainPy
		}
	}
drainPy:
	for {
		select {
		case p := <-r.py:
			p.Close()
		default:
			goto drainGo
		}
	}
drainGo:
	for {
		select {
		case p := <-r.go_:
			p.Close()
		default:
			return
		}
	}
}

// Parse runs fn with a parsed tree; fn must not retain tree or parser.
func (r *Registry) Parse(lang model.Language, src []byte, fn func(tree *sitter.Tree) error) error {
	var ch chan *sitter.Parser
	switch lang {
	case model.LangTypeScript:
		ch = r.ts
	case model.LangPython:
		ch = r.py
	case model.LangGo:
		ch = r.go_
	default:
		return errUnsupported(lang)
	}
	p := <-ch
	defer func() { ch <- p }()
	tree := p.Parse(src, nil)
	if tree == nil {
		return errParseFailed(lang)
	}
	defer tree.Close()
	return fn(tree)
}

// ParseTSFile parses one TypeScript file with the grammar matching its extension.
func (r *Registry) ParseTSFile(rel string, src []byte, fn func(tree *sitter.Tree) error) error {
	p := <-r.ts
	defer func() { r.ts <- p }()
	if err := p.SetLanguage(tsextract.LanguageForPath(rel)); err != nil {
		return err
	}
	tree := p.Parse(src, nil)
	if tree == nil {
		return errParseFailed(model.LangTypeScript)
	}
	defer tree.Close()
	return fn(tree)
}
