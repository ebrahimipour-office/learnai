// Package render turns untrusted Markdown (AI output, user answers) into safe
// HTML: goldmark without raw HTML, then bluemonday as a second layer.
package render

import (
	"bytes"
	"html/template"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/microcosm-cc/bluemonday"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	east "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// bidiControls are embeddings, overrides and isolates (Trojan Source); ZWNJ and LRM/RLM stay.
var bidiControls = strings.NewReplacer(
	"\u202A", "", "\u202B", "", "\u202C", "", "\u202D", "", "\u202E", "",
	"\u2066", "", "\u2067", "", "\u2068", "", "\u2069", "",
)

// StripBidi removes BiDi control characters; use it on any displayed text.
func StripBidi(s string) string { return bidiControls.Replace(s) }

var md = goldmark.New(
	goldmark.WithExtensions(extension.NewTable(extension.WithTableCellAlignMethod(extension.TableCellAlignAttribute)), extension.Strikethrough, extension.Linkify),
	goldmark.WithParserOptions(parser.WithAutoHeadingID(), parser.WithASTTransformers(util.Prioritized(dirTransformer{}, 100))),
	// no html.WithUnsafe: raw HTML is dropped and dangerous link schemes are blanked.
)

var policy = func() *bluemonday.Policy {
	p := bluemonday.NewPolicy()
	p.AllowElements("p", "br", "hr", "h1", "h2", "h3", "h4", "h5", "h6",
		"ul", "ol", "li", "blockquote", "pre", "code", "em", "strong", "del",
		"table", "thead", "tbody", "tr", "th", "td")
	p.AllowAttrs("dir").Matching(regexp.MustCompile(`^(auto|ltr|rtl)$`)).Globally()
	p.AllowAttrs("id").Matching(regexp.MustCompile(`^h-[\p{L}\p{M}\p{N}_-]+$`)).OnElements("h1", "h2", "h3", "h4", "h5", "h6")
	p.AllowAttrs("class").Matching(regexp.MustCompile(`^language-[\w+#-]+$`)).OnElements("code")
	p.AllowAttrs("align").Matching(regexp.MustCompile(`^(left|right|center)$`)).OnElements("th", "td")
	p.AllowAttrs("start").Matching(bluemonday.Integer).OnElements("ol")
	p.AllowAttrs("href").OnElements("a")
	p.AllowURLSchemes("http", "https", "mailto")
	p.AllowRelativeURLs(true)
	p.RequireParseableURLs(true)
	p.RequireNoReferrerOnLinks(true)
	p.AddTargetBlankToFullyQualifiedLinks(true) // also adds noopener
	return p
}()

// parse reads src with heading ids from headingIDs, so Markdown and TOC agree on them.
func parse(src []byte) (ast.Node, []byte) {
	src = []byte(StripBidi(string(src)))
	ctx := parser.NewContext(parser.WithIDs(&headingIDs{used: map[string]bool{}}))
	return md.Parser().Parse(text.NewReader(src), parser.WithContext(ctx)), src
}

// Markdown renders src to sanitized HTML safe to embed in a template.
func Markdown(src []byte) template.HTML {
	var buf bytes.Buffer
	doc, src := parse(src)
	if err := md.Renderer().Render(&buf, src, doc); err != nil {
		return template.HTML(template.HTMLEscapeString(string(src)))
	}
	// goldmark escapes all text and drops raw HTML, so a literal "<pre>" can only be its own tag.
	out := strings.ReplaceAll(buf.String(), "<pre>", `<pre dir="ltr">`)
	out = strings.ReplaceAll(out, "<code>", `<code dir="ltr">`)
	return template.HTML(policy.Sanitize(out))
}

// Heading is one table-of-contents entry (T9.10).
type Heading struct {
	Level int
	ID    string
	Text  string
}

// TOC lists the level 2 and 3 headings of src with the ids Markdown gives them.
func TOC(src []byte) []Heading {
	doc, src := parse(src)
	var out []Heading
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		h, ok := n.(*ast.Heading)
		if !entering || !ok {
			return ast.WalkContinue, nil
		}
		if id, ok := h.AttributeString("id"); ok && (h.Level == 2 || h.Level == 3) {
			out = append(out, Heading{Level: h.Level, ID: string(id.([]byte)), Text: plainText(h, src)})
		}
		return ast.WalkSkipChildren, nil
	})
	return out
}

// plainText joins the text and code-span contents under n.
func plainText(n ast.Node, src []byte) string {
	var b strings.Builder
	_ = ast.Walk(n, func(c ast.Node, entering bool) (ast.WalkStatus, error) {
		if t, ok := c.(*ast.Text); ok && entering {
			b.Write(t.Segment.Value(src))
			if t.SoftLineBreak() || t.HardLineBreak() {
				b.WriteByte(' ')
			}
		}
		return ast.WalkContinue, nil
	})
	return b.String()
}

// headingIDs keeps letters and digits of any script (goldmark's default drops non-ASCII, so every
// Persian heading would be "heading"). The "h-" prefix keeps ids from clobbering the page's own.
type headingIDs struct{ used map[string]bool }

func (g *headingIDs) Generate(value []byte, _ ast.NodeKind) []byte {
	var b strings.Builder
	b.WriteString("h-")
	dash := false
	for _, r := range strings.TrimSpace(string(value)) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.Is(unicode.Mn, r):
			if dash {
				b.WriteByte('-')
			}
			dash = false
			b.WriteRune(unicode.ToLower(r))
		case unicode.IsSpace(r) || r == '-' || r == '_':
			dash = b.Len() > 2
		}
	}
	base := b.String()
	if base == "h-" {
		base = "h-heading"
	}
	id := base
	for i := 1; g.used[id]; i++ {
		id = base + "-" + strconv.Itoa(i)
	}
	g.used[id] = true
	return []byte(id)
}

func (g *headingIDs) Put(value []byte) { g.used[string(value)] = true }

// dirTransformer marks generated text blocks dir="auto" and drops images.
type dirTransformer struct{}

func (dirTransformer) Transform(doc *ast.Document, reader text.Reader, _ parser.Context) {
	src := reader.Source()
	var images []ast.Node
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch n.(type) {
		case *ast.Paragraph, *ast.Heading, *ast.ListItem, *ast.Blockquote, *east.TableCell:
			n.SetAttributeString("dir", []byte(blockDir(n, src)))
		case *ast.Image:
			images = append(images, n)
		}
		return ast.WalkContinue, nil
	})
	for _, img := range images {
		img.Parent().RemoveChild(img.Parent(), img)
	}
}

// tagPrefix matches a leading ASCII label such as "[q01] " in exercise headings.
var tagPrefix = regexp.MustCompile(`^\[[A-Za-z0-9_-]+\]\s`)

// blockDir is "auto", except a block opening with a Latin label like "[q01]" takes
// its direction from the text after the label, so "[q01] مقایسه" stays RTL.
func blockDir(n ast.Node, src []byte) string {
	if n.Type() != ast.TypeBlock || n.Lines().Len() == 0 {
		return "auto"
	}
	first := n.Lines().At(0)
	line := first.Value(src)
	loc := tagPrefix.FindIndex(line)
	if loc == nil {
		return "auto"
	}
	for _, r := range string(line[loc[1]:]) {
		if unicode.In(r, unicode.Arabic, unicode.Hebrew, unicode.Syriac, unicode.Thaana, unicode.Nko) {
			return "rtl"
		}
		if unicode.IsLetter(r) {
			return "auto"
		}
	}
	return "auto"
}
