package roadmap

import (
	"errors"
	"strings"
	"testing"
)

const book = `# Book
preface
## One
text
### One.a
more
## Two
` + "```" + `
# not a heading
` + "```" + `
## Three
end
`

func TestValidateChapters(t *testing.T) {
	tree := Parse([]byte(book))
	lines := LineCount([]byte(book))
	if lines != 12 {
		t.Fatalf("LineCount = %d, want 12", lines)
	}
	ok := []Chapter{{3, 6, "یک"}, {7, 10, "دو"}, {11, 12, "سه"}}
	if err := ValidateChapters(tree, lines, ok); err != nil {
		t.Fatalf("valid chapters rejected: %v", err)
	}
	// gap (preface, skipped chapter) and a merged range are fine
	if err := ValidateChapters(tree, lines, []Chapter{{3, 10, "یک و دو"}}); err != nil {
		t.Fatalf("merged chapter rejected: %v", err)
	}
	for name, chs := range map[string][]Chapter{
		"empty":           nil,
		"not a heading":   {{4, 6, "x"}},
		"code-block line": {{9, 9, "x"}},
		"end before line": {{3, 2, "x"}},
		"end past file":   {{11, 13, "x"}},
		"overlap":         {{3, 7, "x"}, {7, 10, "y"}},
		"unsorted":        {{7, 10, "y"}, {3, 6, "x"}},
		"nested":          {{3, 4, "x"}, {5, 6, "y"}},
		"blank title":     {{3, 6, "  "}},
		"long title":      {{3, 6, strings.Repeat("ب", MaxChapterTitle+1)}},
	} {
		var ce *ChapterError
		if err := ValidateChapters(tree, lines, chs); !errors.As(err, &ce) {
			t.Errorf("%s: want ChapterError, got %v", name, err)
		}
	}
}

func TestSelectChapters(t *testing.T) {
	tree := Parse([]byte(book))
	deep, err := Select(tree, Rule{Kind: RuleLevel, Level: 2})
	if err != nil {
		t.Fatal(err)
	}
	sel, err := Select(tree, Rule{Kind: RuleChapters, Chapters: []Chapter{{3, 6, " فصل یک "}, {11, 12, "فصل سه"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(sel.Topics) != 2 || sel.Topics[0].Title != "فصل یک" || sel.Topics[1].Title != "فصل سه" {
		t.Fatalf("topics = %+v", sel.Topics)
	}
	// keys come from the heading path, not the chapter title
	if sel.Topics[0].StableKey != deep.Topics[0].StableKey || sel.Topics[1].StableKey != deep.Topics[2].StableKey {
		t.Errorf("chapter keys differ from heading keys")
	}
	if _, err := Select(tree, Rule{Kind: RuleChapters}); err == nil {
		t.Error("chapters rule without chapters accepted")
	}
}
