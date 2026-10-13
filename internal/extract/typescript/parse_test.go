package typescript

import (
	"testing"

	sitter "github.com/tree-sitter/go-tree-sitter"
)

// setPathLanguage sets the parser grammar matching LanguageForPath(path):
// plain TypeScript files parse as TypeScript (angle-bracket assertions are
// not JSX there), TSX files keep the TSX grammar. Tests must parse with the
// same grammar Extract's queries were compiled against.
func setPathLanguage(t *testing.T, p *sitter.Parser, path string) {
	t.Helper()
	if err := p.SetLanguage(LanguageForPath(path)); err != nil {
		t.Fatal(err)
	}
}
