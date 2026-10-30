// Package adapters is the single source of truth for per-language pipeline
// behavior: file extensions, parser grammar, extraction, import resolution,
// same-file hooks, and the language-specific resolve passes.
package adapters

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"

	"srcinsight/internal/model"
	"srcinsight/internal/storage/cold"
	"srcinsight/services/resolve"
)

// Prep is language-private state computed once per file before SameFileCall.
type Prep any

// Language describes everything the pipeline needs to know about one language.
// Nil optional fields mean "not supported".
type Language struct {
	Dialect model.Language
	Exts    []string

	NewParser func() (*sitter.Parser, error)
	Grammar   func(rel string) *sitter.Language
	Extract   func(p *Pool, rel string, src []byte) (model.FileExtract, error)

	Resolver       func(*Runtime) resolve.ImportResolver
	InternalSource func(rt *Runtime, file, source string) bool
	Invalidate     func(*Runtime)

	PrepareSameFile func(fns []*model.Function) Prep
	SameFileCall    func(prep Prep, caller *model.Function, name string, byName map[string][]*model.Function) ([]*model.Function, bool)
	IsBuiltin       func(recv, probe string) bool

	// RefWithoutImport resolves a loose end without an import edge when the
	// language's scoping rules allow it (Go same-package names).
	RefWithoutImport func(targetFile string, le cold.LooseEndRow, recv string) (handled, ok bool)

	Forward      []ForwardPass
	ForwardShard []ForwardPass
	RecvShard    []ShardPass
	Reverse      []ReversePass
}

// langOrder fixes resolve pass ordering: Go same-package, then TypeScript DI,
// then receiver passes.
var langOrder = []model.Language{model.LangGo, model.LangTypeScript, model.LangPython}

var (
	registry = map[model.Language]*Language{}
	byExt    = map[string]*Language{}
)

func register(l *Language) {
	if l == nil {
		return
	}
	registry[l.Dialect] = l
	for _, ext := range l.Exts {
		byExt[strings.ToLower(ext)] = l
	}
}

// Get returns the descriptor for lang, or nil when unsupported.
func Get(lang model.Language) *Language { return registry[lang] }

// All returns registered descriptors in resolve-pass order.
func All() []*Language {
	out := make([]*Language, 0, len(langOrder))
	for _, lang := range langOrder {
		if l := registry[lang]; l != nil {
			out = append(out, l)
		}
	}
	return out
}

// Lookup resolves a root-relative path to its language descriptor.
func Lookup(rel string) (*Language, bool) {
	l, ok := byExt[strings.ToLower(filepath.Ext(rel))]
	return l, ok
}

// IsSourceFile checks for an indexable source file.
func IsSourceFile(rel string) bool {
	_, ok := Lookup(rel)
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

func newParserWith(grammar *sitter.Language) (*sitter.Parser, error) {
	p := sitter.NewParser()
	if err := p.SetLanguage(grammar); err != nil {
		p.Close()
		return nil, err
	}
	return p, nil
}
