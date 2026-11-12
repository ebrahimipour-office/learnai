package roadmap

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite testdata/*.golden.json from current output")

func toJSON(t *testing.T, n *Node) []byte {
	t.Helper()
	b, err := json.MarshalIndent(n, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(b, '\n')
}

// TestGolden: inspect the generated .golden.json by eye after any -update run.
func TestGolden(t *testing.T) {
	files, err := filepath.Glob("testdata/*.md")
	if err != nil || len(files) < 6 {
		t.Fatalf("need >= 6 golden inputs, found %d (%v)", len(files), err)
	}
	for _, f := range files {
		t.Run(filepath.Base(f), func(t *testing.T) {
			src, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			got := toJSON(t, Parse(src))
			golden := strings.TrimSuffix(f, ".md") + ".golden.json"
			if *update {
				if err := os.WriteFile(golden, got, 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("missing golden (run go test ./internal/roadmap -update): %v", err)
			}
			want = bytes.ReplaceAll(want, []byte("\r\n"), []byte("\n"))
			gotNorm := bytes.ReplaceAll(got, []byte("\r\n"), []byte("\n"))
			if !bytes.Equal(gotNorm, want) {
				t.Errorf("output differs from %s\n--- got ---\n%s", golden, got)
			}
		})
	}
}

func TestDeterministic(t *testing.T) {
	src, err := os.ReadFile("testdata/mixed.md")
	if err != nil {
		t.Fatal(err)
	}
	first := toJSON(t, Parse(src))
	for i := 0; i < 100; i++ {
		if got := toJSON(t, Parse(src)); !bytes.Equal(got, first) {
			t.Fatalf("run %d produced different output", i)
		}
	}
}

func TestCRLFAndBOMMatchLF(t *testing.T) {
	src, err := os.ReadFile("testdata/persian.md")
	if err != nil {
		t.Fatal(err)
	}
	crlf := append([]byte("\xEF\xBB\xBF"), bytes.ReplaceAll(src, []byte("\n"), []byte("\r\n"))...)
	if !bytes.Equal(toJSON(t, Parse(crlf)), toJSON(t, Parse(src))) {
		t.Error("CRLF+BOM input parsed differently from LF input")
	}
}

func TestKeepsZWNJ(t *testing.T) {
	root := Parse([]byte("# نوع‌ها\n"))
	if got := root.Children[0].Title; got != "نوع‌ها" {
		t.Errorf("ZWNJ lost: %q", got)
	}
}

func TestSliceChapters(t *testing.T) {
	ch1 := "## Chapter 1\nintro\n```go\n# not a heading\n## nor this\n```\n### Sub\nsub text\n"
	ch2 := "## Chapter 2\nlast\n"
	lf := "# Book\npreface\n" + ch1 + ch2
	for name, src := range map[string]string{
		"lf":       lf,
		"crlf+bom": "\xEF\xBB\xBF" + strings.ReplaceAll(lf, "\n", "\r\n"),
	} {
		t.Run(name, func(t *testing.T) {
			book := Parse([]byte(src)).Children[0]
			if book.Line != 1 || book.EndLine != 12 {
				t.Fatalf("book range %d..%d, want 1..12", book.Line, book.EndLine)
			}
			if len(book.Children) != 2 {
				t.Fatalf("got %d chapters, want 2 (code-block '#' must not split)", len(book.Children))
			}
			for i, want := range []string{ch1, ch2} {
				c := book.Children[i]
				if got := string(Slice([]byte(src), c.Line, c.EndLine)); got != want {
					t.Errorf("chapter %d slice = %q, want %q", i+1, got, want)
				}
			}
			sub := book.Children[0].Children[0]
			if got := string(Slice([]byte(src), sub.Line, sub.EndLine)); got != "### Sub\nsub text\n" {
				t.Errorf("sub slice = %q", got)
			}
		})
	}
}

func TestSliceEdges(t *testing.T) {
	src := []byte("a\nb\nc") // no trailing newline
	for _, tc := range []struct {
		start, end int
		want       string
	}{{1, 1, "a\n"}, {2, 3, "b\nc"}, {3, 99, "c"}, {0, 2, ""}, {3, 2, ""}, {4, 4, ""}} {
		if got := string(Slice(src, tc.start, tc.end)); got != tc.want {
			t.Errorf("Slice(%d,%d) = %q, want %q", tc.start, tc.end, got, tc.want)
		}
	}
	if items := Parse([]byte("- x\n- y\n")).Children; items[1].EndLine != 2 {
		t.Errorf("item EndLine = %d, want its Line 2", items[1].EndLine)
	}
}
