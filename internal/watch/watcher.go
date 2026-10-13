package watch

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Options configures FileWatcher; filter callbacks take slash-separated paths relative to Root.
// The watcher is trigger-only: filters decide what is worth collecting, while Sync remains the source of truth.
type Options struct {
	// Root is the project root to watch recursively. Required.
	Root string
	// DebounceMs coalesces bursts into one sync. <=0 means DefaultDebounceMs.
	DebounceMs int
	// MaxDirWatches bounds per-directory watches (Linux inotify). <=0 means DefaultMaxDirWatches.
	MaxDirWatches int
	// HintBudget caps watcher hints; above it we fall back to full CollectDisk. <=0 means DefaultHintBudget (456).
	HintBudget int
	// IsIgnoredDir reports whether a relative dir should be skipped.
	// If nil, only built-in always-ignored dirs apply.
	IsIgnoredDir func(rel string) bool
	// IsIgnoredFile reports whether a relative file should be skipped.
	// If nil, only built-in always-ignored files apply.
	IsIgnoredFile func(rel string) bool
	// IsSourceFile reports whether a file is indexable; nil collects every non-ignored file.
	// The config file ".srcinsightignore" always triggers a sync regardless.
	IsSourceFile func(rel string) bool
	// Sync runs debounced with the pending set; required for Start and Flush to do anything.
	// It may be nil in event-collection tests using GetPendingFiles.
	Sync SyncFunc
	// HintedSync is preferred over Sync when non-nil; nil Paths means full scan.
	HintedSync HintedSyncFunc
	// Warnf receives non-fatal warnings (inotify limits, cap reached).
	// Defaults to log.Printf-style output on stderr.
	Warnf func(format string, args ...any)
}

// PendingFile is one candidate path awaiting sync.
type PendingFile struct {
	Path        string `json:"path"`
	FirstSeenMs int64  `json:"firstSeenMs"`
	LastSeenMs  int64  `json:"lastSeenMs"`
}

// FileWatcher collects changed paths and triggers Sync debounced.
// It is safe for concurrent use.
type FileWatcher struct {
	root          string
	debounce      time.Duration
	debounceMs    int
	maxDirs       int
	hintBudget    int
	syncFn        SyncFunc
	hintedSyncFn  HintedSyncFunc
	isIgnoredDir  func(rel string) bool
	isIgnoredFile func(rel string) bool
	isSourceFile  func(rel string) bool
	warnf         func(format string, args ...any)

	mu            sync.Mutex
	fw            *fsnotify.Watcher
	pending       map[string]*pendingEntry
	timer         *time.Timer
	syncing       bool
	syncStartedMs int64
	stopped       bool
	done          chan struct{}
	dirCount      int
	warnedCap     bool
	lockRetries   int
	requiresFull  bool
}

type pendingEntry struct {
	firstSeenMs int64
	lastSeenMs  int64
}

// New validates Options and returns a stopped FileWatcher.
// Call Start to begin watching.
func New(o Options) (*FileWatcher, error) {
	root := strings.TrimSpace(o.Root)
	if root == "" {
		return nil, fmt.Errorf("watch: Root is required")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("watch: resolve root: %w", err)
	}
	debounceMs := o.DebounceMs
	if debounceMs <= 0 {
		debounceMs = ParseDebounceMsFromEnv("SRCINSIGHT_WATCH_DEBOUNCE_MS")
	}
	if debounceMs < MinDebounceMs {
		debounceMs = MinDebounceMs
	}
	if debounceMs > MaxDebounceMs {
		debounceMs = MaxDebounceMs
	}
	maxDirs := o.MaxDirWatches
	if maxDirs <= 0 {
		maxDirs = ParseMaxDirWatches(os.Getenv("SRCINSIGHT_MAX_DIR_WATCHES"))
	}
	hintBudget := o.HintBudget
	if hintBudget <= 0 {
		hintBudget = DefaultHintBudget
	}
	warnf := o.Warnf
	if warnf == nil {
		warnf = func(format string, args ...any) { log.Printf("watch: "+format, args...) }
	}
	return &FileWatcher{
		root:          abs,
		debounce:      time.Duration(debounceMs) * time.Millisecond,
		debounceMs:    debounceMs,
		maxDirs:       maxDirs,
		hintBudget:    hintBudget,
		syncFn:        o.Sync,
		hintedSyncFn:  o.HintedSync,
		isIgnoredDir:  o.IsIgnoredDir,
		isIgnoredFile: o.IsIgnoredFile,
		isSourceFile:  o.IsSourceFile,
		warnf:         warnf,
		pending:       make(map[string]*pendingEntry),
		done:          make(chan struct{}),
	}, nil
}

// Root returns the absolute project root.
func (w *FileWatcher) Root() string { return w.root }

// DebounceMs returns the configured debounce interval.
func (w *FileWatcher) DebounceMs() int { return w.debounceMs }

// PendingCount returns the number of paths awaiting sync.
func (w *FileWatcher) PendingCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.pending)
}

// Syncing reports whether a Sync is currently running.
func (w *FileWatcher) Syncing() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.syncing
}

// GetPendingFiles snapshots the pending set for observability
// (e.g. "N files changed, reading directly from disk").
func (w *FileWatcher) GetPendingFiles() []PendingFile {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]PendingFile, 0, len(w.pending))
	for p, e := range w.pending {
		out = append(out, PendingFile{Path: p, FirstSeenMs: e.firstSeenMs, LastSeenMs: e.lastSeenMs})
	}
	return out
}

// Start adds recursive watches then serves events until ctx is cancelled or
// Stop is called. It returns nil on graceful shutdown.
func (w *FileWatcher) Start(ctx context.Context) error {
	if reason := WatchDisabledReason(w.root); reason != "" {
		w.warnf("watch disabled (%s); use manual sync", reason)
		<-ctx.Done()
		return nil
	}
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("watch: new watcher: %w", err)
	}
	w.mu.Lock()
	if w.stopped {
		w.mu.Unlock()
		_ = fw.Close()
		return fmt.Errorf("watch: already stopped")
	}
	w.fw = fw
	w.mu.Unlock()

	if err := w.addRecursive(w.root); err != nil {
		_ = fw.Close()
		return err
	}

	for {
		select {
		case <-ctx.Done():
			_ = w.close()
			return nil
		case <-w.done:
			return nil
		case ev, ok := <-fw.Events:
			if !ok {
				return nil
			}
			w.handleEvent(ev)
		case werr, ok := <-fw.Errors:
			if !ok {
				return nil
			}
			w.handleWatcherError(werr)
		}
	}
}

// Stop shuts down the watcher idempotently.
func (w *FileWatcher) Stop() error {
	w.mu.Lock()
	if w.stopped {
		w.mu.Unlock()
		return nil
	}
	w.stopped = true
	if w.timer != nil {
		w.timer.Stop()
	}
	select {
	case <-w.done:
	default:
		close(w.done)
	}
	fw := w.fw
	w.mu.Unlock()
	if fw != nil {
		return fw.Close()
	}
	return nil
}

func (w *FileWatcher) close() error { return w.Stop() }

// Flush runs Sync immediately, independent of the debounce timer; events arriving mid-sync stay pending.
// With HintedSync nil Paths means full scan, otherwise pending keys within budget are hints.
func (w *FileWatcher) Flush(ctx context.Context) (int, error) {
	hasHinted := w.hintedSyncFn != nil
	hasLegacy := w.syncFn != nil
	if !hasHinted && !hasLegacy {
		return 0, nil
	}
	w.mu.Lock()
	if w.syncing {
		w.mu.Unlock()
		return 0, fmt.Errorf("watch: sync already in progress")
	}
	// Build hints before marking syncing, so scheduleSync races don't lose a
	// requiresFull flag. requiresFull is cleared only after a successful full.
	pendingSize := len(w.pending)
	requiresFull := w.requiresFull
	hintBudget := w.hintBudget
	var hints SyncHints
	useHints := false
	if hasHinted {
		if pendingSize == 0 && !requiresFull {
			w.mu.Unlock()
			return 0, nil
		}
		if requiresFull || pendingSize == 0 || pendingSize > hintBudget {
			hints = SyncHints{Paths: nil}
		} else {
			paths := make([]string, 0, pendingSize)
			for p := range w.pending {
				paths = append(paths, p)
			}
			hints = SyncHints{Paths: paths}
		}
		useHints = true
	} else {
		if pendingSize == 0 && !requiresFull {
			w.mu.Unlock()
			return 0, nil
		}
	}
	w.syncing = true
	w.syncStartedMs = time.Now().UnixMilli()
	started := w.syncStartedMs
	w.mu.Unlock()

	var changed int
	var err error
	if useHints {
		changed, err = w.hintedSyncFn(ctx, hints)
	} else {
		changed, err = w.syncFn(ctx)
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	w.syncing = false
	if err != nil {
		w.lockRetries++
		// Keep the full pending set so nothing is lost; caller retries.
		return 0, err
	}
	w.lockRetries = 0
	// A successful full scan clears the requiresFull latch; a hinted sync
	// leaves it for the next flush to do a full scan.
	if useHints {
		if hints.Paths == nil {
			w.requiresFull = false
		}
	} else {
		// Legacy path always does full, so clear.
		w.requiresFull = false
	}
	for p, e := range w.pending {
		if e.lastSeenMs <= started {
			delete(w.pending, p)
		}
	}
	if len(w.pending) > 0 || w.requiresFull {
		w.scheduleSyncLocked()
	}
	return changed, nil
}

// RequiresFull reports whether the next Flush will force a full scan
// (large burst exceeded HintBudget or a scope config changed).
func (w *FileWatcher) RequiresFull() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.requiresFull
}

// HintBudget returns the configured hint ceiling.
func (w *FileWatcher) HintBudget() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.hintBudget
}

// PendingBudgetExceeded reports whether pending would overflow the hint budget.
func (w *FileWatcher) PendingBudgetExceeded() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.pending) > w.hintBudget
}

// handleEvent normalizes, filters, records, and reschedules; it stats only newly created dirs.
func (w *FileWatcher) handleEvent(ev fsnotify.Event) {
	rel, ok := w.relativize(ev.Name)
	if !ok {
		return
	}
	// Directory created or moved in: watch it recursively to close the mkdir-plus-write race.
	if ev.Op&(fsnotify.Create|fsnotify.Rename) != 0 {
		if info, err := os.Stat(ev.Name); err == nil && info.IsDir() {
			if w.isAlwaysIgnored(rel) || w.ignoredDir(rel) {
				return
			}
			_ = w.addRecursive(ev.Name)
			w.markTreePending(ev.Name)
			w.scheduleSync()
			return
		}
	}
	// A gone non-source path may hide a deleted dir (children emit no events), so force a full rescan.
	if isTempFile(rel) {
		return
	}
	if w.isAlwaysIgnored(rel) || w.ignoredDirPrefix(rel) || w.ignoredFile(rel) {
		return
	}
	isCfg := w.isConfigFile(rel)
	if isCfg {
		// Scope file changed (.srcinsightignore / tsconfig) - next sync must be full to reconcile scope.
		w.markRequiresFull()
	}
	if !isCfg && !w.sourceFile(rel) {
		// Potential directory removal: stat the full path, if gone mark full.
		if _, err := os.Stat(ev.Name); err != nil && os.IsNotExist(err) {
			w.markRequiresFull()
			w.scheduleSync()
		}
		return
	}
	w.handleChange(rel)
}

// handleChange records one candidate path and (re)arms the debounce timer.
func (w *FileWatcher) handleChange(rel string) {
	now := time.Now().UnixMilli()
	w.mu.Lock()
	if e, ok := w.pending[rel]; ok {
		e.lastSeenMs = now
	} else {
		w.pending[rel] = &pendingEntry{firstSeenMs: now, lastSeenMs: now}
	}
	w.scheduleSyncLocked()
	w.mu.Unlock()
}

// scheduleSync (re)arms the debounce timer. Caller must hold w.mu for the
// Locked variant.
func (w *FileWatcher) scheduleSync() {
	w.mu.Lock()
	w.scheduleSyncLocked()
	w.mu.Unlock()
}

func (w *FileWatcher) scheduleSyncLocked() {
	if w.stopped || (w.syncFn == nil && w.hintedSyncFn == nil) {
		return
	}
	if w.timer == nil {
		w.timer = time.AfterFunc(w.debounce, func() { _, _ = w.Flush(context.Background()) })
		return
	}
	w.timer.Reset(w.debounce)
}

// markRequiresFull forces the next Flush to do a full scan. Caller must hold mu or use the helper.
func (w *FileWatcher) markRequiresFullLocked() { w.requiresFull = true }

func (w *FileWatcher) markRequiresFull() {
	w.mu.Lock()
	w.requiresFull = true
	w.mu.Unlock()
}

// addRecursive adds one fsnotify watch per non-ignored dir, bounded by MaxDirWatches.
// Beyond the cap it warns once and leaves the rest to manual sync.
func (w *FileWatcher) addRecursive(dir string) error {
	var firstErr error
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // best effort: unreadable subtree does not abort watching
		}
		if !d.IsDir() {
			return nil
		}
		rel, ok := w.relativize(path)
		if !ok {
			return nil
		}
		if rel != "." && (w.isAlwaysIgnored(rel) || w.ignoredDir(rel)) {
			return filepath.SkipDir
		}
		w.mu.Lock()
		overCap := w.dirCount >= w.maxDirs
		fw := w.fw
		w.mu.Unlock()
		if overCap {
			w.warnOnceCap()
			return filepath.SkipDir
		}
		if fw == nil {
			return nil
		}
		if aerr := fw.Add(path); aerr != nil {
			if IsWatchResourceExhaustion(aerr) {
				w.handleWatcherError(aerr)
				return filepath.SkipDir
			}
			if firstErr == nil {
				firstErr = aerr
			}
			return nil
		}
		w.mu.Lock()
		w.dirCount++
		w.mu.Unlock()
		return nil
	})
	return firstErr
}

// markTreePending marks every source file under a newly watched directory as
// pending so files created between mkdir and Add are not missed.
func (w *FileWatcher) markTreePending(dir string) {
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, ok := w.relativize(path)
		if !ok || isTempFile(rel) || w.isAlwaysIgnored(rel) || w.ignoredFile(rel) {
			return nil
		}
		if !w.isConfigFile(rel) && !w.sourceFile(rel) {
			return nil
		}
		now := time.Now().UnixMilli()
		w.mu.Lock()
		if e, ok := w.pending[rel]; ok {
			e.lastSeenMs = now
		} else {
			w.pending[rel] = &pendingEntry{firstSeenMs: now, lastSeenMs: now}
		}
		w.mu.Unlock()
		return nil
	})
}

func (w *FileWatcher) handleWatcherError(err error) {
	if err == nil {
		return
	}
	switch {
	case isInotifyLimit(err):
		w.warnf("inotify limit reached (%v); watching known dirs only, rest needs manual sync", err)
	case isFDExhaustion(err):
		w.warnf("file descriptor exhausted (%v); watch degraded to manual sync", err)
	default:
		w.warnf("watcher error: %v", err)
	}
}

func (w *FileWatcher) warnOnceCap() {
	w.mu.Lock()
	warned := w.warnedCap
	w.warnedCap = true
	max := w.maxDirs
	w.mu.Unlock()
	if !warned {
		w.warnf("max dir watches (%d) reached; remaining dirs need manual sync (SRCINSIGHT_MAX_DIR_WATCHES)", max)
	}
}

// relativize maps an absolute event path to a slash-separated root-relative path.
// It returns (".", true) for the root and ("", false) for outside paths.
func (w *FileWatcher) relativize(abs string) (string, bool) {
	rel, err := filepath.Rel(w.root, abs)
	if err != nil {
		return "", false
	}
	if rel == "." {
		return ".", true
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

func (w *FileWatcher) ignoredDir(rel string) bool {
	if w.isIgnoredDir == nil {
		return false
	}
	return w.isIgnoredDir(rel)
}

func (w *FileWatcher) ignoredFile(rel string) bool {
	if w.isIgnoredFile == nil {
		return false
	}
	return w.isIgnoredFile(rel)
}

func (w *FileWatcher) sourceFile(rel string) bool {
	if w.isSourceFile == nil {
		return true
	}
	return w.isSourceFile(rel)
}

// ignoredDirPrefix drops events for anything under an ignored directory
// (e.g. "node_modules/x/y.ts" when "node_modules" is ignored).
func (w *FileWatcher) ignoredDirPrefix(rel string) bool {
	if w.isIgnoredDir == nil {
		return false
	}
	// Fast path: exact match or any parent prefix matches.
	parts := strings.Split(rel, "/")
	for i := 1; i <= len(parts); i++ {
		if w.isIgnoredDir(strings.Join(parts[:i], "/")) {
			return true
		}
	}
	return false
}

// isAlwaysIgnored is the last-resort built-in filter applied even when the
// caller passes nil filters: .git, .srcinsight state, and editor artifacts.
func (w *FileWatcher) isAlwaysIgnored(rel string) bool {
	if rel == "." {
		return true
	}
	for _, seg := range strings.Split(rel, "/") {
		switch seg {
		case ".git", ".srcinsight", ".out", ".cache":
			return true
		}
	}
	base := rel
	if i := strings.LastIndex(rel, "/"); i >= 0 {
		base = rel[i+1:]
	}
	_ = base
	return false
}

// isConfigFile always triggers a sync so ignore and alias changes take effect.
// Suffixes mirror internal/typescript.IsConfigRel plus go.mod and go.work.
func (w *FileWatcher) isConfigFile(rel string) bool {
	if rel == ".srcinsightignore" || strings.HasSuffix(rel, "/.srcinsightignore") {
		return true
	}
	base := rel
	if i := strings.LastIndex(rel, "/"); i >= 0 {
		base = rel[i+1:]
	}
	if base == "tsconfig.json" || base == "jsconfig.json" {
		return true
	}
	if base == "go.mod" || base == "go.work" {
		return true
	}
	return strings.HasPrefix(base, "tsconfig.") && strings.HasSuffix(base, ".json")
}

// isTempFile drops editor/swap artifacts before they reach the pending set.
func isTempFile(rel string) bool {
	base := rel
	if i := strings.LastIndex(rel, "/"); i >= 0 {
		base = rel[i+1:]
	}
	switch {
	case strings.HasSuffix(base, "~"),
		strings.HasSuffix(base, ".swp"),
		strings.HasSuffix(base, ".swo"),
		strings.HasSuffix(base, ".tmp"),
		strings.HasSuffix(base, ".bak"),
		strings.HasPrefix(base, ".#"),
		strings.HasPrefix(base, "#") && strings.HasSuffix(base, "#"):
		return true
	}
	return false
}
