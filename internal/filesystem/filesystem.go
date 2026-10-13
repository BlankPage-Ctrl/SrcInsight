package filesystem

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Root is a project root all reads/walks are jailed to.
// Every public method rejects paths escaping the root.
type Root struct {
	root          string
	maxFileSize   int64
	followSymlink bool
}

// New binds a Root to root (must be an existing directory).
func New(root string, opts ...Option) (*Root, error) {
	o := applyOptions(opts)
	abs, err := filepath.Abs(strings.TrimSpace(root))
	if err != nil {
		return nil, fmt.Errorf("filesystem: resolve root: %w", err)
	}
	abs = filepath.Clean(abs)
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("filesystem: stat root: %w", err)
	}
	if !info.IsDir() {
		return nil, ErrNotDir
	}
	// Canonicalize the root (symlinks, short names, junctions) so Resolve compares canonical paths.
	if canonical, err := filepath.EvalSymlinks(abs); err == nil {
		abs = canonical
	}
	return &Root{root: abs, maxFileSize: o.MaxFileSize, followSymlink: o.FollowSymlink}, nil
}

// Base returns the absolute cleaned root.
func (r *Root) Base() string { return r.root }

// MaxFileSize returns the configured single-file cap.
func (r *Root) MaxFileSize() int64 { return r.maxFileSize }

// FollowSymlink reports whether Walk descends into symlinked dirs.
func (r *Root) FollowSymlink() bool { return r.followSymlink }

// Resolve jails rel to the root and returns the absolute path plus slash-separated relative path.
// It rejects absolute paths, ".." escapes, and symlink targets outside the root.
func (r *Root) Resolve(rel string) (abs string, relSlash string, err error) {
	rel = strings.TrimSpace(rel)
	if rel == "" {
		return "", "", ErrEscape
	}
	// IsAbs is platform-specific, so reject any rooted path lexically.
	if filepath.IsAbs(rel) || strings.HasPrefix(rel, "/") || strings.HasPrefix(rel, "\\") {
		return "", "", fmt.Errorf("%w: %q", ErrEscape, rel)
	}
	cleaned := filepath.Clean(rel)
	if cleaned == "." {
		return r.root, ".", nil
	}
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("%w: %q", ErrEscape, rel)
	}
	lexAbs := filepath.Join(r.root, cleaned)
	if !insideRoot(r.root, lexAbs) {
		return "", "", fmt.Errorf("%w: %q", ErrEscape, rel)
	}
	// Symlink check: EvalSymlinks resolves final + parent components.
	// If the path does not exist yet, fall back to the lexical check above.
	resolved, evalErr := filepath.EvalSymlinks(lexAbs)
	if evalErr != nil {
		// Try the nearest existing ancestor so "a-symlink/b/c" escapes are
		// still caught even when the full path does not exist.
		if ancestor, aerr := nearestExisting(lexAbs); aerr == nil {
			if rTarget, rerr := filepath.EvalSymlinks(ancestor); rerr == nil {
				if !insideRoot(r.root, rTarget) {
					return "", "", fmt.Errorf("%w: %q", ErrSymlinkEscape, rel)
				}
				// Ancestor is inside; the lexical check already bounds the rest.
				return lexAbs, filepath.ToSlash(cleaned), nil
			}
		}
		return lexAbs, filepath.ToSlash(cleaned), nil
	}
	if !insideRoot(r.root, resolved) {
		return "", "", fmt.Errorf("%w: %q", ErrSymlinkEscape, rel)
	}
	// If the on-disk entry is a symlink, use the resolved target for I/O so
	// reads go through the jail-checked path.
	if isSymlink(lexAbs) {
		return resolved, filepath.ToSlash(cleaned), nil
	}
	return lexAbs, filepath.ToSlash(cleaned), nil
}

// ReadFile reads rel with jail and size cap.
// It streams through a limit so a growing file cannot blow up memory.
func (r *Root) ReadFile(rel string) ([]byte, error) {
	abs, _, err := r.Resolve(rel)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(abs)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, fmt.Errorf("filesystem: not a file: %q", rel)
	}
	if info.Size() > r.maxFileSize {
		return nil, fmt.Errorf("%w: %q (%d > %d)", ErrTooLarge, rel, info.Size(), r.maxFileSize)
	}
	// LimitReader guards against TOCTOU growth.
	lr := io.LimitReader(f, r.maxFileSize+1)
	data, err := io.ReadAll(lr)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > r.maxFileSize {
		return nil, fmt.Errorf("%w: %q", ErrTooLarge, rel)
	}
	return data, nil
}

// Open opens rel for reading after jail validation. Caller closes the file.
func (r *Root) Open(rel string) (*os.File, error) {
	abs, _, err := r.Resolve(rel)
	if err != nil {
		return nil, err
	}
	return os.Open(abs)
}

func insideRoot(root, abs string) bool {
	// Windows paths are case-insensitive (and drive-letter case may vary
	// between Abs and EvalSymlinks results); normalize before Rel.
	if runtime.GOOS == "windows" {
		root = strings.ToUpper(root)
		abs = strings.ToUpper(abs)
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func isSymlink(abs string) bool {
	info, err := os.Lstat(abs)
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeSymlink != 0
}

// nearestExisting climbs up until an existing ancestor is found.
func nearestExisting(abs string) (string, error) {
	cur := abs
	for {
		if _, err := os.Lstat(cur); err == nil {
			return cur, nil
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", fmt.Errorf("filesystem: no existing ancestor for %q", abs)
		}
		cur = parent
	}
}
