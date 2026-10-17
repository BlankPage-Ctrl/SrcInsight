package cold

import (
	"path/filepath"
	"sort"
	"strings"

	"srcinsight/internal/model"
)

// OriginRecord is one tracked source file: the durable twin of the
// filesystem's identity (size+mtime pre-filter, hash confirmation).
type OriginRecord struct {
	Path        string
	Fingerprint string // hex content hash; "" means "hash not confirmed yet"
	Dialect     model.Language
	Bytes       int64
	AlteredAt   int64 // unix millis, floored like the stat pre-filter
	CatalogedAt int64 // unix millis of last successful cataloging
	EntryCount  int
	Faults      string // JSON array of per-file errors, "" when none
}

// EntryRow is one indexed definition, flattened for SQL. Vessel fields carry
// the enclosing container; relations live in links and loose_ends.
type EntryRow struct {
	ID         model.EntryID
	Kind       model.Kind
	Name       string
	Qualified  string
	FilePath   string
	Dialect    model.Language
	StartLine  int
	EndLine    int
	Signature  string
	Doc        string
	TestTitle  string
	Tags       []string
	VesselKind model.ContainerKind
	VesselName string
	VesselID   model.EntryID
}

// EncodeTags renders tags as a space-separated column value (values never
// contain spaces, so Fields round-trips).
func EncodeTags(tags []string) string {
	return strings.Join(tags, " ")
}

// DecodeTags parses a tags column value back to its list form.
func DecodeTags(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return strings.Fields(s)
}

// Valid reports whether the row carries the fields SQLite binds require;
// invalid rows stay skipped so links never dangle.
func (r EntryRow) Valid() bool {
	return r.ID != "" && r.Kind != "" && r.Name != "" && r.FilePath != "" && r.Dialect != ""
}

// EntryRowOf converts a model function to its row form, keeping the file
// vessel present and the class vessel ID intact.
func EntryRowOf(f *model.Function, dialect model.Language) EntryRow {
	r := EntryRow{
		ID:        f.ID,
		Kind:      f.Kind,
		Name:      f.Name,
		Qualified: f.QualifiedName(),
		FilePath:  f.FilePath,
		Dialect:   dialect,
		StartLine: f.LineRange.StartLine,
		EndLine:   f.LineRange.EndLine,
		Signature: f.Signature,
		Doc:       f.Doc,
		TestTitle: f.TestTitle,
		Tags:      append([]string(nil), f.Tags...),
	}
	if f.Container != nil {
		r.VesselKind = f.Container.Kind
		r.VesselName = f.Container.Name
		r.VesselID = f.Container.ID
	}
	return r
}

// Model renders a row back to a model function. Relations stay empty for
// the caller to fill via filtered reads.
func (r EntryRow) Model() model.Function {
	f := model.Function{
		FunctionRef: model.FunctionRef{
			ID:       r.ID,
			Name:     r.Name,
			FilePath: r.FilePath,
			LineRange: model.Range{
				StartLine: r.StartLine,
				EndLine:   r.EndLine,
			},
		},
		Kind:      r.Kind,
		Signature: r.Signature,
		Doc:       r.Doc,
		TestTitle: r.TestTitle,
		Tags:      append([]string(nil), r.Tags...),
		File: model.Container{
			Kind: model.ContainerFile,
			Name: filepath.Base(r.FilePath),
			ID:   model.EntryID("file:" + r.FilePath),
		},
		Calls:    []model.FunctionRef{},
		CalledBy: []model.FunctionRef{},
	}
	if r.VesselName != "" {
		f.Container = &model.Container{
			Kind: r.VesselKind,
			ID:   r.VesselID,
			Name: r.VesselName,
		}
	}
	return f
}

func refOf(f model.Function) model.FunctionRef {
	return model.FunctionRef{
		ID:        f.ID,
		Name:      f.Name,
		FilePath:  f.FilePath,
		LineRange: f.LineRange,
		Scope:     model.InternalScope(f.FilePath),
	}
}

// sortedRefs orders references by ID for deterministic output.
func sortedRefs(refs []model.FunctionRef) []model.FunctionRef {
	if refs == nil {
		return []model.FunctionRef{}
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].ID < refs[j].ID })
	return refs
}

// LinkRow is one resolved directed relationship. SiteStart and SiteEnd hold
// the call-site line range (0,0 means unknown); SiteStartCol and SiteEndCol
// hold the 1-indexed identifier column range inside those lines (end
// exclusive). Cond holds the branch condition.
type LinkRow struct {
	Origin       model.EntryID
	Target       model.EntryID
	Kind         string // model.EdgeCalls / EdgeExtends / EdgeImplements
	SiteStart    int
	SiteEnd      int
	SiteStartCol int
	SiteEndCol   int
	Cond         string
}

// Valid reports whether both endpoints are present; dangling links stay
// dropped instead of violating foreign keys.
func (r LinkRow) Valid() bool { return r.Origin != "" && r.Target != "" && r.Kind != "" }

// ImportRow is one import statement bound to its file.
type ImportRow struct {
	FilePath  string
	LocalName string
	Source    string
	Kind      string
	Alias     string
}

func ImportRowOf(filePath string, imp model.ImportStmt) ImportRow {
	return ImportRow{
		FilePath:  filePath,
		LocalName: imp.LocalName,
		Source:    imp.Source,
		Kind:      imp.Kind,
		Alias:     imp.Alias,
	}
}

func (r ImportRow) Model() model.ImportStmt {
	return model.ImportStmt{
		LocalName: r.LocalName,
		Source:    r.Source,
		Kind:      r.Kind,
		Alias:     r.Alias,
	}
}

// RecvBindRow binds one receiver name in a file to a type. Owner scopes it
// (empty means file level); ViaCall names a deferred factory call.
type RecvBindRow struct {
	FilePath string
	Owner    string
	Recv     string
	Type     string
	ViaCall  string
}

// Valid reports whether the row carries a usable receiver and something to
// resolve it with (a type name or a factory call).
func (r RecvBindRow) Valid() bool {
	return r.FilePath != "" && r.Recv != "" && (r.Type != "" || r.ViaCall != "")
}

func RecvBindRowOf(filePath, owner string, b model.RecvBinding) RecvBindRow {
	return RecvBindRow{
		FilePath: filePath,
		Owner:    owner,
		Recv:     b.Recv,
		Type:     b.Type,
		ViaCall:  b.ViaCall,
	}
}

func (r RecvBindRow) Model() model.RecvBinding {
	return model.RecvBinding{
		Recv:    r.Recv,
		Type:    r.Type,
		ViaCall: r.ViaCall,
	}
}

// LooseEndRow is one unresolved reference. Scope keeps the classification
// so stats and display survive the round trip; SiteStartCol/SiteEndCol keep
// the identifier column range and Cond survives promotion.
type LooseEndRow struct {
	From         model.EntryID
	Name         string
	Kind         string // calls / extends / implements
	Scope        string
	FilePath     string
	Dialect      model.Language
	SiteStart    int
	SiteEnd      int
	SiteStartCol int
	SiteEndCol   int
	Cond         string
}

// TransportEndpointRow is one server route hint. Path is already normalized
// (:id/{id}/[id]/* folded); Method is uppercased. Owner is the handler entry,
// empty for file-scope routes (express router.get at module scope).
type TransportEndpointRow struct {
	FilePath  string
	Owner     model.EntryID
	Transport string
	Method    string
	Path      string
	Operation string
	Framework string
	SiteStart int
}

// Valid reports whether the row carries a matchable key: a normalized path
// for HTTP, or an operation name for GraphQL/WS.
func (r TransportEndpointRow) Valid() bool {
	if r.FilePath == "" {
		return false
	}
	if r.Operation != "" {
		return true
	}
	return r.Path != ""
}

// TransportEndpointRowOf converts a model hint plus its resolved owner.
func TransportEndpointRowOf(filePath string, owner model.EntryID, e model.TransportEndpoint, siteStart int) TransportEndpointRow {
	return TransportEndpointRow{
		FilePath:  filePath,
		Owner:     owner,
		Transport: e.Transport,
		Method:    e.Method,
		Path:      e.Path,
		Operation: e.Operation,
		Framework: e.Framework,
		SiteStart: siteStart,
	}
}

// TransportCallRow is one client fetch hint. URL is the normalized path.
type TransportCallRow struct {
	FilePath  string
	Owner     model.EntryID
	Transport string
	Method    string
	URL       string
	Operation string
	Lib       string
	SiteStart int
}

// Valid reports whether the row carries a matchable key.
func (r TransportCallRow) Valid() bool {
	if r.FilePath == "" || r.Owner == "" {
		return false
	}
	if r.Operation != "" {
		return true
	}
	return r.URL != "" && !isEndpointMarker(r.URL)
}

// isEndpointMarker reports the internal express-route marker, which rides the
// Calls slice only to reach file-level collection and never the vault.
func isEndpointMarker(url string) bool {
	return len(url) > 13 && url[:14] == "__endpoint__:"
}

// TransportCallRowOf converts a model hint plus its resolved owner.
func TransportCallRowOf(filePath string, owner model.EntryID, c model.TransportCall, siteStart int) TransportCallRow {
	return TransportCallRow{
		FilePath:  filePath,
		Owner:     owner,
		Transport: c.Transport,
		Method:    c.Method,
		URL:       c.URL,
		Operation: c.Operation,
		Lib:       c.Lib,
		SiteStart: siteStart,
	}
}

// ScoredEntryRow is one search hit with its ranking signal. Score is raw
// FTS5 bm25 (negative, smaller means more relevant); other modes score 0.
type ScoredEntryRow struct {
	EntryRow
	Score float64
	Mode  string
	Rank  int
}

// OriginBundle is everything one file contributes, written atomically by
// ReplaceOrigin. Incoming edges re-attach by the vault itself.
type OriginBundle struct {
	Origin    OriginRecord
	Entries   []EntryRow
	Links     []LinkRow
	Imports   []ImportRow
	LooseEnds []LooseEndRow
	RecvBinds []RecvBindRow
	Endpoints []TransportEndpointRow
	Calls     []TransportCallRow
}
