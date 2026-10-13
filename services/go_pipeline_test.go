package services

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"srcinsight/internal/model"
	"srcinsight/services/methods"
)

// TestGoFullScanEndToEnd indexes a small Go module and checks the graph
// shape: struct/interface/type nodes, receiver containers, same-file links,
// same-package links, cross-package (module) links, heritage, and scopes.
func TestGoFullScanEndToEnd(t *testing.T) {
	ctx := context.Background()
	p, _ := testPipeline(t, map[string]string{
		"go.mod": "module example.com/demo\n\ngo 1.21\n",
		"server/server.go": `package server

import (
	"fmt"

	"example.com/demo/util"
)

type Base struct{}

func (b *Base) Reset() {}

type Server struct {
	Base
	name string
}

func (s *Server) Start(port int) error {
	s.Reset()
	fmt.Println(util.FormatAddr(s.name, port))
	return util.Retry(3)
}
`,
		"server/helper.go": `package server

func NewServer(name string) *Server {
	s := &Server{name: name}
	ping(s.name)
	return s
}

func ping(name string) {}
`,
		"util/util.go": `package util

import "fmt"

func FormatAddr(host string, port int) string {
	return fmt.Sprintf("%s:%d", host, port)
}

func Retry(n int) error {
	fmt.Println("attempt", n)
	return nil
}
`,
	})
	defer p.Close()

	if _, err := p.FullScan(ctx, true); err != nil {
		t.Fatal(err)
	}

	links, err := p.Vault().FetchAllLinks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	has := func(fromSub, toSub, kind string) bool {
		for _, l := range links {
			if strings.Contains(string(l.Origin), fromSub) &&
				strings.Contains(string(l.Target), toSub) && l.Kind == kind {
				return true
			}
		}
		return false
	}
	// Same-file via own-receiver rewrite: Start → Reset (this.Reset).
	if !has("method:Server.Start", "method:Base.Reset", model.EdgeCalls) {
		t.Errorf("missing Start→Reset link: %+v", links)
	}
	// Same-package: NewServer → ping (plain call, no import in Go).
	if !has("function:NewServer", "server/helper.go:function:ping", model.EdgeCalls) {
		t.Errorf("missing NewServer→ping link: %+v", links)
	}
	// Cross-package via module path: Start → Retry, Start → FormatAddr.
	if !has("method:Server.Start", "util/util.go:function:Retry", model.EdgeCalls) {
		t.Errorf("missing Start→Retry link: %+v", links)
	}
	if !has("method:Server.Start", "util/util.go:function:FormatAddr", model.EdgeCalls) {
		t.Errorf("missing Start→FormatAddr link: %+v", links)
	}
	// Heritage: Server extends Base.
	if !has("class:Server", "class:Base", model.EdgeExtends) {
		t.Errorf("missing Server→Base extends link: %+v", links)
	}
	// Type node indexed with receiver container backfilled.
	entries, err := p.Vault().FetchEntriesByFile(ctx, "server/server.go")
	if err != nil {
		t.Fatal(err)
	}
	var serverNode, startNode bool
	for _, e := range entries {
		if e.Name == "Server" && e.Kind == model.KindClassDef {
			serverNode = true
		}
		if e.Name == "Start" && e.Kind == model.KindMethod {
			startNode = true
		}
	}
	if !serverNode || !startNode {
		t.Errorf("server.go entries missing type/method nodes: %+v", entries)
	}
}

// TestGoReversePromote covers the callee-added-later case: the caller is
// indexed first (loose ends kept), then the sibling file lands and the
// same-package reverse pass promotes the link without touching the caller.
func TestGoReversePromote(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	write := func(rel, src string) {
		t.Helper()
		abs := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.com/demo\n\ngo 1.21\n")
	write("pkg/a.go", `package pkg

func Alpha() string {
	return Beta()
}
`)
	p, _ := testPipelineAt(t, dir)
	defer p.Close()

	if _, err := p.FullScan(ctx, true); err != nil {
		t.Fatal(err)
	}
	// The sibling file arrives later; reverse promotion must link without
	// re-parsing the caller.
	write("pkg/b.go", "package pkg\n\nfunc Beta() string { return \"b\" }\n")
	if _, err := p.FullScan(ctx, false); err != nil {
		t.Fatal(err)
	}
	links, err := p.Vault().FetchAllLinks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, l := range links {
		if strings.Contains(string(l.Origin), "function:Alpha") &&
			strings.Contains(string(l.Target), "function:Beta") {
			found = true
		}
	}
	if !found {
		t.Errorf("missing promoted Alpha→Beta link: %+v", links)
	}
}

// TestGoExternalScope checks stdlib calls stay external:runtime-scoped
// loose ends instead of links.
func TestGoExternalScope(t *testing.T) {
	ctx := context.Background()
	p, _ := testPipeline(t, map[string]string{
		"go.mod": "module example.com/demo\n\ngo 1.21\n",
		"main.go": `package main

import "fmt"

func main() {
	fmt.Println("hi")
	println("raw")
}
`,
	})
	defer p.Close()

	if _, err := p.FullScan(ctx, true); err != nil {
		t.Fatal(err)
	}
	loose, err := p.Vault().FetchLooseEndsByFile(ctx, "main.go")
	if err != nil {
		t.Fatal(err)
	}
	scopes := map[string]string{}
	for _, le := range loose {
		scopes[le.Name] = le.Scope
	}
	if scopes["fmt.Println"] != "external:fmt" {
		t.Errorf("fmt.Println scope = %q, want external:fmt (%v)", scopes["fmt.Println"], scopes)
	}
	if scopes["println"] != model.ScopeRuntime {
		t.Errorf("println scope = %q, want runtime (%v)", scopes["println"], scopes)
	}
}

// TestGoBranchCondRoundTrip proves a branch guard captured by the extractor
// survives the vault round trip: the stored link carries it and graph
// returns it on the edge and the call ref.
func TestGoBranchCondRoundTrip(t *testing.T) {
	ctx := context.Background()
	p, _ := testPipeline(t, map[string]string{
		"go.mod": "module example.com/demo\n\ngo 1.21\n",
		"pkg/a.go": `package pkg

func Flow(ok bool) {
	plain()
	if ok {
		guarded()
	} else {
		fallback()
	}
}

func plain() {}
func guarded() {}
func fallback() {}
`,
	})
	defer p.Close()

	if _, err := p.FullScan(ctx, true); err != nil {
		t.Fatal(err)
	}
	links, err := p.Vault().FetchAllLinks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	condByTarget := map[string]string{}
	for _, l := range links {
		parts := strings.Split(string(l.Target), ":")
		condByTarget[parts[len(parts)-2]] = l.Cond
	}
	if condByTarget["guarded"] != "ok" {
		t.Errorf("guarded link cond = %q, want 'ok' (%+v)", condByTarget["guarded"], links)
	}
	if condByTarget["fallback"] != "!(ok)" {
		t.Errorf("fallback link cond = %q, want '!(ok)' (%+v)", condByTarget["fallback"], links)
	}
	if condByTarget["plain"] != "" {
		t.Errorf("plain link cond = %q, want empty", condByTarget["plain"])
	}

	g, err := methods.Graph(ctx, p.Vault(), methods.GraphOptions{ID: "go:pkg/a.go:function:Flow:1"})
	if err != nil {
		t.Fatal(err)
	}
	edgeCond := map[string]string{}
	for _, e := range g.Edges {
		parts := strings.Split(string(e.To), ":")
		edgeCond[parts[len(parts)-2]] = e.Cond
	}
	if edgeCond["guarded"] != "ok" || edgeCond["fallback"] != "!(ok)" {
		t.Errorf("graph edges cond = %v", edgeCond)
	}
	for _, n := range g.Nodes {
		if strings.Contains(string(n.ID), "function:Flow") {
			for _, c := range n.Calls {
				if c.Name == "guarded" && c.Cond != "ok" {
					t.Errorf("graph call ref cond = %q, want 'ok'", c.Cond)
				}
			}
		}
	}
}
