//go:build windows

package cold

import (
	"golang.org/x/sys/windows"
)

// fileIdent is the NTFS identity at open time: volume serial plus file index.
// os.SameFile cannot tell Windows replacements apart, so this is needed.
type fileIdent struct {
	vol uint32
	idx uint64
	ok  bool
}

// captureFileIdent reads the identity of path with read-attribute access.
func captureFileIdent(path string) fileIdent {
	var id fileIdent
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return id
	}
	h, err := windows.CreateFile(p, windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return id
	}
	defer windows.CloseHandle(h)
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		return id
	}
	id.vol = info.VolumeSerialNumber
	id.idx = uint64(info.FileIndexHigh)<<32 | uint64(info.FileIndexLow)
	id.ok = true
	return id
}

// invalid reports whether no identity was captured (file absent at Open).
func (id fileIdent) invalid() bool { return !id.ok }

// sameFile reports whether id and other name the same on-disk file.
func (id fileIdent) sameFile(other fileIdent) bool {
	return id.ok && other.ok && id.vol == other.vol && id.idx == other.idx
}
