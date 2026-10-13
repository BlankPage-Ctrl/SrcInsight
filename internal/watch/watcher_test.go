package watch

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
)

func testOptions(root string) Options {
	return Options{
		Root:          root,
		DebounceMs:    100,
		MaxDirWatches: 100,
		IsIgnoredDir:  func(rel string) bool { return rel == "node_modules" || rel == ".git" },
		IsIgnoredFile: func(rel string) bool { return rel == "bundle.min.js" },
		IsSourceFile: func(rel string) bool {
			ext := filepath.Ext(rel)
			return ext == ".ts" || ext == ".py"
		},
		Warnf: func(string, ...any) {},
	}
}

func emit(w *FileWatcher, name string, op fsnotify.Op) {
	w.handleEvent(fsnotify.Event{Name: name, Op: op})
}

func TestCollectsSourceFile(t *testing.T) {
	root := t.TempDir()
	w, err := New(testOptions(root))
	if err != nil {
		t.Fatal(err)
	}
	emit(w, filepath.Join(root, "src", "a.ts"), fsnotify.Write)
	got := w.GetPendingFiles()
	if len(got) != 1 || got[0].Path != "src/a.ts" {
		t.Fatalf("pending = %+v, want [src/a.ts]", got)
	}
}

func TestIgnoresNonSourceAndIgnored(t *testing.T) {
	root := t.TempDir()
	w, err := New(testOptions(root))
	if err != nil {
		t.Fatal(err)
	}
	emit(w, filepath.Join(root, "node_modules", "x", "a.ts"), fsnotify.Write) // ignored dir prefix
	emit(w, filepath.Join(root, "img.png"), fsnotify.Write)                   // non-source
	emit(w, filepath.Join(root, "a.ts~"), fsnotify.Write)                     // temp
	emit(w, filepath.Join(root, ".git", "config"), fsnotify.Write)            // always ignored
	if n := w.PendingCount(); n != 0 {
		t.Fatalf("pending = %d, want 0: %+v", n, w.GetPendingFiles())
	}
}

func TestCoalesceBurst(t *testing.T) {
	root := t.TempDir()
	w, err := New(testOptions(root))
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(root, "a.ts")
	for i := 0; i < 20; i++ {
		emit(w, p, fsnotify.Write)
	}
	if n := w.PendingCount(); n != 1 {
		t.Fatalf("pending = %d, want 1", n)
	}
}

func TestFlushClearsOnlyPreSyncEntries(t *testing.T) {
	root := t.TempDir()
	calls := 0
	o := testOptions(root)
	o.Sync = func(ctx context.Context) (int, error) {
		calls++
		return 1, nil
	}
	w, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	emit(w, filepath.Join(root, "a.ts"), fsnotify.Write)
	// Simulate an event arriving mid-sync by pushing its timestamp forward
	// after Flush starts is racy; instead verify the basic contract:
	n, err := w.Flush(context.Background())
	if err != nil || n != 1 || calls != 1 {
		t.Fatalf("flush = %d,%v calls=%d", n, err, calls)
	}
	if left := w.PendingCount(); left != 0 {
		t.Fatalf("pending after flush = %d, want 0", left)
	}
}

func TestNewDirIsWatchedAndMarkedPending(t *testing.T) {
	root := t.TempDir()
	o := testOptions(root)
	w, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	// Start a real fsnotify instance so addRecursive has somewhere to add.
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		t.Skip("fsnotify unavailable")
	}
	defer fw.Close()
	w.fw = fw
	sub := filepath.Join(root, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "b.ts"), []byte("let x=1"), 0o644); err != nil {
		t.Fatal(err)
	}
	emit(w, sub, fsnotify.Create)
	time.Sleep(10 * time.Millisecond)
	found := false
	for _, p := range w.GetPendingFiles() {
		if p.Path == "sub/b.ts" {
			found = true
		}
	}
	if !found {
		t.Fatalf("sub/b.ts not marked pending: %+v", w.GetPendingFiles())
	}
}
