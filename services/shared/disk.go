// Package shared holds disk identity, hashing, and fault helpers for write paths.
package shared

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"

	"srcinsight/internal/filesystem"
	"srcinsight/internal/model"
	"srcinsight/internal/storage/cold"
)

// DiskFile is one live source file with its cheap stat identity.
type DiskFile struct {
	Rel       string
	Size      int64
	AlteredAt int64 // unix millis, floored like the stored value
}

// CollectDisk walks once in lexical order without extra stat or content reads.
func CollectDisk(ctx context.Context, root *filesystem.Root, filter filesystem.Filter) ([]DiskFile, error) {
	var out []DiskFile
	err := root.Walk(ctx, func(relSlash, _ string, d os.DirEntry) error {
		info, ierr := d.Info()
		if ierr != nil || info.IsDir() {
			return nil
		}
		out = append(out, DiskFile{
			Rel:       relSlash,
			Size:      info.Size(),
			AlteredAt: info.ModTime().UnixMilli(),
		})
		return nil
	}, filter)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ToDiskStats maps disk files to the diff input for cold.Reconcile.
func ToDiskStats(files []DiskFile) []cold.DiskStat {
	stats := make([]cold.DiskStat, 0, len(files))
	for _, d := range files {
		stats = append(stats, cold.DiskStat{Rel: d.Rel, Size: d.Size, AlteredAt: d.AlteredAt})
	}
	return stats
}

// ByRel indexes disk files by path.
func ByRel(files []DiskFile) map[string]DiskFile {
	m := make(map[string]DiskFile, len(files))
	for _, d := range files {
		m[d.Rel] = d
	}
	return m
}

// HashBytes returns hex sha256 matching filesystem.Root.Hash without re-reading.
func HashBytes(src []byte) string {
	sum := sha256.Sum256(src)
	return hex.EncodeToString(sum[:])
}

// EncodeFaults renders per-file errors as JSON; it never fails.
func EncodeFaults(faults []string) string {
	if len(faults) == 0 {
		return ""
	}
	enc, err := json.Marshal(faults)
	if err != nil {
		enc, _ = json.Marshal([]string{"fault encode error"})
	}
	return string(enc)
}

// FaultBundle records stat identity plus faults with no entries.
func FaultBundle(df DiskFile, lang model.Language, hash string, faults []string) cold.OriginBundle {
	return cold.OriginBundle{
		Origin: cold.OriginRecord{
			Path:        df.Rel,
			Fingerprint: hash,
			Dialect:     lang,
			Bytes:       df.Size,
			AlteredAt:   df.AlteredAt,
			Faults:      EncodeFaults(faults),
		},
	}
}

// IsTooLarge reports size-cap rejections; other errors skip and retry.
func IsTooLarge(err error) bool {
	return errors.Is(err, filesystem.ErrTooLarge)
}
