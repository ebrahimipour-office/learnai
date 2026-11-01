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
			if !bytes.Equal(got, want) {
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
