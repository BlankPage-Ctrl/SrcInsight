package transport

import "strings"

// Score ranks a call against an endpoint: 1.0 exact, 0.7 method-unknown, 0 no match.
// GraphQL/WS match on operation; REST matches on method plus normalized path.
func Score(call CallSite, ep Endpoint) float64 {
	if call.Transport != "" && ep.Transport != "" && call.Transport != ep.Transport {
		return 0
	}
	if call.Transport == KindGraphQL || ep.Transport == KindGraphQL {
		if call.Operation == "" || ep.Operation == "" {
			return 0
		}
		if strings.EqualFold(call.Operation, ep.Operation) {
			return 1
		}
		return 0
	}
	callPath := StripURL(call.URL)
	epPath := NormalizePath(ep.Path)
	if callPath == "" || epPath == "" || !MatchPath(callPath, epPath) {
		return 0
	}
	if call.Method == "" || ep.Method == "" {
		return 0.7
	}
	if NormalizeMethod(call.Method) == NormalizeMethod(ep.Method) {
		return 1
	}
	return 0
}

// MatchPath compares normalized paths segment-wise; {param} matches any single segment.
func MatchPath(call, ep string) bool {
	cs := splitSegs(call)
	es := splitSegs(ep)
	if len(cs) != len(es) {
		return false
	}
	for i := range cs {
		if es[i] == "{param}" {
			if cs[i] == "" {
				return false
			}
			continue
		}
		if cs[i] != es[i] {
			return false
		}
	}
	return true
}

func splitSegs(p string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(p); i++ {
		if i == len(p) || p[i] == '/' {
			if seg := p[start:i]; seg != "" {
				out = append(out, seg)
			}
			start = i + 1
		}
	}
	return out
}

// Matches reports whether Score is positive.
func Matches(call CallSite, ep Endpoint) bool {
	return Score(call, ep) > 0
}
