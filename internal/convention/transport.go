package convention

import "strings"

var httpVerbs = map[string]struct{}{
	"Get": {}, "Post": {}, "Put": {}, "Delete": {},
	"Patch": {}, "Options": {}, "Head": {}, "All": {},
}

var axiosMethods = map[string]struct{}{
	"get": {}, "post": {}, "put": {}, "delete": {},
	"patch": {}, "head": {}, "options": {}, "request": {},
}

// NormalizeMethod uppercases verbs; empty stays empty so callers can tell unknown apart.
func NormalizeMethod(m string) string {
	return strings.ToUpper(strings.TrimSpace(m))
}

// NormalizePath folds :id, {id}, [id], and * segments into {param} for cross-framework matching.
func NormalizePath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	if p[0] != '/' {
		p = "/" + p
	}
	segs := strings.Split(p, "/")
	out := make([]string, 0, len(segs))
	for _, s := range segs {
		if s == "" {
			continue
		}
		if q := strings.IndexByte(s, '?'); q >= 0 {
			s = s[:q]
		}
		if s == "" {
			continue
		}
		if s == "*" || strings.HasPrefix(s, ":") || isBraceParam(s) || isBracketParam(s) {
			out = append(out, "{param}")
			continue
		}
		out = append(out, s)
	}
	if len(out) == 0 {
		return "/"
	}
	return "/" + strings.Join(out, "/")
}

func isBraceParam(s string) bool {
	return len(s) >= 2 && s[0] == '{' && s[len(s)-1] == '}'
}

func isBracketParam(s string) bool {
	return len(s) >= 2 && s[0] == '[' && s[len(s)-1] == ']'
}

// StripURL reduces a fetch URL to its path: drops scheme, host, query, and fragment.
func StripURL(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	if i := strings.IndexByte(s, '?'); i >= 0 {
		s = s[:i]
	}
	if i := strings.IndexByte(s, '#'); i >= 0 {
		s = s[:i]
	}
	if j := strings.Index(s, "://"); j >= 0 {
		rest := s[j+3:]
		if k := strings.IndexByte(rest, '/'); k >= 0 {
			return NormalizePath(rest[k:])
		}
		return "/"
	}
	return NormalizePath(s)
}

// JoinPrefix combines a controller prefix with a method path.
func JoinPrefix(prefix, path string) string {
	prefix = strings.Trim(strings.TrimSpace(prefix), "/")
	path = strings.Trim(strings.TrimSpace(path), "/")
	switch {
	case prefix == "" && path == "":
		return "/"
	case prefix == "":
		return NormalizePath(path)
	case path == "":
		return NormalizePath(prefix)
	default:
		return NormalizePath(prefix + "/" + path)
	}
}

// ClientLib classifies a call into a transport lib using the receiver plus imports.
// Bare fetch has no import; axios and HttpService do.
func ClientLib(recv, name string, importByLocal map[string]string) string {
	if recv == "" && name == "fetch" {
		if _, ok := importByLocal["fetch"]; !ok {
			return "bare-fetch"
		}
	}
	if recv == "" && (name == "$fetch" || name == "useFetch" || name == "useAsyncData") {
		return "nuxt-fetch"
	}
	src := importByLocal[recv]
	if recv == "axios" || src == "axios" {
		if _, ok := axiosMethods[strings.ToLower(name)]; ok {
			return "axios"
		}
	}
	if recv == "HttpService" || recv == "httpService" || recv == "HttpClient" {
		return "nestjs-http"
	}
	if recv == "" && (name == "useQuery" || name == "useMutation") {
		return "gql-client"
	}
	return ""
}

// AxiosMethod maps an axios call name to its HTTP verb; request relies on options.
func AxiosMethod(name string) string {
	lower := strings.ToLower(name)
	if lower == "request" {
		return ""
	}
	if _, ok := axiosMethods[lower]; ok {
		return strings.ToUpper(lower)
	}
	return ""
}

// ServerFramework names the server framework owning a route decorator or call.
func ServerFramework(decorator, recv, source string) string {
	if _, ok := httpVerbs[decorator]; ok {
		return "nestjs"
	}
	if IsNestGQLMethodDecor(decorator) {
		return "nestjs-gql"
	}
	if recv == "router" || recv == "app" {
		return "express"
	}
	if source == "net/http" {
		return "go-net-http"
	}
	return ""
}

// ExpressMethod reports whether name is an express-style route verb.
func ExpressMethod(name string) (string, bool) {
	switch strings.ToLower(name) {
	case "get", "post", "put", "delete", "patch", "options", "head", "all":
		return strings.ToUpper(name), true
	default:
		return "", false
	}
}
