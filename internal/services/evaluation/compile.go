package evaluation

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
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
	packageRe = regexp.MustCompile(`(?m)^package\s+\w+`)
	// Imports outside the standard library cannot resolve with GOPROXY=off: the environment, not the answer.
	externalRe = regexp.MustCompile(`GOPROXY=off|no required module provides package|cannot find module providing package`)
)

// CompileCheck type-checks a Go answer with `go vet` in a throwaway module under .app/work/compile
// (T11.4, A13). The code is never run: no go test, no go run. Anything that is the environment's
// fault rather than the answer's (no go binary, a snippet without package, an external import,
// a timeout) is skipped, so a correct answer is never penalized for it.
func CompileCheck(ctx context.Context, root *fsx.Root, name string, answer []byte, timeout time.Duration) schema.Compile {
	goBin, err := exec.LookPath("go")
	if err != nil {
		return schema.Compile{Status: compileSkipped, Output: "go not found on PATH"}
	}
	if !packageRe.Match(answer) {
		return schema.Compile{Status: compileSkipped, Output: "no package clause (snippet)"}
	}
	rel := path.Join(".app/work/compile", name)
	_ = root.RemoveAll(rel) // a leftover from a crash
	defer root.RemoveAll(rel)
	if err := root.MkdirAll(rel, 0o755); err != nil {
		return schema.Compile{Status: compileSkipped, Output: err.Error()}
	}
	if err := root.WriteNew(path.Join(rel, "answer.go"), answer, 0o644); err != nil {
		return schema.Compile{Status: compileSkipped, Output: err.Error()}
	}
	dir := filepath.Join(root.BaseDir(), filepath.FromSlash(rel))

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	env := append(os.Environ(), "GOTOOLCHAIN=local", "GOPROXY=off", "GOFLAGS=-mod=mod", "CGO_ENABLED=0", "GOWORK=off",
		"GOENV=off", "GOCACHE="+filepath.Join(root.BaseDir(), ".app", "gocache"))
	var out bytes.Buffer
	run := func(args ...string) error {
		cmd := exec.CommandContext(ctx, goBin, args...)
		cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = dir, env, &out, &out
		g, err := procgroup.Start(cmd)
		if err != nil {
			return err
		}
		defer g.Close()
		return cmd.Wait()
	}
	// go mod init writes the local toolchain's go version, so generics and newer syntax type-check.
	err = run("mod", "init", "answer")
	if err == nil {
		out.Reset()
		err = run("vet", "./...")
	}
	text := strings.ReplaceAll(out.String(), dir+string(filepath.Separator), "")
	text = strings.ReplaceAll(text, dir, ".")
	if len(text) > maxVetOutput {
		text = text[:maxVetOutput] + "\n…"
	}
	var exit *exec.ExitError
	switch {
	case ctx.Err() != nil:
		return schema.Compile{Status: compileSkipped, Output: fmt.Sprintf("go vet timed out after %s", timeout)}
	case err == nil:
		return schema.Compile{Status: compileOK}
	case !errors.As(err, &exit):
		return schema.Compile{Status: compileSkipped, Output: err.Error()}
	case externalRe.MatchString(text):
		return schema.Compile{Status: compileSkipped, Output: "imports a module outside the standard library"}
	}
	return schema.Compile{Status: compileFailed, Output: strings.TrimSpace(text)}
}
