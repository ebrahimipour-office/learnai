package roadmap

import (
	"os"
	"reflect"
	"testing"
)

const nested = `# Roadmap
## Phase 1
### Git
- branching
  - rebase
- merge
### Linux
## Phase 2
### Docker
#### Images
## Resources
- a book
`

// mustSelect blanks stable keys so these tests check structure only; stablekey_test.go covers keys.
func mustSelect(t *testing.T, src string, r Rule) *Selection {
	t.Helper()
	s, err := Select(Parse([]byte(src)), r)
	if err != nil {
		t.Fatal(err)
	}
	for i := range s.Groups {
		s.Groups[i].StableKey = ""
	}
	for i := range s.Topics {
		s.Topics[i].StableKey = ""
	}
	return s
}

func titles(s *Selection) []string {
	out := []string{}
	for _, tp := range s.Topics {
		out = append(out, tp.Title)
	}
	return out
}

func TestSelectDeepestDefault(t *testing.T) {
	// Leaf headings at any level are topics, including "Resources" which only has list items (B1).
	s := mustSelect(t, nested, Rule{})
	want := []Topic{
		{Title: "Git", Line: 3, Group: 1, Outline: "- branching\n  - rebase\n- merge"},
		{Title: "Linux", Line: 7, Group: 1},
		{Title: "Images", Line: 10, Group: 3},
		{Title: "Resources", Line: 11, Group: 0, Outline: "- a book"},
	}
	if !reflect.DeepEqual(s.Topics, want) {
		t.Fatalf("topics = %+v", s.Topics)
	}
	wantGroups := []Group{
		{Title: "Roadmap", Level: 1, Line: 1, Parent: -1},
		{Title: "Phase 1", Level: 2, Line: 2, Parent: 0},
		{Title: "Phase 2", Level: 2, Line: 8, Parent: 0},
		{Title: "Docker", Level: 3, Line: 9, Parent: 2},
	}
	if !reflect.DeepEqual(s.Groups, wantGroups) {
		t.Fatalf("groups = %+v", s.Groups)
	}
	if !reflect.DeepEqual(mustSelect(t, nested, Rule{Kind: RuleDeepest}), s) {
		t.Fatal(`Rule{} and Rule{Kind: "deepest"} differ`)
	}
}

// T7.0.a acceptance: the chapter without sub-headings is a topic, not silently dropped.
func TestSelectDeepestSampleBook(t *testing.T) {
	src, err := os.ReadFile("../../testdata/sample-book.md")
	if err != nil {
		t.Fatal(err)
	}
	got := titles(mustSelect(t, string(src), Rule{}))
	want := []string{"ثابت‌ها", "توابع متغیرآرگومان", "فصل ۳: برش‌ها (Slices)"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("topics = %q, want %q", got, want)
	}
}

func TestSelectLevelNestedGroupsAndOutline(t *testing.T) {
	s := mustSelect(t, nested, Rule{Kind: RuleLevel, Level: 3})
	if got := titles(s); !reflect.DeepEqual(got, []string{"Git", "Linux", "Docker"}) {
		t.Fatalf("topics = %v", got)
	}
	// "Resources" has no level-3 heading, so it is not listed as a group.
	want := []Group{
		{Title: "Roadmap", Level: 1, Line: 1, Parent: -1},
		{Title: "Phase 1", Level: 2, Line: 2, Parent: 0},
		{Title: "Phase 2", Level: 2, Line: 8, Parent: 0},
	}
	if !reflect.DeepEqual(s.Groups, want) {
		t.Fatalf("groups = %+v", s.Groups)
	}
	wantTopics := []Topic{
		{Title: "Git", Line: 3, Group: 1, Outline: "- branching\n  - rebase\n- merge"},
		{Title: "Linux", Line: 7, Group: 1},
		{Title: "Docker", Line: 9, Group: 2, Outline: "- Images"},
	}
	if !reflect.DeepEqual(s.Topics, wantTopics) {
		t.Fatalf("topics = %+v", s.Topics)
	}
}

func TestSelectLevelTopIsUngrouped(t *testing.T) {
	s := mustSelect(t, nested, Rule{Kind: RuleLevel, Level: 1})
	if len(s.Groups) != 0 || len(s.Topics) != 1 || s.Topics[0].Group != -1 {
		t.Fatalf("got %+v", s)
	}
}

func TestSelectItems(t *testing.T) {
	s := mustSelect(t, nested, Rule{Kind: RuleItems})
	if got := titles(s); !reflect.DeepEqual(got, []string{"branching", "merge", "a book"}) {
		t.Fatalf("topics = %v", got)
	}
	if s.Topics[0].Outline != "- rebase" {
		t.Fatalf("outline = %q", s.Topics[0].Outline)
	}
	if g := s.Groups[s.Topics[0].Group]; g.Title != "Git" || s.Groups[g.Parent].Title != "Phase 1" {
		t.Fatalf("branching grouped under %+v", g)
	}
	if g := s.Groups[s.Topics[2].Group]; g.Title != "Resources" {
		t.Fatalf("a book grouped under %+v", g)
	}
}

func TestSelectItemsWithoutHeadings(t *testing.T) {
	s := mustSelect(t, "- one\n- two\n", Rule{Kind: RuleItems})
	if got := titles(s); !reflect.DeepEqual(got, []string{"one", "two"}) || s.Topics[0].Group != -1 {
		t.Fatalf("got %+v", s)
	}
	if d := mustSelect(t, "- one\n", Rule{}); len(d.Topics) != 0 {
		t.Fatalf("deepest with no headings should select nothing, got %+v", d)
	}
}

func TestSelectRejectsBadRule(t *testing.T) {
	root := Parse([]byte(nested))
	for _, r := range []Rule{{Kind: RuleLevel}, {Kind: RuleLevel, Level: 7}, {Kind: "bogus"}} {
		if _, err := Select(root, r); err == nil {
			t.Errorf("Select(%+v) returned no error", r)
		}
	}
}
