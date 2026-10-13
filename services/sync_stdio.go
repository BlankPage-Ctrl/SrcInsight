package services

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"srcinsight/internal/watch"
	"srcinsight/transport/stdio"
)

// Status answers the status method; advisory only since writes can start after.
type Status struct {
	Syncing      bool `json:"syncing"`
	Pending      int  `json:"pending"`
	RequiresFull bool `json:"requiresFull"`
}

// Status snapshots the pipeline without blocking.
func (p *Pipeline) Status() Status {
	busy := false
	if !p.mu.TryLock() {
		busy = true
	} else {
		p.mu.Unlock()
	}
	st := Status{Syncing: busy}
	p.watchMu.Lock()
	w := p.watcher
	p.watchMu.Unlock()
	if w != nil {
		if w.Syncing() {
			st.Syncing = true
		}
		st.Pending = w.PendingCount()
		st.RequiresFull = w.RequiresFull()
	}
	return st
}

func (p *Pipeline) setWatcher(w *watch.FileWatcher) {
	p.watchMu.Lock()
	defer p.watchMu.Unlock()
	p.watcher = w
}

// HandleSync serves sync without waiting; a held mutex answers busy.
func (p *Pipeline) HandleSync(ctx context.Context, raw json.RawMessage) (any, *stdio.RPCError) {
	if rerr := checkEmptyParams(raw, "sync"); rerr != nil {
		return nil, rerr
	}
	res, ok, err := p.TrySync(ctx)
	if err != nil {
		return nil, syncInternal("sync", err)
	}
	if !ok {
		return nil, &stdio.RPCError{Code: stdio.CodeBusy, Message: "busy: sync in progress"}
	}
	return res, nil
}

// HandleIndex serves index without waiting; a held mutex answers busy.
func (p *Pipeline) HandleIndex(ctx context.Context, raw json.RawMessage) (any, *stdio.RPCError) {
	var params stdio.IndexParams
	if len(bytes.TrimSpace(raw)) != 0 {
		if err := json.Unmarshal(raw, &params); err != nil {
			return nil, &stdio.RPCError{Code: stdio.CodeInvalidParams, Message: fmt.Sprintf("index: invalid params: %v", err)}
		}
	}
	res, ok, err := p.TryFullScan(ctx, params.Force)
	if err != nil {
		return nil, syncInternal("index", err)
	}
	if !ok {
		return nil, &stdio.RPCError{Code: stdio.CodeBusy, Message: "busy: index in progress"}
	}
	return res, nil
}

// HandleStatus serves an advisory snapshot that never blocks.
func (p *Pipeline) HandleStatus(ctx context.Context, _ json.RawMessage) (any, *stdio.RPCError) {
	if err := ctx.Err(); err != nil {
		return nil, &stdio.RPCError{Code: stdio.CodeInternalError, Message: "status: cancelled"}
	}
	return p.Status(), nil
}

// checkEmptyParams accepts empty bodies and points force users to index.
func checkEmptyParams(raw json.RawMessage, method string) *stdio.RPCError {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" || string(trimmed) == "{}" {
		return nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return &stdio.RPCError{Code: stdio.CodeInvalidParams, Message: fmt.Sprintf("%s: invalid params: %v", method, err)}
	}
	if _, ok := fields["force"]; ok {
		return &stdio.RPCError{Code: stdio.CodeInvalidParams, Message: fmt.Sprintf("%s: takes no params; use index for force", method)}
	}
	return &stdio.RPCError{Code: stdio.CodeInvalidParams, Message: fmt.Sprintf("%s: invalid params: unknown fields", method)}
}

func syncInternal(method string, err error) *stdio.RPCError {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return &stdio.RPCError{Code: stdio.CodeInternalError, Message: method + ": cancelled"}
	}
	return &stdio.RPCError{Code: stdio.CodeInternalError, Message: fmt.Sprintf("%s: %v", method, err)}
}
