package roadmap

import (
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

func mustSelect(t *testing.T, src string, r Rule) *Selection {
	t.Helper()
	s, err := Select(Parse([]byte(src)), r)
	if err != nil {
		t.Fatal(err)
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
	// Deepest heading level is 4, so only "Images" is a topic.
	s := mustSelect(t, nested, Rule{})
	if got := titles(s); !reflect.DeepEqual(got, []string{"Images"}) {
		t.Fatalf("topics = %v", got)
	}
	want := []Group{
		{Title: "Roadmap", Level: 1, Line: 1, Parent: -1},
		{Title: "Phase 2", Level: 2, Line: 8, Parent: 0},
		{Title: "Docker", Level: 3, Line: 9, Parent: 1},
	}
	if !reflect.DeepEqual(s.Groups, want) {
		t.Fatalf("groups = %+v", s.Groups)
	}
	if s.Topics[0].Group != 2 {
		t.Fatalf("topic group = %d, want 2", s.Topics[0].Group)
	}
	if !reflect.DeepEqual(mustSelect(t, nested, Rule{Kind: RuleDeepest}), s) {
		t.Fatal(`Rule{} and Rule{Kind: "deepest"} differ`)
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
