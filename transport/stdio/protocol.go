package stdio

import (
	"context"
	"encoding/json"
)

// Version is the JSON-RPC version; framing is NDJSON with one object per line.
const Version = "2.0"

// CodeParseError and siblings are JSON-RPC error codes; CodeServerError ends the session.
// CodeBusy reports a busy server without ending it.
const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603
	CodeServerError    = -32000
	CodeBusy           = -32001
)

// MethodPing and MethodShutdown are served by the transport; Register ignores them.
const (
	MethodPing     = "ping"
	MethodShutdown = "shutdown"
)

// MethodSync, MethodIndex, and MethodStatus trigger sync, full index, and status.
const (
	MethodSync   = "sync"
	MethodIndex  = "index"
	MethodStatus = "status"
)

// SyncParams is the body for sync; it takes no options.
// Sending force is rejected so callers use index for full rescans.
type SyncParams struct{}

// IndexParams is the request body for `index` (whole-vault FullScan).
type IndexParams struct {
	Force bool `json:"force"`
}

// Request is one JSON-RPC request; raw ID is echoed exactly and nil means notification.
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

func (r Request) HasID() bool { return r.ID != nil }

type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func (e *RPCError) Error() string { return e.Message }

// Response is one JSON-RPC response; ID is null when the request ID was unknown.
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

func NewResult(id json.RawMessage, result any) Response {
	return Response{JSONRPC: Version, ID: normalizeID(id), Result: result}
}

func NewError(id json.RawMessage, code int, msg string) Response {
	return Response{JSONRPC: Version, ID: normalizeID(id), Error: &RPCError{Code: code, Message: msg}}
}

func MethodNotFound(id json.RawMessage, method string) Response {
	r := NewError(id, CodeMethodNotFound, "method not found: "+method)
	r.Error.Data = "supported in v1: search, graph, trace, sync, index, status, ping, shutdown"
	return r
}

func InvalidParams(id json.RawMessage, msg string) Response {
	return NewError(id, CodeInvalidParams, msg)
}

func Internal(id json.RawMessage, msg string) Response {
	return NewError(id, CodeInternalError, msg)
}

// ServerError is the terminal frame: id null, session over.
func ServerError(msg string) Response {
	return Response{JSONRPC: Version, ID: json.RawMessage("null"), Error: &RPCError{Code: CodeServerError, Message: msg}}
}

func normalizeID(id json.RawMessage) json.RawMessage {
	if len(id) == 0 {
		return json.RawMessage("null")
	}
	return id
}

// Handler serves one method; params may be empty. Nil *RPCError = success.
type Handler func(ctx context.Context, params json.RawMessage) (any, *RPCError)
