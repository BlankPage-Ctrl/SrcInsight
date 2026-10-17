// Package extract defines shared helpers and types for language-specific
// extractors. Concrete extractors live in extract/typescript and extract/python.
package extract

import (
	"bytes"
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"

	"srcinsight/internal/model"
)

// ImportStmt aliases model.ImportStmt for call-site readability.
type ImportStmt = model.ImportStmt

// FileExtract aliases model.FileExtract.
type FileExtract = model.FileExtract

// NodeRange converts a tree-sitter node to a 1-indexed line and column range.
// Columns are 1-indexed byte offsets within their line; EndCol is exclusive
// (one past the last byte), matching tree-sitter positions.
func NodeRange(n *sitter.Node) model.Range {
	return model.Range{
		StartLine: int(n.StartPosition().Row) + 1,
		EndLine:   int(n.EndPosition().Row) + 1,
		StartCol:  int(n.StartPosition().Column) + 1,
		EndCol:    int(n.EndPosition().Column) + 1,
	}
}

// CallRange maps a call-name node to its line and column range inside the
// enclosing def.
func CallRange(n *sitter.Node) model.Range {
	if n == nil {
		return model.Range{}
	}
	return model.Range{
		StartLine: int(n.StartPosition().Row) + 1,
		EndLine:   int(n.EndPosition().Row) + 1,
		StartCol:  int(n.StartPosition().Column) + 1,
		EndCol:    int(n.EndPosition().Column) + 1,
	}
}

// SiteRange widens CallRange to the whole call statement via same-row ancestors.
// Lines span the widened statement; columns stay pinned to the identifier so a
// site keeps an exact reference point. Hops cap at 12; nil yields the zero range.
func SiteRange(n *sitter.Node) model.Range {
	if n == nil {
		return model.Range{}
	}
	startRow := n.StartPosition().Row
	endRow := n.EndPosition().Row
	startCol := int(n.StartPosition().Column) + 1
	endCol := int(n.EndPosition().Column) + 1
	for p, hops := n.Parent(), 0; p != nil && hops < 12; p, hops = p.Parent(), hops+1 {
		if p.StartPosition().Row != startRow {
			break
		}
		if er := p.EndPosition().Row; er > endRow {
			endRow = er
		}
	}
	return model.Range{
		StartLine: int(startRow) + 1,
		EndLine:   int(endRow) + 1,
		StartCol:  startCol,
		EndCol:    endCol,
	}
}

// Text returns the trimmed source slice covered by n in one alloc.
func Text(src []byte, n *sitter.Node) string {
	if n == nil {
		return ""
	}
	start, end := int(n.StartByte()), int(n.EndByte())
	if start > end || end > len(src) {
		return ""
	}
	b := bytes.TrimSpace(src[start:end])
	if len(b) == 0 {
		return ""
	}
	return string(b)
}

// HasAncestorType reports whether n or any ancestor has type containing token.
func HasAncestorType(n *sitter.Node, token string) bool {
	for n != nil {
		if strings.Contains(n.Kind(), token) {
			return true
		}
		n = n.Parent()
	}
	return false
}

// JoinInline collapses multi-line parts into a single-space-joined string.
func JoinInline(parts ...string) string {
	var b bytes.Buffer
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(p)
	}
	return strings.Join(strings.Fields(b.String()), " ")
}
