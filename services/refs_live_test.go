package services

import (
	"context"
	"encoding/json"
	"testing"

	"srcinsight/internal/model"
	"srcinsight/internal/storage/cold"
	"srcinsight/services/methods"
)

// entryByName finds a single indexed entry by file and name.
func entryByName(t *testing.T, p *Pipeline, file, name string) cold.EntryRow {
	t.Helper()
	rows, err := p.Vault().FetchEntriesByFile(context.Background(), file)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.Name == name {
			return r
		}
	}
	t.Fatalf("entry %s in %s not found", name, file)
	return cold.EntryRow{}
}

func TestVerifyRefsConfirmsImport(t *testing.T) {
	p, _ := testPipeline(t, map[string]string{
		"src/a.ts": "export function foo(): number { return 1; }\n",
		"src/b.ts": "import { foo } from './a';\nexport function bar(): number { return foo(); }\n",
	})
	defer p.Close()
	ctx := context.Background()
	if _, err := p.FullScan(ctx, true); err != nil {
		t.Fatal(err)
	}
	target := entryByName(t, p, "src/a.ts", "foo")
	caller := entryByName(t, p, "src/b.ts", "bar")
	loose := []cold.LooseEndRow{{
		From: caller.ID, Name: "foo", Kind: model.EdgeCalls, Scope: model.ScopeInternal,
		FilePath: "src/b.ts", Dialect: model.LangTypeScript, SiteStart: 2, SiteEnd: 2,
	}}
	got, err := p.VerifyRefs(ctx, target, loose)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Caller != caller.ID {
		t.Fatalf("verify = %+v, want one caller %s", got, caller.ID)
	}
}

func TestVerifyRefsRejectsMissingImport(t *testing.T) {
	p, _ := testPipeline(t, map[string]string{
		"src/a.ts": "export function foo(): number { return 1; }\n",
		"src/c.ts": "export function baz(): number { return foo(); }\n",
	})
	defer p.Close()
	ctx := context.Background()
	if _, err := p.FullScan(ctx, true); err != nil {
		t.Fatal(err)
	}
	target := entryByName(t, p, "src/a.ts", "foo")
	caller := entryByName(t, p, "src/c.ts", "baz")
	loose := []cold.LooseEndRow{{
		From: caller.ID, Name: "foo", Kind: model.EdgeCalls, Scope: model.ScopeInternal,
		FilePath: "src/c.ts", Dialect: model.LangTypeScript, SiteStart: 1, SiteEnd: 1,
	}}
	got, err := p.VerifyRefs(ctx, target, loose)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("verify = %+v, want none without an import", got)
	}
}

func TestVerifyRefsGoSamePackage(t *testing.T) {
	p, _ := testPipeline(t, map[string]string{
		"pkg/a.go": "package pkg\n\nfunc Foo() int { return 1 }\n",
		"pkg/b.go": "package pkg\n\nfunc Bar() int { return Foo() }\n",
	})
	defer p.Close()
	ctx := context.Background()
	if _, err := p.FullScan(ctx, true); err != nil {
		t.Fatal(err)
	}
	target := entryByName(t, p, "pkg/a.go", "Foo")
	caller := entryByName(t, p, "pkg/b.go", "Bar")
	loose := []cold.LooseEndRow{{
		From: caller.ID, Name: "Foo", Kind: model.EdgeCalls, Scope: model.ScopeUnresolved,
		FilePath: "pkg/b.go", Dialect: model.LangGo, SiteStart: 3, SiteEnd: 3,
	}}
	got, err := p.VerifyRefs(ctx, target, loose)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("verify = %+v, want same-package match", got)
	}
}

func TestPipelineRefsEndToEnd(t *testing.T) {
	p, _ := testPipeline(t, map[string]string{
		"src/a.ts": "export function foo(): number { return 1; }\n",
		"src/b.ts": "import { foo } from './a';\nexport function bar(): number { return foo(); }\n",
	})
	defer p.Close()
	ctx := context.Background()
	if _, err := p.FullScan(ctx, true); err != nil {
		t.Fatal(err)
	}
	foo := entryByName(t, p, "src/a.ts", "foo")
	svc := methods.New(p.Vault(), methods.WithLive(p))
	res, rerr := svc.HandleRefs(ctx, json.RawMessage(`{"id":"`+string(foo.ID)+`"}`))
	if rerr != nil {
		t.Fatalf("handler error: %+v", rerr)
	}
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	var rep methods.RefsReport
	if err := json.Unmarshal(raw, &rep); err != nil {
		t.Fatal(err)
	}
	if len(rep.Sites) == 0 {
		t.Fatalf("no call sites for foo: %+v", rep)
	}
	found := false
	for _, s := range rep.Sites {
		if s.CallerName == "bar" {
			found = true
		}
	}
	if !found {
		t.Fatalf("bar not among callers: %+v", rep.Sites)
	}
}
