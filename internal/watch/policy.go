// Package watch collects candidate paths into a pending set, debounces them, and calls Sync.
// It never parses files or touches the graph; Sync remains the source of truth via stat and hashes.
package watch

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	// DefaultDebounceMs coalesces a burst of saves into a single sync.
	DefaultDebounceMs = 2000
	MinDebounceMs     = 100
	MaxDebounceMs     = 60000

	// DefaultMaxDirWatches bounds inotify file descriptors on Linux.
	// fsnotify is per-directory (not recursive), so watch count is O(dirs).
	DefaultMaxDirWatches = 50000

	// MaxBackoffMs caps retry backoff when Sync fails (e.g. store locked).
	MaxBackoffMs = 30000
	// MaxLockRetries is how many times flush retries a failed sync before
	// giving up and keeping the pending set for a later manual sync.
	MaxLockRetries = 5

	// HintBudget caps hint-scoped syncs; above it a full CollectDisk scan self-heals coalesce loss.
	HintBudget = 456
	// DefaultHintBudget aliases HintBudget for Options defaulting.
	DefaultHintBudget = HintBudget
)

// SyncFunc reconciles, re-parses changed files, and updates the store; it returns files actually changed.
// Injected so watch avoids importing the indexer and stays testable.
type SyncFunc func(ctx context.Context) (filesChanged int, err error)

// SyncHints carries pending paths for a scoped sync; nil (or empty) Paths means full scan.
type SyncHints struct {
	Paths []string
}

// HintedSyncFunc stats only hinted paths within budget, else falls back to full scan.
type HintedSyncFunc func(ctx context.Context, hints SyncHints) (filesChanged int, err error)

// ParseDebounceMs reads env like SRCINSIGHT_WATCH_DEBOUNCE_MS, clamps it into
// [MinDebounceMs, MaxDebounceMs], and falls back to DefaultDebounceMs.
func ParseDebounceMs(envValue string) int {
	if envValue == "" {
		return DefaultDebounceMs
	}
	n, err := strconv.Atoi(strings.TrimSpace(envValue))
	if err != nil {
		return DefaultDebounceMs
	}
	if n < MinDebounceMs {
		return MinDebounceMs
	}
	if n > MaxDebounceMs {
		return MaxDebounceMs
	}
	return n
}

// ParseDebounceMsFromEnv is shorthand for ParseDebounceMs(os.Getenv(key)).
func ParseDebounceMsFromEnv(key string) int {
	return ParseDebounceMs(os.Getenv(key))
}

// ParseMaxDirWatches reads env like SRCINSIGHT_MAX_DIR_WATCHES with a sane floor.
func ParseMaxDirWatches(envValue string) int {
	if envValue == "" {
		return DefaultMaxDirWatches
	}
	n, err := strconv.Atoi(strings.TrimSpace(envValue))
	if err != nil || n < 1 {
		return DefaultMaxDirWatches
	}
	return n
}

// WatchDisabledReason returns "" when watching is allowed, else a fallback reason (opt-out or WSL2 bind mount).
func WatchDisabledReason(root string) string {
	if v := strings.TrimSpace(os.Getenv("SRCINSIGHT_NO_WATCH")); v == "1" || strings.EqualFold(v, "true") {
		return "disabled via SRCINSIGHT_NO_WATCH=1"
	}
	// WSL2: /mnt/<drive>/... inotify events are lossy; prefer manual sync.
	lower := strings.ToLower(root)
	if strings.HasPrefix(lower, "/mnt/") && len(lower) > len("/mnt/x") && lower[5] >= 'a' && lower[5] <= 'z' {
		return "disabled on WSL2 bind mount " + root + " (inotify unreliable, use manual sync)"
	}
	return ""
}

// BackoffForRetry returns debounce * 2^(attempt-1) capped at MaxBackoffMs.
// attempt is 1-based.
func BackoffForRetry(attempt int, baseDebounceMs int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if baseDebounceMs <= 0 {
		baseDebounceMs = DefaultDebounceMs
	}
	d := int64(baseDebounceMs)
	for i := 1; i < attempt; i++ {
		d *= 2
		if d >= MaxBackoffMs {
			break
		}
	}
	if d > MaxBackoffMs {
		d = MaxBackoffMs
	}
	return time.Duration(d) * time.Millisecond
}

// isInotifyLimit reports ENOSPC ("too many watches"): non-fatal, warn once and
// keep serving already-watched directories; the rest falls back to manual sync.
func isInotifyLimit(err error) bool {
	return errors.Is(err, syscall.ENOSPC) || containsErr(err, "too many open files") && containsErr(err, "inotify") ||
		containsErr(err, "no space left on device")
}

// isFDExhaustion reports EMFILE/ENFILE: fatal for watching, degrade to manual.
func isFDExhaustion(err error) bool {
	return errors.Is(err, syscall.EMFILE) || errors.Is(err, syscall.ENFILE) ||
		containsErr(err, "too many open files")
}

// IsWatchResourceExhaustion reports whether err means the watcher cannot add
// more directories (either ENOSPC or EMFILE/ENFILE).
func IsWatchResourceExhaustion(err error) bool {
	return isInotifyLimit(err) || isFDExhaustion(err)
}

func containsErr(err error, substr string) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), strings.ToLower(substr))
}
