package roadmap

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// RuleChapters selects explicit chapters (design §12.4): each is a heading plus a line range.
const RuleChapters RuleKind = "chapters"

// MaxChapterTitle bounds a chapter title in runes.
const MaxChapterTitle = 200

// Chapter is one entry of a chapters rule. Line must be a heading line; the topic is that heading,
// so its stable key ignores Title. Lines are 1-based on the normalized source (see Slice).
type Chapter struct {
	Line    int    `json:"line"`
	EndLine int    `json:"end_line"`
	Title   string `json:"title"`
}

// ChapterError is a ValidateChapters failure; its message is user-facing (Persian).
type ChapterError struct{ Msg string }

func (e *ChapterError) Error() string { return e.Msg }

// ValidateChapters checks chapters against the parsed tree of a source with lines lines: at least one;
// each starts on a heading, line <= end_line <= lines, ascending without overlap, no chapter heading
// under another chapter heading, and a non-blank title of at most MaxChapterTitle runes.
// Gaps between chapters are allowed. It is used for both AI output and edits in the preview.
func ValidateChapters(tree *Node, lines int, chs []Chapter) error {
	fail := func(format string, a ...any) error { return &ChapterError{fmt.Sprintf(format, a...)} }
	if len(chs) == 0 {
		return fail("حداقل یک فصل لازم است.")
	}
	ancestors := map[int][]int{} // heading line → lines of the headings above it
	var walk func(n *Node, above []int)
	walk = func(n *Node, above []int) {
		for _, c := range n.Children {
			if c.Kind == KindHeading {
				ancestors[c.Line] = above
				walk(c, append(above[:len(above):len(above)], c.Line))
			}
		}
	}
	walk(tree, nil)

	isChapter := map[int]bool{}
	for _, c := range chs {
		isChapter[c.Line] = true
	}
	for i, c := range chs {
		n := i + 1
		above, ok := ancestors[c.Line]
		title := strings.TrimSpace(c.Title)
		switch {
		case !ok:
			return fail("فصل %d: خط %d سرفصل نیست.", n, c.Line)
		case c.EndLine < c.Line || c.EndLine > lines:
			return fail("فصل %d: خط پایان %d باید بین %d و %d باشد.", n, c.EndLine, c.Line, lines)
		case i > 0 && c.Line <= chs[i-1].EndLine:
			return fail("فصل %d با فصل قبلی هم‌پوشانی دارد یا ترتیب فصل‌ها به‌هم خورده است.", n)
		case title == "":
			return fail("فصل %d: عنوان خالی است.", n)
		case utf8.RuneCountInString(title) > MaxChapterTitle:
			return fail("فصل %d: عنوان بیش از %d نویسه است.", n, MaxChapterTitle)
		}
		for _, a := range above {
			if isChapter[a] {
				return fail("فصل %d داخل فصل دیگری (خط %d) است.", n, a)
			}
		}
	}
	return nil
}

// LineCount is the number of lines of src as Parse and Slice number them.
func LineCount(src []byte) int { return lineCount(normalize(src)) }
