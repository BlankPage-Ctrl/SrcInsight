package filesystem

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// FileMeta is cheap filesystem identity for sync/incremental logic:
// Stat (size+mtime) is the pre-filter, Hash confirms content.
type FileMeta struct {
	Rel     string    `json:"rel"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"modTime"`
	Hash    string    `json:"hash,omitempty"` // hex sha256, empty unless Hash() used
}

// Stat returns size+mtime without reading content (no hash).
func (r *Root) Stat(rel string) (FileMeta, error) {
	abs, relSlash, err := r.Resolve(rel)
	if err != nil {
		return FileMeta{}, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return FileMeta{}, err
	}
	if info.IsDir() {
		return FileMeta{}, fmt.Errorf("filesystem: not a file: %q", rel)
	}
	return FileMeta{Rel: relSlash, Size: info.Size(), ModTime: info.ModTime()}, nil
}

// Hash streams the file through sha256 with the size cap enforced.
// Returns ErrTooLarge when the cap is exceeded (checked via Stat + streaming).
func (r *Root) Hash(rel string) (FileMeta, error) {
	abs, relSlash, err := r.Resolve(rel)
	if err != nil {
		return FileMeta{}, err
	}
	f, err := os.Open(abs)
	if err != nil {
		return FileMeta{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return FileMeta{}, err
	}
	if info.IsDir() {
		return FileMeta{}, fmt.Errorf("filesystem: not a file: %q", rel)
	}
	if info.Size() > r.maxFileSize {
		return FileMeta{}, fmt.Errorf("%w: %q (%d > %d)", ErrTooLarge, rel, info.Size(), r.maxFileSize)
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, r.maxFileSize+1))
	if err != nil {
		return FileMeta{}, err
	}
	if n > r.maxFileSize {
		return FileMeta{}, fmt.Errorf("%w: %q", ErrTooLarge, rel)
	}
	return FileMeta{
		Rel:     relSlash,
		Size:    info.Size(),
		ModTime: info.ModTime(),
		Hash:    hex.EncodeToString(h.Sum(nil)),
	}, nil
}

// Abs returns the absolute path for rel after re-validating the jail.
// Never trust raw filepath.Join outside Root.
func (r *Root) Abs(rel string) (string, error) {
	abs, _, err := r.Resolve(rel)
	if err != nil {
		return "", err
	}
	return filepath.Clean(abs), nil
}
