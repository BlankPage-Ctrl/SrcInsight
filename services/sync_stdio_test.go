package services

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"srcinsight/transport/stdio"
)

func syncResultOf(t *testing.T, res any, rerr *stdio.RPCError) SyncResult {
	t.Helper()
	if rerr != nil {
		t.Fatalf("handler error: %+v", rerr)
	}
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	var out SyncResult
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("result is not a SyncResult: %v", err)
	}
	return out
}

// Fresh index then no-op sync: nothing changed, SyncResult empty of writes.
func TestHandleSyncNoop(t *testing.T) {
	ctx := context.Background()
	p, _ := testPipeline(t, map[string]string{
		"src/a.ts": "export function foo(): number { return 1; }\n",
	})
	defer p.Close()

	if _, err := p.FullScan(ctx, true); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []json.RawMessage{nil, {}, json.RawMessage(`{}`)} {
		res, rerr := p.HandleSync(ctx, raw)
		if out := syncResultOf(t, res, rerr); out.Changed() != 0 {
			t.Fatalf("params %q changed = %+v, want no-op", raw, out)
		}
	}
}

// One file changed: sync reports exactly one modification.
func TestHandleSyncModified(t *testing.T) {
	ctx := context.Background()
	p, dir := testPipeline(t, map[string]string{
		"src/a.ts": "export function foo(): number { return 1; }\n",
	})
	defer p.Close()

	if _, err := p.FullScan(ctx, true); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "src", "a.ts"),
		[]byte("export function foo(): number { return 2; }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, rerr := p.HandleSync(ctx, nil)
	if out := syncResultOf(t, res, rerr); out.Modified != 1 {
		t.Fatalf("sync = %+v, want modified=1", out)
	}
}

// Sync rejects force: that belongs to index.
func TestHandleSyncRejectsForce(t *testing.T) {
	ctx := context.Background()
	p, _ := testPipeline(t, map[string]string{
		"src/a.ts": "export function foo(): number { return 1; }\n",
	})
	defer p.Close()

	_, rerr := p.HandleSync(ctx, json.RawMessage(`{"force":true}`))
	if rerr == nil || rerr.Code != stdio.CodeInvalidParams {
		t.Fatalf("force = %+v, want -32602", rerr)
	}
}

// Index with force re-parses everything.
func TestHandleIndexForce(t *testing.T) {
	ctx := context.Background()
	p, _ := testPipeline(t, map[string]string{
		"src/a.ts": "export function foo(): number { return 1; }\n",
		"src/b.ts": "export function bar(): number { return 2; }\n",
	})
	defer p.Close()

	if _, err := p.FullScan(ctx, true); err != nil {
		t.Fatal(err)
	}
	res, rerr := p.HandleIndex(ctx, json.RawMessage(`{"force":true}`))
	if out := syncResultOf(t, res, rerr); out.FilesChecked != 2 || out.Added != 2 {
		t.Fatalf("index force = %+v, want full rescan of 2", out)
	}
}

// A held write mutex answers busy instead of blocking.
func TestHandleSyncBusy(t *testing.T) {
	ctx := context.Background()
	p, _ := testPipeline(t, map[string]string{
		"src/a.ts": "export function foo(): number { return 1; }\n",
	})
	defer p.Close()

	p.mu.Lock()
	defer p.mu.Unlock()
	_, rerr := p.HandleSync(ctx, nil)
	if rerr == nil || rerr.Code != stdio.CodeBusy {
		t.Fatalf("sync during write = %+v, want -32001", rerr)
	}
	_, rerr = p.HandleIndex(ctx, json.RawMessage(`{"force":false}`))
	if rerr == nil || rerr.Code != stdio.CodeBusy {
		t.Fatalf("index during write = %+v, want -32001", rerr)
	}
}

// Status never blocks and never goes busy.
func TestHandleStatus(t *testing.T) {
	ctx := context.Background()
	p, _ := testPipeline(t, map[string]string{
		"src/a.ts": "export function foo(): number { return 1; }\n",
	})
	defer p.Close()

	res, rerr := p.HandleStatus(ctx, nil)
	if rerr != nil {
		t.Fatalf("status error: %+v", rerr)
	}
	raw, _ := json.Marshal(res)
	var st Status
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatalf("status is not a Status: %v", err)
	}
	if st.Syncing {
		t.Fatalf("fresh pipeline reports syncing: %+v", st)
	}
}

// Cancelled context surfaces as internal, not busy.
func TestHandleSyncCancelled(t *testing.T) {
	p, _ := testPipeline(t, map[string]string{
		"src/a.ts": "export function foo(): number { return 1; }\n",
	})
	defer p.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, rerr := p.HandleSync(ctx, nil)
	if rerr == nil || rerr.Code != stdio.CodeInternalError {
		t.Fatalf("cancelled = %+v, want -32603", rerr)
	}
}
