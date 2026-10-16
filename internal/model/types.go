// Package model defines the public, JSON-serializable data types for srcinsight.
package model

import (
	"strconv"
	"strings"
)

// Call scope constants classify where a target lives relative to the project.
// Values are stable strings read directly by consumers.
const (
	ScopeInternal   = "internal"
	ScopeExternal   = "external"
	ScopeRuntime    = "external:runtime"
	ScopeUnresolved = "unresolved"
)

// InternalScope builds the scope for a callee living inside the project.
func InternalScope(file string) string {
	if file == "" {
		return ScopeInternal
	}
	return ScopeInternal + ":" + file
}

// ExternalScope builds the scope for a callee from an external module.
func ExternalScope(module string) string {
	if module == "" {
		return ScopeRuntime
	}
	return ScopeExternal + ":" + module
}

// IsInternalScope reports whether a scope stays inside the indexed project.
func IsInternalScope(scope string) bool {
	return scope == ScopeInternal || strings.HasPrefix(scope, ScopeInternal+":")
}

// IsExternalScope reports whether a scope points outside the project.
func IsExternalScope(scope string) bool {
	return scope == ScopeExternal || strings.HasPrefix(scope, ScopeExternal+":") || scope == ScopeRuntime
}

// Language identifies the source language of an indexed file.
type Language string

const (
	LangTypeScript Language = "typescript"
	LangPython     Language = "python"
	LangGo         Language = "go"
)

// Kind classifies a function-like definition.
type Kind string

const (
	KindFunction      Kind = "function"
	KindMethod        Kind = "method"
	KindArrow         Kind = "arrow"
	KindConstructor   Kind = "constructor"
	KindGenerator     Kind = "generator"
	KindClassDef      Kind = "class"
	KindInterface     Kind = "interface"
	KindTypeAlias     Kind = "type"
	KindEnum          Kind = "enum"
	KindModule        Kind = "module"
	KindConst         Kind = "const"
	KindUnresolved    Kind = "unresolved"
	KindAsyncFunction Kind = "async-function"
	KindAsyncArrow    Kind = "async-arrow"
	KindAsyncMethod   Kind = "async-method"
)

// ContainerKind classifies a structural container a function lives in.
type ContainerKind string

const (
	ContainerFile      ContainerKind = "file"
	ContainerClass     ContainerKind = "class"
	ContainerModule    ContainerKind = "module"
	ContainerNamespace ContainerKind = "namespace"
	ContainerFunction  ContainerKind = "function"
)

// Container holds a function-like definition; ID stays stable for grouping.
type Container struct {
	Kind ContainerKind `json:"kind"`
	ID   EntryID       `json:"id"`
	Name string        `json:"name"`
}

// Range is a 1-indexed, inclusive line range in a file.
type Range struct {
	StartLine int `json:"start"`
	EndLine   int `json:"end"`
}

// EntryID is a stable five-part identifier; file containers use "file:<relPath>".
type EntryID string

// Language returns the language part, or empty for an invalid ID.
func (id EntryID) Language() Language {
	lang, _, _, _, _, ok := id.parts()
	if !ok {
		return ""
	}
	return Language(lang)
}

// FilePath returns the relative source path, or empty for an invalid ID.
func (id EntryID) FilePath() string {
	_, path, _, _, _, ok := id.parts()
	if !ok {
		return ""
	}
	return path
}

// Kind returns the node-kind part, or empty for an invalid ID.
func (id EntryID) Kind() Kind {
	_, _, kind, _, _, ok := id.parts()
	if !ok {
		return ""
	}
	return Kind(kind)
}

// QualifiedName returns the qualified name part, or empty for an invalid ID.
func (id EntryID) QualifiedName() string {
	_, _, _, name, _, ok := id.parts()
	if !ok {
		return ""
	}
	return name
}

// Ordinal returns the 1-based source-order counter, or zero for an invalid ID.
func (id EntryID) Ordinal() int {
	_, _, _, _, ordinal, ok := id.parts()
	if !ok {
		return 0
	}
	return ordinal
}

// parts parses the fixed five-part EntryID without allocating; fields must not contain ':'.
func (id EntryID) parts() (lang, path, kind, name string, ordinal int, ok bool) {
	lang, rest, ok := strings.Cut(string(id), ":")
	if !ok || lang == "" {
		return "", "", "", "", 0, false
	}
	path, rest, ok = strings.Cut(rest, ":")
	if !ok || path == "" {
		return "", "", "", "", 0, false
	}
	kind, rest, ok = strings.Cut(rest, ":")
	if !ok || kind == "" {
		return "", "", "", "", 0, false
	}
	name, rawOrdinal, ok := strings.Cut(rest, ":")
	if !ok || name == "" {
		return "", "", "", "", 0, false
	}
	ordinal, err := strconv.Atoi(rawOrdinal)
	if err != nil || ordinal <= 0 {
		return "", "", "", "", 0, false
	}
	return lang, path, kind, name, ordinal, true
}

// FunctionRef is a lightweight reference to a function (no signature/body).
type FunctionRef struct {
	ID        EntryID `json:"id"`
	Name      string  `json:"name"`
	FilePath  string  `json:"filePath"`
	LineRange Range   `json:"lineRange"`
	Scope     string  `json:"scope,omitempty"` // internal / external / unresolved
	SiteRange Range   `json:"siteRange,omitempty"`
	// Cond guards this call site; empty means unconditional or unknown.
	// It round-trips through the vault so trace can render when.
	Cond string `json:"cond,omitempty"`
}

// Function is a full definition node; type nodes leave Calls empty and use heritage refs.
// RecvHints holds param receiver bindings for resolve only and never reaches the vault.
type Function struct {
	FunctionRef
	Kind        Kind          `json:"kind"`
	File        Container     `json:"file"`                // enclosing file container
	Container   *Container    `json:"container,omitempty"` // nearest enclosing class or module
	Signature   string        `json:"signature"`           // one line, no body
	Calls       []FunctionRef `json:"calls"`
	CalledBy    []FunctionRef `json:"calledBy"`
	Extends     []FunctionRef `json:"extends,omitempty"`     // heritage targets
	Implements  []FunctionRef `json:"implements,omitempty"`  // implemented interfaces
	ParamTypes  []FunctionRef `json:"paramTypes,omitempty"`  // unwrapped param base types
	ReturnTypes []FunctionRef `json:"returnTypes,omitempty"` // unwrapped return base types
	Uses        []FunctionRef `json:"uses,omitempty"`        // const value dependencies
	Doc         string        `json:"doc,omitempty"`
	TestTitle   string        `json:"testTitle,omitempty"` // test title for anonymous test callbacks
	Tags        []string      `json:"tags,omitempty"`      // framework marks; entry level only
	RecvHints   []RecvBinding `json:"recvHints,omitempty"` // resolve-time receiver bindings
}

// ContainerName returns the nearest enclosing class/module name, if any.
func (f Function) ContainerName() string {
	if f.Container == nil {
		return ""
	}
	return f.Container.Name
}

// QualifiedName returns the fully container-qualified name, if any.
func (f Function) QualifiedName() string {
	if name := f.ContainerName(); name != "" {
		return name + "." + f.Name
	}
	return f.Name
}

// Edge kind constants for Edge.Kind.
const (
	EdgeCalls      = "calls"
	EdgeExtends    = "extends"
	EdgeImplements = "implements"
	EdgeParam      = "param"
	EdgeReturn     = "return"
	EdgeUses       = "uses"
	EdgeTransport  = "transport"
)

// Edge links two functions; zero SiteRange means unknown.
// Cond guards the call site; empty means unconditional or unknown.
type Edge struct {
	From      EntryID `json:"from"`
	To        EntryID `json:"to"`
	Kind      string  `json:"kind"` // "calls", "extends", "implements", "param", "return", "uses"
	SiteRange Range   `json:"siteRange,omitempty"`
	Cond      string  `json:"cond,omitempty"`
}

// ImportStmt is the canonical shared import shape; siblings must reuse it.
type ImportStmt struct {
	LocalName string `json:"localName"` // "auth" | "validateToken" | "*"
	Source    string `json:"source"`    // "express" | "./validator" | "..models"
	Kind      string `json:"kind"`      // "default" | "named" | "namespace" | "side-effect"
	Alias     string `json:"alias"`     // "as v" -> "v"
}

// ImportKindGoPackage carries the Go package clause; only the same-package pass reads it.
const ImportKindGoPackage = "gopackage"

// RecvBinding maps one receiver name to a type; ViaCall names the factory or stays empty.
type RecvBinding struct {
	Recv    string `json:"recv"`
	Type    string `json:"type"`
	ViaCall string `json:"viaCall,omitempty"`
}

// TransportEndpoint is one server route hint from extract; Owner fills in after indexing.
type TransportEndpoint struct {
	Transport string `json:"transport"`
	Method    string `json:"method"`
	Path      string `json:"path"`
	Operation string `json:"operation"`
	Framework string `json:"framework"`
	Func      string `json:"func"`
	Line      int    `json:"line"`
}

// TransportCall is one client fetch hint from extract; Owner fills in after indexing.
type TransportCall struct {
	Transport string `json:"transport"`
	Method    string `json:"method"`
	URL       string `json:"url"`
	Operation string `json:"operation"`
	Lib       string `json:"lib"`
	Func      string `json:"func"`
	Line      int    `json:"line"`
}

// FileExtract is the canonical per-file result shared by extractors, indexer, and resolver.
type FileExtract struct {
	Path         string              `json:"path"`
	Language     Language            `json:"language"`
	Functions    []Function          `json:"functions"`
	Imports      []ImportStmt        `json:"imports"`
	RecvBindings []RecvBinding       `json:"recvBindings,omitempty"`
	Endpoints    []TransportEndpoint `json:"endpoints,omitempty"`
	Calls        []TransportCall     `json:"calls,omitempty"`
}

// Graph is a set of nodes and directed edges.
type Graph struct {
	Nodes []Function `json:"nodes"`
	Edges []Edge     `json:"edges"`
}

// Stats summarises indexing and query output sizes.
type Stats struct {
	FilesScanned     int `json:"filesScanned"`
	FunctionsIndexed int `json:"functionsIndexed"`
	NodesReturned    int `json:"nodesReturned"`
	EdgesReturned    int `json:"edgesReturned"`
	InternalCalls    int `json:"internalCalls,omitempty"`
	ExternalCalls    int `json:"externalCalls,omitempty"`
	UnresolvedCalls  int `json:"unresolvedCalls,omitempty"`
}

// QueryResult is the full response for a single query.
type QueryResult struct {
	Query   string     `json:"query"`
	Matches []Function `json:"matches"`
	Graph   Graph      `json:"graph"`
	Stats   Stats      `json:"stats"`
}

// SearchHit is a ranked result; Score is raw FTS5 bm25 (negative, smaller wins).
// Mode names the winning primitive and Rank is its position before union/filter.
type SearchHit struct {
	ID        EntryID    `json:"id"`
	Name      string     `json:"name"`
	Qualified string     `json:"qualifiedName"`
	Kind      Kind       `json:"kind"`
	FilePath  string     `json:"filePath"`
	LineRange Range      `json:"lineRange"`
	Dialect   Language   `json:"dialect"`
	Signature string     `json:"signature"`
	Doc       string     `json:"doc"`
	Tags      []string   `json:"tags,omitempty"`
	Vessel    *Container `json:"vessel,omitempty"`
	Score     float64    `json:"score"`
	Mode      string     `json:"mode"`
	Rank      int        `json:"rank"`
}

// SearchResult is the flat-list response of a search (no graph expansion).
type SearchResult struct {
	Query string      `json:"query"`
	Hits  []SearchHit `json:"hits"`
	Stats Stats       `json:"stats"`
}
