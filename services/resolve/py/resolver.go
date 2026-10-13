// Package pyresolve is the Python import resolver stub; same-file only.
package pyresolve

import (
	"srcinsight/internal/model"
)

type PyResolver struct{}

func (PyResolver) Dialect() model.Language { return model.LangPython }
func (PyResolver) CandidatePaths(_, _ string) []string {
	return nil
}
