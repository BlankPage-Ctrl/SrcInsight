package services

import (
	"context"
	"encoding/json"
	"testing"

	"srcinsight/services/methods"
)

// refsCallers runs the refs handler for one entry and returns the caller names.
func refsCallers(t *testing.T, p *Pipeline, id string) []string {
	t.Helper()
	svc := methods.New(p.Vault(), methods.WithLive(p))
	res, rerr := svc.HandleRefs(context.Background(), json.RawMessage(`{"id":"`+id+`"}`))
	if rerr != nil {
		t.Fatalf("refs handler error: %+v", rerr)
	}
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	var rep methods.RefsReport
	if err := json.Unmarshal(raw, &rep); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, s := range rep.Sites {
		names = append(names, s.CallerName)
	}
	return names
}

func hasName(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}

// TestRefsAliasedImportResolves covers `import { members as installMembers }`:
// the call site is spelled with the alias, so it must resolve to the original
// definition instead of dropping out as an unresolved loose end.
func TestRefsAliasedImportResolves(t *testing.T) {
	p, _ := testPipeline(t, map[string]string{
		"src/util.ts": "export function members(proto: object, table: object): void {}\n",
		"src/core.ts": "import { members as installMembers } from './util';\nexport function run(): void { installMembers({}, {}); }\n",
	})
	defer p.Close()
	ctx := context.Background()
	if _, err := p.FullScan(ctx, true); err != nil {
		t.Fatal(err)
	}
	target := entryByName(t, p, "src/util.ts", "members")
	names := refsCallers(t, p, string(target.ID))
	if !hasName(names, "run") {
		t.Fatalf("callers = %v, want run via the aliased import", names)
	}
}

// TestRefsReExportRenameResolves covers `export { _normalize as normalize }`:
// a consumer of the public name must link back to the private definition.
func TestRefsReExportRenameResolves(t *testing.T) {
	p, _ := testPipeline(t, map[string]string{
		"src/api.ts":    "export function _normalize(): number { return 1; }\n",
		"src/checks.ts": "export { _normalize as normalize } from './api';\n",
		"src/use.ts":    "import { normalize } from './checks';\nexport function run(): number { return normalize(); }\n",
	})
	defer p.Close()
	ctx := context.Background()
	if _, err := p.FullScan(ctx, true); err != nil {
		t.Fatal(err)
	}
	target := entryByName(t, p, "src/api.ts", "_normalize")
	names := refsCallers(t, p, string(target.ID))
	if !hasName(names, "run") {
		t.Fatalf("callers = %v, want run through the re-export rename", names)
	}
}
