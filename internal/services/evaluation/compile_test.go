package evaluation_test

import (
	"context"
	"os/exec"
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
