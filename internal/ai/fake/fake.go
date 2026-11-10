// Package fake is a scripted ai.Adapter for tests, no network or binary (TDD §6.7).
// shortcut: no auth_required/rate_limited/crash_midway yet; add them with the handlers that test them.
package fake

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"sync"
	"time"

	"learninghub/internal/ai"
)

const (
	OK        = "ok"
	Injection = "injection" // acts like a prompt-injected agent: trashes WorkDir, returns hostile output
	Slow      = "slow"      // sends a "step" event every Tick, returns Output after Delay
	Hang      = "hang"      // sends nothing and never finishes on its own

	InvalidJSON      = "invalid_json"       // every run returns output that fails the schema
	InvalidThenValid = "invalid_then_valid" // first run invalid, later runs return Output
	Missing          = "missing"            // the tool binary is not installed (adapter_missing)
)

// invalid is valid JSON that no output schema accepts.
var invalid = json.RawMessage(`{"unexpected":true}`)

type Adapter struct {
	Scenario string
	Output   json.RawMessage  // returned by OK and Slow
	Delay    time.Duration    // Slow: total run time
	Tick     time.Duration    // Slow: event interval; 0 means 10ms
	Caps     *ai.Capabilities // returned by Probe when set
	ProbeErr error            // returned by Probe when set

	mu      sync.Mutex
	prompts []string // every req.Prompt passed to Run, in order
}

// Calls returns a copy of the prompts seen so far.
func (a *Adapter) Calls() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.prompts...)
}

func (a *Adapter) Name() string { return "fake" }

func (a *Adapter) Probe(context.Context) (ai.Capabilities, error) {
	if a.ProbeErr != nil || a.Caps != nil {
		return *cmp.Or(a.Caps, &ai.Capabilities{}), a.ProbeErr
	}
	return ai.Capabilities{Version: "fake", Authenticated: true, HeadlessJSON: true, StructuredOutput: true}, nil
}

func (a *Adapter) Run(ctx context.Context, req ai.Request, events chan<- ai.Event) (*ai.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("%w: %v", ai.ErrCanceled, err)
	}
	a.mu.Lock()
	a.prompts = append(a.prompts, req.Prompt)
	n := len(a.prompts)
	a.mu.Unlock()
	switch a.Scenario {
	case InvalidJSON:
		return &ai.Response{Structured: invalid}, nil
	case InvalidThenValid:
		if n == 1 {
			return &ai.Response{Structured: invalid}, nil
		}
		return &ai.Response{Structured: a.Output}, nil
	case OK:
		return &ai.Response{Structured: a.Output}, nil
	case Missing:
		return nil, fmt.Errorf("%w: fake-agent not on PATH", ai.ErrBinaryNotFound)
	case Slow, Hang:
		return a.wait(ctx, req, events)
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

// wait honours ctx and req.Timeout like a real adapter: ErrCanceled / ErrTimeout.
func (a *Adapter) wait(ctx context.Context, req ai.Request, events chan<- ai.Event) (*ai.Response, error) {
	var timeout, finished <-chan time.Time
	if req.Timeout > 0 {
		timeout = time.After(req.Timeout)
	}
	var tick <-chan time.Time
	if a.Scenario == Slow {
		finished = time.After(a.Delay)
		t := time.NewTicker(cmp.Or(a.Tick, 10*time.Millisecond))
		defer t.Stop()
		tick = t.C
	}
	for {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("%w: %v", ai.ErrCanceled, ctx.Err())
		case <-timeout:
			return nil, fmt.Errorf("%w: after %s", ai.ErrTimeout, req.Timeout)
		case <-finished:
			return &ai.Response{Structured: a.Output}, nil
		case <-tick:
			if events != nil {
				select {
				case events <- ai.Event{Kind: "step", Info: "working"}:
				case <-ctx.Done():
				}
			}
		}
	}
}
