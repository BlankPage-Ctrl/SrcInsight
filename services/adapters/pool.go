package adapters

import (
	sitter "github.com/tree-sitter/go-tree-sitter"

	"srcinsight/internal/model"
)

// Pool leases one parser per language, sized for the worker pool count.
type Pool struct {
	size int
	ch   map[model.Language]chan *sitter.Parser
}

// NewPool builds the pool with ParseWorkers slots per language.
func NewPool() *Pool { return NewPoolSize(ParseWorkers()) }

// NewPoolSize builds the pool with n slots per language.
func NewPoolSize(n int) *Pool {
	if n < 1 {
		n = 1
	}
	p := &Pool{size: n, ch: make(map[model.Language]chan *sitter.Parser)}
	for _, l := range All() {
		ch := make(chan *sitter.Parser, n)
		for range n {
			parser, err := l.NewParser()
			if err != nil {
				panic(err)
			}
			ch <- parser
		}
		p.ch[l.Dialect] = ch
	}
	return p
}

// Size returns the per-language pool depth.
func (p *Pool) Size() int { return p.size }

// Close releases pooled parsers.
func (p *Pool) Close() {
	for _, l := range All() {
		drain(p.ch[l.Dialect])
	}
}

func drain(ch chan *sitter.Parser) {
	for {
		select {
		case parser := <-ch:
			parser.Close()
		default:
			return
		}
	}
}

// Parse runs fn with a parsed tree; fn must not retain tree or parser.
func (p *Pool) Parse(l *Language, rel string, src []byte, fn func(tree *sitter.Tree) error) error {
	ch := p.ch[l.Dialect]
	parser := <-ch
	defer func() { ch <- parser }()
	if l.Grammar != nil {
		if err := parser.SetLanguage(l.Grammar(rel)); err != nil {
			return err
		}
	}
	tree := parser.Parse(src, nil)
	if tree == nil {
		return errParseFailed(l.Dialect)
	}
	defer tree.Close()
	return fn(tree)
}
