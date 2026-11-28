package evaluation_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"learninghub/internal/fsx"
	"learninghub/internal/services/evaluation"
)

func TestCompileCheck(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not on PATH")
	}
	root, err := fsx.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	hdr := "// @hub:answer q01\n// [q01] title\n"
	cases := []struct {
		name, src, status, out string
		timeout                time.Duration
	}{
		{"ok", hdr + "package main\n\nfunc Max[T int | float64](a, b T) T {\n\tif a > b {\n\t\treturn a\n\t}\n\treturn b\n}\n", "ok", "", 0},
		{"syntax", hdr + "package main\n\nfunc f() int { return \"x\" }\n", "failed", "answer.go", 0},
		{"snippet", hdr + "func f() {}\n", "skipped", "snippet", 0},
		{"external", hdr + "package main\n\nimport _ \"github.com/example/nope\"\n", "skipped", "outside the standard library", 0},
		// vet never runs code: an init that never returns is just type-checked.
		{"init loop", hdr + "package main\n\nfunc init() {\n\tfor {\n\t}\n}\n\nfunc main() {}\n", "ok", "", 0},
		{"timeout", hdr + "package main\n", "skipped", "timed out", time.Millisecond},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			timeout := c.timeout
			if timeout == 0 {
				timeout = time.Minute
			}
			got := evaluation.CompileCheck(context.Background(), root, "job-"+strings.ReplaceAll(c.name, " ", "_"), []byte(c.src), timeout)
			if got.Status != c.status || !strings.Contains(got.Output, c.out) {
				t.Fatalf("got %+v, want %s containing %q", got, c.status, c.out)
			}
			if strings.Contains(got.Output, root.BaseDir()) {
				t.Errorf("output leaks the temp dir: %q", got.Output)
			}
		})
	}
	if root.Exists(".app/work/compile/job-ok") || root.Exists(".app/work/compile/job-syntax") || root.Exists(".app/work/compile/job-timeout") {
		t.Error("temp module left behind")
	}
}

// T12.4: the rubric's hidden test runs against the answer, inside the guards.
func TestTestCheck(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not on PATH")
	}
	root, err := fsx.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	t.Setenv("LH_SECRET", "sk-should-not-leak")
	hdr := "// @hub:answer q01\n// [q01] title\n"
	add := hdr + "package main\n\nfunc Add(a, b int) int { return a + b }\n"
	// The AI named the package "solution" and a comment mentions "package main": only the clause is rewritten.
	test := "// Tests for package main helpers.\npackage solution\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif got := Add(2, 3); got != 5 {\n\t\tt.Fatalf(\"Add(2, 3) = %d, want 5\", got)\n\t}\n}\n"
	cases := []struct {
		name, answer, test, status, out string
	}{
		{"pass", add, test, "ok", ""},
		{"fail", hdr + "package main\n\nfunc Add(a, b int) int { return a - b }\n", test, "failed", "Add(2, 3) = -1, want 5"},
		{"syntax", hdr + "package main\n\nfunc Add(a, b int) int { return \"x\" }\n", test, "failed", "answer.go"},
		{"endless loop", hdr + "package main\n\nfunc Add(a, b int) int {\n\tfor {\n\t}\n}\n", test, "failed", "timed out"},
		{"snippet", "func Add(a, b int) int { return a + b }\n", test, "skipped", "snippet"},
		{"test imports os", add, strings.Replace(test, `import "testing"`, "import (\n\t\"os\"\n\t\"testing\"\n)\n\nvar _ = os.Remove", 1), "skipped", `"os"`},
		{"test directive", add, test + "\n//go:noinline\nfunc helper() {}\n", "skipped", "directive"},
		{"test does not parse", add, "package x\nfunc {", "skipped", "does not parse"},
		{"answer imports os/exec", hdr + "package main\n\nimport \"os/exec\"\n\nvar _ = exec.Command\n\nfunc Add(a, b int) int { return a + b }\n", test, "skipped", `"os/exec"`},
		{"answer imports net/http", hdr + "package main\n\nimport _ \"net/http\"\n\nfunc Add(a, b int) int { return a + b }\n", test, "skipped", `"net/http"`},
		// T13.6: the answer may only import computational std packages; anything else is never run.
		{"answer imports os", hdr + "package main\n\nimport \"os\"\n\nfunc Add(a, b int) int { return a + b + len(os.Getenv(\"LH_SECRET\")) }\n", test, "skipped", `"os"`},
		{"answer imports crypto/tls", hdr + "package main\n\nimport _ \"crypto/tls\"\n\nfunc Add(a, b int) int { return a + b }\n", test, "skipped", `"crypto/tls"`},
		{"answer imports log/syslog", hdr + "package main\n\nimport _ \"log/syslog\"\n\nfunc Add(a, b int) int { return a + b }\n", test, "skipped", `"log/syslog"`},
		{"answer imports go/build", hdr + "package main\n\nimport _ \"go/build\"\n\nfunc Add(a, b int) int { return a + b }\n", test, "skipped", `"go/build"`},
		{"answer imports do not parse", hdr + "package main\n\nimport (\"fmt\"\n\nfunc Add(a, b int) int { return a + b }\n", test, "skipped", "do not parse"},
		{"answer strings fmt", hdr + "package main\n\nimport (\n\t\"fmt\"\n\t\"strings\"\n)\n\nfunc Add(a, b int) int { return len(strings.TrimSpace(fmt.Sprint(\" \", a+b))) + a + b - 1 }\n", test, "ok", ""},
		{"answer build ignore", "//go:build ignore\n\n" + add, test, "skipped", "build constraint"},
		{"test without Test", add, "package x\n\nimport \"testing\"\n\nfunc helper(t *testing.T) {}\n", "skipped", "TestXxx"},
		{"test +build", add, "// +build ignore\n\n" + test, "skipped", "directive"},
		// The clause is parsed: a "package x" line inside a block comment is not the package.
		{"package in block comment", "/*\npackage solution\n*/\n" + add, test, "ok", ""},
		// An answer printing forever is capped in memory and in the report.
		{"endless print", hdr + "package main\n\nimport \"fmt\"\n\nfunc Add(a, b int) int {\n\tfor {\n\t\tfmt.Println(\"spam spam spam spam spam spam spam spam\")\n\t}\n}\n", test, "failed", "spam"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := evaluation.TestCheck(context.Background(), root, "job-t-"+strings.ReplaceAll(c.name, " ", "_"), []byte(c.answer), []byte(c.test), 3*time.Second)
			if got.Status != c.status || !strings.Contains(got.Output, c.out) {
				t.Fatalf("got %+v, want %s containing %q", got, c.status, c.out)
			}
			if strings.Contains(got.Output, root.BaseDir()) {
				t.Errorf("output leaks the temp dir: %q", got.Output)
			}
			if len(got.Output) > 4<<10+8 {
				t.Errorf("output not capped: %d bytes", len(got.Output))
			}
		})
	}
	if entries, _ := os.ReadDir(filepath.Join(root.BaseDir(), ".app", "work", "compile")); len(entries) != 0 {
		t.Errorf("temp modules left behind: %v", entries)
	}
}
