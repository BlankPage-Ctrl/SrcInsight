//go:build !windows

package cold

import "os"

// fileIdent is the database file identity at Open time. On Unix os.SameFile
// compares device+inode, which reliably detects delete+recreate and renames.
type fileIdent struct {
	info os.FileInfo
}

// captureFileIdent stats path; a nil info marks "absent".
func captureFileIdent(path string) fileIdent {
	info, _ := os.Stat(path)
	return fileIdent{info: info}
}

// invalid reports whether no identity was captured (file absent at Open).
func (id fileIdent) invalid() bool { return id.info == nil }

// sameFile reports whether id and other name the same on-disk file.
func (id fileIdent) sameFile(other fileIdent) bool {
	return !id.invalid() && !other.invalid() && os.SameFile(id.info, other.info)
}
