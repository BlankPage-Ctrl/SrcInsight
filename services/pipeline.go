package services

import (
	"context"
	"os"
	"sync"

	"srcinsight/internal/filesystem"
	"srcinsight/internal/storage/cold"
	"srcinsight/internal/watch"
	goresolve "srcinsight/services/resolve/go"
	tspkg "srcinsight/services/resolve/ts"
)

// ExtractorVersion forces a full rescan when extractor output changes shape.
const ExtractorVersion = "10"

// SyncResult counts one write run; FilesChecked is the live source count.
type SyncResult struct {
	FilesChecked int `json:"filesChecked"`
	Added        int `json:"added"`
	Modified     int `json:"modified"`
	Removed      int `json:"removed"`
}

// Changed sums Added, Modified, and Removed.
func (r SyncResult) Changed() int { return r.Added + r.Modified + r.Removed }

// Pipeline threads filesystem data through extract, indexer, and vault.
// It serializes vault writes with mu: at most one FullScan or Sync runs at a time.
type Pipeline struct {
	rootPath string
	root     *filesystem.Root
	ignore   *filesystem.Ignore
	reg      *Registry
	vault    *cold.Vault
	dbPath   string
	// tsAlias caches per-directory tsconfig paths; nil means relative-only.
	tsAlias *tspkg.Resolver
	// ws maps workspace package names to dirs; nil resolves nothing.
	ws *tspkg.Workspace
	// goMods caches per-directory go.mod lookups for module-aware Go resolution.
	goMods *goresolve.ModResolver

	// OnSync fires after watcher Sync runs; it must be goroutine-safe.
	OnSync func(SyncResult)

	// watcher is the running FileWatcher while Serve holds it.
	watchMu sync.Mutex
	watcher *watch.FileWatcher

	mu sync.Mutex
}

// PipelineOptions configures NewWithOptions with a DB path override.
type PipelineOptions struct {
	Root string
	// DBPath overrides the database path; relative paths resolve against Root.
	DBPath string
}

// New opens the pipeline for rootPath with a jailed filesystem and vault.
func New(rootPath string) (*Pipeline, error) {
	return NewWithOptions(PipelineOptions{Root: rootPath})
}

// NewWithOptions opens the pipeline, resolving DBPath against Root.
func NewWithOptions(opts PipelineOptions) (*Pipeline, error) {
	root, err := filesystem.New(opts.Root)
	if err != nil {
		return nil, err
	}
	dbPath, err := cold.ResolveDBPath(root.Base(), opts.DBPath)
	if err != nil {
		return nil, err
	}
	vault, err := cold.Open(dbPath)
	if err != nil {
		return nil, err
	}
	return &Pipeline{
		rootPath: root.Base(),
		root:     root,
		ignore:   filesystem.Load(root.Base()),
		reg:      NewRegistry(),
		vault:    vault,
		dbPath:   dbPath,
		tsAlias:  tspkg.New(root),
		ws:       tspkg.NewWorkspace(root),
		goMods:   goresolve.New(root),
	}, nil
}

// newForTest injects a vault for tests.
func newForTest(root *filesystem.Root, vault *cold.Vault) *Pipeline {
	dbPath := ""
	if vault != nil {
		dbPath = vault.Path()
	}
	return &Pipeline{
		rootPath: root.Base(),
		root:     root,
		ignore:   filesystem.Load(root.Base()),
		reg:      NewRegistry(),
		vault:    vault,
		dbPath:   dbPath,
		tsAlias:  tspkg.New(root),
		ws:       tspkg.NewWorkspace(root),
		goMods:   goresolve.New(root),
	}
}

// DBPath returns the resolved database file path.
func (p *Pipeline) DBPath() string { return p.dbPath }

// Close checkpoints and releases the vault.
func (p *Pipeline) Close() error { return p.vault.Close() }

// Vault exposes the underlying store for reads.
func (p *Pipeline) Vault() *cold.Vault { return p.vault }

// sourceFilter skips ignored and non-source paths so discovery and watcher agree.
func (p *Pipeline) sourceFilter() filesystem.Filter {
	return func(relSlash string, d os.DirEntry) bool {
		if d != nil && d.IsDir() {
			return p.ignore.IgnoredDir(relSlash)
		}
		if !IsSourceFile(relSlash) {
			return true
		}
		return p.ignore.IgnoredFile(relSlash)
	}
}

// checkVersion checks whether the vault stamp matches this code.
func (p *Pipeline) checkVersion(ctx context.Context) (bool, error) {
	val, found, err := p.vault.GetMeta(ctx, cold.MetaExtractor)
	if err != nil {
		return false, err
	}
	return found && val == ExtractorVersion, nil
}

// stampVersion records the extractor version and root after a scan.
func (p *Pipeline) stampVersion(ctx context.Context) error {
	if err := p.vault.PutMeta(ctx, cold.MetaExtractor, ExtractorVersion); err != nil {
		return err
	}
	return p.vault.PutMeta(ctx, cold.MetaRoot, p.rootPath)
}

// refreshTraceStats recomputes the vault shape summary trace reads to size
// anchor breadth. It runs only after content changed.
func (p *Pipeline) refreshTraceStats(ctx context.Context) error {
	st, err := p.vault.ComputeTraceStats(ctx)
	if err != nil {
		return err
	}
	return p.vault.PutTraceStats(ctx, st)
}
