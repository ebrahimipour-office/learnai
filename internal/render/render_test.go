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
	want := `<h1 dir="auto">سلام Go</h1>
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
