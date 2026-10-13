package filesystem

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestResolveRejectsEscape(t *testing.T) {
	root := t.TempDir()
	r, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"", "../x", "a/../../b", "..", "/abs", "/etc/passwd"} {
		if _, _, err := r.Resolve(rel); !errors.Is(err, ErrEscape) {
			t.Errorf("Resolve(%q) = %v, want ErrEscape", rel, err)
		}
		if _, err := r.ReadFile(rel); !errors.Is(err, ErrEscape) {
			t.Errorf("ReadFile(%q) = %v, want ErrEscape", rel, err)
		}
	}
}

func TestReadFileInsideRoot(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "src/a.ts"), "const x = 1;\n")
	r, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	data, err := r.ReadFile("src/a.ts")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "const x = 1;\n" {
		t.Fatalf("data = %q", data)
	}
}

func TestReadFileTooLarge(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "big.ts"), "0123456789ABCDEF")
	r, err := New(root, WithMaxFileSize(4))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.ReadFile("big.ts"); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("want ErrTooLarge, got %v", err)
	}
	if _, err := r.Hash("big.ts"); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("hash want ErrTooLarge, got %v", err)
	}
}

func TestSymlinkEscapeRejected(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	writeFile(t, filepath.Join(outside, "secret.txt"), "secret")
	writeFile(t, filepath.Join(root, "ok.ts"), "ok")

	// File symlink pointing outside.
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(root, "evil.ts")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	r, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.Resolve("evil.ts"); !errors.Is(err, ErrSymlinkEscape) {
		t.Fatalf("evil symlink Resolve = %v, want ErrSymlinkEscape", err)
	}
	if _, err := r.ReadFile("evil.ts"); !errors.Is(err, ErrSymlinkEscape) {
		t.Fatalf("evil symlink Read = %v, want ErrSymlinkEscape", err)
	}

	// Dir symlink pointing outside must be skipped by Walk, not followed.
	if err := os.Symlink(outside, filepath.Join(root, "linkdir")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	got, err := r.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range got {
		if strings.HasPrefix(p, "linkdir") {
			t.Fatalf("walk followed escaping dir symlink: %v", got)
		}
	}
	found := false
	for _, p := range got {
		if p == "ok.ts" {
			found = true
		}
	}
	if !found {
		t.Fatalf("walk missed ok.ts: %v", got)
	}
}

func TestSymlinkInsideRootReadable(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "real/a.ts"), "hello")
	if err := os.Symlink(filepath.Join(root, "real/a.ts"), filepath.Join(root, "alias.ts")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	r, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	data, err := r.ReadFile("alias.ts")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello" {
		t.Fatalf("alias data = %q", data)
	}
	got, err := r.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	has := map[string]bool{}
	for _, p := range got {
		has[p] = true
	}
	if !has["real/a.ts"] || !has["alias.ts"] {
		t.Fatalf("walk = %v, want real/a.ts + alias.ts", got)
	}
}

func TestWalkFiltersAndSkipDir(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "src/a.ts"), "a")
	writeFile(t, filepath.Join(root, "src/b.py"), "b")
	writeFile(t, filepath.Join(root, "node_modules/dep/index.ts"), "dep")
	writeFile(t, filepath.Join(root, ".git/config"), "git")

	r, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	ignoreDir := func(rel string, d os.DirEntry) bool {
		if !d.IsDir() {
			return false
		}
		base := rel
		if i := strings.LastIndex(rel, "/"); i >= 0 {
			base = rel[i+1:]
		}
		return base == "node_modules" || base == ".git"
	}
	got, err := r.List(context.Background(), ignoreDir)
	if err != nil {
		t.Fatal(err)
	}
	has := map[string]bool{}
	for _, p := range got {
		has[p] = true
	}
	if !has["src/a.ts"] || !has["src/b.py"] {
		t.Fatalf("missing src files: %v", got)
	}
	for _, p := range got {
		if strings.HasPrefix(p, "node_modules") || strings.HasPrefix(p, ".git") {
			t.Fatalf("filter leaked: %v", got)
		}
	}
}

func TestStatAndHash(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.ts"), "content-1")
	r, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	meta, err := r.Stat("a.ts")
	if err != nil {
		t.Fatal(err)
	}
	if meta.Rel != "a.ts" || meta.Hash != "" || meta.Size != int64(len("content-1")) {
		t.Fatalf("stat = %+v", meta)
	}
	h1, err := r.Hash("a.ts")
	if err != nil {
		t.Fatal(err)
	}
	if len(h1.Hash) != 64 {
		t.Fatalf("hash = %q", h1.Hash)
	}
	h2, err := r.Hash("a.ts")
	if err != nil {
		t.Fatal(err)
	}
	if h1.Hash != h2.Hash {
		t.Fatalf("unstable hash %q vs %q", h1.Hash, h2.Hash)
	}
	writeFile(t, filepath.Join(root, "a.ts"), "content-2")
	h3, err := r.Hash("a.ts")
	if err != nil {
		t.Fatal(err)
	}
	if h3.Hash == h1.Hash {
		t.Fatalf("hash did not change after edit")
	}
}

func TestWalkCancelled(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.ts"), "a")
	r, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_ = r.Walk(ctx, func(_, _ string, _ os.DirEntry) error { return nil })
	// Walk may return nil (root short-circuit) or ctx.Err; both acceptable,
	// it must just not hang. Explicit check below with fresh ctx.
	if _, err := r.List(context.Background()); err != nil {
		t.Fatal(err)
	}
}
