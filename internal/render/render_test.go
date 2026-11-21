package render

import (
	"regexp"
	"strings"
	"testing"
)

func TestGolden(t *testing.T) {
	src := "# سلام Go\n\nمتن فارسی با `fmt.Println` و کلمه English.\n\n" +
		"- یک\n- دو\n\n1. اول\n2. دوم\n\n" +
		"| الف | b |\n|---|:-:|\n| ۱ | x |\n\n" +
		"```go\nfmt.Println(\"<b>\")\n```\n\n" +
		"[بیرونی](https://go.dev) [داخلی](/topics) ![img](https://e.com/a.png)\n\n> نقل\n"
	want := `<h2 id="h-سلام-go" dir="auto">سلام Go</h2>
<p dir="auto">متن فارسی با <code dir="ltr">fmt.Println</code> و کلمه English.</p>
<ul>
<li dir="auto">یک</li>
<li dir="auto">دو</li>
</ul>
<ol>
<li dir="auto">اول</li>
<li dir="auto">دوم</li>
</ol>
<table>
<thead>
<tr>
<th dir="auto">الف</th>
<th align="center" dir="auto">b</th>
</tr>
</thead>
<tbody>
<tr>
<td dir="auto">۱</td>
<td align="center" dir="auto">x</td>
</tr>
</tbody>
</table>
<pre dir="ltr"><code class="language-go">fmt.Println(&#34;&lt;b&gt;&#34;)
</code></pre>
<p dir="auto"><a href="https://go.dev" rel="noreferrer noopener" target="_blank">بیرونی</a> <a href="/topics" rel="noreferrer">داخلی</a> </p>
<blockquote dir="auto"><p dir="auto">نقل</p>
</blockquote>
`
	if got := string(Markdown([]byte(src))); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestXSS(t *testing.T) {
	payloads := []string{
		`<script>alert(1)</script>`,
		`<img src=x onerror=alert(1)>`,
		`<svg onload=alert(1)>`,
		`<iframe src="https://evil.com"></iframe>`,
		`[x](javascript:alert(1))`,
		`[x](JaVaScRiPt:alert(1))`,
		`[x](  javascript:alert(1))`,
		`[x](java&#x09;script:alert(1))`,
		`[x](&#106;avascript:alert(1))`,
		`[x](data:text/html;base64,PHNjcmlwdD5hbGVydCgxKTwvc2NyaXB0Pg==)`,
		`[x](vbscript:msgbox(1))`,
		`![x](javascript:alert(1))`,
		`![x](https://evil.com/track.png)`,
		`<a href="javascript:alert(1)">x</a>`,
		`<div style="background:url(javascript:alert(1))">x</div>`,
		`<object data="x.swf"></object>`,
		`<embed src="x.swf">`,
		`<form action="https://evil.com"><input name=p></form>`,
		`<meta http-equiv="refresh" content="0;url=https://evil.com">`,
		`<style>body{display:none}</style>`,
		`<link rel=stylesheet href=https://evil.com/x.css>`,
		"```\n</code></pre><script>alert(1)</script>\n```",
		"`<script>alert(1)</script>`",
		`<details open ontoggle=alert(1)>`,
		`[x](https://ok.com "a\" onmouseover=\"alert(1)")`,
		`<base href="https://evil.com/">`,
		`javascript:alert(1)`,
		`<math><mi xlink:href="javascript:alert(1)">x</mi></math>`,
		`<a href=x/><img src=x onerror=alert(1)>`, // CVE-2025-22872: unquoted attribute ending in /
		`<math><style><img src=x onerror=alert(1)//></style></math>`,
	}
	bad := regexp.MustCompile(`(?i)<(script|img|svg|iframe|object|embed|form|input|meta|style|link|base|div|details|math)\b|\son\w+=|javascript:|vbscript:|data:`)
	for _, p := range payloads {
		got := string(Markdown([]byte(p)))
		// text content is escaped; only real tags/attributes are dangerous
		if m := bad.FindString(stripText(got)); m != "" {
			t.Errorf("payload %q leaked %q in %s", p, m, got)
		}
	}
}

// stripText keeps only tags so escaped text like "&lt;script&gt;" or the literal word "javascript:" in prose is ignored.
func stripText(h string) string {
	return strings.Join(regexp.MustCompile(`<[^>]*>`).FindAllString(h, -1), "")
}

func TestStripBidi(t *testing.T) {
	got := string(Markdown([]byte("a\u202Eb\u2066c\u2069 نیم\u200Cفاصله `x\u202Ey`")))
	if strings.ContainsAny(got, "\u202E\u2066\u2069") || !strings.Contains(got, "نیم\u200Cفاصله") || !strings.Contains(got, ">xy<") {
		t.Fatalf("bidi not stripped or ZWNJ lost: %q", got)
	}
}

var headingID = regexp.MustCompile(` id="[^"]*"`)

// T9.2: a Latin label like "[q01]" must not make a Persian heading LTR.
func TestLabelledBlockDir(t *testing.T) {
	for src, want := range map[string]string{
		"## [q01] مقایسه var و :=": `<h3 dir="rtl">[q01] مقایسه var و :=</h3>`,
		"## [q01] Compare vars":    `<h3 dir="auto">[q01] Compare vars</h3>`,
		"[q02] متن پاراگراف":       `<p dir="rtl">[q02] متن پاراگراف</p>`,
		"## [q01]":                 `<h3 dir="auto">[q01]</h3>`,
		"## q01 مقایسه":            `<h3 dir="auto">q01 مقایسه</h3>`,
		"## [q 1] مقایسه":          `<h3 dir="auto">[q 1] مقایسه</h3>`,
	} {
		got := headingID.ReplaceAllString(strings.TrimSpace(string(Markdown([]byte(src)))), "") // ids are TestHeadingIDsAndTOC's
		if got != want {
			t.Errorf("%q:\n got %s\nwant %s", src, got, want)
		}
	}
}

// T9.10: Persian headings get non-empty, unique, sanitizer-proof ids that TOC links to.
func TestHeadingIDsAndTOC(t *testing.T) {
	src := []byte("# درس\n\n## مقدمه\n\nمتن\n\n### نوع `int`\n\n## مقدمه\n\n## !!!\n\n#### عمیق\n")
	got := TOC(src)
	want := []Heading{{2, "h-مقدمه", "مقدمه"}, {3, "h-نوع-int", "نوع int"}, {2, "h-مقدمه-1", "مقدمه"}, {2, "h-heading", "!!!"}}
	if len(got) != len(want) {
		t.Fatalf("TOC = %+v, want %+v", got, want)
	}
	html := string(Markdown(src))
	// T10.2: the page has the only <h1>; every Markdown heading renders one level down.
	for _, tag := range []string{`<h2 id="h-درس"`, `<h3 id="h-مقدمه"`, `<h4 id="h-نوع-int"`, `<h5 id="h-عمیق"`} {
		if !strings.Contains(html, tag) {
			t.Errorf("rendered HTML lacks %s:\n%s", tag, html)
		}
	}
	if strings.Contains(html, "<h1") || !strings.Contains(string(Markdown([]byte("###### شش"))), "<h6") {
		t.Errorf("want no <h1> and ###### capped at <h6>")
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("TOC[%d] = %+v, want %+v", i, got[i], want[i])
		}
		if !strings.Contains(html, `id="`+want[i].ID+`"`) {
			t.Errorf("rendered HTML lacks id %q:\n%s", want[i].ID, html)
		}
	}
	// an explicit id attribute from Markdown must not reach the page unprefixed
	if h := string(Markdown([]byte("## x {#job-area}\n"))); strings.Contains(h, `id="job-area"`) {
		t.Errorf("unprefixed id leaked: %s", h)
	}
}

// T10.12: an exercise heading "[q01] ..." gets the id h-q01 so the exercise list can link to it.
func TestLabelledHeadingID(t *testing.T) {
	html := string(Markdown([]byte("## [q01] مقایسه\n\n## [q02]\n\n## [q01] تکرار\n\n## [q 1] x\n")))
	for _, tag := range []string{`id="h-q01"`, `id="h-q02"`, `id="h-q01-1"`, `id="h-q-1-x"`} {
		if !strings.Contains(html, tag) {
			t.Errorf("rendered HTML lacks %s:\n%s", tag, html)
		}
	}
}
