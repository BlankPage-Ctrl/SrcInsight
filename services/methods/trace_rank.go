package methods

import (
	"context"
	"sort"
	"strings"

	"srcinsight/internal/convention"
	"srcinsight/internal/model"
	"srcinsight/internal/storage/cold"
)

// Ripple walk bounds over reverse calls edges only.
const (
	rippleMaxDepth = 3
	rippleMaxNodes = 150
	rippleMaxHeads = 24
	rippleMaxFiles = 8
)

// cappedSortedKeys renders a path set deterministically capped.
func cappedSortedKeys(set map[string]struct{}, max int) []string {
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	sort.Strings(out)
	if len(out) > max {
		out = out[:max]
	}
	return out
}

var askFileExts = convention.AskFileExts

func stripAskExt(t string) string {
	return convention.StripAskExt(t)
}

// isAskSymbol reports shape-precise tokens versus bare lowercase words.
func isAskSymbol(t string) bool {
	if strings.ContainsAny(t, "._$:/") {
		return true
	}
	for i := 0; i < len(t); i++ {
		c := t[i]
		if c >= 'A' && c <= 'Z' {
			if i == 0 {
				return true
			}
			if prev := t[i-1]; prev >= 'a' && prev <= 'z' {
				return true
			}
		}
	}
	return false
}

// askTypeTokens picks PascalCase tokens to bias vessel selection.
func askTypeTokens(tokens []string) []string {
	var out []string
	for _, t := range tokens {
		if len(t) < 4 {
			continue
		}
		c := t[0]
		if c < 'A' || c > 'Z' {
			continue
		}
		ok := true
		for i := 1; i < len(t); i++ {
			c := t[i]
			if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9') {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, t)
		}
	}
	return out
}

// inAskTypedContext reports rows under a type token path or name.
func inAskTypedContext(r cold.ScoredEntryRow, typeToks []string) bool {
	if len(typeToks) == 0 {
		return false
	}
	fp := strings.ToLower(r.FilePath)
	q := strings.ToLower(r.Qualified)
	for _, tt := range typeToks {
		lc := strings.ToLower(tt)
		if strings.Contains(fp, lc) || strings.Contains(q, lc) {
			return true
		}
	}
	return false
}

// askKindWeight scores kind worth for architecture asks.
func askKindWeight(k model.Kind) float64 {
	switch k {
	case model.KindFunction, model.KindMethod, model.KindArrow,
		model.KindConstructor, model.KindGenerator,
		model.KindAsyncFunction, model.KindAsyncArrow, model.KindAsyncMethod,
		model.KindClassDef, model.KindInterface, model.KindTypeAlias, model.KindEnum:
		return 1
	case model.KindModule, model.KindConst:
		return 0.8
	case model.KindUnresolved:
		return 0.3
	default:
		return 0.5
	}
}

// isTraceTestPath reports test paths; ranking demotes only.
func isTraceTestPath(p string) bool {
	return convention.IsTestPath(p)
}

// isTraceGeneratedPath reports generated paths by name only.
func isTraceGeneratedPath(p string) bool {
	return convention.IsGeneratedPath(p)
}

// askRankScore folds kind worth and path demotion into one number.
func askRankScore(r cold.ScoredEntryRow) float64 {
	w := askKindWeight(r.Kind)
	pen := 1.0
	if isTraceTestPath(r.FilePath) {
		pen = 0.5
	}
	if isTraceGeneratedPath(r.FilePath) && 0.3 < pen {
		pen = 0.3
	}
	return w * pen
}

// corroboratedAskFile checks whether a sibling token names a file symbol.
func corroboratedAskFile(ctx context.Context, v *cold.Vault, token, filePath string, lcTokens map[string]struct{}, cache map[string]map[string]struct{}) bool {
	self := strings.ToLower(token)
	set, ok := cache[filePath]
	if !ok {
		rows, err := v.FetchEntriesByFile(ctx, filePath)
		if err != nil {
			return true
		}
		set = make(map[string]struct{}, len(rows))
		for _, r := range rows {
			set[strings.ToLower(r.Name)] = struct{}{}
		}
		cache[filePath] = set
	}
	for other := range lcTokens {
		if other == self {
			continue
		}
		if _, ok := set[other]; ok {
			return true
		}
	}
	return false
}
