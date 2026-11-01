// Package roadmap turns a roadmap Markdown file into a deterministic tree (R2.1).
package roadmap

import (
	"bytes"
	"sort"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

// NodeKind identifies what a tree node was parsed from.
type NodeKind string

const (
	KindRoot    NodeKind = "root"
	KindHeading NodeKind = "heading"
	KindItem    NodeKind = "item"
)

// Node is one entry of the parsed roadmap tree. It is serialized as-is into import_drafts.parsed_tree.
type Node struct {
	Kind     NodeKind `json:"kind"`
	Level    int      `json:"level"` // heading: 1-6; item: list depth starting at 1; root: 0
	Title    string   `json:"title"`
	Line     int      `json:"line"` // 1-based source line; 0 for root
	Children []*Node  `json:"children,omitempty"`
}

var md = goldmark.New() // CommonMark only: no raw-HTML rendering is involved, we only read the AST.

// Parse builds the tree. Headings nest by level; lists attach to the nearest preceding heading
// (or the root). Paragraphs, code blocks, block quotes and HTML are ignored, so a '#' inside a
// fenced block never becomes a heading. Headings with no text are dropped.
func Parse(src []byte) *Node {
	src = bytes.TrimPrefix(src, []byte("\xEF\xBB\xBF"))
	src = bytes.ReplaceAll(src, []byte("\r\n"), []byte("\n"))

	doc := md.Parser().Parse(text.NewReader(src))
	lines := lineStarts(src)
	root := &Node{Kind: KindRoot}

	// stack[i] is the innermost open heading; stack[0] is the root.
	stack := []*Node{root}

	for n := doc.FirstChild(); n != nil; n = n.NextSibling() {
		switch b := n.(type) {
		case *ast.Heading:
			title := inlineText(b, src)
			if title == "" {
				continue
			}
			for len(stack) > 1 && stack[len(stack)-1].Level >= b.Level {
				stack = stack[:len(stack)-1]
			}
			h := &Node{Kind: KindHeading, Level: b.Level, Title: title, Line: lineOf(b, lines)}
			parent := stack[len(stack)-1]
			parent.Children = append(parent.Children, h)
			stack = append(stack, h)
		case *ast.List:
			parent := stack[len(stack)-1]
			parent.Children = append(parent.Children, listItems(b, 1, src, lines)...)
		}
	}
	return root
}

// listItems converts the items of list l (at nesting depth) into nodes. An item with no text of
// its own (e.g. "- " followed by a nested list) is dropped and its sub-items are promoted.
func listItems(l *ast.List, depth int, src []byte, lines []int) []*Node {
	var out []*Node
	for li := l.FirstChild(); li != nil; li = li.NextSibling() {
		var title string
		var line int
		var kids []*Node
		for c := li.FirstChild(); c != nil; c = c.NextSibling() {
			switch b := c.(type) {
			case *ast.TextBlock, *ast.Paragraph:
				if title == "" {
					title, line = inlineText(b, src), lineOf(b, lines)
				}
			case *ast.List:
				kids = append(kids, listItems(b, depth+1, src, lines)...)
			}
		}
		if title == "" {
			out = append(out, kids...)
			continue
		}
		out = append(out, &Node{Kind: KindItem, Level: depth, Title: title, Line: line, Children: kids})
	}
	return out
}

// inlineText returns the visible text of a block's inline children with whitespace collapsed.
// Link and emphasis markup is dropped (their text kept); raw HTML is dropped entirely.
func inlineText(n ast.Node, src []byte) string {
	var sb strings.Builder
	_ = ast.Walk(n, func(c ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch t := c.(type) {
		case *ast.RawHTML:
			return ast.WalkSkipChildren, nil
		case *ast.Text:
			sb.Write(t.Segment.Value(src))
			if t.SoftLineBreak() || t.HardLineBreak() {
				sb.WriteByte(' ')
			}
		case *ast.String:
			sb.Write(t.Value)
		case *ast.AutoLink:
			sb.Write(t.Label(src))
		}
		return ast.WalkContinue, nil
	})
	// strings.Fields splits on Unicode spaces only, so ZWNJ (نیم‌فاصله) is preserved.
	return strings.Join(strings.Fields(sb.String()), " ")
}

// lineStarts returns the byte offset at which each line begins.
func lineStarts(src []byte) []int {
	starts := []int{0}
	for i, b := range src {
		if b == '\n' {
			starts = append(starts, i+1)
		}
	}
	return starts
}

// lineOf returns the 1-based line of a block's first source segment.
func lineOf(n ast.Node, starts []int) int {
	if n.Lines().Len() == 0 {
		return 0
	}
	off := n.Lines().At(0).Start
	return sort.Search(len(starts), func(i int) bool { return starts[i] > off })
}
