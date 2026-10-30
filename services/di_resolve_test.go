package services

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"srcinsight/internal/model"
)

// --- unit: local stage (ResolveFile) ---

func mkDIClass(id, name string) *model.Function {
	return &model.Function{
		FunctionRef: model.FunctionRef{ID: model.EntryID(id), Name: name, FilePath: "f.ts"},
		Kind:        model.KindClassDef,
	}
}

func mkDIMethod(id, name, vessel string, calls ...string) *model.Function {
	f := mkFn(id, name, "f.ts", vessel, calls...)
	return f
}

func mkDICtor(id, vessel string, binds ...model.RecvBinding) *model.Function {
	f := &model.Function{
		FunctionRef: model.FunctionRef{ID: model.EntryID(id), Name: "constructor", FilePath: "f.ts"},
		Kind:        model.KindConstructor,
		Container:   &model.Container{Kind: model.ContainerClass, Name: vessel},
		RecvHints:   binds,
	}
	return f
}

func TestDIChainSameFileLinksTypedTarget(t *testing.T) {
	ctor := mkDICtor("ctor", "C", model.RecvBinding{Recv: "svc", Type: "Svc"})
	caller := mkDIMethod("caller", "findAll", "C", "this.svc.findAll")
	svcFind := mkDIMethod("target", "findAll", "Svc")
	links, loose := ResolveFile("f.ts",
		[]*model.Function{ctor, caller, svcFind}, nil, model.LangTypeScript)
	if len(loose) != 0 {
		t.Fatalf("loose = %+v, want none", loose)
	}
	if len(links) != 1 || links[0].Origin != "caller" || links[0].Target != "target" {
		t.Fatalf("links = %+v, want caller->target", links)
	}
}

func TestDIChainBareThisUnchanged(t *testing.T) {
	caller := mkDIMethod("a", "run", "A", "this.help")
	helpA := mkDIMethod("b", "help", "A")
	links, loose := ResolveFile("f.ts",
		[]*model.Function{caller, helpA}, nil, model.LangTypeScript)
	if len(loose) != 0 {
		t.Fatalf("loose = %+v", loose)
	}
	if len(links) != 1 || links[0].Target != "b" {
		t.Fatalf("links = %+v, want legacy same-container link", links)
	}
}

func TestDIChainUnknownFieldStaysLoose(t *testing.T) {
	// Same-named method in the caller's own class must NOT win: the
	// qualifier proves another object was meant.
	caller := mkDIMethod("a", "findAll", "C", "this.svc.findAll")
	own := mkDIMethod("b", "findAll", "C")
	links, loose := ResolveFile("f.ts",
		[]*model.Function{caller, own}, nil, model.LangTypeScript)
	if len(links) != 0 {
		t.Fatalf("links = %+v, chained receiver must never self-guess", links)
	}
	if len(loose) != 1 || loose[0].Name != "this.svc.findAll" || loose[0].Scope != model.ScopeUnresolved {
		t.Fatalf("loose = %+v, want unresolved full chain", loose)
	}
}

func TestDIChainPythonUntouched(t *testing.T) {
	caller := mkDIMethod("a", "findAll", "C", "self.svc.findAll")
	own := mkDIMethod("b", "findAll", "C")
	links, _ := ResolveFile("f.py",
		[]*model.Function{caller, own}, nil, model.LangPython)
	// Legacy same-container guess is self-inclusive; the point is only
	// that the chain rule (TS-only) does not touch Python.
	if len(links) != 2 {
		t.Fatalf("links = %+v, python keeps legacy behavior", links)
	}
}

// --- e2e: batch forward + reverse over real pipeline ---

const diServiceSrc = `export class CatsService {
  findAll() { return []; }
}
`

const diControllerSrc = `import { CatsService } from './cats.service';
export class CatsController {
  constructor(private svc: CatsService) {}
  findAll() { return this.svc.findAll(); }
}
`

const diBrokenSrc = `export class BrokenController {
  findAll() { return this.missing.findAll(); }
}
`

func diLinks(t *testing.T, p *Pipeline, from model.EntryID) []string {
	t.Helper()
	return recvLinks(t, p, from)
}

func TestDILinksAcrossFiles(t *testing.T) {
	ctx := context.Background()
	p, _ := testPipeline(t, map[string]string{
		"src/cats.service.ts":    diServiceSrc,
		"src/cats.controller.ts": diControllerSrc,
		"src/broken.ts":          diBrokenSrc,
	})
	defer p.Close()
	if _, err := p.FullScan(ctx, true); err != nil {
		t.Fatal(err)
	}
	caller := model.EntryID("typescript:src/cats.controller.ts:method:CatsController.findAll:1")
	target := model.EntryID("typescript:src/cats.service.ts:method:CatsService.findAll:1")
	if links := diLinks(t, p, caller); !hasLink(links, target) {
		t.Fatalf("controller calls = %v, want link to %s", links, target)
	}
	// No self-loop survived.
	for _, l := range diLinks(t, p, caller) {
		if l == string(caller) {
			t.Fatalf("controller calls contain self-loop: %v", diLinks(t, p, caller))
		}
	}
	// Unknown field stays loose, never guessed.
	broken := model.EntryID("typescript:src/broken.ts:method:BrokenController.findAll:1")
	if links := diLinks(t, p, broken); len(links) != 0 {
		t.Fatalf("broken calls = %v, want none", links)
	}
}

func TestDIReverseOnLaterService(t *testing.T) {
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
	write("src/cats.controller.ts", diControllerSrc)
	p, _ := testPipelineAt(t, dir)
	defer p.Close()
	if _, err := p.FullScan(ctx, true); err != nil {
		t.Fatal(err)
	}
	caller := model.EntryID("typescript:src/cats.controller.ts:method:CatsController.findAll:1")
	if links := diLinks(t, p, caller); len(links) != 0 {
		t.Fatalf("before service lands, calls = %v, want none", links)
	}
	// The service file arrives later; reverse promotion must link without
	// re-parsing the caller.
	write("src/cats.service.ts", diServiceSrc)
	if _, err := p.FullScan(ctx, false); err != nil {
		t.Fatal(err)
	}
	target := model.EntryID("typescript:src/cats.service.ts:method:CatsService.findAll:1")
	if links := diLinks(t, p, caller); !hasLink(links, target) {
		t.Fatalf("after service lands, calls = %v, want link to %s", links, target)
	}
}

// Cross-module DI through a baseUrl-absolute import (`src/...` under a
// package tsconfig with baseUrl "./", the hoppscotch backend layout).
// Without baseUrl mapping the type's file has no candidates and the chain
// stays loose.
func TestDIBaseURLCrossModule(t *testing.T) {
	ctx := context.Background()
	p, _ := testPipeline(t, map[string]string{
		"pkg/tsconfig.json": `{"compilerOptions": {"baseUrl": "./"}}`,
		"pkg/src/user/user.service.ts": `export class UserService {
  getUser(): number { return this.helper(); }
  helper(): number { return 1; }
}
`,
		"pkg/src/profile/profile.service.ts": `import { UserService } from 'src/user/user.service';
export class ProfileService {
  constructor(private users: UserService) {}
  load(): number { return this.users.getUser(); }
}
`,
		"pkg/src/other/usesfp.ts": `import * as E from 'fp-ts/Either';
export function f(): number { return 1; }
`,
	})
	defer p.Close()
	if _, err := p.FullScan(ctx, true); err != nil {
		t.Fatal(err)
	}
	caller := model.EntryID("typescript:pkg/src/profile/profile.service.ts:method:ProfileService.load:1")
	target := model.EntryID("typescript:pkg/src/user/user.service.ts:method:UserService.getUser:1")
	if links := diLinks(t, p, caller); !hasLink(links, target) {
		t.Fatalf("profile calls = %v, want link to %s", links, target)
	}
	for _, l := range diLinks(t, p, caller) {
		if l == string(caller) {
			t.Fatalf("profile calls contain self-loop: %v", diLinks(t, p, caller))
		}
	}
}
