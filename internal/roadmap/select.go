package roadmap

import (
	"fmt"
	"strings"
)

// RuleKind names a topic-selection rule (R2.2).
type RuleKind string

const (
	RuleDeepest RuleKind = "deepest" // default (A9): headings at the deepest heading level in the file
	RuleLevel   RuleKind = "level"   // headings at Rule.Level
	RuleItems   RuleKind = "items"   // top-level list items under headings (or the root)
)

// Rule chooses which tree nodes become topics. Stored as JSON in roadmaps.select_rule.
type Rule struct {
	Kind  RuleKind `json:"kind"`
	Level int      `json:"level,omitempty"` // RuleLevel only: 1-6
}

// Group is a heading above a topic. Parent indexes Selection.Groups; -1 means top level.
type Group struct {
	Title  string `json:"title"`
	Level  int    `json:"level"`
	Line   int    `json:"line"`
	Parent int    `json:"parent"`
}

// Topic is one selected topic. Group indexes Selection.Groups; -1 means none.
// Outline holds the topic's sub-entries as a nested Markdown list (topics.outline).
type Topic struct {
	Title   string `json:"title"`
	Line    int    `json:"line"`
	Group   int    `json:"group"`
	Outline string `json:"outline,omitempty"`
}

// Selection is the result of applying a Rule, in document order.
// Only groups that contain at least one topic are listed; a parent always precedes its children.
type Selection struct {
	Groups []Group `json:"groups"`
	Topics []Topic `json:"topics"`
}

// Select applies r to the tree produced by Parse. Everything below a topic goes into its outline;
// headings above it become groups. An empty Kind means RuleDeepest.
func Select(root *Node, r Rule) (*Selection, error) {
	isTopic, err := topicTest(root, r)
	if err != nil {
		return nil, err
	}
	s := &Selection{Groups: []Group{}, Topics: []Topic{}}
	groupIdx := map[*Node]int{}

	// group returns the index of the innermost heading in path, creating missing groups outermost first.
	group := func(path []*Node) int {
		parent := -1
		for _, h := range path {
			i, ok := groupIdx[h]
			if !ok {
				i = len(s.Groups)
				groupIdx[h] = i
				s.Groups = append(s.Groups, Group{Title: h.Title, Level: h.Level, Line: h.Line, Parent: parent})
			}
			parent = i
		}
		return parent
	}

	var walk func(n *Node, path []*Node)
	walk = func(n *Node, path []*Node) {
		for _, c := range n.Children {
			if isTopic(c, n) {
				s.Topics = append(s.Topics, Topic{Title: c.Title, Line: c.Line, Group: group(path), Outline: outline(c)})
			} else if c.Kind == KindHeading {
				walk(c, append(path[:len(path):len(path)], c))
			}
		}
	}
	walk(root, nil)
	return s, nil
}

func topicTest(root *Node, r Rule) (func(c, parent *Node) bool, error) {
	headingAt := func(level int) func(c, _ *Node) bool {
		return func(c, _ *Node) bool { return c.Kind == KindHeading && c.Level == level }
	}
	switch r.Kind {
	case RuleDeepest, "":
		return headingAt(maxHeadingLevel(root)), nil
	case RuleLevel:
		if r.Level < 1 || r.Level > 6 {
			return nil, fmt.Errorf("roadmap: heading level %d out of range 1-6", r.Level)
		}
		return headingAt(r.Level), nil
	case RuleItems:
		// parent check, not Level==1: items promoted from an empty parent item keep a deeper Level.
		return func(c, p *Node) bool { return c.Kind == KindItem && p.Kind != KindItem }, nil
	}
	return nil, fmt.Errorf("roadmap: unknown selection rule %q", r.Kind)
}

// maxHeadingLevel returns the deepest heading level in the tree, or 0 if there are no headings.
func maxHeadingLevel(n *Node) int {
	deepest := 0
	for _, c := range n.Children {
		if c.Kind == KindHeading {
			deepest = max(deepest, c.Level, maxHeadingLevel(c))
		}
	}
	return deepest
}

func outline(n *Node) string {
	var lines []string
	var rec func(n *Node, depth int)
	rec = func(n *Node, depth int) {
		for _, c := range n.Children {
			lines = append(lines, strings.Repeat("  ", depth)+"- "+c.Title)
			rec(c, depth+1)
		}
	}
	rec(n, 0)
	return strings.Join(lines, "\n")
}
