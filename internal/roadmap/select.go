package roadmap

import (
	"fmt"
	"strings"
)

// RuleKind names a topic-selection rule (R2.2).
type RuleKind string

const (
	RuleDeepest RuleKind = "deepest" // default (A9): leaf headings, i.e. headings with no sub-heading (T7.0.a)
	RuleLevel   RuleKind = "level"   // headings at Rule.Level
	RuleItems   RuleKind = "items"   // top-level list items under headings (or the root)
)

// Rule chooses which tree nodes become topics. Stored as JSON in roadmaps.select_rule.
type Rule struct {
	Kind     RuleKind  `json:"kind"`
	Level    int       `json:"level,omitempty"`    // RuleLevel only: 1-6
	Chapters []Chapter `json:"chapters,omitempty"` // RuleChapters only; checked by ValidateChapters
}

// Group is a heading above a topic. Parent indexes Selection.Groups; -1 means top level.
type Group struct {
	StableKey string `json:"stable_key"`
	Title     string `json:"title"`
	Level     int    `json:"level"`
	Line      int    `json:"line"`
	Parent    int    `json:"parent"`
}

// Topic is one selected topic. Group indexes Selection.Groups; -1 means none.
// Outline holds the topic's sub-entries as a nested Markdown list (topics.outline).
type Topic struct {
	StableKey string `json:"stable_key"`
	Title     string `json:"title"`
	Line      int    `json:"line"`
	Group     int    `json:"group"`
	Outline   string `json:"outline,omitempty"`
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
	isTopic, err := topicTest(r)
	if err != nil {
		return nil, err
	}
	s := &Selection{Groups: []Group{}, Topics: []Topic{}}
	groupIdx := map[*Node]int{}
	var groupPaths []string // normalized key path of each entry in s.Groups
	usedG, usedT := map[string]bool{}, map[string]bool{}
	chTitle := map[int]string{} // RuleChapters: the chapter title replaces the heading title, not its key
	if r.Kind == RuleChapters {
		for _, c := range r.Chapters {
			chTitle[c.Line] = strings.TrimSpace(c.Title)
		}
	}
	pathOf := func(group int) string {
		if group < 0 {
			return ""
		}
		return groupPaths[group]
	}

	// group returns the index of the innermost heading in path, creating missing groups outermost first.
	group := func(path []*Node) int {
		parent := -1
		for _, h := range path {
			i, ok := groupIdx[h]
			if !ok {
				i = len(s.Groups)
				groupIdx[h] = i
				p := childPath(pathOf(parent), h.Title, usedG)
				groupPaths = append(groupPaths, p)
				s.Groups = append(s.Groups, Group{StableKey: stableKey(p), Title: h.Title, Level: h.Level, Line: h.Line, Parent: parent})
			}
			parent = i
		}
		return parent
	}

	var walk func(n *Node, path []*Node)
	walk = func(n *Node, path []*Node) {
		for _, c := range n.Children {
			if isTopic(c, n) {
				g := group(path)
				key := stableKey(childPath(pathOf(g), c.Title, usedT))
				title, ok := chTitle[c.Line]
				if !ok {
					title = c.Title
				}
				s.Topics = append(s.Topics, Topic{StableKey: key, Title: title, Line: c.Line, Group: g, Outline: outline(c)})
			} else if c.Kind == KindHeading {
				walk(c, append(path[:len(path):len(path)], c))
			}
		}
	}
	walk(root, nil)
	// B2: a sole H1 is the roadmap title (Title), not a group. Keys were already computed with it in
	// the path, so stable keys of existing roadmaps do not change.
	if h := soleH1(root); h != nil {
		if i, ok := groupIdx[h]; ok {
			s.dropGroup(i)
		}
	}
	return s, nil
}

// Title returns the text of the first H1 (always a root child), or "" when there is none.
func Title(root *Node) string {
	for _, c := range root.Children {
		if c.Kind == KindHeading && c.Level == 1 {
			return c.Title
		}
	}
	return ""
}

func soleH1(root *Node) *Node {
	var h1 *Node
	for _, c := range root.Children {
		if c.Kind == KindHeading && c.Level == 1 {
			if h1 != nil {
				return nil
			}
			h1 = c
		}
	}
	return h1
}

// dropGroup removes group i; its children move to its parent. A parent precedes its children, so
// Groups[i].Parent < i is unaffected by the shift.
func (s *Selection) dropGroup(i int) {
	up := s.Groups[i].Parent
	fix := func(g int) int {
		switch {
		case g == i:
			return up
		case g > i:
			return g - 1
		}
		return g
	}
	s.Groups = append(s.Groups[:i], s.Groups[i+1:]...)
	for k := range s.Groups {
		s.Groups[k].Parent = fix(s.Groups[k].Parent)
	}
	for k := range s.Topics {
		s.Topics[k].Group = fix(s.Topics[k].Group)
	}
}

func topicTest(r Rule) (func(c, parent *Node) bool, error) {
	headingAt := func(level int) func(c, _ *Node) bool {
		return func(c, _ *Node) bool { return c.Kind == KindHeading && c.Level == level }
	}
	switch r.Kind {
	case RuleDeepest, "":
		// leaves, not the file-wide deepest level: that silently dropped chapters without sub-headings (B1).
		// Every old-rule topic is also a leaf under the same groups, so existing stable keys are unchanged.
		return func(c, _ *Node) bool { return c.Kind == KindHeading && !hasHeadingChild(c) }, nil
	case RuleLevel:
		if r.Level < 1 || r.Level > 6 {
			return nil, fmt.Errorf("roadmap: heading level %d out of range 1-6", r.Level)
		}
		return headingAt(r.Level), nil
	case RuleChapters:
		if len(r.Chapters) == 0 {
			return nil, fmt.Errorf("roadmap: chapters rule without chapters")
		}
		lines := map[int]bool{}
		for _, c := range r.Chapters {
			lines[c.Line] = true
		}
		return func(c, _ *Node) bool { return c.Kind == KindHeading && lines[c.Line] }, nil
	case RuleItems:
		// parent check, not Level==1: items promoted from an empty parent item keep a deeper Level.
		return func(c, p *Node) bool { return c.Kind == KindItem && p.Kind != KindItem }, nil
	}
	return nil, fmt.Errorf("roadmap: unknown selection rule %q", r.Kind)
}

func hasHeadingChild(n *Node) bool {
	for _, c := range n.Children {
		if c.Kind == KindHeading {
			return true
		}
	}
	return false
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
