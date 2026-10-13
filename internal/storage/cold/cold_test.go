package cold

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"srcinsight/internal/model"
)

func openMemory(t *testing.T) *Vault {
	t.Helper()
	v, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = v.Close() })
	return v
}

func TestOpenMemoryAndVersion(t *testing.T) {
	v := openMemory(t)
	ver, err := v.VaultVersion()
	if err != nil {
		t.Fatal(err)
	}
	if ver != CurrentLedgerVersion {
		t.Fatalf("version = %d, want %d", ver, CurrentLedgerVersion)
	}
	if v.replacedOnDisk() {
		t.Fatal(":memory: vault must never report replacement")
	}
}

func TestOpenFileAndPragmas(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cold.db")
	v, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	var journal string
	if err := v.db.QueryRow(`PRAGMA journal_mode`).Scan(&journal); err != nil {
		t.Fatal(err)
	}
	if journal != "wal" {
		t.Fatalf("journal_mode = %q, want wal", journal)
	}
	var fk int
	if err := v.db.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil {
		t.Fatal(err)
	}
	if fk != 1 {
		t.Fatal("foreign_keys must be ON (cascades depend on it)")
	}
	// Reopen: migrations must be idempotent.
	if err := v.Close(); err != nil {
		t.Fatal(err)
	}
	v2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer v2.Close()
	if ver, err := v2.VaultVersion(); err != nil || ver != CurrentLedgerVersion {
		t.Fatalf("reopen version = %d, %v", ver, err)
	}
	// Replacement detection: swap the file under the vault. The swap goes
	// through rename so the replacement carries a distinct file identity
	// even on fast filesystems. The vault is closed first because Windows
	// locks open database files (no POSIX unlink-while-open);
	// replacedOnDisk only needs the open-time identity, which Close
	// retains, so the check stays meaningful on every OS.
	if err := v2.Close(); err != nil {
		t.Fatal(err)
	}
	swap := filepath.Join(dir, "swap.db")
	if err := os.WriteFile(swap, []byte("not a database"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(swap, path); err != nil {
		t.Fatal(err)
	}
	if !v2.replacedOnDisk() {
		t.Fatal("swapped file must report replacement")
	}
}

func TestDefaultPath(t *testing.T) {
	root := t.TempDir()
	got, err := DefaultPath(root)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, ".srcinsight", "srcinsight.db")
	if got != want {
		t.Fatalf("DefaultPath = %q, want %q", got, want)
	}
}

func TestLedgerRoundTrip(t *testing.T) {
	ctx := context.Background()
	v := openMemory(t)
	rec := OriginRecord{Path: "a.ts", Fingerprint: "abc", Dialect: model.LangTypeScript, Bytes: 10, AlteredAt: 5, CatalogedAt: 6, EntryCount: 2}
	if err := v.NoteOrigin(ctx, rec); err != nil {
		t.Fatal(err)
	}
	got, found, err := v.FetchOrigin(ctx, "a.ts")
	if err != nil || !found {
		t.Fatalf("fetch = %+v, %v, %v", got, found, err)
	}
	if got.Fingerprint != "abc" || got.EntryCount != 2 {
		t.Fatalf("round trip = %+v", got)
	}
	if _, found, _ := v.FetchOrigin(ctx, "missing.ts"); found {
		t.Fatal("missing path must not be found")
	}
	tracked, err := v.FetchTracked(ctx)
	if err != nil || len(tracked) != 1 || tracked[0].Path != "a.ts" {
		t.Fatalf("tracked = %+v, %v", tracked, err)
	}
}

func TestEntriesRoundTripAndValidation(t *testing.T) {
	ctx := context.Background()
	v := openMemory(t)
	rows := []EntryRow{
		{ID: "typescript:a.ts:function:foo:1", Kind: model.KindFunction, Name: "foo", Qualified: "foo", FilePath: "a.ts", Dialect: model.LangTypeScript, StartLine: 1, EndLine: 3, Signature: "foo()"},
		{ID: "", Kind: model.KindFunction, Name: "broken"}, // invalid: skipped
	}
	n, err := v.CatalogEntries(ctx, rows)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("written = %d, want 1 (invalid skipped)", n)
	}
	got, err := v.FetchEntriesByFile(ctx, "a.ts")
	if err != nil || len(got) != 1 || got[0].ID != "typescript:a.ts:function:foo:1" {
		t.Fatalf("fetch = %+v, %v", got, err)
	}
}

func TestLinksDedupAndNeighbors(t *testing.T) {
	ctx := context.Background()
	v := openMemory(t)
	mk := func(id, name string) EntryRow {
		return EntryRow{ID: model.EntryID(id), Kind: model.KindFunction, Name: name, Qualified: name, FilePath: "a.ts", Dialect: model.LangTypeScript, StartLine: 1, EndLine: 2}
	}
	if _, err := v.CatalogEntries(ctx, []EntryRow{mk("a", "caller"), mk("b", "callee")}); err != nil {
		t.Fatal(err)
	}
	link := LinkRow{Origin: "a", Target: "b", Kind: model.EdgeCalls}
	if _, err := v.CatalogLinks(ctx, []LinkRow{link, link}); err != nil {
		t.Fatal(err)
	}
	if _, err := v.CatalogLinks(ctx, []LinkRow{{Origin: "a", Target: "ghost", Kind: model.EdgeCalls}}); err != nil {
		t.Fatal(err)
	}
	n, l, err := v.Counts(ctx)
	if err != nil || n != 2 || l != 1 {
		t.Fatalf("counts = %d/%d, %v (want 2/1: dup + dangling dropped)", n, l, err)
	}
	deps, err := v.FetchNeighbors(ctx, "a", nil, false)
	if err != nil || len(deps) != 1 || deps[0].ID != "b" {
		t.Fatalf("dependencies = %+v, %v", deps, err)
	}
	callers, err := v.FetchNeighbors(ctx, "b", nil, true)
	if err != nil || len(callers) != 1 || callers[0].ID != "a" {
		t.Fatalf("dependents = %+v, %v", callers, err)
	}
}

func TestImportsAndLooseEndsRoundTrip(t *testing.T) {
	ctx := context.Background()
	v := openMemory(t)
	if _, err := v.CatalogEntries(ctx, []EntryRow{
		{ID: "a", Kind: model.KindFunction, Name: "a", Qualified: "a", FilePath: "a.ts", Dialect: model.LangTypeScript, StartLine: 1, EndLine: 1},
	}); err != nil {
		t.Fatal(err)
	}
	if err := v.CatalogImports(ctx, []ImportRow{{FilePath: "a.ts", LocalName: "foo", Source: "./b", Kind: "named"}}); err != nil {
		t.Fatal(err)
	}
	imps, err := v.FetchAllImports(ctx)
	if err != nil || len(imps["a.ts"]) != 1 || imps["a.ts"][0].Source != "./b" {
		t.Fatalf("imports = %+v, %v", imps, err)
	}
	if err := v.CatalogLooseEnds(ctx, []LooseEndRow{
		{From: "a", Name: "express.Router", Kind: model.EdgeCalls, Scope: "external:express", FilePath: "a.ts", Dialect: model.LangTypeScript},
		{From: "", Name: "nowhere"}, // invalid: skipped
	}); err != nil {
		t.Fatal(err)
	}
	loose, err := v.FetchAllLooseEnds(ctx)
	if err != nil || len(loose) != 1 || loose[0].Scope != "external:express" {
		t.Fatalf("loose ends = %+v, %v", loose, err)
	}
}

func TestForgetOriginCascades(t *testing.T) {
	ctx := context.Background()
	v := openMemory(t)
	mk := func(id string) EntryRow {
		return EntryRow{ID: model.EntryID(id), Kind: model.KindFunction, Name: id, Qualified: id, FilePath: "a.ts", Dialect: model.LangTypeScript, StartLine: 1, EndLine: 1}
	}
	if _, err := v.CatalogEntries(ctx, []EntryRow{mk("a"), mk("b")}); err != nil {
		t.Fatal(err)
	}
	if _, err := v.CatalogLinks(ctx, []LinkRow{{Origin: "a", Target: "b", Kind: model.EdgeCalls}}); err != nil {
		t.Fatal(err)
	}
	if err := v.CatalogLooseEnds(ctx, []LooseEndRow{{From: "a", Name: "x", Kind: model.EdgeCalls, Scope: "unresolved"}}); err != nil {
		t.Fatal(err)
	}
	if err := v.CatalogImports(ctx, []ImportRow{{FilePath: "a.ts", LocalName: "m", Source: "mod"}}); err != nil {
		t.Fatal(err)
	}
	if err := v.NoteOrigin(ctx, OriginRecord{Path: "a.ts", Fingerprint: "h", Dialect: model.LangTypeScript}); err != nil {
		t.Fatal(err)
	}
	if err := v.ForgetOrigin(ctx, "a.ts"); err != nil {
		t.Fatal(err)
	}
	n, l, err := v.Counts(ctx)
	if err != nil || n != 0 || l != 0 {
		t.Fatalf("counts after forget = %d/%d, want 0/0", n, l)
	}
	if loose, _ := v.FetchAllLooseEnds(ctx); len(loose) != 0 {
		t.Fatalf("loose ends survive: %+v", loose)
	}
	if imps, _ := v.FetchAllImports(ctx); len(imps) != 0 {
		t.Fatalf("imports survive: %+v", imps)
	}
	if _, found, _ := v.FetchOrigin(ctx, "a.ts"); found {
		t.Fatal("origin survives")
	}
	var fts int
	if err := v.db.QueryRow(`SELECT COUNT(*) FROM entries_fts`).Scan(&fts); err != nil || fts != 0 {
		t.Fatalf("fts rows = %d, %v (want 0 via trigger)", fts, err)
	}
}

func seedTwoFiles(t *testing.T, ctx context.Context, v *Vault) {
	t.Helper()
	mk := func(id, file string) EntryRow {
		return EntryRow{ID: model.EntryID(id), Kind: model.KindFunction, Name: id, Qualified: id, FilePath: file, Dialect: model.LangTypeScript, StartLine: 1, EndLine: 2}
	}
	bundle := func(file string, ids ...string) OriginBundle {
		var es []EntryRow
		for _, id := range ids {
			es = append(es, mk(id, file))
		}
		return OriginBundle{Origin: OriginRecord{Path: file, Fingerprint: "h", Dialect: model.LangTypeScript}, Entries: es}
	}
	// b.ts caller -> a.ts foo (cross-file incoming into a.ts).
	if err := v.ReplaceOrigin(ctx, bundle("a.ts", "a-foo", "a-bar")); err != nil {
		t.Fatal(err)
	}
	bb := bundle("b.ts", "b-caller")
	bb.Links = []LinkRow{{Origin: "b-caller", Target: "a-foo", Kind: model.EdgeCalls}}
	if err := v.ReplaceOrigin(ctx, bb); err != nil {
		t.Fatal(err)
	}
}

func TestReplaceOriginPreservesCallers(t *testing.T) {
	ctx := context.Background()
	v := openMemory(t)
	seedTwoFiles(t, ctx, v)

	// Re-index a.ts with foo intact plus a new sibling; the cross-file edge
	// from b-caller must survive the delete+reinsert.
	mk := func(id string) EntryRow {
		return EntryRow{ID: model.EntryID(id), Kind: model.KindFunction, Name: id, Qualified: id, FilePath: "a.ts", Dialect: model.LangTypeScript, StartLine: 1, EndLine: 2}
	}
	fresh := OriginBundle{
		Origin:  OriginRecord{Path: "a.ts", Fingerprint: "h2", Dialect: model.LangTypeScript},
		Entries: []EntryRow{mk("a-foo"), mk("a-baz")},
		Links:   []LinkRow{{Origin: "a-baz", Target: "a-foo", Kind: model.EdgeCalls}},
	}
	if err := v.ReplaceOrigin(ctx, fresh); err != nil {
		t.Fatal(err)
	}
	links, err := v.FetchAllLinks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := map[LinkRow]bool{
		{Origin: "b-caller", Target: "a-foo", Kind: model.EdgeCalls}: true,
		{Origin: "a-baz", Target: "a-foo", Kind: model.EdgeCalls}:    true,
	}
	if len(links) != len(want) {
		t.Fatalf("links = %+v, want %d", links, len(want))
	}
	for _, l := range links {
		if !want[l] {
			t.Fatalf("unexpected link %+v", l)
		}
	}
	rec, found, err := v.FetchOrigin(ctx, "a.ts")
	if err != nil || !found || rec.Fingerprint != "h2" || rec.EntryCount != 2 {
		t.Fatalf("origin = %+v, %v, %v", rec, found, err)
	}
}

func TestReplaceOriginDropsRenamedTargets(t *testing.T) {
	ctx := context.Background()
	v := openMemory(t)
	seedTwoFiles(t, ctx, v)

	// foo renamed to foo2: the caller edge must drop (no kind+name match).
	mk := func(id string) EntryRow {
		return EntryRow{ID: model.EntryID(id), Kind: model.KindFunction, Name: id, Qualified: id, FilePath: "a.ts", Dialect: model.LangTypeScript, StartLine: 1, EndLine: 2}
	}
	fresh := OriginBundle{
		Origin:  OriginRecord{Path: "a.ts", Fingerprint: "h3", Dialect: model.LangTypeScript},
		Entries: []EntryRow{mk("a-foo2")},
	}
	if err := v.ReplaceOrigin(ctx, fresh); err != nil {
		t.Fatal(err)
	}
	links, err := v.FetchAllLinks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 0 {
		t.Fatalf("renamed target must drop the edge, got %+v", links)
	}
}

func TestReconcileAndVerify(t *testing.T) {
	tracked := []TrackedOrigin{
		{Path: "same.ts", Fingerprint: "h1", Bytes: 10, AlteredAt: 100},
		{Path: "touched.ts", Fingerprint: "h2", Bytes: 10, AlteredAt: 100},
		{Path: "gone.ts", Fingerprint: "h3", Bytes: 10, AlteredAt: 100},
	}
	disk := []DiskStat{
		{Rel: "same.ts", Size: 10, AlteredAt: 100},
		{Rel: "touched.ts", Size: 12, AlteredAt: 200},
		{Rel: "new.ts", Size: 5, AlteredAt: 300},
	}
	added, changed, removed := Reconcile(tracked, disk)
	if len(added) != 1 || added[0] != "new.ts" {
		t.Fatalf("added = %v", added)
	}
	if len(changed) != 1 || changed[0] != "touched.ts" {
		t.Fatalf("changed = %v", changed)
	}
	if len(removed) != 1 || removed[0] != "gone.ts" {
		t.Fatalf("removed = %v", removed)
	}
	// Same hash despite new timestamps: false alarm, not modified.
	if got := Verify(tracked, []HashedFile{{Rel: "touched.ts", Hash: "h2"}}); len(got) != 0 {
		t.Fatalf("verify false alarm = %v", got)
	}
	if got := Verify(tracked, []HashedFile{{Rel: "touched.ts", Hash: "hX"}, {Rel: "new.ts", Hash: "hY"}}); len(got) != 1 || got[0] != "touched.ts" {
		t.Fatalf("verify = %v (untracked new.ts must be ignored)", got)
	}
}

func TestAssembleSnapshot(t *testing.T) {
	ctx := context.Background()
	v := openMemory(t)
	mk := func(id, file, sig string) EntryRow {
		return EntryRow{ID: model.EntryID(id), Kind: model.KindFunction, Name: id, Qualified: id, FilePath: file, Dialect: model.LangTypeScript, StartLine: 1, EndLine: 2, Signature: sig}
	}
	if err := v.ReplaceOrigin(ctx, OriginBundle{
		Origin:  OriginRecord{Path: "a.ts", Fingerprint: "h", Dialect: model.LangTypeScript},
		Entries: []EntryRow{mk("a-caller", "a.ts", "a-caller()"), mk("a-cal", "a.ts", "a-cal()")},
		Links:   []LinkRow{{Origin: "a-caller", Target: "a-cal", Kind: model.EdgeCalls}},
		LooseEnds: []LooseEndRow{
			{From: "a-caller", Name: "express.Router", Kind: model.EdgeCalls, Scope: "external:express", FilePath: "a.ts", Dialect: model.LangTypeScript},
		},
	}); err != nil {
		t.Fatal(err)
	}
	snap, err := v.AssembleSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Nodes) != 2 || len(snap.Edges) != 1 {
		t.Fatalf("snapshot nodes=%d edges=%d", len(snap.Nodes), len(snap.Edges))
	}
	byID := map[model.EntryID]model.Function{}
	for _, n := range snap.Nodes {
		byID[n.ID] = n
	}
	caller := byID["a-caller"]
	if len(caller.Calls) != 2 {
		t.Fatalf("caller calls = %+v (want resolved + external)", caller.Calls)
	}
	cal := byID["a-cal"]
	if len(cal.CalledBy) != 1 || cal.CalledBy[0].ID != "a-caller" {
		t.Fatalf("callee calledBy = %+v", cal.CalledBy)
	}
	if caller.Signature != "a-caller()" {
		t.Fatalf("signature lost: %q", caller.Signature)
	}
}

func TestMetaWipeCounts(t *testing.T) {
	ctx := context.Background()
	v := openMemory(t)
	if err := v.PutMeta(ctx, MetaExtractor, "test-1"); err != nil {
		t.Fatal(err)
	}
	val, found, err := v.GetMeta(ctx, MetaExtractor)
	if err != nil || !found || val != "test-1" {
		t.Fatalf("meta = %q, %v, %v", val, found, err)
	}
	if _, found, _ := v.GetMeta(ctx, "nope"); found {
		t.Fatal("absent key must not be found")
	}
	if _, err := v.CatalogEntries(ctx, []EntryRow{
		{ID: "a", Kind: model.KindFunction, Name: "a", Qualified: "a", FilePath: "a.ts", Dialect: model.LangTypeScript, StartLine: 1, EndLine: 1},
	}); err != nil {
		t.Fatal(err)
	}
	if n, l, err := v.Counts(ctx); err != nil || n != 1 || l != 0 {
		t.Fatalf("counts = %d/%d, %v", n, l, err)
	}
	if err := v.Wipe(ctx); err != nil {
		t.Fatal(err)
	}
	if n, _, _ := v.Counts(ctx); n != 0 {
		t.Fatal("wipe must clear content")
	}
	if val, found, _ := v.GetMeta(ctx, MetaExtractor); !found || val != "test-1" {
		t.Fatal("wipe must preserve metadata")
	}
}

func TestTransactRollsBack(t *testing.T) {
	ctx := context.Background()
	v := openMemory(t)
	// A statement failure inside Transact rolls everything back.
	err := v.Transact(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO entries (id) VALUES (NULL)`); err != nil {
			return err
		}
		return nil
	})
	if err == nil {
		t.Fatal("expected constraint error")
	}
	if n, _, _ := v.Counts(ctx); n != 0 {
		t.Fatal("rolled-back rows must not persist")
	}
	// A replace whose links are all stale still commits the valid rows.
	mk := func(id string) EntryRow {
		return EntryRow{ID: model.EntryID(id), Kind: model.KindFunction, Name: id, Qualified: id, FilePath: "a.ts", Dialect: model.LangTypeScript, StartLine: 1, EndLine: 1}
	}
	if err := v.ReplaceOrigin(ctx, OriginBundle{
		Origin:  OriginRecord{Path: "a.ts", Fingerprint: "h", Dialect: model.LangTypeScript},
		Entries: []EntryRow{mk("keep-me")},
	}); err != nil {
		t.Fatal(err)
	}
	bad := OriginBundle{
		Origin:  OriginRecord{Path: "a.ts", Fingerprint: "h2", Dialect: model.LangTypeScript},
		Entries: []EntryRow{mk("new-guy")},
		Links:   []LinkRow{{Origin: "new-guy", Target: "", Kind: model.EdgeCalls}}, // skipped, not an error
	}
	if err := v.ReplaceOrigin(ctx, bad); err != nil {
		t.Fatal(err)
	}
	got, err := v.FetchEntriesByFile(ctx, "a.ts")
	if err != nil || len(got) != 1 || got[0].ID != "new-guy" {
		t.Fatalf("after replace = %+v, %v", got, err)
	}
}

func TestRecatalogLooseEnds(t *testing.T) {
	ctx := context.Background()
	v := openMemory(t)
	mk := func(id string) EntryRow {
		return EntryRow{ID: model.EntryID(id), Kind: model.KindFunction, Name: id, Qualified: id, FilePath: "a.ts", Dialect: model.LangTypeScript, StartLine: 1, EndLine: 1}
	}
	if _, err := v.CatalogEntries(ctx, []EntryRow{mk("a"), mk("b")}); err != nil {
		t.Fatal(err)
	}
	// A re-resolve promotes B's ref to resolved and demotes A's to loose:
	// replacing the whole set must converge exactly, not append.
	if err := v.CatalogLooseEnds(ctx, []LooseEndRow{
		{From: "a", Name: "old-unresolved", Kind: model.EdgeCalls, Scope: model.ScopeUnresolved},
		{From: "b", Name: "foo", Kind: model.EdgeCalls, Scope: model.ScopeUnresolved},
	}); err != nil {
		t.Fatal(err)
	}
	if err := v.RecatalogLooseEnds(ctx, []LooseEndRow{
		{From: "a", Name: "express.Router", Kind: model.EdgeCalls, Scope: "external:express", FilePath: "a.ts", Dialect: model.LangTypeScript},
		{From: "", Name: "nowhere"}, // invalid: skipped
	}); err != nil {
		t.Fatal(err)
	}
	got, err := v.FetchAllLooseEnds(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].From != "a" || got[0].Scope != "external:express" {
		t.Fatalf("after recatalog = %+v, want exactly the fresh row", got)
	}
}
