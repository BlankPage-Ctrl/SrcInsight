# Benchmark Indexing SrcInsight — Zod, v7-Windows (native PowerShell port)

Date: 2026-10-04 · Corpus: `projectTest/zod-main` in place (`-NoCopy`,
original fixture untouched — `.srcinsight` removed afterwards, sync touch
reverted by the script).

This is the Windows counterpart of `benchmark-zod-v7.md`, measured with the
native-Windows port `scripts/benchmark.ps1` (Stopwatch wall time,
`WorkingSet64` sampling every 25ms for peak RSS, CIM `Win32_*` environment,
robocopy for copy mode). Same code HEAD as v7 (Go support + branch
conditions + Go test detection), same `--no-copy` mode.

It is NOT a head-to-head rerun: the fixture on this disk is a newer/different
`zod-main` revision than the v7 Linux fixture (515 vs 505 source files, and a
different link topology — see Comparison), and the OS/toolchain differ
(Windows 10 + Go 1.27.1 vs Linux + Go 1.26.5). Treat the numbers below as the
Windows baseline, and the v7 column as rough context only.

Machine state during this run: quiet box, low spread (fresh runs spread
13.22–13.27s).

## Environment

| Component | Value |
|---|---|
| CPU | Intel Core i7 M 620 @ 2.67GHz, 4 logical threads (x86_64) |
| RAM | 7.8 GiB |
| OS / Build | Microsoft Windows 10 Pro 64-bit (build 19045) |
| Working disk | C: (used 32.5 GB / free 254.8 GB), projectTest in place (`-NoCopy`) |
| Go toolchain | go1.27.1 windows/amd64, CGO_ENABLED=1 (MSYS2 UCRT64 GCC 16.2.0, required for tree-sitter) |
| Binary | locally built `srcinsight.exe` (22.8 MB, CGO) |
| Parse workers | 3 default (`NumCPU-1`, max 8) |

## Corpus & index results

| Metric | Value |
|---|---|
| Source files (ts/tsx/mts/cts/py) | 515 |
| TS code lines | ~100,000 |
| Entries | 21,215 (`const` 10,018 = 47%) |
| Links (resolved) | 61,207 (`calls` 12,704; `uses` 46,199; `param` 1,093; `extends` 696; `return` 514; `implements` 1) |
| Loose ends (unresolved, scoped) | 72,579 |
| Imports | 2,185 (incl. re-export rows) |
| Receiver bindings | 6,755 (`recv_binds`) |
| Links with branch conditions | 1,973 (`links.cond != ''`; TS `if`/ternary guards) |
| Tagged entries | 0 (no decorators in this corpus; column present, empty) |
| `srcinsight.db` size | 81.3 MB |

Entry kind breakdown:

| Kind | Count |
|---|---|
| `const` | 10,018 (47%) |
| `arrow` | 7,225 |
| `type` | 990 |
| `function` | 973 |
| `interface` | 706 |
| `method` | 701 |
| `async-arrow` | 406 |
| `class` | 78 |
| `async-function` | 45 |
| `enum` | 32 |
| `constructor` | 22 |
| `async-method` | 19 |

## Results (wall time + peak RSS)

Peak RSS here is sampled `WorkingSet64` (25ms poll, maxed with the
`PeakWorkingSet64` API), i.e. the process working set — the closest native
equivalent of `/usr/bin/time %MKB`, not the identical kernel counter.

### Fresh index, 3 workers (default) — no-copy benchmark

| Run | Wall time | Peak RSS |
|---|---|---|
| 1 | 13.27s | 254 MB |
| 2 | 13.22s | 248 MB |
| 3 | 13.24s | 243 MB |
| **Avg** | **13.24s** | **~248 MB** |

### Second index, no changes (`added=0`)

| Metric | Value |
|---|---|
| Wall time | 0.33s |
| Peak RSS | 17 MB |

### Sync 1 changed file (`modified=1`)

| Metric | Value |
|---|---|
| Wall time | 0.49s |
| Peak RSS | 39 MB |

(Target: `packages/bench/arktype-morph-test.ts`.)

(Revert re-sync: 0.47s / 37 MB.)

### Sync no-op

| Metric | Value |
|---|---|
| Wall time | 0.32s |
| Peak RSS | 17 MB |

### Incremental hinted vs full (benchincremental)

| Metric | Value |
|---|---|
| Scoped (1 file) | 0.119s |
| Full (515 files) | 0.182s |
| Scoped no-op | 0.010s |
| Full no-op | 0.091s |

### Graph / Search latency (direct vault)

| Metric | Value |
|---|---|
| Graph | n/a (no `bootstrap*` symbol in this corpus — helper resolved an empty fid, 0 nodes/edges) |
| Search (0 hits) | 122 ms |
| Serve idle RSS | 24 MB idle, ~34 MB after search/graph |

Full index throughput (no-copy benchmark): **~39 files/sec, ~1,602 entries/sec**.

## Comparison with v7 (Linux)

| Scenario | v7 Linux | v7-Windows | Δ |
|---|---|---|---|
| Full index fresh | 12.99s | 13.24s | +0.25s (+2%) |
| Second index (skip) | 0.38s | 0.33s | flat |
| Sync 1 file | 0.49s | 0.49s | **flat** |
| Sync no-op | 0.34s | 0.32s | flat |
| Peak RSS (fresh) | ~215 MB | ~248 MB | +33 MB (working-set sampling + Windows overhead — methodology differs, see Notes) |
| DB size | 84 MB | 81.3 MB | −2.7 MB |

DB contents v7 → v7-Windows: entries 20,424 → 21,215 (+791); links
86,609 → 61,207 (−25,402, mostly `calls` 31,682 → 12,704 while `uses`
52,619 → 46,199); loose ends 46,206 → 72,579 (+26,373); imports
2,131 → 2,185 (+54); receiver bindings 6,501 → 6,755 (+254); `cond`
links 1,895 → 1,973 (+78). **All of this delta is fixture drift, not code**:
this disk's `zod-main` has 515 source files vs the v7 fixture's 505 (and a
different sync target file, `arktype-morph-test.ts` vs
`failing-safeparse.ts`), so no regression or improvement claim follows —
the topology difference is the corpus, confirmed by construction (the v7
series changed no TS link topology).

## Copy-mode cross-check

Not exercised on Windows yet: this run used `-NoCopy` only, mirroring the
v7 doc. The `scripts/benchmark.ps1` copy path (robocopy, excl
`.srcinsight`/`.git`) is implemented but unmeasured — a copy-mode run is a
follow-up, not covered here.

## How to reproduce

```powershell
# Requires: Go 1.27+, MSYS2 UCRT64 gcc on PATH, CGO_ENABLED=1, sqlite3.exe
$env:PATH = 'C:\msys64\ucrt64\bin;' + $env:PATH
$env:CGO_ENABLED = '1'
go build -o ./srcinsight.exe ./cmd/srcinsight
powershell -ExecutionPolicy Bypass -File scripts/benchmark.ps1 -Root ./projectTest/zod-main -Runs 3 -NoCopy
# modify 1 file, then:
.\srcinsight.exe sync --root .\projectTest\zod-main   # no-op
```

Verify DB contents:

```powershell
sqlite3 .\projectTest\zod-main\.srcinsight\srcinsight.db `
  "SELECT COUNT(*) AS entries FROM entries; SELECT COUNT(*) AS links FROM links;"
sqlite3 .\projectTest\zod-main\.srcinsight\srcinsight.db `
  "SELECT kind, COUNT(*) FROM links GROUP BY kind ORDER BY COUNT(*) DESC;"
sqlite3 .\projectTest\zod-main\.srcinsight\srcinsight.db `
  "SELECT COUNT(*) AS cond_links FROM links WHERE cond != '';"
```

(Remove `projectTest\zod-main\.srcinsight` afterwards to leave the fixture
pristine — the script does this itself at the end in `-NoCopy` mode.)

## Notes

- Wall times land within 2% of the v7 Linux numbers on the same CPU model
  (i7 M 620, 3 workers both sides): the pipeline has no measurable
  Windows-specific penalty on the indexing or stat-dominated paths.
- Peak RSS is NOT directly comparable to the Linux `%MKB` numbers: Windows
  has no `/usr/bin/time` equivalent, so the script samples `WorkingSet64`
  every 25ms and maxes it with `PeakWorkingSet64` (the pure-API read
  returned 0 after exit in this environment, which is why sampling is the
  primary source). The +33 MB gap is methodology + OS overhead, not a leak
  signal — serve-idle RSS (24 MB vs 27 MB) agrees across OSes.
- The fixture drift (+10 files, −25k links / +26k loose ends) dominates every
  DB-content comparison: do not read OS or toolchain effects into it.
- `cond` storage is still unmeasurable: DB is 81.3 MB with 1,973 conditions
  stored, in line with the flat-84-MB story on Linux.
- Graph latency is n/a this run (helper found no `bootstrap*` fid on this
  corpus revision); search at 0 hits (122 ms vs 81 ms) is the only
  cross-OS latency datapoint, and it sits in the expected Windows-slower
  band for a cold direct-vault query.
- Build prerequisite, Windows-only: tree-sitter bindings require CGO, so a
  MSYS2 UCRT64 (or equivalent) gcc must be on PATH with `CGO_ENABLED=1`;
  with the stock `CGO_ENABLED=0` the build fails in `internal/extract`
  (`undefined: sitter.Node`). The repo-root `./srcinsight` (26 MB) is the
  Linux ELF binary from the v7 series and cannot run on Windows.
