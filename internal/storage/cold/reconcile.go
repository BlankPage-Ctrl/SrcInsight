package cold

import (
	"srcinsight/internal/model"
)

// TrackedOrigin is the stored half of the reconcile diff. Cold never imports
// the filesystem package; the orchestrator maps DiskStat from file metadata.
type TrackedOrigin struct {
	Path        string
	Fingerprint string
	Dialect     model.Language
	Bytes       int64
	AlteredAt   int64 // unix millis
}

// DiskStat is one file's live filesystem identity for the diff.
type DiskStat struct {
	Rel       string
	Size      int64
	AlteredAt int64 // unix millis, floored like the stored value
}

// Reconcile diffs stored origins against live files; size plus mtime picks
// candidates for Verify. Same-size same-mtime changes stay invisible.
func Reconcile(tracked []TrackedOrigin, disk []DiskStat) (added, changed, removed []string) {
	byPath := make(map[string]TrackedOrigin, len(tracked))
	for _, t := range tracked {
		byPath[t.Path] = t
	}
	onDisk := make(map[string]DiskStat, len(disk))
	for _, d := range disk {
		onDisk[d.Rel] = d
	}
	for _, d := range disk {
		t, ok := byPath[d.Rel]
		if !ok {
			added = append(added, d.Rel)
			continue
		}
		if d.Size != t.Bytes || d.AlteredAt != t.AlteredAt {
			changed = append(changed, d.Rel)
		}
	}
	for _, t := range tracked {
		if _, ok := onDisk[t.Path]; !ok {
			removed = append(removed, t.Path)
		}
	}
	return added, changed, removed
}

// HashedFile is a changed candidate with its confirmed content hash.
type HashedFile struct {
	Rel  string
	Hash string // hex content hash, comparable to TrackedOrigin.Fingerprint
}

// Verify confirms which changed candidates really changed content. Same hash
// means timestamps only; untracked paths belong to added, not here.
func Verify(tracked []TrackedOrigin, hashed []HashedFile) (modified []string) {
	byPath := make(map[string]TrackedOrigin, len(tracked))
	for _, t := range tracked {
		byPath[t.Path] = t
	}
	for _, h := range hashed {
		t, ok := byPath[h.Rel]
		if !ok {
			continue
		}
		if h.Hash != t.Fingerprint {
			modified = append(modified, h.Rel)
		}
	}
	return modified
}
