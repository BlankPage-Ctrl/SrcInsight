package convention

import "strings"

// GeneratedSuffixes are name-only markers for generated files; header sniffing would cost a read per candidate.
var GeneratedSuffixes = []string{
	".d.ts",
	".pb.go",
	".pulsar.go",
	".g.go",
	".min.js",
}

// GeneratedInfixes are name substrings marking generated trees.
var GeneratedInfixes = []string{
	"__generated__",
}

// IsGeneratedPath reports generated/ambient declaration paths by name alone.
func IsGeneratedPath(p string) bool {
	lower := strings.ToLower(p)
	for _, suf := range GeneratedSuffixes {
		if strings.HasSuffix(lower, suf) {
			return true
		}
	}
	for _, inf := range GeneratedInfixes {
		if strings.Contains(lower, inf) {
			return true
		}
	}
	return false
}
