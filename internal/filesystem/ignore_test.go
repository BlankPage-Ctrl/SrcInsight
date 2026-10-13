package filesystem

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeIgnoreFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestIgnoreDefaults(t *testing.T) {
	root := t.TempDir()
	ign := Load(root)
	if !ign.IgnoredDir("node_modules") {
		t.Fatal("node_modules should be ignored by default")
	}
	if !ign.IgnoredDir("src/node_modules/foo") {
		t.Fatal("nested node_modules should be ignored by default")
	}
	if ign.IgnoredDir("src/auth") {
		t.Fatal("src/auth should not be ignored")
	}
}

func TestIgnoreFileRules(t *testing.T) {
	root := t.TempDir()
	writeIgnoreFile(t, filepath.Join(root, ".srcinsightignore"), "# comment\n\ndist\n*.min.js\n")
	ign := Load(root)
	if !ign.IgnoredDir("dist") {
		t.Fatal("dist should be ignored via .srcinsightignore")
	}
	if !ign.IgnoredDir("dist/sub") {
		t.Fatal("dist/sub should be ignored via prefix")
	}
	if !ign.IgnoredFile("app.min.js") {
		t.Fatal("app.min.js should be ignored via pattern")
	}
	if ign.IgnoredFile("src/auth/validator.ts") {
		t.Fatal("validator.ts should not be ignored")
	}
}

func TestIgnoreReadsGitignore(t *testing.T) {
	root := t.TempDir()
	writeIgnoreFile(t, filepath.Join(root, ".gitignore"), "*.log\nbuild/\n")
	ign := Load(root)
	if !ign.IgnoredFile("a.log") {
		t.Fatal("a.log should be ignored via .gitignore")
	}
	if !ign.IgnoredFile("src/a.log") {
		t.Fatal("src/a.log should be ignored via basename pattern")
	}
	if !ign.IgnoredDir("build") {
		t.Fatal("build dir itself should be ignored via 'build/'")
	}
	if !ign.IgnoredFile("build/x.ts") {
		t.Fatal("build/x.ts should be ignored via 'build/'")
	}
	if ign.IgnoredFile("src/auth/validator.ts") {
		t.Fatal("validator.ts should not be ignored")
	}
}

func TestIgnoreSrcinsightignoreWinsOverGitignore(t *testing.T) {
	root := t.TempDir()
	writeIgnoreFile(t, filepath.Join(root, ".gitignore"), "dist\n")
	writeIgnoreFile(t, filepath.Join(root, ".srcinsightignore"), "!dist/keep.ts\n")
	ign := Load(root)
	if !ign.IgnoredDir("dist") {
		t.Fatal("dist should be ignored")
	}
	if !ign.IgnoredFile("dist/other.ts") {
		t.Fatal("dist/other.ts should be ignored")
	}
	if ign.IgnoredFile("dist/keep.ts") {
		t.Fatal("dist/keep.ts should be re-included by .srcinsightignore '!'")
	}
}

func TestIgnoreChildScopeOverridesParent(t *testing.T) {
	root := t.TempDir()
	writeIgnoreFile(t, filepath.Join(root, ".gitignore"), "*.log\n")
	writeIgnoreFile(t, filepath.Join(root, "a", ".gitignore"), "!keep.log\n")
	ign := Load(root)
	if !ign.IgnoredFile("a/other.log") {
		t.Fatal("a/other.log should stay ignored by parent")
	}
	if ign.IgnoredFile("a/keep.log") {
		t.Fatal("a/keep.log should be re-included by child '!'")
	}
	if !ign.IgnoredFile("b/other.log") {
		t.Fatal("b/other.log should stay ignored (no child scope)")
	}
}

func TestIgnoreChildScopeAddsOwnRules(t *testing.T) {
	root := t.TempDir()
	writeIgnoreFile(t, filepath.Join(root, "gen", ".gitignore"), "*.tmp\n")
	ign := Load(root)
	if !ign.IgnoredFile("gen/a.tmp") {
		t.Fatal("gen/a.tmp should be ignored by child scope")
	}
	if ign.IgnoredFile("src/a.tmp") {
		t.Fatal("src/a.tmp must NOT leak child scope to the rest of the repo")
	}
}

func TestIgnoreAlwaysIgnoredNotReIncludable(t *testing.T) {
	root := t.TempDir()
	writeIgnoreFile(t, filepath.Join(root, ".gitignore"), "!.git/\n!.srcinsight/\n!node_modules\n")
	ign := Load(root)
	if !ign.IgnoredDir(".git") {
		t.Fatal(".git must stay ignored")
	}
	if !ign.IgnoredFile(".git/config") {
		t.Fatal(".git/config must stay ignored")
	}
	if !ign.IgnoredDir(".srcinsight") {
		t.Fatal(".srcinsight must stay ignored")
	}
}

func TestIgnoreAsFilterPrunesWalk(t *testing.T) {
	root := t.TempDir()
	writeIgnoreFile(t, filepath.Join(root, "src", "a.ts"), "a")
	writeIgnoreFile(t, filepath.Join(root, "node_modules", "dep", "index.ts"), "dep")
	writeIgnoreFile(t, filepath.Join(root, ".git", "config"), "git")
	writeIgnoreFile(t, filepath.Join(root, ".gitignore"), "ignored/\n")
	writeIgnoreFile(t, filepath.Join(root, "ignored", "x.ts"), "x")

	r, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	ign := Load(root)
	got, err := r.List(context.Background(), ign.AsFilter())
	if err != nil {
		t.Fatal(err)
	}
	has := map[string]bool{}
	for _, p := range got {
		has[p] = true
	}
	if !has["src/a.ts"] {
		t.Fatalf("missing src/a.ts: %v", got)
	}
	for _, p := range got {
		if p == ".git" || strings.HasPrefix(p, ".git/") {
			t.Fatalf("filter leaked .git: %v", got)
		}
		if p == "ignored" || strings.HasPrefix(p, "ignored/") {
			t.Fatalf("filter leaked ignored/: %v", got)
		}
	}
}

func TestIgnoreNilSafe(t *testing.T) {
	var ign *Ignore
	if ign.IgnoredDir("a") || ign.IgnoredFile("a.ts") {
		t.Fatal("nil Ignore must never ignore")
	}
	if ign.AsFilter()("a.ts", nil) {
		t.Fatal("nil Ignore filter must never skip (nil entry)")
	}
}
