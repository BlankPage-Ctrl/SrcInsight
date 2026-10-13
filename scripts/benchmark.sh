#!/usr/bin/env bash
# Usage:
#   ./scripts/benchmark.sh --root ./projectTest/angular-main --bin /tmp/srcinsight --work ~/fg-disk/wk-angular --runs 3
#   ./scripts/benchmark.sh --root ./projectTest/zod-main --bin ./srcinsight --work /tmp/wk-zod --runs 1 --no-copy
#
# Variables (all overridable via flags or env):
#   ROOT        Project root to index (default: ./projectTest/zod-main)
#   BIN         Path to srcinsight binary (default: ./srcinsight or built to /tmp/srcinsight)
#   WORK        Working copy dir (default: ~/fg-disk/wk-<basename>-$$, physical disk to avoid /tmp tmpfs noise)
#   RUNS        Fresh index runs (default: 3)
#   KEEP_COPY   If set, keep working copy after run
#   NO_COPY     If set, benchmark directly on ROOT without copying (noise: git + original .srcinsight)
#
# Stats collected (all via /usr/bin/time -f "%M" peak RSS + wall):
#   - Fresh index (no db) x RUNS
#   - Second index no changes (stat pre-filter)
#   - Sync 1 changed file (modify 1 file)
#   - Sync no-op
#   - Graph latency (single-root exact FunctionID) + Search latency
#   - Idle/Search/Graph RSS via serve coproc (VmRSS)

set -euo pipefail

ROOT="${ROOT:-./projectTest/zod-main}"
BIN="${BIN:-./srcinsight}"
WORK="${WORK:-}"
RUNS="${RUNS:-3}"
KEEP_COPY="${KEEP_COPY:-}"
NO_COPY="${NO_COPY:-}"

# Parse flags
while [[ $# -gt 0 ]]; do
  case "$1" in
    --root) ROOT="$2"; shift 2;;
    --bin) BIN="$2"; shift 2;;
    --work) WORK="$2"; shift 2;;
    --runs) RUNS="$2"; shift 2;;
    --keep-copy) KEEP_COPY=1; shift;;
    --no-copy) NO_COPY=1; shift;;
    --help|-h) echo "Usage: $0 [--root DIR] [--bin PATH] [--work DIR] [--runs N] [--no-copy] [--keep-copy]"; exit 0;;
    *) echo "Unknown arg $1"; exit 1;;
  esac
done

# Resolve absolute
ROOT=$(realpath "$ROOT")
if [[ "$BIN" != /* ]]; then BIN=$(realpath "$BIN" 2>/dev/null || echo "$BIN"); fi
if [[ -z "$WORK" ]]; then
  # Default to physical disk ~/fg-disk to avoid /tmp tmpfs noise (per benchmark-zod-v3.md)
  WORK="$HOME/fg-disk/wk-$(basename "$ROOT")-$$"
fi
WORK=$(realpath -m "$WORK")

# Build binary if missing
if [[ ! -x "$BIN" ]]; then
  echo "Building $BIN ..."
  go build -o "$BIN" ./cmd/srcinsight
fi

# System info
echo "=== Environment ==="
echo "Date: $(date -I)"
echo "CPU: $(lscpu | grep 'Model name' | cut -d: -f2 | xargs) ($(nproc) threads)"
echo "RAM: $(grep MemTotal /proc/meminfo | awk '{print $2/1024/1024}') GiB"
echo "OS: $(uname -r) $(lsb_release -d 2>/dev/null | cut -f2 || cat /etc/os-release | grep PRETTY | cut -d= -f2)"
echo "Disk: $(df -h "$WORK" 2>/dev/null | tail -n 1 | awk '{print $1, $2, $3}')"
echo "Go: $(go version)"
echo "Binary: $BIN ($(du -h "$BIN" | awk '{print $1}'))"
echo "Root: $ROOT"
echo "Work: $WORK (NO_COPY=$NO_COPY)"
echo "Runs: $RUNS"
echo ""

# Prepare work dir
if [[ -z "$NO_COPY" ]]; then
  echo "Copying $ROOT -> $WORK (physical disk, non-noise) ..."
  rm -rf "$WORK"
  mkdir -p "$(dirname "$WORK")"
  cp -r "$ROOT" "$WORK"
  rm -rf "$WORK/.srcinsight" "$WORK/.git" 2>/dev/null || true
  BENCH_ROOT="$WORK"
else
  echo "No-copy: benchmarking directly on $ROOT ..."
  BENCH_ROOT="$ROOT"
  # Clean only .srcinsight, not whole dir
  rm -rf "$BENCH_ROOT/.srcinsight"
fi

# Corpus stats (pre-filter)
SRC_FILES=$(find "$BENCH_ROOT" -type f \( -name "*.ts" -o -name "*.tsx" -o -name "*.mts" -o -name "*.cts" -o -name "*.py" \) 2>/dev/null | wc -l)
CORPUS_SIZE=$(du -sh "$BENCH_ROOT" 2>/dev/null | awk '{print $1}')
TS_LINES=$(find "$BENCH_ROOT" -type f -name "*.ts" -exec cat {} + 2>/dev/null | wc -l | awk '{print int($1/1000)"k"}')
echo "Corpus size: $CORPUS_SIZE"
echo "Source files (pre-filter): $SRC_FILES"
echo "TS lines (approx): $TS_LINES"
echo ""

# Helper to run with /usr/bin/time
run_timed() {
  /usr/bin/time -f "wall=%es maxrss=%MKB" "$@" 2>&1
}

# Fresh index
for i in $(seq 1 "$RUNS"); do
  rm -rf "$BENCH_ROOT/.srcinsight"
  echo "--- Fresh index run $i ---"
  run_timed "$BIN" index --root "$BENCH_ROOT" > /tmp/bench_out 2>&1
  cat /tmp/bench_out
done

# Second index no changes
echo "--- Second index no changes ---"
run_timed "$BIN" index --root "$BENCH_ROOT" > /tmp/bench_out 2>&1
cat /tmp/bench_out

# DB stats (never fail the whole bench)
set +e
if [[ -f "$BENCH_ROOT/.srcinsight/srcinsight.db" ]]; then
  echo "--- DB stats ---"
  sqlite3 "$BENCH_ROOT/.srcinsight/srcinsight.db" "SELECT 'entries ' || COUNT(*) FROM entries; SELECT 'links ' || COUNT(*) FROM links; SELECT 'loose ' || COUNT(*) FROM loose_ends; SELECT 'imports ' || COUNT(*) FROM brought_in;" 2>&1 | head -n 10 || true
  ls -lh "$BENCH_ROOT/.srcinsight/srcinsight.db" 2>/dev/null | awk '{print "DB size:", $5}' || true
  # Detailed breakdown
  sqlite3 "$BENCH_ROOT/.srcinsight/srcinsight.db" "SELECT kind, COUNT(*) FROM entries GROUP BY kind ORDER BY COUNT(*) DESC;" 2>&1 | head -n 15 | awk '{print " entries kind:", $0}' || true
fi
set -e

# Sync 1 changed file (robust against pipefail)
set +e
TARGET=$(find "$BENCH_ROOT/packages" -name "*.ts" 2>/dev/null | head -n 1 || true)
if [[ -z "$TARGET" ]]; then TARGET=$(find "$BENCH_ROOT" -name "*.ts" 2>/dev/null | head -n 1 || true); fi
set -e
echo "Target for sync: $TARGET"
if [[ -n "$TARGET" && -f "$TARGET" ]]; then
  echo "// bench touch $(date)" >> "$TARGET"
  echo "--- Sync 1 changed file ---"
  run_timed "$BIN" sync --root "$BENCH_ROOT" > /tmp/bench_out 2>&1 || true
  cat /tmp/bench_out || true
  echo "--- Sync no-op ---"
  run_timed "$BIN" sync --root "$BENCH_ROOT" > /tmp/bench_out 2>&1 || true
  cat /tmp/bench_out || true
  # Revert
  head -n -1 "$TARGET" > /tmp/revert.tmp && mv /tmp/revert.tmp "$TARGET" || true
  run_timed "$BIN" sync --root "$BENCH_ROOT" > /tmp/bench_out 2>&1 || true
  cat /tmp/bench_out || true
else
  echo "No TS file found for sync test"
fi

# Incremental hinted (HintBudget 456) vs full stat — literal Watcher+Sync path
echo "--- Incremental hinted vs full (HintBudget 456, watch pending) ---"
set +e
REPO_ROOT="$(realpath "$(dirname "${BASH_SOURCE[0]}")/..")"
( cd "$REPO_ROOT" && go run ./cmd/benchincremental "$BENCH_ROOT" 2>&1 ) || echo "incremental bench failed"
set -e

# Graph/Search latency & RAM (via direct vault, not serve)
echo "--- Graph / Search latency (direct vault) ---"
set +e
REPO_ROOT="$(realpath "$(dirname "${BASH_SOURCE[0]}")/..")"
( cd "$REPO_ROOT" && go run ./cmd/benchhelper "$BENCH_ROOT" 2>&1 | head -n 10 ) || echo "graph latency helper failed"
( cd "$REPO_ROOT" && /usr/bin/time -f "maxrss=%MKB" go run ./cmd/benchhelper "$BENCH_ROOT" 2>&1 | grep maxrss ) || true
set -e

# Idle serve RSS (coproc) — must not be killed by set -e / pipefail
set +e
echo "--- Idle serve RSS (coproc) ---"
coproc SERVE { exec "$BIN" serve --root "$BENCH_ROOT" 2> /tmp/serve.err; }
SPID=$SERVE_PID
sleep 4
if kill -0 $SPID 2>/dev/null; then
  ps -o pid,rss,vsz,cmd -p $SPID | head -n 2
  cat /proc/$SPID/status 2>/dev/null | grep -E "VmRSS|VmHWM" | head -n 2 || true
  printf '{"jsonrpc":"2.0","id":1,"method":"search","params":{"query":"bootstrapApplication"}}\n' >&${SERVE[1]} || true
  read -u ${SERVE[0]} -t 5 line || true
  sleep 1
  ps -o pid,rss -p $SPID | tail -n 1 || true
  cat /proc/$SPID/status 2>/dev/null | grep VmRSS | head -n 1 || true
  FID2=$(sqlite3 "$BENCH_ROOT/.srcinsight/srcinsight.db" "SELECT id FROM entries LIMIT 1;" 2>/dev/null | head -n 1 || true)
  if [[ -n "$FID2" ]]; then
    printf '{"jsonrpc":"2.0","id":2,"method":"graph","params":{"id":"'"$FID2"'" }}\n' >&${SERVE[1]} || true
    read -u ${SERVE[0]} -t 5 line2 || true
    sleep 1
    ps -o pid,rss -p $SPID | tail -n 1 || true
    cat /proc/$SPID/status 2>/dev/null | grep VmRSS | head -n 1 || true
  fi
  printf '{"jsonrpc":"2.0","id":99,"method":"shutdown"}\n' >&${SERVE[1]} || true
  sleep 1
  wait $SPID 2>/dev/null || true
  echo "serve exited"
else
  echo "serve failed to start"
  cat /tmp/serve.err 2>/dev/null | head -n 20 || true
fi
set -e

# Cleanup
if [[ -z "$KEEP_COPY" && -z "$NO_COPY" ]]; then
  echo "Cleaning $WORK ..."
  # rm -rf "$WORK"
  echo "Kept for inspection: $WORK (use --keep-copy to retain)"
fi

echo "Done. Work dir: $WORK"
