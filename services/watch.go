package services

import (
	"context"
	"path/filepath"
	"strings"

	"srcinsight/internal/watch"
)

// SyncChanged runs Sync and returns the watch payload; it fires OnSync when set.
func (p *Pipeline) SyncChanged(ctx context.Context) (int, error) {
	return p.SyncWithHintsChanged(ctx, watch.SyncHints{Paths: nil})
}

// SyncWithHintsChanged runs hint-scoped Sync, else full Sync.
func (p *Pipeline) SyncWithHintsChanged(ctx context.Context, hints watch.SyncHints) (int, error) {
	var res SyncResult
	var err error
	if hints.Paths == nil {
		res, err = p.Sync(ctx)
	} else {
		res, err = p.SyncWithHints(ctx, hints.Paths)
	}
	if err != nil {
		return 0, err
	}
	if p.OnSync != nil {
		p.OnSync(res)
	}
	return res.Changed(), nil
}

// WatchOptions builds the trigger-only watcher config sharing discovery scope.
// An in-root database path stays ignored so WAL writes never trigger a sync loop.
func (p *Pipeline) WatchOptions() watch.Options {
	ignoredDir := p.ignore.IgnoredDir
	ignoredFile := p.ignore.IgnoredFile

	// Ignore an in-root database so WAL writes skip sync.
	if p.dbPath != "" && p.dbPath != ":memory:" && !strings.Contains(p.dbPath, "://") && !strings.HasPrefix(p.dbPath, "file:") {
		if rel, err := filepath.Rel(p.rootPath, p.dbPath); err == nil && rel != "." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != ".." {
			relSlash := filepath.ToSlash(rel)
			dirSlash := filepath.ToSlash(filepath.Dir(relSlash))
			if dirSlash == "." {
				dirSlash = ""
			}
			// Cover WAL sidecars too.
			walRel := relSlash + "-wal"
			shmRel := relSlash + "-shm"
			journalRel := relSlash + "-journal"

			origDir := ignoredDir
			origFile := ignoredFile
			ignoredDir = func(r string) bool {
				if origDir != nil && origDir(r) {
					return true
				}
				// Ignore directory-mode DB dirs only, to avoid pruning source dirs.
				if dirSlash != "" && (r == dirSlash || strings.HasPrefix(r, dirSlash+"/")) {
					if filepath.Base(relSlash) == "srcinsight.db" {
						return true
					}
				}
				return false
			}
			ignoredFile = func(r string) bool {
				if origFile != nil && origFile(r) {
					return true
				}
				if r == relSlash || r == walRel || r == shmRel || r == journalRel {
					return true
				}
				// Ignore everything under a directory-mode DB dir.
				if dirSlash != "" && filepath.Base(relSlash) == "srcinsight.db" && strings.HasPrefix(r, dirSlash+"/") {
					return true
				}
				return false
			}
		}
	}

	return watch.Options{
		Root:          p.rootPath,
		HintBudget:    watch.HintBudget,
		IsIgnoredDir:  ignoredDir,
		IsIgnoredFile: ignoredFile,
		IsSourceFile:  IsSourceFile,
		HintedSync:    p.SyncWithHintsChanged,
	}
}

// NewWatcher returns a stopped FileWatcher for this pipeline.
func (p *Pipeline) NewWatcher() (*watch.FileWatcher, error) {
	return watch.New(p.WatchOptions())
}
