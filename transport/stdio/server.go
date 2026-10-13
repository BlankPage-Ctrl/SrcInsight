// Package stdio serves JSON-RPC 2.0 over NDJSON: one request per stdin line, one response per stdout line.
//
// Requests run concurrently and responses may arrive out of order, so clients must match by id.
package stdio

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"
)

// Bounds one NDJSON frame; the scanner default (64KiB) is too small.
const maxLineBytes = 4 << 20

// Loop drain wait for in-flight handlers after shutdown or EOF.
const drainTimeout = 30 * time.Second

// Server holds handlers registered before Loop; not safe for concurrent use.
type Server struct {
	handlers map[string]Handler
	mu       sync.Mutex
	enc      *json.Encoder
}

func New() *Server {
	return &Server{handlers: make(map[string]Handler)}
}

// Register adds a method handler; ping and shutdown are reserved and return false.
func (s *Server) Register(method string, h Handler) bool {
	if method == "" || h == nil || method == MethodPing || method == MethodShutdown {
		return false
	}
	s.handlers[method] = h
	return true
}

func (s *Server) encodeLocked(v any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.enc == nil {
		return fmt.Errorf("stdio: not serving")
	}
	return s.enc.Encode(v)
}

// line is one scanner result; channel close means EOF.
type line struct {
	body []byte
	err  error
}

// Loop serves until ctx is cancelled, stdin ends, or shutdown.
// Each request runs in its own goroutine; shutdown drains in-flight handlers bounded by drainTimeout.
func (s *Server) Loop(ctx context.Context, stdin io.Reader, stdout io.Writer) error {
	ch := make(chan line, 16)
	go func() {
		defer close(ch)
		sc := bufio.NewScanner(stdin)
		sc.Buffer(make([]byte, 64*1024), maxLineBytes)
		for sc.Scan() {
			body := bytes.Clone(sc.Bytes()) // Scanner reuses its buffer.
			if len(bytes.TrimSpace(body)) == 0 {
				continue
			}
			select {
			case ch <- line{body: body}:
			case <-ctx.Done():
				return
			}
		}
		if err := sc.Err(); err != nil {
			select {
			case ch <- line{err: err}:
			case <-ctx.Done():
			}
		}
	}()

	s.mu.Lock()
	s.enc = json.NewEncoder(stdout)
	s.mu.Unlock()

	loopCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup
	var errMu sync.Mutex
	var firstErr error
	fail := func(err error) {
		if err == nil {
			return
		}
		errMu.Lock()
		if firstErr == nil {
			firstErr = err
		}
		errMu.Unlock()
		cancel()
	}

loop:
	for {
		select {
		case <-loopCtx.Done():
			break loop
		case l, ok := <-ch:
			if !ok {
				break loop // EOF: graceful.
			}
			if l.err != nil {
				fail(l.err)
				break loop
			}
			if done := s.dispatch(loopCtx, &wg, fail, l.body); done {
				break loop // shutdown accepted: stop after in-flight handlers drain.
			}
		}
	}

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(drainTimeout):
	}

	errMu.Lock()
	defer errMu.Unlock()
	if firstErr == nil {
		return nil // EOF, shutdown, or external cancel: graceful.
	}
	return firstErr
}

// dispatch handles one line; fast paths answer inline while handlers run in their own goroutine.
// done is true when shutdown was accepted and the loop should stop after draining.
func (s *Server) dispatch(ctx context.Context, wg *sync.WaitGroup, fail func(error), body []byte) (done bool) {
	var req Request
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		fail(s.encodeLocked(NewError(nil, CodeParseError, fmt.Sprintf("parse error: %v", err))))
		return false
	}
	if req.JSONRPC != Version || req.Method == "" {
		fail(s.encodeLocked(NewError(req.ID, CodeInvalidRequest, "invalid request: want {\"jsonrpc\":\"2.0\",\"id\":..,\"method\":\"..\"}")))
		return false
	}
	if !req.HasID() {
		return false // notification: no response, no side effects
	}
	if req.Method == MethodShutdown {
		fail(s.encodeLocked(NewResult(req.ID, nil)))
		return true
	}
	if req.Method == MethodPing {
		fail(s.encodeLocked(NewResult(req.ID, map[string]bool{"ok": true})))
		return false
	}
	h, ok := s.handlers[req.Method]
	if !ok {
		fail(s.encodeLocked(MethodNotFound(req.ID, req.Method)))
		return false
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		result, rerr := h(ctx, req.Params)
		if rerr != nil {
			fail(s.encodeLocked(Response{JSONRPC: Version, ID: normalizeID(req.ID), Error: rerr}))
			return
		}
		fail(s.encodeLocked(NewResult(req.ID, result)))
	}()
	return false
}
