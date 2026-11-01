// Package render turns untrusted Markdown (AI output, user answers) into safe
// HTML: goldmark without raw HTML, then bluemonday as a second layer.
package render

import (
	"bytes"
	"html/template"
	"regexp"
	"strings"

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
	goldmark.WithParserOptions(parser.WithASTTransformers(util.Prioritized(dirTransformer{}, 100))),
	// no html.WithUnsafe: raw HTML is dropped and dangerous link schemes are blanked.
)

var policy = func() *bluemonday.Policy {
	p := bluemonday.NewPolicy()
	p.AllowElements("p", "br", "hr", "h1", "h2", "h3", "h4", "h5", "h6",
		"ul", "ol", "li", "blockquote", "pre", "code", "em", "strong", "del",
		"table", "thead", "tbody", "tr", "th", "td")
	p.AllowAttrs("dir").Matching(regexp.MustCompile(`^(auto|ltr|rtl)$`)).Globally()
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

// Markdown renders src to sanitized HTML safe to embed in a template.
func Markdown(src []byte) template.HTML {
	var buf bytes.Buffer
	if err := md.Convert([]byte(StripBidi(string(src))), &buf); err != nil {
		return template.HTML(template.HTMLEscapeString(string(src)))
	}
	// goldmark escapes all text and drops raw HTML, so a literal "<pre>" can only be its own tag.
	out := strings.ReplaceAll(buf.String(), "<pre>", `<pre dir="ltr">`)
	out = strings.ReplaceAll(out, "<code>", `<code dir="ltr">`)
	return template.HTML(policy.Sanitize(out))
}

// dirTransformer marks generated text blocks dir="auto" and drops images.
type dirTransformer struct{}

func (dirTransformer) Transform(doc *ast.Document, _ text.Reader, _ parser.Context) {
	var images []ast.Node
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch n.(type) {
		case *ast.Paragraph, *ast.Heading, *ast.ListItem, *ast.Blockquote, *east.TableCell:
			n.SetAttributeString("dir", []byte("auto"))
		case *ast.Image:
			images = append(images, n)
		}
		return ast.WalkContinue, nil
	})
	for _, img := range images {
		img.Parent().RemoveChild(img.Parent(), img)
	}
}
