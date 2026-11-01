// Package fake is a scripted ai.Adapter for tests, no network or binary (TDD §6.7).
// shortcut: only "ok" and "injection" so far; slow/invalid_json/auth_required/... land with T3.4/T3.5.
package fake

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"

	"learninghub/internal/ai"
)

const (
	OK        = "ok"
	Injection = "injection" // acts like a prompt-injected agent: trashes WorkDir, returns hostile output
)

type Adapter struct {
	Scenario string
	Output   json.RawMessage // returned by OK
}

func (a *Adapter) Name() string { return "fake" }

func (a *Adapter) Probe(context.Context) (ai.Capabilities, error) {
	return ai.Capabilities{Version: "fake", Authenticated: true, HeadlessJSON: true, StructuredOutput: true}, nil
}

func (a *Adapter) Run(ctx context.Context, req ai.Request, events chan<- ai.Event) (*ai.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("%w: %v", ai.ErrCanceled, err)
	}
	switch a.Scenario {
	case OK:
		return &ai.Response{Structured: a.Output}, nil
	case Injection:
		if err := trash(req.WorkDir); err != nil {
			return nil, fmt.Errorf("%w: %v", ai.ErrToolFailed, err)
		}
		return &ai.Response{Structured: json.RawMessage(
			`{"path":"../../x","markdown":"<script>alert(1)</script><img src=x onerror=alert(1)>"}`)}, nil
	}
	return nil, fmt.Errorf("%w: unknown scenario %q", ai.ErrToolFailed, a.Scenario)
}

// trash overwrites every file in workDir and drops a new one, the worst an agent confined to its cwd can do.
func trash(workDir string) error {
	root, err := os.OpenRoot(workDir)
	if err != nil {
		return err
	}
	defer root.Close()
	err = fs.WalkDir(root.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		return root.WriteFile(p, []byte("pwned"), 0o644)
	})
	if err != nil {
		return err
	}
	return root.WriteFile("evil.sh", []byte("rm -rf ~"), 0o755)
}
