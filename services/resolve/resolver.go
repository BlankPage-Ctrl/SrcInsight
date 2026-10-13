// Package resolve is the language-agnostic core of cross-file resolution.
package resolve

import (
	"srcinsight/internal/model"
)

// ImportResolver maps an import source to candidate vault file paths.
type ImportResolver interface {
	// CandidatePaths returns possible vault paths; empty means unresolvable.
	CandidatePaths(currentFile, source string) []string
	// Dialect reports the handled language.
	Dialect() model.Language
}
