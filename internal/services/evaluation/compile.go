package evaluation

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"learninghub/internal/ai/procgroup"
	"learninghub/internal/fsx"
	"learninghub/internal/schema"
)

const (
	compileOK      = "ok"
	compileFailed  = "failed"
	compileSkipped = "skipped"
	maxVetOutput   = 4 << 10
)

var (
	// Imports outside the standard library cannot resolve with GOPROXY=off: the environment, not the answer.
	externalRe = regexp.MustCompile(`GOPROXY=off|no required module provides package|cannot find module providing package`)
)

// goResult is one run of the go tool in a throwaway module; skip is set when nothing could be judged.
type goResult struct {
	text     string // output of the last command, temp dir stripped, capped at maxVetOutput
	err      error
	timedOut bool
	skip     string
}

// runGo writes files into a fresh module under .app/work/compile/<name>, runs `go mod init answer`
// then cmd, and removes the module whatever happens. The environment is closed and minimal: no proxy,
// no cgo, the local toolchain only, and none of the user's variables (an API key must never reach
// code that runs, T12.4). Every process is in a procgroup, so the deadline kills the whole tree.
func runGo(ctx context.Context, root *fsx.Root, name string, files map[string][]byte, timeout time.Duration, cmd ...string) goResult {
	goBin, err := exec.LookPath("go")
	if err != nil {
		return goResult{skip: "go not found on PATH"}
	}
	rel := path.Join(".app/work/compile", name)
	_ = root.RemoveAll(rel) // a leftover from a crash
	defer root.RemoveAll(rel)
	// Temp files go to .tmp inside the module: go ignores a go.mod that sits in the temp root itself.
	if err := root.MkdirAll(path.Join(rel, ".tmp"), 0o755); err != nil {
		return goResult{skip: err.Error()}
	}
	for n, b := range files {
		if err := root.WriteNew(path.Join(rel, n), b, 0o644); err != nil {
			return goResult{skip: err.Error()}
		}
	}
	dir := filepath.Join(root.BaseDir(), filepath.FromSlash(rel))
	tmp := filepath.Join(dir, ".tmp")
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	env := []string{"GOTOOLCHAIN=local", "GOPROXY=off", "GOFLAGS=-mod=mod", "CGO_ENABLED=0", "GOWORK=off", "GOENV=off", "GOTELEMETRY=off",
		"GOCACHE=" + filepath.Join(root.BaseDir(), ".app", "gocache"), "GOPATH=" + filepath.Join(dir, ".gopath"),
		"HOME=" + dir, "USERPROFILE=" + dir, "TMPDIR=" + tmp, "TMP=" + tmp, "TEMP=" + tmp}
	for _, k := range []string{"PATH", "GOROOT", "SystemRoot", "SYSTEMROOT", "WINDIR", "ComSpec", "PATHEXT"} {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	var out capWriter
	run := func(args ...string) error {
		out.Reset()
		c := exec.CommandContext(ctx, goBin, args...)
		c.Dir, c.Env, c.Stdout, c.Stderr = dir, env, &out, &out
		c.WaitDelay = 5 * time.Second
		g, err := procgroup.Start(c)
		if err != nil {
			return err
		}
		defer g.Close()
		return c.Wait()
	}
	// go mod init writes the local toolchain's go version, so generics and newer syntax type-check.
	err = run("mod", "init", "answer")
	if err == nil {
		err = run(cmd...)
	}
	text := strings.ReplaceAll(out.String(), dir+string(filepath.Separator), "")
	text = strings.ReplaceAll(text, dir, ".")
	if len(text) > maxVetOutput {
		text = text[:maxVetOutput] + "\n…"
	}
	return goResult{text: strings.TrimSpace(text), err: err, timedOut: ctx.Err() != nil}
}

// capWriter keeps the first 2*maxVetOutput bytes and drops the rest, so an answer that prints
// forever cannot fill memory before the deadline (only the start is ever shown).
type capWriter struct{ bytes.Buffer }

func (w *capWriter) Write(p []byte) (int, error) {
	if room := 2*maxVetOutput - w.Len(); room > 0 {
		w.Buffer.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

// packageName is the answer's package clause, parsed (a "package x" inside a comment does not count);
// "" for a snippet without one.
func packageName(answer []byte) string {
	f, err := parser.ParseFile(token.NewFileSet(), "answer.go", answer, parser.PackageClauseOnly)
	if err != nil {
		return ""
	}
	return f.Name.Name
}

// judge maps a run to ok / failed / skipped: anything that is the environment's fault (no go, a
// timeout of the tool, an external import) is skipped, so a correct answer is never penalized for it.
func (r goResult) judge(what string, timeout time.Duration) schema.Compile {
	var exit *exec.ExitError
	switch {
	case r.skip != "":
		return schema.Compile{Status: compileSkipped, Output: r.skip}
	case r.timedOut:
		return schema.Compile{Status: compileSkipped, Output: fmt.Sprintf("%s timed out after %s", what, timeout)}
	case r.err == nil:
		return schema.Compile{Status: compileOK}
	case !errors.As(r.err, &exit):
		return schema.Compile{Status: compileSkipped, Output: r.err.Error()}
	case externalRe.MatchString(r.text):
		return schema.Compile{Status: compileSkipped, Output: "imports a module outside the standard library"}
	}
	return schema.Compile{Status: compileFailed, Output: r.text}
}

// CompileCheck type-checks a Go answer with `go vet` in a throwaway module under .app/work/compile
// (T11.4, A13). The code is never run: no go test, no go run.
func CompileCheck(ctx context.Context, root *fsx.Root, name string, answer []byte, timeout time.Duration) schema.Compile {
	if packageName(answer) == "" {
		return schema.Compile{Status: compileSkipped, Output: "no package clause (snippet)"}
	}
	return runGo(ctx, root, name, map[string][]byte{"answer.go": answer}, timeout, "vet", "./...").judge("go vet", timeout)
}

// testImports is all a generated test may import: no file system, network, process or unsafe access,
// so a test the AI wrote (and a prompt injection could have shaped) can only call the answer and compare.
var testImports = []string{"testing", "fmt", "strings", "strconv", "bytes", "errors", "sort", "slices", "maps", "cmp", "iter",
	"math", "math/bits", "math/rand", "math/rand/v2", "reflect", "unicode", "unicode/utf8", "time", "context", "sync", "sync/atomic",
	"container/heap", "container/list", "container/ring", "encoding/json", "regexp", "bufio", "io"}

// allowedAnswerImport reports an import the answer may use when it is run: the user wrote the answer,
// but its starter came from the AI, so only computational std packages are allowed — no os, file system,
// network, process, syscall or unsafe access, and nothing outside the standard library.
func allowedAnswerImport(p string) bool {
	for _, fam := range []string{"math/", "unicode/", "encoding/", "sync/", "container/", "hash/", "text/"} {
		if strings.HasPrefix(p, fam) {
			return true
		}
	}
	return slices.Contains(testImports, p) ||
		slices.Contains([]string{"path", "log", "html", "crypto/sha256", "crypto/sha1", "crypto/sha512", "crypto/md5"}, p)
}

// buildConstraint reports a //go:build or // +build line, which could exclude a file from the build.
func buildConstraint(f *ast.File) bool {
	for _, g := range f.Comments {
		for _, c := range g.List {
			if strings.HasPrefix(c.Text, "//go:build") || strings.HasPrefix(c.Text, "// +build") {
				return true
			}
		}
	}
	return false
}

// hasTest reports a func TestXxx(t *testing.T) in the test file: without one go test passes vacuously.
func hasTest(f *ast.File) bool {
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Recv != nil || !strings.HasPrefix(fn.Name.Name, "Test") || len(fn.Type.Params.List) != 1 {
			continue
		}
		star, ok := fn.Type.Params.List[0].Type.(*ast.StarExpr)
		if !ok {
			continue
		}
		if sel, ok := star.X.(*ast.SelectorExpr); ok && sel.Sel.Name == "T" {
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == "testing" && len(fn.Type.Params.List[0].Names) <= 1 {
				return true
			}
		}
	}
	return false
}

// TestCheck runs the rubric's hidden Go test against the answer with `go test` (T12.4). This is the
// one place the app runs code, only when evaluation.run_tests is on: the generated test is limited to
// testImports, must hold a TestXxx and may carry no //go: or +build line, the answer may only import
// allowedAnswerImport and carry no build constraint, and the
// run has a closed environment (runGo), a deadline and a killed process tree. Anything refused is skipped.
func TestCheck(ctx context.Context, root *fsx.Root, name string, answer, goTest []byte, timeout time.Duration) schema.Compile {
	skip := func(why string) schema.Compile { return schema.Compile{Status: compileSkipped, Output: why} }
	pkg := packageName(answer)
	if pkg == "" {
		return skip("no package clause (snippet)")
	}
	fset := token.NewFileSet()
	test, err := parser.ParseFile(fset, "answer_test.go", goTest, parser.ParseComments)
	if err != nil {
		return skip("the generated test does not parse")
	}
	for _, im := range test.Imports {
		if p, _ := strconv.Unquote(im.Path.Value); !slices.Contains(testImports, p) {
			return skip(fmt.Sprintf("the generated test imports %q, which is not allowed", p))
		}
	}
	for _, g := range test.Comments {
		for _, c := range g.List {
			if strings.HasPrefix(c.Text, "//go:") || strings.HasPrefix(c.Text, "//line ") || strings.HasPrefix(c.Text, "// +build") {
				return skip("the generated test carries a compiler directive")
			}
		}
	}
	if !hasTest(test) {
		return skip("the generated test has no func TestXxx(t *testing.T)")
	}
	// Only the header is parsed: a syntax error in a body is left to go test, which reports it as a failure.
	f, err := parser.ParseFile(fset, "answer.go", answer, parser.ImportsOnly|parser.ParseComments)
	if err != nil {
		return skip("the answer's imports do not parse; it is not run")
	}
	if buildConstraint(f) {
		return skip("the answer carries a build constraint; it is not run")
	}
	for _, im := range f.Imports {
		if p, _ := strconv.Unquote(im.Path.Value); !allowedAnswerImport(p) {
			return skip(fmt.Sprintf("the answer imports %q; it is not run with that import", p))
		}
	}
	// The test is written for the answer's package, whatever the AI called it.
	from, to := fset.Position(test.Package).Offset, fset.Position(test.Name.End()).Offset
	goTest = slices.Concat(goTest[:from], []byte("package "+pkg), goTest[to:])
	// The tool gets extra time to build; the test binary itself stops at -timeout (an endless loop is the answer's fault).
	files := map[string][]byte{"answer.go": answer, "answer_test.go": goTest}
	r := runGo(ctx, root, name, files, timeout+time.Minute, "test", "-count=1", "-vet=off", "-timeout="+timeout.String(), "./...")
	// In package-list mode a passing package prints only its ok line, so the answer cannot forge this marker.
	if r.err == nil && r.skip == "" && !r.timedOut && strings.Contains(r.text, "[no tests to run]") {
		return skip("go test ran no tests")
	}
	return r.judge("go test", timeout+time.Minute)
}
