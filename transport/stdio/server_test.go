package stdio

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"
)

// runSession feeds input lines through a loop and returns the decoded
// responses in order.
func runSession(t *testing.T, s *Server, input string) []Response {
	t.Helper()
	var out bytes.Buffer
	if err := s.Loop(context.Background(), strings.NewReader(input), &out); err != nil {
		t.Fatalf("loop = %v", err)
	}
	var res []Response
	for _, ln := range bytes.Split(bytes.TrimSpace(out.Bytes()), []byte("\n")) {
		if len(bytes.TrimSpace(ln)) == 0 {
			continue
		}
		var r Response
		if err := json.Unmarshal(ln, &r); err != nil {
			t.Fatalf("stdout line is not JSON: %q (%v)", ln, err)
		}
		res = append(res, r)
	}
	return res
}

func TestPingAndShutdown(t *testing.T) {
	s := New()
	res := runSession(t, s, "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"ping\"}\n{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"shutdown\"}\n")
	if len(res) != 2 {
		t.Fatalf("responses = %+v, want 2", res)
	}
	// Dispatch is concurrent: match by id, never by position.
	got := make(map[string]Response, len(res))
	for _, r := range res {
		got[string(r.ID)] = r
	}
	ping := got["1"]
	if ping.Error != nil || string(ping.ID) != "1" {
		t.Fatalf("ping = %+v", ping)
	}
	var ok struct {
		OK bool `json:"ok"`
	}
	raw, _ := json.Marshal(ping.Result)
	if err := json.Unmarshal(raw, &ok); err != nil || !ok.OK {
		t.Fatalf("ping result = %v, %v", ping.Result, err)
	}
	if shutdown := got["2"]; shutdown.Error != nil || string(shutdown.ID) != "2" {
		t.Fatalf("shutdown = %+v", shutdown)
	}
}

func TestDispatchToHandler(t *testing.T) {
	s := New()
	if !s.Register("echo", func(_ context.Context, params json.RawMessage) (any, *RPCError) {
		return map[string]string{"got": string(params)}, nil
	}) {
		t.Fatal("register echo failed")
	}
	res := runSession(t, s, "{\"jsonrpc\":\"2.0\",\"id\":\"a\",\"method\":\"echo\",\"params\":{\"x\":1}}\n")
	if len(res) != 1 || res[0].Error != nil || string(res[0].ID) != `"a"` {
		t.Fatalf("echo = %+v", res)
	}
}

func TestUnknownMethodAndBadFrames(t *testing.T) {
	s := New()
	res := runSession(t, s,
		"{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"query\"}\n"+
			"not json\n"+
			"{\"jsonrpc\":\"2.0\",\"id\":2}\n"+ // missing method
			"{\"jsonrpc\":\"1.0\",\"id\":3,\"method\":\"ping\"}\n") // wrong version
	if len(res) != 4 {
		t.Fatalf("responses = %+v, want 4", res)
	}
	if res[0].Error == nil || res[0].Error.Code != CodeMethodNotFound {
		t.Fatalf("unknown method = %+v", res[0])
	}
	if res[1].Error == nil || res[1].Error.Code != CodeParseError || string(res[1].ID) != "null" {
		t.Fatalf("parse error = %+v", res[1])
	}
	if res[2].Error == nil || res[2].Error.Code != CodeInvalidRequest {
		t.Fatalf("missing method = %+v", res[2])
	}
	if res[3].Error == nil || res[3].Error.Code != CodeInvalidRequest {
		t.Fatalf("wrong version = %+v", res[3])
	}
}

func TestNotificationsAndBlanksIgnored(t *testing.T) {
	called := false
	s := New()
	s.Register("work", func(_ context.Context, _ json.RawMessage) (any, *RPCError) {
		called = true
		return true, nil
	})
	res := runSession(t, s,
		"\n"+
			"   \n"+
			"{\"jsonrpc\":\"2.0\",\"method\":\"work\"}\n"+ // notification: no id
			"{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"ping\"}\n")
	if called {
		t.Fatal("notification must not trigger the handler")
	}
	if len(res) != 1 || string(res[0].ID) != "1" {
		t.Fatalf("responses = %+v, want only the ping", res)
	}
}

func TestHandlerErrorSurfaces(t *testing.T) {
	s := New()
	s.Register("boom", func(_ context.Context, _ json.RawMessage) (any, *RPCError) {
		return nil, &RPCError{Code: CodeInvalidParams, Message: "bad query"}
	})
	res := runSession(t, s, "{\"jsonrpc\":\"2.0\",\"id\":7,\"method\":\"boom\"}\n")
	if len(res) != 1 || res[0].Error == nil || res[0].Error.Code != CodeInvalidParams {
		t.Fatalf("boom = %+v", res)
	}
	if res[0].Result != nil {
		t.Fatalf("error response must not carry a result: %+v", res[0])
	}
}

func TestReservedNamesKeptBuiltin(t *testing.T) {
	s := New()
	if s.Register("ping", func(_ context.Context, _ json.RawMessage) (any, *RPCError) {
		return "hijacked", nil
	}) {
		t.Fatal("registering a reserved name must fail")
	}
	res := runSession(t, s, "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"ping\"}\n")
	raw, _ := json.Marshal(res[0].Result)
	if string(raw) != `{"ok":true}` {
		t.Fatalf("ping = %s, want builtin", raw)
	}
}

func TestLoopReturnsReaderError(t *testing.T) {
	s := New()
	want := errors.New("stdin exploded")
	r := &errReader{err: want}
	var out bytes.Buffer
	if err := s.Loop(context.Background(), r, &out); !errors.Is(err, want) {
		t.Fatalf("loop = %v, want %v", err, want)
	}
}

type errReader struct{ err error }

func (r *errReader) Read([]byte) (int, error) { return 0, r.err }

func TestLoopStopsOnCancel(t *testing.T) {
	s := New()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out bytes.Buffer
	// Blocked stdin, cancelled context: must return promptly, not hang.
	if err := s.Loop(ctx, &blockingReader{}, &out); err != nil {
		t.Fatalf("loop = %v, want nil on cancel", err)
	}
}

type blockingReader struct{}

func (blockingReader) Read([]byte) (int, error) {
	select {} // block forever
}

// Concurrent dispatch: N slow handlers must all be answered (matched by
// id), even though they overlap in time.
func TestConcurrentDispatchAnswersAll(t *testing.T) {
	s := New()
	s.Register("slow", func(_ context.Context, _ json.RawMessage) (any, *RPCError) {
		return "done", nil
	})
	var in strings.Builder
	for i := 1; i <= 20; i++ {
		fmt.Fprintf(&in, "{\"jsonrpc\":\"2.0\",\"id\":%d,\"method\":\"slow\"}\n", i)
	}
	res := runSession(t, s, in.String())
	if len(res) != 20 {
		t.Fatalf("responses = %d, want 20", len(res))
	}
	seen := make(map[string]bool, len(res))
	for _, r := range res {
		if r.Error != nil {
			t.Fatalf("response errored: %+v", r)
		}
		seen[string(r.ID)] = true
	}
	for i := 1; i <= 20; i++ {
		if !seen[strconv.Itoa(i)] {
			t.Fatalf("missing response for id %d in %+v", i, res)
		}
	}
}

// Shutdown answers itself immediately but still delivers an in-flight
// handler response before the loop returns.
func TestShutdownWaitsInflight(t *testing.T) {
	s := New()
	s.Register("slow", func(_ context.Context, _ json.RawMessage) (any, *RPCError) {
		time.Sleep(200 * time.Millisecond)
		return "late", nil
	})
	res := runSession(t, s,
		"{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"slow\"}\n"+
			"{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"shutdown\"}\n")
	if len(res) != 2 {
		t.Fatalf("responses = %+v, want 2", res)
	}
	got := make(map[string]Response, len(res))
	for _, r := range res {
		got[string(r.ID)] = r
	}
	if r := got["1"]; r.Error != nil {
		t.Fatalf("slow = %+v", r)
	}
	if r := got["2"]; r.Error != nil {
		t.Fatalf("shutdown = %+v", r)
	}
}
