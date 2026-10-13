package convention

import (
	"path"
	"strings"
)

// TestDirSegments mark test/spec/fixture trees for extractor gating, ranker demotion, and ripple detection.
var TestDirSegments = []string{
	"test", "tests", "spec", "specs", "__tests__", "testdata",
	"mocks", "mock", "fixtures", "fixture",
}

// IsTestPath reports test paths by dir segment, file infix, or Go _test.go suffix.
// Callers use it for ranking or gating, but the shape never differs.
func IsTestPath(p string) bool {
	lower := strings.ToLower(p)
	for _, seg := range strings.Split(lower, "/") {
		for _, want := range TestDirSegments {
			if seg == want {
				return true
			}
		}
	}
	base := path.Base(lower)
	return strings.Contains(base, ".test.") || strings.Contains(base, ".spec.") ||
		strings.HasSuffix(base, "_test.go")
}
