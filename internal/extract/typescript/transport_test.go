package typescript

import (
	"testing"

	sitter "github.com/tree-sitter/go-tree-sitter"
)

func extractTransport(t *testing.T, path, src string) (int, int, string, string) {
	t.Helper()
	parser := sitter.NewParser()
	defer parser.Close()
	setPathLanguage(t, parser, path)
	tree := parser.Parse([]byte(src), nil)
	if tree == nil {
		t.Fatal("parse failed")
	}
	defer tree.Close()
	fe, err := Extract(path, []byte(src), tree)
	if err != nil {
		t.Fatal(err)
	}
	epPath, callURL := "", ""
	if len(fe.Endpoints) > 0 {
		epPath = fe.Endpoints[0].Method + " " + fe.Endpoints[0].Path
	}
	if len(fe.Calls) > 0 {
		callURL = fe.Calls[0].Method + " " + fe.Calls[0].URL + "|" + fe.Calls[0].Lib
	}
	return len(fe.Endpoints), len(fe.Calls), epPath, callURL
}

func TestTransportNestPrefix(t *testing.T) {
	src := `import { Controller, Get } from "@nestjs/common";
@Controller("cats")
export class CatsController {
  @Get(":id")
  findOne() { return 1; }
}
`
	n, _, ep, _ := extractTransport(t, "src/cats.controller.ts", src)
	if n != 1 {
		t.Fatalf("endpoints=%d want 1", n)
	}
	if ep != "Get /cats/{param}" {
		t.Fatalf("endpoint=%q want Get /cats/{param}", ep)
	}
}

func TestTransportBareVsAxios(t *testing.T) {
	bare := `export async function load() { const r = await fetch("/api/users/42"); return r; }`
	_, n, _, call := extractTransport(t, "src/load.ts", bare)
	if n != 1 {
		t.Fatalf("bare calls=%d want 1", n)
	}
	if call != " /api/users/42|bare-fetch" {
		t.Fatalf("bare call=%q", call)
	}
	ax := `import axios from "axios";
export function save() { return axios.post("/api/users"); }`
	_, n, _, call = extractTransport(t, "src/save.ts", ax)
	if n != 1 {
		t.Fatalf("axios calls=%d want 1", n)
	}
	if call != "POST /api/users|axios" {
		t.Fatalf("axios call=%q", call)
	}
}

func TestTransportExpressFileLevel(t *testing.T) {
	src := `import { Router } from "express";
const router = Router();
router.get("/users/:id", (req, res) => res.send(1));
`
	n, _, ep, _ := extractTransport(t, "src/routes.ts", src)
	if n != 1 {
		t.Fatalf("endpoints=%d want 1", n)
	}
	if ep != "GET /users/{param}" {
		t.Fatalf("endpoint=%q", ep)
	}
}
