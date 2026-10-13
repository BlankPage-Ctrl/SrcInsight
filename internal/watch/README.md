# The Watcher — Trigger Only, Never the Source of Truth

`internal/watch` collects **candidate paths** and triggers a debounced sync.
It never parses files, never hashes content, and never mutates the graph.
The sync layer (`Indexer`) owns the truth by reconciling filesystem-vs-store.

## The golden rule

> Watcher proposes. Sync disposes.

- Watcher: `fsnotify event → normalize → filter → pending[path] = now → scheduleSync() → Sync()`.
- Sync: `scan + stat/size+mtime pre-filter → read+hash → re-parse changed only → ReplaceFile/RemoveFile → re-resolve → Save`.
- Event **kind is ignored** (`Create`/`Write`/`Remove`/`Rename` all mean "check this path"). The sync verifies with `os.Stat`: missing = delete, present = add-or-modify. This makes rename handling free (`delete(old) + add(new)`) and immune to missed/coalesced events.

## Why trigger-only is faster and safer

1. **Bounded cost.** One `fsnotify` watch per *directory* (`O(dirs)`), never per file. macOS/Windows need one recursive watch; Linux needs the `addRecursive` walk in `watcher.go`. Cap it (`SRCINSIGHT_MAX_DIR_WATCHES`, default 50000). Beyond the cap: warn once, serve watched dirs, leave the rest to manual sync.
2. **Cheap event path.** `handleEvent` does no I/O except a single `os.Stat` when the path *is a new directory* (to start watching it). Everything else is string filtering + a map write. No `ReadFile`, no hash, no parse in the watcher.
3. **Filter early, verify late.** Drop in the watcher: `.git/`, `.srcinsight/`, ignored dirs (including any *parent prefix*), ignored files, non-source extensions, editor temp files (`~`, `.swp`, `.tmp`, `.bak`, `.#*`). Verify in sync: `exists?`, `size+mtime changed?`, `hash changed?`.
4. **Debounce globally.** One timer (default 2000ms, env `SRCINSIGHT_WATCH_DEBOUNCE_MS`, clamped 100–60000). A 100-save burst = 1 sync. Events arriving mid-sync stay pending (`lastSeen > syncStarted`) for a follow-up flush. Sync failure keeps the full pending set and retries with backoff (`debounce * 2^(n-1)`, capped 30s, max 5 tries).
5. **No lost files on mkdir.** When a directory is created, `addRecursive(newDir)` + `markTreePending(newDir)` so files written between `mkdir` and `Add` are still collected. This closes the classic `mkdir+write` race.
6. **Degrade gracefully.** `ENOSPC` (inotify limit) = warn, keep going. `EMFILE/ENFILE` (fd exhaustion) = degrade to manual sync. `SRCINSIGHT_NO_WATCH=1` or WSL2 `/mnt/*` mounts = watcher disabled by policy (`policy.go: WatchDisabledReason`).

## Wiring recipe (for future Indexer + Graph work)

```go
ign := filesystem.Load(root)
reg := indexer.NewRegistry()

w, _ := watch.New(watch.Options{
    Root: root,
    IsIgnoredDir:  ign.IgnoredDir,
    IsIgnoredFile: ign.IgnoredFile,
    IsSourceFile: func(rel string) bool {
        _, ok := reg.LookupFile(rel)
        return ok
    },
    Sync: func(ctx context.Context) (int, error) {
        res, err := indexerSvc.Sync(ctx, w.GetPendingFiles())
        // res.Added + res.Modified + res.Removed
        return res.Changed(), err
    },
})
go w.Start(ctx)
```

Requirements on the future `Indexer.Sync(ctx, pending []watch.PendingFile)`:

1. **Reconcile, don't trust.** Re-scan the root (respecting `Ignore` + `Registry`), diff against `Store` file list. `pending` is only a *hint* for logging/metrics — the diff decides.
2. **Pre-filter before read.** Skip files whose `size` and `mtime` (floored) match the tracked record. Only changed candidates get `read + SHA256 + tree-sitter parse`.
3. **Partial store update.** Per-file `Store.ReplaceFile(path, nodes, edges)` for add/modify, `Store.RemoveFile(path)` for delete — never full `Save()` on a watch tick. `Graph.ReplaceFile` already exists for this.
4. **Re-resolve safely (phase 1: global).** Rebuild resolution from all functions + imports (`resolve.New(all).Resolve()`), then `ReplaceFile`. Phase 1 cost is `O(N)` resolve but `O(changed)` parse — correct for renames/signature changes. Optimize to changed-only resolution later, once edge-identity (`kind\0name`) snapshot logic exists.
5. **Watch out for ID instability.** `EntryID` format is `<lang>:<path>:<kind>:<qualifiedName>:<ordinal>`, so ordinal shifts if same-named kinds are reordered. `ReplaceFile` (remove-all-then-add) already handles this; a naive per-function upsert would leave ghosts.

## Tuning knobs

| Env | Default | Meaning |
|-----|---------|---------|
| `SRCINSIGHT_WATCH_DEBOUNCE_MS` | `2000` | Coalescing window (clamped 100–60000) |
| `SRCINSIGHT_MAX_DIR_WATCHES` | `50000` | Cap on recursive directory watches |
| `SRCINSIGHT_NO_WATCH` | unset | Set `1` to disable watching (CI) |

## Testing the recipe

- Burst 20 writes → `Sync` called once, `pending` holds 1 path.
- Write to `node_modules/`, `*.min.js`, `.git/` → `Sync` never fires.
- `mkdir sub/ + write sub/a.ts` → `sub/a.ts` pending (recursive add works).
- `rm a.ts` → still pending (sync confirms deletion via `os.Stat`).
- `Sync` fails once → pending intact + backoff retry.
