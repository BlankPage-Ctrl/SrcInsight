package filesystem

import (
	"context"
	"os"
	"path/filepath"
)

// Filter reports whether relSlash should be skipped; true on a dir prunes it.
// Keep filters cheap: they run per entry.
type Filter func(relSlash string, d os.DirEntry) bool

// WalkFunc is called once per non-skipped file (never for directories).
type WalkFunc func(relSlash, abs string, d os.DirEntry) error

// Walk visits each non-skipped file once using os.DirEntry without per-entry Stat.
// Directories matching any filter are pruned; dir symlinks need WithFollowSymlink and never escape the root.
func (r *Root) Walk(ctx context.Context, fn WalkFunc, filters ...Filter) error {
	if fn == nil {
		return nil
	}
	return filepath.WalkDir(r.root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // best effort: unreadable subtree does not abort
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if path == r.root {
			return nil
		}
		rel, rerr := filepath.Rel(r.root, path)
		if rerr != nil {
			return nil
		}
		relSlash := filepath.ToSlash(rel)

		// Symlink entry? Resolve once, enforce jail.
		if d.Type()&os.ModeSymlink != 0 {
			target, terr := filepath.EvalSymlinks(path)
			if terr != nil {
				return nil // dangling/broken: skip
			}
			if !insideRoot(r.root, target) {
				return nil // escape: skip silently
			}
			info, serr := os.Stat(path) // follows: is target a dir?
			if serr != nil {
				return nil
			}
			if info.IsDir() {
				if !r.followSymlink {
					return nil // dir symlink: do not descend, do not report
				}
				// Following is on: still prune via filters, but WalkDir will
				// not descend into the symlink itself, so enumerate target.
				for _, f := range filters {
					if f != nil && f(relSlash, d) {
						return filepath.SkipDir
					}
				}
				return r.walkTargetDir(ctx, target, relSlash, fn, filters)
			}
			// File symlink inside root: fall through to file handling.
		}

		if d.IsDir() {
			for _, f := range filters {
				if f != nil && f(relSlash, d) {
					return filepath.SkipDir
				}
			}
			return nil
		}
		for _, f := range filters {
			if f != nil && f(relSlash, d) {
				return nil
			}
		}
		return fn(relSlash, path, d)
	})
}

// List collects Walk results as slash-relative paths in walk order.
func (r *Root) List(ctx context.Context, filters ...Filter) ([]string, error) {
	var out []string
	err := r.Walk(ctx, func(relSlash, _ string, _ os.DirEntry) error {
		out = append(out, relSlash)
		return nil
	}, filters...)
	return out, err
}

// walkTargetDir enumerates a symlinked-dir target when following is enabled.
// Entries are reported under the symlink's relSlash prefix.
func (r *Root) walkTargetDir(ctx context.Context, target, prefix string, fn WalkFunc, filters []Filter) error {
	return filepath.WalkDir(target, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if path == target {
			return nil
		}
		sub, rerr := filepath.Rel(target, path)
		if rerr != nil {
			return nil
		}
		relSlash := prefix + "/" + filepath.ToSlash(sub)
		if d.Type()&os.ModeSymlink != 0 {
			t2, terr := filepath.EvalSymlinks(path)
			if terr != nil || !insideRoot(r.root, t2) {
				return nil
			}
			if info, serr := os.Stat(path); serr == nil && info.IsDir() {
				if !r.followSymlink {
					return nil
				}
				// Nested symlink dir: avoid infinite loops, do not recurse deeper.
				return filepath.SkipDir
			}
		}
		if d.IsDir() {
			for _, f := range filters {
				if f != nil && f(relSlash, d) {
					return filepath.SkipDir
				}
			}
			return nil
		}
		// Jail the resolved file too (paranoia: target was inside root).
		if abs, _, rerr := r.Resolve(relSlash); rerr != nil {
			_ = abs
			return nil
		}
		for _, f := range filters {
			if f != nil && f(relSlash, d) {
				return nil
			}
		}
		abs := filepath.Join(target, sub)
		return fn(relSlash, abs, d)
	})
}
