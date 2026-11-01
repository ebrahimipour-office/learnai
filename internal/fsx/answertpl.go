package fsx

import (
	"fmt"
	"strings"
)

const (
	// AnswerMarker is the standard line separating the template header from the student's answer.
	AnswerMarker = "--- WRITE YOUR ANSWER BELOW THIS LINE ---"
)

// CommentStyle defines prefix and suffix for single-line comments in a language.
type CommentStyle struct {
	Prefix string
	Suffix string
}

// commentStyles maps common file extensions to comment syntax.
var commentStyles = map[string]CommentStyle{
	".go":   {Prefix: "// "},
	".rs":   {Prefix: "// "},
	".c":    {Prefix: "// "},
	".cpp":  {Prefix: "// "},
	".java": {Prefix: "// "},
	".js":   {Prefix: "// "},
	".ts":   {Prefix: "// "},
	".py":   {Prefix: "# "},
	".sh":   {Prefix: "# "},
	".yaml": {Prefix: "# "},
	".yml":  {Prefix: "# "},
	".sql":  {Prefix: "-- "},
	".md":   {Prefix: "<!-- ", Suffix: " -->"},
	".html": {Prefix: "<!-- ", Suffix: " -->"},
	".txt":  {Prefix: "# "},
	".json": {Prefix: "// "}, // JSON doesn't support comments officially, but template header uses standard //
}

// GenerateAnswerTemplate constructs the initial answer file content with metadata header (R3.3).
func GenerateAnswerTemplate(stableKey string, setVersion int, exID string, title string, ext string, starterCode string) []byte {
	style, ok := commentStyles[strings.ToLower(ext)]
	if !ok {
		style = CommentStyle{Prefix: "// "}
	}

	comment := func(line string) string {
		if style.Suffix != "" {
			line = strings.ReplaceAll(line, strings.TrimSpace(style.Suffix), "") // a title must not close the comment early
			return fmt.Sprintf("%s%s%s\n", style.Prefix, line, style.Suffix)
		}
		return fmt.Sprintf("%s%s\n", style.Prefix, line)
	}

	var sb strings.Builder

	// Metadata tag for reconstruction & sync: @hub:topic=<stableKey>,set=v<setVersion>,ex=<exID>
	sb.WriteString(comment(fmt.Sprintf("@hub:topic=%s,set=v%d,ex=%s", stableKey, setVersion, exID)))
	sb.WriteString(comment(fmt.Sprintf("[%s] %s", exID, title)))
	sb.WriteString(comment(""))
	sb.WriteString(comment(AnswerMarker))
	sb.WriteString("\n")

	// AI starter text may carry a BOM or CRLF; answer files are always UTF-8 without BOM and "\n" only.
	starterCode = strings.TrimPrefix(starterCode, "\uFEFF")
	starterCode = strings.ReplaceAll(strings.ReplaceAll(starterCode, "\r\n", "\n"), "\r", "\n")
	if strings.TrimSpace(starterCode) != "" {
		sb.WriteString(starterCode)
		if !strings.HasSuffix(starterCode, "\n") {
			sb.WriteString("\n")
		}
	}

	return []byte(sb.String())
}

// ExtractAnswerContent splits the raw file content around AnswerMarker.
// Returns the raw student answer after the marker, trimmed of leading newline.
func ExtractAnswerContent(raw []byte) string {
	content := string(raw)
	idx := strings.Index(content, AnswerMarker)
	if idx == -1 {
		return strings.TrimSpace(content)
	}

	afterMarker := content[idx+len(AnswerMarker):]
	// If the comment style had a closing suffix on the same line, drop that line
	lines := strings.SplitN(afterMarker, "\n", 2)
	if len(lines) > 1 {
		return strings.TrimSpace(lines[1])
	}
	return strings.TrimSpace(afterMarker)
}
