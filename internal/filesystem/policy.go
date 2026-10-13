// Package filesystem is the single choke point for filesystem access, jailed to a project root.
// Scope policy lives here too: hierarchical .gitignore plus .srcinsightignore with built-in defaults.
//
// The orchestrator prunes discovery with Ignore.AsFilter and the watcher uses IgnoredDir/IgnoredFile, so both agree.
// New code should use Root instead of filepath.WalkDir or os.ReadFile so jail and size caps stay in one place.
package filesystem

import "errors"

var (
	// ErrEscape is returned when a requested path escapes the root lexically
	// (absolute path, ".." traversal, empty path).
	ErrEscape = errors.New("filesystem: path escapes root")
	// ErrSymlinkEscape is returned when a symlink (file or any parent
	// component) resolves to a target outside the root.
	ErrSymlinkEscape = errors.New("filesystem: symlink escapes root")
	// ErrTooLarge is returned when a file exceeds MaxFileSize.
	ErrTooLarge = errors.New("filesystem: file too large")
	// ErrNotDir is returned by New when root is not a directory.
	ErrNotDir = errors.New("filesystem: root is not a directory")
)

// DefaultMaxFileSize caps single-file reads/hashes (10 MiB).
const DefaultMaxFileSize = 10 << 20

// Options tunes a Root. Zero value means defaults.
type Options struct {
	// MaxFileSize caps ReadFile and Hash. <=0 means DefaultMaxFileSize.
	MaxFileSize int64
	// FollowSymlink allows Walk to descend into symlinked dirs; escapes outside the root stay rejected.
	FollowSymlink bool
}

// Option is a functional option for New.
type Option func(*Options)

// WithMaxFileSize overrides the read/hash size cap. n <= 0 resets to default.
func WithMaxFileSize(n int64) Option {
	return func(o *Options) { o.MaxFileSize = n }
}

// WithFollowSymlink controls whether Walk descends into symlinked dirs.
func WithFollowSymlink(follow bool) Option {
	return func(o *Options) { o.FollowSymlink = follow }
}

func applyOptions(opts []Option) Options {
	o := Options{MaxFileSize: DefaultMaxFileSize}
	for _, fn := range opts {
		if fn != nil {
			fn(&o)
		}
	}
	if o.MaxFileSize <= 0 {
		o.MaxFileSize = DefaultMaxFileSize
	}
	return o
}
