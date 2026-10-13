package services

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"srcinsight/internal/model"
	"srcinsight/transport/stdio"
)

// Second FullScan with only mtime drift (same size, same content) must not
// rewrite rows: Added stays 0 and stored entries/fingerprints are stable.
func TestFullScanSkipsUnchangedContent(t *testing.T) {
	ctx := context.Background()
	p, dir := testPipeline(t, map[string]string{
		"src/a.ts": "export function foo(): number { return 1; }\n",
	})
	defer p.Close()

	if _, err := p.FullScan(ctx, true); err != nil {
		t.Fatal(err)
	}
	before, err := p.Vault().FetchEntriesByFile(ctx, "src/a.ts")
	if err != nil || len(before) != 1 {
		t.Fatalf("entries = %+v, %v", before, err)
	}

	// Touch mtime only: same bytes, new timestamp.
	abs := filepath.Join(dir, "src", "a.ts")
	now := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(abs, now, now); err != nil {
		t.Fatal(err)
	}

	res, err := p.FullScan(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.FilesChecked != 1 {
		t.Fatalf("checked = %d, want 1", res.FilesChecked)
	}
	if res.Added != 0 || res.Modified != 0 || res.Removed != 0 {
		t.Fatalf("rescan with identical content wrote rows: %+v", res)
	}
	after, err := p.Vault().FetchEntriesByFile(ctx, "src/a.ts")
	if err != nil || len(after) != 1 || after[0].ID != before[0].ID {
		t.Fatalf("entries drifted: %+v", after)
	}
}

// Second FullScan with real content change re-indexes only that file.
func TestFullScanRewritesOnlyChanged(t *testing.T) {
	ctx := context.Background()
	p, dir := testPipeline(t, map[string]string{
		"src/a.ts": "export function foo(): number { return 1; }\n",
		"src/b.ts": "export function bar(): number { return 2; }\n",
	})
	defer p.Close()

	if _, err := p.FullScan(ctx, true); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "src", "a.ts"),
		[]byte("export function foo(): number { return 1; }\nexport function baz(): number { return 3; }\n"),
		0o644); err != nil {
		t.Fatal(err)
	}
	res, err := p.FullScan(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Added != 1 {
		t.Fatalf("res = %+v, want exactly 1 rewrite", res)
	}
	if entries, _ := p.Vault().FetchEntriesByFile(ctx, "src/a.ts"); len(entries) != 2 {
		t.Fatalf("a.ts entries = %d, want 2", len(entries))
	}
	if entries, _ := p.Vault().FetchEntriesByFile(ctx, "src/b.ts"); len(entries) != 1 {
		t.Fatalf("b.ts entries = %d, want 1", len(entries))
	}
}

func TestEnsureIndexedDecisions(t *testing.T) {
	ctx := context.Background()
	p, dir := testPipeline(t, map[string]string{
		"src/a.ts": "export function foo(): number { return 1; }\n",
	})
	defer p.Close()

	// Empty vault → full index.
	res, full, err := p.EnsureIndexed(ctx)
	if err != nil || !full {
		t.Fatalf("ensure = %+v, %v, %v (want full)", res, full, err)
	}
	if res.Added != 1 {
		t.Fatalf("res = %+v", res)
	}
	// Populated vault, no changes → scan with skip, not a rebuild.
	res, full, err = p.EnsureIndexed(ctx)
	if err != nil || full {
		t.Fatalf("ensure = %+v, %v, %v (want scan)", res, full, err)
	}
	if res.Changed() != 0 {
		t.Fatalf("res = %+v, want no-op", res)
	}
	// Populated vault, one file changed → only that file re-indexed.
	if err := os.WriteFile(filepath.Join(dir, "src", "a.ts"),
		[]byte("export function foo(): number { return 9; }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, full, err = p.EnsureIndexed(ctx)
	if err != nil || full {
		t.Fatalf("ensure = %+v, %v, %v", res, full, err)
	}
	if res.Added != 1 {
		t.Fatalf("res = %+v, want 1 rewrite", res)
	}
}

// decodeStdio splits stdout into lines and requires every line to be a valid
// JSON-RPC response: stdout is protocol-pure.
func decodeStdio(t *testing.T, out []byte) []stdio.Response {
	t.Helper()
	var res []stdio.Response
	for _, ln := range bytes.Split(bytes.TrimSpace(out), []byte("\n")) {
		if len(bytes.TrimSpace(ln)) == 0 {
			continue
		}
		var r stdio.Response
		if err := json.Unmarshal(ln, &r); err != nil {
			t.Fatalf("stdout line is not JSON: %q (%v)", ln, err)
		}
		if r.JSONRPC != "2.0" {
			t.Fatalf("stdout line is not JSON-RPC: %q", ln)
		}
		res = append(res, r)
	}
	return res
}

// byID indexes decoded responses by their echoed request id. Dispatch is
// concurrent, so responses may arrive out of order — tests must match by
// id, never by position.
func byID(t *testing.T, res []stdio.Response) map[string]stdio.Response {
	t.Helper()
	out := make(map[string]stdio.Response, len(res))
	for _, r := range res {
		raw, err := json.Marshal(r.ID)
		if err != nil {
			t.Fatal(err)
		}
		key := string(raw)
		if _, dup := out[key]; dup {
			t.Fatalf("duplicate response id %s in %+v", key, res)
		}
		out[key] = r
	}
	return out
}

func searchHitNames(t *testing.T, r stdio.Response) []string {
	t.Helper()
	if r.Error != nil {
		t.Fatalf("search errored: %+v", r.Error)
	}
	raw, err := json.Marshal(r.Result)
	if err != nil {
		t.Fatal(err)
	}
	var sr model.SearchResult
	if err := json.Unmarshal(raw, &sr); err != nil {
		t.Fatalf("search result is not a SearchResult: %v", err)
	}
	var names []string
	for _, h := range sr.Hits {
		names = append(names, h.Name)
	}
	return names
}

// Serve with watching disabled is fatal: a terminal error frame (id null,
// code -32000) as the last word on stdout, then a non-nil return. No frozen
// index, no silent limp.
func TestServeWatchDisabledIsFatal(t *testing.T) {
	t.Setenv("SRCINSIGHT_NO_WATCH", "1")
	ctx := context.Background()
	p, _ := testPipeline(t, map[string]string{
		"src/a.ts": "export function foo(): number { return 1; }\n",
	})
	defer p.Close()

	var stdout bytes.Buffer
	err := p.Serve(ctx, strings.NewReader(""), &stdout)
	if err == nil || !strings.Contains(err.Error(), "watcher") {
		t.Fatalf("serve err = %v, want a watcher error", err)
	}
	res := decodeStdio(t, stdout.Bytes())
	if len(res) != 1 {
		t.Fatalf("responses = %+v, want exactly the terminal frame", res)
	}
	if string(res[0].ID) != "null" || res[0].Error == nil || res[0].Error.Code != stdio.CodeServerError {
		t.Fatalf("terminal frame = %+v, want id null + code -32000", res[0])
	}
	if res[0].Result != nil {
		t.Fatalf("terminal frame must not carry a result: %+v", res[0])
	}
}

// Unknown methods, malformed frames and blank queries follow the protocol:
// -32601 / -32700 / empty hits — and the loop survives them. Runs with the
// real watcher (watch-disabled is fatal now, so NO_WATCH must stay unset).
func TestServeStdioErrors(t *testing.T) {
	ctx := context.Background()
	p, _ := testPipeline(t, map[string]string{
		"src/a.ts": "export function foo(): number { return 1; }\n",
	})
	defer p.Close()

	stdin := strings.NewReader(
		"{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"query\"}\n" +
			"not json\n" +
			"{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"search\",\"params\":{\"query\":\"\",\"mode\":\"bogus\"}}\n" +
			"{\"jsonrpc\":\"2.0\",\"id\":3,\"method\":\"search\",\"params\":{\"query\":\"   \"}}\n")
	var stdout bytes.Buffer
	if err := p.Serve(ctx, stdin, &stdout); err != nil {
		t.Fatal(err)
	}
	res := decodeStdio(t, stdout.Bytes())
	if len(res) != 4 {
		t.Fatalf("responses = %+v, want 4", res)
	}
	got := byID(t, res)
	if r := got["1"]; r.Error == nil || r.Error.Code != stdio.CodeMethodNotFound {
		t.Fatalf("unknown method = %+v", r)
	}
	if r := got["null"]; r.Error == nil || r.Error.Code != stdio.CodeParseError {
		t.Fatalf("malformed = %+v", r)
	}
	if r := got["2"]; r.Error == nil || r.Error.Code != stdio.CodeInvalidParams {
		t.Fatalf("bogus mode = %+v", r)
	}
	if names := searchHitNames(t, got["3"]); len(names) != 0 {
		t.Fatalf("blank query hits = %v, want empty", names)
	}
}

// Serve with the realtime watcher enabled (real inotify on a temp dir):
// search works while the watcher goroutine runs, and "shutdown" ends the
// loop without needing stdin EOF.
func TestServeStdioWatcherEnabled(t *testing.T) {
	ctx := context.Background()
	p, _ := testPipeline(t, map[string]string{
		"src/a.ts": "export function foo(): number { return 1; }\n",
	})
	defer p.Close()

	stdin := strings.NewReader(
		"{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"search\",\"params\":{\"query\":\"foo\"}}\n" +
			"{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"shutdown\"}\n")
	var stdout bytes.Buffer
	if err := p.Serve(ctx, stdin, &stdout); err != nil {
		t.Fatal(err)
	}
	res := decodeStdio(t, stdout.Bytes())
	if len(res) != 2 {
		t.Fatalf("responses = %+v, want 2", res)
	}
	got := byID(t, res)
	if names := searchHitNames(t, got["1"]); len(names) != 1 || names[0] != "foo" {
		t.Fatalf("search hits = %v, want [foo]", names)
	}
	if r := got["2"]; r.Error != nil {
		t.Fatalf("shutdown = %+v", r)
	}
}
