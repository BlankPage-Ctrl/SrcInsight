package transport

import "testing"

func TestNormalizePath(t *testing.T) {
	cases := map[string]string{
		"/api/users/:id":  "/api/users/{param}",
		"/api/users/{id}": "/api/users/{param}",
		"/api/users/[id]": "/api/users/{param}",
		"/api/users/*":    "/api/users/{param}",
		"/api/users/":     "/api/users",
		"api/users":       "/api/users",
		"/api/users?a=1":  "/api/users",
		"https://h/api/u": "/https:/h/api/u",
		"":                "",
		"/":               "/",
	}
	for in, want := range cases {
		if got := NormalizePath(in); got != want {
			t.Errorf("NormalizePath(%q)=%q want %q", in, got, want)
		}
	}
}

func TestStripURL(t *testing.T) {
	if got := StripURL("https://api.com/api/users/1?x=2"); got != "/api/users/1" {
		t.Errorf("host strip got %q", got)
	}
	if got := StripURL("/api/users?page=2"); got != "/api/users" {
		t.Errorf("query strip got %q", got)
	}
}

func TestJoinPrefix(t *testing.T) {
	if got := JoinPrefix("users", ":id"); got != "/users/{param}" {
		t.Errorf("join got %q", got)
	}
	if got := JoinPrefix("", "health"); got != "/health" {
		t.Errorf("empty prefix got %q", got)
	}
}

func TestScore(t *testing.T) {
	ep := Endpoint{Transport: KindHTTP, Method: "GET", Path: "/api/users/{param}"}
	exact := CallSite{Transport: KindHTTP, Method: "get", URL: "/api/users/42"}
	if Score(exact, ep) != 1 {
		t.Errorf("exact should score 1")
	}
	unknown := CallSite{Transport: KindHTTP, URL: "/api/users/42"}
	if Score(unknown, ep) != 0.7 {
		t.Errorf("method-unknown should score 0.7")
	}
	wrong := CallSite{Transport: KindHTTP, Method: "POST", URL: "/api/users/42"}
	if Matches(wrong, ep) {
		t.Errorf("method mismatch should not match")
	}
	gqlCall := CallSite{Transport: KindGraphQL, Operation: "GetUser"}
	gqlEp := Endpoint{Transport: KindGraphQL, Operation: "getuser"}
	if Score(gqlCall, gqlEp) != 1 {
		t.Errorf("graphql op should match case-insensitively")
	}
}
