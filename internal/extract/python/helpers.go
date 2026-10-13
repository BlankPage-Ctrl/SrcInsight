package python

import (
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"

	"srcinsight/internal/extract"
)

// condMaxLen caps a captured branch condition with truncation.
const condMaxLen = 120

// branchCond returns the nearest enclosing branch condition guarding the call.
// Nested function boundaries stop the walk; else branches negate.
func branchCond(src []byte, nameNode, def *sitter.Node) string {
	defID := def.Id()
	nameStart, nameEnd := nameNode.StartByte(), nameNode.EndByte()
	for n := nameNode.Parent(); n != nil; n = n.Parent() {
		if n.Id() == defID {
			break
		}
		switch n.Kind() {
		case "lambda", "function_definition":
			return ""
		case "if_statement":
			// handled below
		default:
			continue
		}
		condNode := n.ChildByFieldName("condition")
		if condNode == nil {
			continue
		}
		cond := extract.JoinInline(extract.Text(src, condNode))
		if cond == "" {
			return ""
		}
		if len(cond) > condMaxLen {
			cond = cond[:condMaxLen-3] + "..."
		}
		if alt := n.ChildByFieldName("alternative"); alt != nil &&
			alt.StartByte() <= nameStart && nameEnd <= alt.EndByte() {
			return "!(" + cond + ")"
		}
		return cond
	}
	return ""
}

// comment is a collected comment block used for Doc capture.
type comment struct {
	endRow int // 0-indexed end row
	text   string
}

// renderSignature renders the one-line head of a definition without the body.
func renderSignature(src []byte, def *sitter.Node) string {
	end := def.EndByte()
	if body := def.ChildByFieldName("body"); body != nil {
		end = body.StartByte()
	}
	start := def.StartByte()
	if int(start) > int(end) || int(end) > len(src) {
		return ""
	}
	return strings.Join(strings.Fields(string(src[start:end])), " ")
}

// collectComments gathers all comment nodes in source order.
func collectComments(src []byte, root *sitter.Node) []comment {
	var out []comment
	qc := sitter.NewQueryCursor()
	defer qc.Close()
	matches := qc.Matches(docQuery, root, src)
	for m := matches.Next(); m != nil; m = matches.Next() {
		for _, c := range m.Captures {
			if c.Index != docID {
				continue
			}
			n := c.Node
			out = append(out, comment{
				endRow: int(n.EndPosition().Row),
				text:   extract.Text(src, &n),
			})
		}
	}
	return out
}

// docFor returns the contiguous comment block immediately preceding def.
func docFor(def *sitter.Node, comments []comment) string {
	if len(comments) == 0 {
		return ""
	}
	start := int(def.StartPosition().Row)
	var lines []string
	for i := len(comments) - 1; i >= 0; i-- {
		c := comments[i]
		if len(lines) == 0 {
			if c.endRow+1 == start || c.endRow+1 == start-1 {
				lines = append([]string{c.text}, lines...)
				start = c.endRow
				continue
			}
			break
		}
		if c.endRow+1 == start {
			lines = append([]string{c.text}, lines...)
			start = c.endRow
			continue
		}
		break
	}
	return strings.Join(lines, "\n")
}

// collectImports finds import statements via one tree-sitter query.
func collectImports(src []byte, root *sitter.Node) []extract.ImportStmt {
	var out []extract.ImportStmt
	qc := sitter.NewQueryCursor()
	defer qc.Close()
	matches := qc.Matches(importQuery, root, src)
	for m := matches.Next(); m != nil; m = matches.Next() {
		for _, c := range m.Captures {
			n := c.Node
			switch c.Index {
			case impStmtID:
				if name := n.ChildByFieldName("name"); name != nil {
					v := extract.Text(src, name)
					out = append(out, extract.ImportStmt{
						LocalName: v,
						Source:    v,
						Kind:      "namespace",
					})
				}
			case impFromID:
				collectFromPairs(src, &n, &out)
			}
		}
	}
	return out
}

// collectFromPairs handles `from module import a, b as c, *`.
func collectFromPairs(src []byte, stmt *sitter.Node, out *[]extract.ImportStmt) {
	var module string
	var pending *extract.ImportStmt
	for i := 0; i < int(stmt.ChildCount()); i++ {
		c := stmt.Child(uint(i))
		if c == nil {
			continue
		}
		switch stmt.FieldNameForChild(uint32(i)) {
		case "module_name":
			module = extract.Text(src, c)
		case "name":
			st := extract.ImportStmt{LocalName: extract.Text(src, c), Source: module, Kind: "named"}
			*out = append(*out, st)
			pending = &(*out)[len(*out)-1]
		case "alias":
			if pending != nil {
				pending.Kind = "default"
				pending.Alias = extract.Text(src, c)
				pending.LocalName = extract.Text(src, c)
			}
		}
	}
}
