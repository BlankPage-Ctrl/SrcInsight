package services

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"srcinsight/internal/filesystem"
	"srcinsight/internal/model"
	"srcinsight/internal/storage/cold"
)

const tsSrc = `import { sign } from './jwt';

export class AuthService {
  async validateToken(token: string): Promise<boolean> {
    return this.decode(token) && sign(token, "s");
  }
  decode(t: string): string { return t; }
}

export function standalone(x: number): number {
  return validateNothing(x);
}
`

const pySrc = `from .util import helper


def get_user(token):
    return helper(token) or len(token)
`

func testPipeline(t *testing.T, files map[string]string) (*Pipeline, string) {
	t.Helper()
	dir := t.TempDir()
	for rel, src := range files {
		abs := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	root, err := filesystem.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	vault, err := cold.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = vault.Close() })
	return newForTest(root, vault), dir
}

func TestFullScanWritesEntries(t *testing.T) {
	ctx := context.Background()
	p, _ := testPipeline(t, map[string]string{
		"src/auth.ts": tsSrc,
		"app/mod.py":  pySrc,
	})
	defer p.Close()

	res, err := p.FullScan(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if res.FilesChecked != 2 {
		t.Fatalf("checked = %d, want 2", res.FilesChecked)
	}
	n, _, err := p.Vault().Counts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("no entries stored")
	}
	// Extractor version stamped.
	if val, found, _ := p.Vault().GetMeta(ctx, cold.MetaExtractor); !found || val != ExtractorVersion {
		t.Fatalf("extractor meta = %q, %v", val, found)
	}
	// Vault shape summary stored for trace anchor sizing.
	st, ok, err := p.Vault().GetTraceStats(ctx)
	if err != nil || !ok {
		t.Fatalf("trace stats = %+v, %v, %v", st, ok, err)
	}
	if st.Entries != n || st.Files == 0 || st.Dirs == 0 {
		t.Fatalf("trace stats = %+v, want entries=%d with files/dirs", st, n)
	}
	// Same-file link resolved: validateToken -> decode.
	links, err := p.Vault().FetchAllLinks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, l := range links {
		if l.Kind == model.EdgeCalls {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("no same-file calls links stored: %+v", links)
	}
	// Unresolved/external calls kept as loose ends with scopes.
	loose, err := p.Vault().FetchAllLooseEnds(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(loose) == 0 {
		t.Fatal("expected loose ends for cross-file/unresolved calls")
	}
	scopes := map[string]bool{}
	for _, l := range loose {
		scopes[l.Scope] = true
	}
	if !scopes["external:runtime"] && !scopes[model.ScopeUnresolved] && !scopes["internal"] {
		t.Fatalf("unexpected loose scopes: %+v", loose)
	}
}

func TestSyncPartialUpdate(t *testing.T) {
	ctx := context.Background()
	p, dir := testPipeline(t, map[string]string{
		"src/a.ts": "export function foo(): number { return 1; }\n",
		"src/b.ts": "export function bar(): number { return 2; }\n",
	})
	defer p.Close()

	if _, err := p.FullScan(ctx, true); err != nil {
		t.Fatal(err)
	}
	// No-op sync: nothing changed.
	res, err := p.Sync(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Changed() != 0 {
		t.Fatalf("no-op sync changed = %+v", res)
	}
	// Modify one file: add a function calling the existing one.
	if err := os.WriteFile(filepath.Join(dir, "src", "a.ts"),
		[]byte("export function foo(): number { return 1; }\nexport function baz(): number { return foo(); }\n"),
		0o644); err != nil {
		t.Fatal(err)
	}
	res, err = p.Sync(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Modified != 1 || res.Added != 0 || res.Removed != 0 {
		t.Fatalf("sync = %+v, want modified=1", res)
	}
	entries, err := p.Vault().FetchEntriesByFile(ctx, "src/a.ts")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("a.ts entries = %d, want 2", len(entries))
	}
	// Unchanged file untouched: still one entry, same fingerprint.
	entriesB, err := p.Vault().FetchEntriesByFile(ctx, "src/b.ts")
	if err != nil {
		t.Fatal(err)
	}
	if len(entriesB) != 1 {
		t.Fatalf("b.ts entries = %d, want 1", len(entriesB))
	}
	// Remove a file.
	if err := os.Remove(filepath.Join(dir, "src", "b.ts")); err != nil {
		t.Fatal(err)
	}
	res, err = p.Sync(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Removed != 1 {
		t.Fatalf("sync = %+v, want removed=1", res)
	}
	if _, found, _ := p.Vault().FetchOrigin(ctx, "src/b.ts"); found {
		t.Fatal("b.ts origin survives removal")
	}
}

func TestSyncVersionMismatchForcesFullScan(t *testing.T) {
	ctx := context.Background()
	p, _ := testPipeline(t, map[string]string{
		"src/a.ts": "export function foo(): number { return 1; }\n",
	})
	defer p.Close()

	if _, err := p.FullScan(ctx, true); err != nil {
		t.Fatal(err)
	}
	// Corrupt the stamp: next Sync must fall back to a full scan.
	if err := p.Vault().PutMeta(ctx, cold.MetaExtractor, "stale"); err != nil {
		t.Fatal(err)
	}
	res, err := p.Sync(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.FilesChecked != 1 || res.Added != 1 {
		t.Fatalf("version-mismatch sync = %+v, want full rescan", res)
	}
}
