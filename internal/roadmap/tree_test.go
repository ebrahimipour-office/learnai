package roadmap

import (
	"errors"
	"os"
	"strings"
	"testing"
)

type span struct {
	title           string
	line, end, prnt int
}

func spans(s *Selection) []span {
	out := []span{}
	for _, t := range s.Topics {
		out = append(out, span{t.Title, t.Line, t.EndLine, t.Parent})
	}
	return out
}

func eqSpans(t *testing.T, got *Selection, want []span) {
	t.Helper()
	g := spans(got)
	if len(g) != len(want) {
		t.Fatalf("topics = %+v, want %+v", g, want)
	}
	for i := range want {
		if g[i] != want[i] {
			t.Errorf("topic %d = %+v, want %+v", i, g[i], want[i])
		}
	}
}

// T8.2: every heading but the sole H1 is a topic; a parent's range is only its introduction; a
// leaf's stable key equals its key under deepest, so re-importing keeps the leaf records.
func TestSelectTreeSampleBook(t *testing.T) {
	src, err := os.ReadFile("../../testdata/sample-book.md")
	if err != nil {
		t.Fatal(err)
	}
	root := Parse(src)
	tree, err := Select(root, Rule{Kind: RuleTree})
	if err != nil {
		t.Fatal(err)
	}
	last := LineCount(src)
	eqSpans(t, tree, []span{
		{"فصل ۱: متغیرها و انواع داده", 5, 15, -1},
		{"ثابت‌ها", 16, 19, 0},
		{"فصل ۲: توابع", 20, 32, -1},
		{"توابع متغیرآرگومان", 33, 36, 2},
		{"فصل ۳: برش‌ها (Slices)", 37, last, -1},
	})
	if len(tree.Groups) != 0 {
		t.Errorf("groups = %+v, want none (the sole H1 is the title)", tree.Groups)
	}
	if tree.Topics[0].Outline != "- ثابت‌ها" {
		t.Errorf("parent outline = %q, want its direct sub-topics", tree.Topics[0].Outline)
	}

	deep, err := Select(root, Rule{})
	if err != nil {
		t.Fatal(err)
	}
	byLine := map[int]Topic{}
	for _, tp := range tree.Topics {
		byLine[tp.Line] = tp
	}
	for _, leaf := range deep.Topics {
		got := byLine[leaf.Line]
		if got.StableKey != leaf.StableKey || got.EndLine != leaf.EndLine || got.Outline != leaf.Outline {
			t.Errorf("leaf %q: tree %+v, deepest %+v", leaf.Title, got, leaf)
		}
	}
}

func TestSelectTreeDuplicateTitlesKeepDeepestKeys(t *testing.T) {
	src := "# R\n## A\n### X\n## A\n### X\n## X\n"
	root := Parse([]byte(src))
	tree, _ := Select(root, Rule{Kind: RuleTree})
	deep, _ := Select(root, Rule{})
	keys := map[string]bool{}
	for _, tp := range tree.Topics {
		if keys[tp.StableKey] {
			t.Errorf("duplicate key %s", tp.StableKey)
		}
		keys[tp.StableKey] = true
	}
	for _, leaf := range deep.Topics {
		if !keys[leaf.StableKey] {
			t.Errorf("deepest leaf %q (line %d) key missing under tree", leaf.Title, leaf.Line)
		}
	}
}

func TestSelectTreeCodeBlockIsNotABoundary(t *testing.T) {
	sel, err := Select(Parse([]byte(book)), Rule{Kind: RuleTree})
	if err != nil {
		t.Fatal(err)
	}
	eqSpans(t, sel, []span{{"One", 3, 4, -1}, {"One.a", 5, 6, 0}, {"Two", 7, 10, -1}, {"Three", 11, 12, -1}})
}

func TestSelectTreeChosenHeadings(t *testing.T) {
	root := Parse([]byte(nested))
	// Phase 2 (8) and Images (10) chosen, Docker (9) between them is not: Docker becomes a group,
	// Images keeps Phase 2 as its parent, and Phase 2's range stops before Images.
	sel, err := Select(root, Rule{Kind: RuleTree, Chapters: []Chapter{{Line: 8, Title: " مرحله ۲ "}, {Line: 10, Title: "Images"}}})
	if err != nil {
		t.Fatal(err)
	}
	eqSpans(t, sel, []span{{"مرحله ۲", 8, 9, -1}, {"Images", 10, 10, 0}})
	if len(sel.Groups) != 1 || sel.Groups[0].Title != "Docker" || sel.Topics[1].Group != 0 || sel.Topics[0].Group != -1 {
		t.Errorf("groups = %+v, topics = %+v", sel.Groups, sel.Topics)
	}
	// a chosen heading whose sub-headings are not chosen is a leaf with its whole text
	sel, _ = Select(root, Rule{Kind: RuleTree, Chapters: []Chapter{{Line: 2, Title: "Phase 1"}}})
	eqSpans(t, sel, []span{{"Phase 1", 2, 7, -1}})
}

func TestValidateChaptersNested(t *testing.T) {
	tree := Parse([]byte(book))
	lines := LineCount([]byte(book))
	for name, chs := range map[string][]Chapter{
		"empty means all":   nil,
		"nested":            {{3, 0, "یک"}, {5, 0, "یک الف"}, {7, 0, "دو"}},
		"end_line ignored":  {{3, 99, "یک"}},
		"title is trimmed":  {{11, 0, " سه "}},
		"single deep child": {{5, 0, "x"}},
	} {
		if err := ValidateChapters(tree, lines, chs, true); err != nil {
			t.Errorf("%s: rejected: %v", name, err)
		}
	}
	for name, chs := range map[string][]Chapter{
		"not a heading":   {{4, 0, "x"}},
		"code-block line": {{9, 0, "x"}},
		"duplicate":       {{3, 0, "x"}, {3, 0, "y"}},
		"unsorted":        {{7, 0, "y"}, {3, 0, "x"}},
		"blank title":     {{3, 0, "  "}},
		"long title":      {{3, 0, strings.Repeat("ب", MaxChapterTitle+1)}},
	} {
		var ce *ChapterError
		if err := ValidateChapters(tree, lines, chs, true); !errors.As(err, &ce) {
			t.Errorf("%s: want ChapterError, got %v", name, err)
		}
	}
}
