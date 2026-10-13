package golang

import (
	"testing"

	sitter "github.com/tree-sitter/go-tree-sitter"

	"srcinsight/internal/extract"
	"srcinsight/internal/model"
)

func parseGo(t *testing.T, path, src string) extract.FileExtract {
	t.Helper()
	p := sitter.NewParser()
	defer p.Close()
	if err := p.SetLanguage(Language); err != nil {
		t.Fatal(err)
	}
	b := []byte(src)
	tree := p.Parse(b, nil)
	defer tree.Close()
	fe, err := Extract(path, b, tree)
	if err != nil {
		t.Fatal(err)
	}
	return fe
}

func byName(fns []model.Function, name string) *model.Function {
	for i := range fns {
		if fns[i].Name == name {
			return &fns[i]
		}
	}
	return nil
}

func callsOf(fn *model.Function) []string {
	var out []string
	for _, c := range fn.Calls {
		out = append(out, c.Name)
	}
	return out
}

func hasCall(fn *model.Function, want string) bool {
	for _, c := range fn.Calls {
		if c.Name == want {
			return true
		}
	}
	return false
}

func TestExtractFuncAndMethod(t *testing.T) {
	fe := parseGo(t, "server/s.go", `package server

// Start the server.
func (s *Server) Start(port int) error {
	s.Reset()
	Setup()
	return nil
}

func Setup() {}
`)
	if fe.Language != model.LangGo {
		t.Fatalf("language = %q", fe.Language)
	}
	m := byName(fe.Functions, "Start")
	if m == nil {
		t.Fatalf("method Start not found: %+v", fe.Functions)
	}
	if m.Kind != model.KindMethod {
		t.Errorf("Start kind = %q, want method", m.Kind)
	}
	if m.Container == nil || m.Container.Name != "Server" {
		t.Errorf("Start container = %+v, want Server", m.Container)
	}
	if !hasCall(m, "this.Reset") {
		t.Errorf("Start calls = %v, want this.Reset (own-receiver rewrite)", callsOf(m))
	}
	if !hasCall(m, "Setup") {
		t.Errorf("Start calls = %v, want plain Setup", callsOf(m))
	}
	if m.Doc != "// Start the server." {
		t.Errorf("Start doc = %q", m.Doc)
	}
	if len(m.RecvHints) == 0 || m.RecvHints[0] != (model.RecvBinding{Recv: "s", Type: "Server"}) {
		t.Errorf("Start recv hints = %+v, want s→Server first", m.RecvHints)
	}
}

func TestExtractTypesAndEmbeds(t *testing.T) {
	fe := parseGo(t, "m/m.go", `package m

type Base struct{}

type Server struct {
	Base
	name string
}

type Handler interface {
	Serve() error
	Reader
}

type Alias = []string

type Plain int
`)
	cases := map[string]model.Kind{
		"Base":    model.KindClassDef,
		"Server":  model.KindClassDef,
		"Handler": model.KindInterface,
		"Alias":   model.KindTypeAlias,
		"Plain":   model.KindTypeAlias,
	}
	for name, want := range cases {
		n := byName(fe.Functions, name)
		if n == nil {
			t.Errorf("node %s not found", name)
			continue
		}
		if n.Kind != want {
			t.Errorf("%s kind = %q, want %q", name, n.Kind, want)
		}
	}
	srv := byName(fe.Functions, "Server")
	if srv == nil {
		t.Fatal("Server missing")
	}
	if len(srv.Extends) != 1 || srv.Extends[0].Name != "Base" {
		t.Errorf("Server extends = %+v, want [Base]", srv.Extends)
	}
	h := byName(fe.Functions, "Handler")
	if h == nil {
		t.Fatal("Handler missing")
	}
	if len(h.Extends) != 1 || h.Extends[0].Name != "Reader" {
		t.Errorf("Handler extends = %+v, want [Reader]", h.Extends)
	}
}

func TestExtractCallsQualified(t *testing.T) {
	fe := parseGo(t, "main.go", `package main

import "fmt"

func main() {
	fmt.Println("hi")
	defer fmt.Print()
	go run()
}
`)
	m := byName(fe.Functions, "main")
	if m == nil {
		t.Fatal("main missing")
	}
	for _, want := range []string{"fmt.Println", "fmt.Print", "run"} {
		if !hasCall(m, want) {
			t.Errorf("main calls = %v, want %s", callsOf(m), want)
		}
	}
}

func TestExtractImports(t *testing.T) {
	fe := parseGo(t, "main.go", `package main

import (
	"fmt"
	f2 "example.com/demo/util"
	. "example.com/demo/dot"
	_ "example.com/demo/side"
	"charm.land/lipgloss/v2"
)
`)
	var got []model.ImportStmt
	for _, im := range fe.Imports {
		if im.Kind == model.ImportKindGoPackage {
			if im.LocalName != "main" {
				t.Errorf("gopackage row = %+v, want main", im)
			}
			continue
		}
		got = append(got, im)
	}
	if len(got) != 4 {
		t.Fatalf("imports = %+v, want 4 real rows", fe.Imports)
	}
	if got[0].LocalName != "fmt" || got[0].Source != "fmt" {
		t.Errorf("plain import = %+v", got[0])
	}
	if got[1].LocalName != "f2" || got[1].Source != "example.com/demo/util" {
		t.Errorf("aliased import = %+v", got[1])
	}
	if got[2].LocalName != "*" || got[2].Source != "example.com/demo/dot" {
		t.Errorf("dot import = %+v", got[2])
	}
	// Major-version path segment falls back to the package name: the
	// package clause behind charm.land/lipgloss/v2 is `lipgloss`, not `v2`.
	if got[3].LocalName != "lipgloss" || got[3].Source != "charm.land/lipgloss/v2" {
		t.Errorf("versioned import = %+v", got[3])
	}
}

func TestExtractParamReturnTypes(t *testing.T) {
	fe := parseGo(t, "a.go", `package a

func Get(c *Cache, k string) (Server, error) {
	return Server{}, nil
}

func Generic[T any](v T, n int) T { return v }
`)
	g := byName(fe.Functions, "Get")
	if g == nil {
		t.Fatal("Get missing")
	}
	names := map[string]bool{}
	for _, r := range g.ParamTypes {
		names[r.Name] = true
	}
	if !names["Cache"] || names["T"] {
		t.Errorf("Get param types = %+v", g.ParamTypes)
	}
	rnames := map[string]bool{}
	for _, r := range g.ReturnTypes {
		rnames[r.Name] = true
	}
	if !rnames["Server"] {
		t.Errorf("Get return types = %+v", g.ReturnTypes)
	}
	gen := byName(fe.Functions, "Generic")
	if gen == nil {
		t.Fatal("Generic missing")
	}
	for _, r := range gen.ParamTypes {
		if r.Name == "T" {
			t.Errorf("type param T must be skipped: %+v", gen.ParamTypes)
		}
	}
	if len(gen.ReturnTypes) != 0 {
		t.Errorf("generic return T must be skipped: %+v", gen.ReturnTypes)
	}
}

func TestExtractBlankAndUnnamedReceiver(t *testing.T) {
	fe := parseGo(t, "a.go", `package a

type Server struct{}

func (_ *Server) Blank() {}

func (*Server) Unnamed() {}
`)
	for _, name := range []string{"Blank", "Unnamed"} {
		m := byName(fe.Functions, name)
		if m == nil {
			t.Fatalf("method %s missing", name)
		}
		if m.Kind != model.KindMethod {
			t.Errorf("%s kind = %q, want method", name, m.Kind)
		}
		if m.Container == nil || m.Container.Name != "Server" {
			t.Errorf("%s container = %+v, want Server", name, m.Container)
		}
		if len(m.RecvHints) != 0 {
			t.Errorf("%s recv hints = %+v, want none (no variable)", name, m.RecvHints)
		}
	}
}

func TestExtractRecvBindings(t *testing.T) {
	fe := parseGo(t, "a.go", `package a

func New() *Server {
	s := &Server{}
	t := NewServer()
	var w Writer = s
	_ = t
	_ = w
	return s
}
`)
	want := map[string]model.RecvBinding{
		"s": {Recv: "s", Type: "Server"},
		"t": {Recv: "t", ViaCall: "NewServer"},
		"w": {Recv: "w", Type: "Writer"},
	}
	if len(fe.RecvBindings) != len(want) {
		t.Fatalf("bindings = %+v", fe.RecvBindings)
	}
	for _, b := range fe.RecvBindings {
		w, ok := want[b.Recv]
		if !ok {
			t.Errorf("unexpected binding %+v", b)
			continue
		}
		if b != w {
			t.Errorf("binding %q = %+v, want %+v", b.Recv, b, w)
		}
	}
}

func TestExtractBranchCond(t *testing.T) {
	fe := parseGo(t, "a.go", `package a

func Flow(x int, err error) {
	plain()
	if x > 0 {
		positive()
	} else {
		nonpositive()
	}
	if err == nil {
		go func() {
			insideLit()
		}()
	}
}
`)
	fn := byName(fe.Functions, "Flow")
	if fn == nil {
		t.Fatal("Flow missing")
	}
	condOf := map[string]string{}
	for _, c := range fn.Calls {
		condOf[c.Name] = c.Cond
	}
	if condOf["plain"] != "" {
		t.Errorf("plain cond = %q, want empty", condOf["plain"])
	}
	if condOf["positive"] != "x > 0" {
		t.Errorf("positive cond = %q, want 'x > 0'", condOf["positive"])
	}
	if condOf["nonpositive"] != "!(x > 0)" {
		t.Errorf("nonpositive cond = %q, want '!(x > 0)'", condOf["nonpositive"])
	}
	if condOf["insideLit"] != "" {
		t.Errorf("insideLit cond = %q, want empty (literal boundary)", condOf["insideLit"])
	}
}

func TestExtractBranchCondIfInit(t *testing.T) {
	fe := parseGo(t, "a.go", `package a

func Flow() {
	if _, err := chained().Run(); err != nil {
		failed()
	}
}
`)
	fn := byName(fe.Functions, "Flow")
	if fn == nil {
		t.Fatal("Flow missing")
	}
	condOf := map[string]string{}
	for _, c := range fn.Calls {
		condOf[c.Name] = c.Cond
	}
	// The chained call produces the condition's input: unconditional.
	if condOf["chained"] != "" {
		t.Errorf("chained cond = %q, want empty (if-initializer)", condOf["chained"])
	}
}
