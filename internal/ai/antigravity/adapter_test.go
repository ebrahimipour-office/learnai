package antigravity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"learninghub/internal/ai"
	"learninghub/internal/fsx"
)

// The test binary doubles as a fake agy: with FAKE_AGY set it plays a scenario and exits.
func TestMain(m *testing.M) {
	if sc := os.Getenv("FAKE_AGY"); sc != "" {
		os.Exit(fakeAgy(sc, os.Args[1:]))
	}
	os.Exit(m.Run())
}

const eligibilityLine = `{"event":"result","result":{"conversation_id":"","status":"ERROR","response":"","error":"Eligibility check failed: Your current account is not eligible for Antigravity, because it is not currently available in your location.","duration_seconds":0,"num_turns":0,"usage":{"input_tokens":0,"output_tokens":0,"thinking_tokens":0,"cache_read_tokens":0,"total_tokens":0},"finished_reason":"error"}}`

func success(extra string) string {
	return `{"event":"result","result":{"conversation_id":"conv-1","status":"SUCCESS",` + extra +
		`,"error":"","duration_seconds":1.5,"usage":{"input_tokens":10,"output_tokens":5,"thinking_tokens":0,"cache_read_tokens":0,"total_tokens":15},"finished_reason":"stop"}}`
}

func fakeAgy(sc string, args []string) int {
	out, errw := os.Stdout, os.Stderr
	if len(args) > 0 {
		switch args[0] {
		case "--version":
			fmt.Fprintln(out, "1.4.12")
			return 0
		case "--help":
			fmt.Fprintln(errw, "  --json-schema  ...\n  --output-format  (text, json, stream-json)")
			return 0
		case "models":
			fmt.Fprintln(out, "Fetching available models...")
			if sc == "ineligible" {
				fmt.Fprintln(out, "Error: Eligibility check failed: not currently available in your location.")
				return 1
			}
			fmt.Fprintln(out, "gemini-3.8-flash-medium\ngemini-3.8-pro-high")
			return 0
		}
	}
	// Contract checks the adapter must honour on every run.
	i := slices.Index(args, "--json-schema")
	if slices.Contains(args, "--dangerously-skip-permissions") || !slices.Contains(args, "stream-json") || i < 0 {
		fmt.Fprintf(errw, "bad args: %q\n", args)
		return 3
	}
	if b, err := os.ReadFile(args[i+1]); err != nil || !json.Valid(b) {
		fmt.Fprintln(errw, "schema file missing or invalid")
		return 3
	}
	if cwd, _ := os.Getwd(); filepath.Dir(args[i+1]) != cwd {
		fmt.Fprintln(errw, "cwd is not WorkDir")
		return 3
	}

	fmt.Fprintln(out, `{"event":"init","session_id":"s"}`)
	switch sc {
	case "ok":
		fmt.Fprintln(out, "not json noise")
		fmt.Fprintln(out, `{"event":"step_update","step":1}`)
		fmt.Fprintln(errw, "debug: Authorization: Bearer topsecret.token")
		fmt.Fprintln(out, success(`"response":"done","structured_output":{"lesson_markdown":"# x"}`))
	case "response_json":
		fmt.Fprintln(out, success(`"response":"{\"lesson_markdown\":\"y\"}"`))
	case "no_structured":
		fmt.Fprintln(out, success(`"response":"I cannot do that"`))
	case "ineligible":
		fmt.Fprintln(out, eligibilityLine)
		fmt.Fprintln(errw, "error: Eligibility check failed")
		return 1
	case "rate_limited":
		fmt.Fprintln(out, `{"event":"result","result":{"status":"ERROR","error":"RESOURCE_EXHAUSTED: quota exceeded"}}`)
		return 1
	case "crash":
		fmt.Fprintln(errw, "panic: runtime error")
		return 2
	case "silent_exit":
		return 0
	case "slow":
		time.Sleep(30 * time.Second)
	}
	return 0
}

func newRun(t *testing.T, sc string, logs *fsx.Root) (*Adapter, ai.Request) {
	t.Helper()
	t.Setenv("FAKE_AGY", sc)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	a := New(Config{Binary: exe, Model: "gemini-3.8-flash-medium", Logs: logs, KillGrace: time.Millisecond})
	return a, ai.Request{
		JobID:        7,
		Task:         ai.TaskLesson,
		Prompt:       "Skill in SKILL.md; inputs in inputs/",
		WorkDir:      t.TempDir(),
		OutputSchema: []byte(`{"type":"object"}`),
		Timeout:      20 * time.Second,
	}
}

func TestRunSuccess(t *testing.T) {
	base := t.TempDir()
	logs, err := fsx.OpenRoot(base)
	if err != nil {
		t.Fatal(err)
	}
	defer logs.Close()
	a, req := newRun(t, "ok", logs)

	events := make(chan ai.Event, 10)
	resp, err := a.Run(context.Background(), req, events)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if string(resp.Structured) != `{"lesson_markdown":"# x"}` || resp.ConversationID != "conv-1" ||
		resp.Usage.TotalTokens != 15 || resp.Text != "done" {
		t.Fatalf("resp = %+v", resp)
	}
	close(events)
	var kinds []string
	for e := range events {
		kinds = append(kinds, e.Kind)
	}
	if !slices.Equal(kinds, []string{"started", "step"}) {
		t.Fatalf("events = %v", kinds)
	}

	// Logs: all five files, secrets masked.
	rel, err := filepath.Rel(base, resp.RawLogDir)
	if err != nil || !strings.HasPrefix(filepath.ToSlash(rel), ".app/logs/jobs/7/") {
		t.Fatalf("RawLogDir = %q", resp.RawLogDir)
	}
	for _, f := range []string{"prompt.txt", "events.ndjson", "response.json", "stderr.txt", "meta.json"} {
		if _, err := os.Stat(filepath.Join(resp.RawLogDir, f)); err != nil {
			t.Errorf("missing log %s: %v", f, err)
		}
	}
	stderr, _ := os.ReadFile(filepath.Join(resp.RawLogDir, "stderr.txt"))
	if strings.Contains(string(stderr), "topsecret") || !strings.Contains(string(stderr), "Bearer ***") {
		t.Errorf("stderr not masked: %q", stderr)
	}
	var meta map[string]any
	b, _ := os.ReadFile(filepath.Join(resp.RawLogDir, "meta.json"))
	if err := json.Unmarshal(b, &meta); err != nil || meta["exit_code"] != float64(0) || meta["error_code"] != "" {
		t.Errorf("meta = %s (%v)", b, err)
	}
}

func TestRunResponseJSONFallback(t *testing.T) {
	a, req := newRun(t, "response_json", nil)
	resp, err := a.Run(context.Background(), req, nil)
	if err != nil || string(resp.Structured) != `{"lesson_markdown":"y"}` {
		t.Fatalf("resp=%+v err=%v", resp, err)
	}
}

func TestRunErrors(t *testing.T) {
	for sc, want := range map[string]error{
		"ineligible":    ai.ErrAuthRequired,
		"rate_limited":  ai.ErrRateLimited,
		"crash":         ai.ErrToolFailed,
		"silent_exit":   ai.ErrToolFailed,
		"no_structured": ai.ErrInvalidOutput,
	} {
		t.Run(sc, func(t *testing.T) {
			a, req := newRun(t, sc, nil)
			_, err := a.Run(context.Background(), req, nil)
			if !errors.Is(err, want) {
				t.Fatalf("want %v, got %v", want, err)
			}
		})
	}
}

func TestRunTimeoutAndCancel(t *testing.T) {
	a, req := newRun(t, "slow", nil)
	req.Timeout = 200 * time.Millisecond
	start := time.Now()
	if _, err := a.Run(context.Background(), req, nil); !errors.Is(err, ai.ErrTimeout) || ai.Code(err) != "timeout" {
		t.Fatalf("timeout: got %v", err)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("timeout took %s", d)
	}

	req.Timeout = 0
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(200*time.Millisecond, cancel)
	if _, err := a.Run(ctx, req, nil); !errors.Is(err, ai.ErrCanceled) {
		t.Fatalf("cancel: got %v", err)
	}
}

func TestRunRejectsBeforeExec(t *testing.T) {
	a, req := newRun(t, "ok", nil)
	a.cfg.Binary = filepath.Join(t.TempDir(), "no-such-agy")
	if _, err := a.Run(context.Background(), req, nil); !errors.Is(err, ai.ErrBinaryNotFound) || ai.Code(err) != "adapter_missing" {
		t.Fatalf("missing binary: %v", err)
	}

	a, req = newRun(t, "ok", nil)
	a.cfg.ExtraArgs = []string{"--dangerously-skip-permissions"}
	if _, err := a.Run(context.Background(), req, nil); !errors.Is(err, ErrForbiddenArg) {
		t.Fatalf("forbidden flag: %v", err)
	}

	req.WorkDir = "relative/dir"
	a.cfg.ExtraArgs = nil
	if _, err := a.Run(context.Background(), req, nil); err == nil {
		t.Fatal("relative WorkDir accepted")
	}
}

func TestProbe(t *testing.T) {
	a, _ := newRun(t, "ok", nil)
	caps, err := a.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if caps.Version != "1.4.12" || !caps.HeadlessJSON || !caps.StructuredOutput || !caps.Authenticated ||
		!slices.Equal(caps.Models, []string{"gemini-3.8-flash-medium", "gemini-3.8-pro-high"}) {
		t.Fatalf("caps = %+v", caps)
	}

	a, _ = newRun(t, "ineligible", nil)
	caps, err = a.Probe(context.Background())
	if err != nil || caps.Authenticated || len(caps.Notes) == 0 || !strings.Contains(caps.Notes[0], "Eligibility") {
		t.Fatalf("ineligible caps = %+v err=%v", caps, err)
	}
}
