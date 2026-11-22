package evaluation

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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
	packageRe = regexp.MustCompile(`(?m)^package\s+(\w+)`)
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
	var out bytes.Buffer
	run := func(args ...string) error {
		out.Reset()
		c := exec.CommandContext(ctx, goBin, args...)
		c.Dir, c.Env, c.Stdout, c.Stderr = dir, env, &out, &out
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
	if !packageRe.Match(answer) {
		return schema.Compile{Status: compileSkipped, Output: "no package clause (snippet)"}
	}
	return runGo(ctx, root, name, map[string][]byte{"answer.go": answer}, timeout, "vet", "./...").judge("go vet", timeout)
}

// testImports is all a generated test may import: no file system, network, process or unsafe access,
// so a test the AI wrote (and a prompt injection could have shaped) can only call the answer and compare.
var testImports = []string{"testing", "fmt", "strings", "strconv", "bytes", "errors", "sort", "slices", "maps", "cmp", "iter",
	"math", "math/bits", "math/rand", "math/rand/v2", "reflect", "unicode", "unicode/utf8", "time", "context", "sync", "sync/atomic",
	"container/heap", "container/list", "container/ring", "encoding/json", "regexp", "bufio", "io"}

// deniedAnswerImport reports an import the answer may not use when it is run: the user wrote the answer,
// but its starter came from the AI, and these reach outside the sandbox (processes, network, raw syscalls).
func deniedAnswerImport(p string) bool {
	return slices.Contains([]string{"os/exec", "syscall", "unsafe", "plugin", "C", "runtime/cgo"}, p) ||
		p == "net" || strings.HasPrefix(p, "net/") || strings.HasPrefix(p, "golang.org/x/sys")
}

// TestCheck runs the rubric's hidden Go test against the answer with `go test` (T12.4). This is the
// one place the app runs code, only when evaluation.run_tests is on: the generated test is limited to
// testImports and may carry no //go: directive, the answer may not import deniedAnswerImport, and the
// run has a closed environment (runGo), a deadline and a killed process tree. Anything refused is skipped.
func TestCheck(ctx context.Context, root *fsx.Root, name string, answer, goTest []byte, timeout time.Duration) schema.Compile {
	skip := func(why string) schema.Compile { return schema.Compile{Status: compileSkipped, Output: why} }
	pkg := packageRe.FindSubmatch(answer)
	if pkg == nil {
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
			if strings.HasPrefix(c.Text, "//go:") || strings.HasPrefix(c.Text, "//line ") {
				return skip("the generated test carries a compiler directive")
			}
		}
	}
	// An answer that does not parse is left to go test, which reports the syntax error as a failure.
	if f, err := parser.ParseFile(fset, "answer.go", answer, parser.ImportsOnly); err == nil {
		for _, im := range f.Imports {
			if p, _ := strconv.Unquote(im.Path.Value); deniedAnswerImport(p) {
				return skip(fmt.Sprintf("the answer imports %q; it is not run with that import", p))
			}
		}
	}
	// The test is written for the answer's package, whatever the AI called it.
	from, to := fset.Position(test.Package).Offset, fset.Position(test.Name.End()).Offset
	goTest = slices.Concat(goTest[:from], []byte("package "+string(pkg[1])), goTest[to:])
	// The tool gets extra time to build; the test binary itself stops at -timeout (an endless loop is the answer's fault).
	files := map[string][]byte{"answer.go": answer, "answer_test.go": goTest}
	return runGo(ctx, root, name, files, timeout+time.Minute, "test", "-count=1", "-vet=off", "-timeout="+timeout.String(), "./...").
		judge("go test", timeout+time.Minute)
}
