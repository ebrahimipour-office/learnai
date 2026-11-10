// Package antigravity runs an AI CLI headless with JSON-lines output (TDD §6.2): agy, and
// with Kind set, Claude Code or Codex (dialects.go). Logging, kill and error mapping are shared.
package antigravity

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"learninghub/internal/ai"
	"learninghub/internal/ai/procgroup"
	"learninghub/internal/fsx"
)

type Config struct {
	Kind    string // KindAgy (default), KindClaude or KindCodex
	Binary  string
	Model   string
	Effort  string
	Sandbox bool
	// SkipPermissions adds --dangerously-skip-permissions (agy only; needs Sandbox).
	SkipPermissions bool
	ExtraArgs       []string
	// Logs is the base-dir root; job logs go to .app/logs/jobs/<id>/. nil disables logging.
	Logs *fsx.Root
	// KillGrace is how long past Request.Timeout we wait for agy's own --print-timeout
	// before killing it. 0 means 30s.
	KillGrace time.Duration
	Patterns  []Pattern // nil means DefaultPatterns
}

type Adapter struct {
	cfg     Config
	version atomic.Pointer[string] // last version seen by Probe, for meta.json
}

var _ ai.Adapter = (*Adapter)(nil)

func New(cfg Config) *Adapter {
	cfg.Kind = cmp.Or(cfg.Kind, KindAgy)
	cfg.Binary = cmp.Or(cfg.Binary, defaultBinary[cfg.Kind])
	cfg.KillGrace = cmp.Or(cfg.KillGrace, 30*time.Second)
	if cfg.Patterns == nil {
		cfg.Patterns = DefaultPatterns
	}
	return &Adapter{cfg: cfg}
}

func (a *Adapter) Name() string { return a.cfg.Kind }

// Model is the configured model ("" = the tool's default); jobs record it (ai.Modeler).
func (a *Adapter) Model() string { return a.cfg.Model }

func (a *Adapter) lookPath() (string, error) {
	bin, err := exec.LookPath(a.cfg.Binary)
	if err != nil {
		return "", fmt.Errorf("%w: %s", ai.ErrBinaryNotFound, a.cfg.Binary)
	}
	return bin, nil
}

// Probe reports version, structured-output support and whether the account can run models.
func (a *Adapter) Probe(ctx context.Context) (ai.Capabilities, error) {
	var caps ai.Capabilities
	bin, err := a.lookPath()
	if err != nil {
		return caps, err
	}
	out := func(args ...string) (string, error) {
		c, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		b, err := exec.CommandContext(c, bin, args...).CombinedOutput()
		return string(b), err
	}

	v, err := out("--version")
	if err != nil {
		return caps, fmt.Errorf("%w: --version: %v", ai.ErrToolFailed, err)
	}
	caps.Version = strings.TrimSpace(v)
	a.version.Store(&caps.Version)
	switch a.cfg.Kind {
	case KindClaude:
		return probeClaude(caps, out), nil
	case KindCodex:
		return probeCodex(caps, out), nil
	}

	help, _ := out("--help") // flag usage may exit non-zero; only the text matters
	caps.HeadlessJSON = strings.Contains(help, "stream-json")
	caps.StructuredOutput = strings.Contains(help, "--json-schema")

	m, err := out("models")
	if perr := classify(a.cfg.Patterns, m); err != nil || !errors.Is(perr, ai.ErrToolFailed) {
		caps.Notes = append(caps.Notes, firstLine(lastLine(m)))
		return caps, nil
	}
	caps.Authenticated = true
	// `agy models` (1.4.12) prints "<id>\t<label>" per line; the id is what --model takes.
	for _, l := range strings.Split(m, "\n") {
		if id, _, _ := strings.Cut(strings.TrimSpace(l), "\t"); id != "" && !strings.HasSuffix(id, "...") {
			caps.Models = append(caps.Models, id)
		}
	}
	return caps, nil
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	return s[strings.LastIndexByte(s, '\n')+1:]
}

// Run executes one prompt. The returned Response may be non-nil alongside an error
// (it then carries Duration/RawLogDir/Text for diagnostics).
func (a *Adapter) Run(ctx context.Context, req ai.Request, events chan<- ai.Event) (*ai.Response, error) {
	if !filepath.IsAbs(req.WorkDir) {
		return nil, fmt.Errorf("antigravity: WorkDir must be absolute: %q", req.WorkDir)
	}
	spec := argSpec{
		Prompt:          req.Prompt,
		WorkDir:         req.WorkDir,
		HasSchema:       len(req.OutputSchema) > 0,
		Model:           cmp.Or(req.Model, a.cfg.Model),
		Effort:          cmp.Or(req.Effort, a.cfg.Effort),
		Timeout:         req.Timeout,
		Sandbox:         a.cfg.Sandbox,
		SkipPermissions: a.cfg.SkipPermissions,
		Extra:           a.cfg.ExtraArgs,
	}
	var args []string
	var err error
	dec := decoder(&agyDecoder{})
	switch a.cfg.Kind {
	case KindClaude:
		args, err = buildClaudeArgs(spec, req.OutputSchema)
		dec = &claudeDecoder{}
	case KindCodex:
		args, err = buildCodexArgs(spec)
		dec = &codexDecoder{}
	default:
		args, err = buildArgs(spec)
	}
	if err != nil {
		return nil, err
	}
	bin, err := a.lookPath()
	if err != nil {
		return nil, err
	}
	if len(req.OutputSchema) > 0 {
		if err := writeSchema(req.WorkDir, req.OutputSchema); err != nil {
			return nil, err
		}
	}
	log, err := a.openLog(req)
	if err != nil {
		return nil, err
	}

	runCtx, cancel := ctx, context.CancelFunc(func() {})
	if req.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, req.Timeout+a.cfg.KillGrace)
	}
	defer cancel()

	cmd := exec.CommandContext(runCtx, bin, args...)
	cmd.Dir = req.WorkDir
	cmd.WaitDelay = waitDelay
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	// Own pipe, not StdoutPipe: Wait must return when the leader exits even if a helper still holds stdout.
	stdout, stdoutW, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ai.ErrToolFailed, err)
	}
	defer stdout.Close()
	cmd.Stdout = stdoutW

	start := time.Now()
	group, err := procgroup.Start(cmd) // cancel/timeout kills agy's whole process tree
	stdoutW.Close()
	if err != nil {
		log.finish(args, nil, stderr.String(), -1, 0, err)
		return nil, fmt.Errorf("%w: start: %v", ai.ErrToolFailed, err)
	}
	var res *result
	var rawRes []byte
	read := make(chan struct{})
	go func() {
		defer close(read)
		res, rawRes = readStream(runCtx, stdout, events, log, dec)
	}()
	waitErr := cmd.Wait()
	group.Close() // kills helpers left in the group, so the reader normally sees EOF at once
	select {
	case <-read:
	case <-time.After(waitDelay):
		// A helper that left the group (setsid) still holds stdout: stop reading, keep what arrived.
		// shortcut: on windows Close does not interrupt a blocked pipe read; the Job Object kill above covers it.
		stdout.Close()
		<-read
	}
	dur := time.Since(start)

	resp := &ai.Response{Duration: dur, RawLogDir: log.absDir()}
	if res != nil {
		resp.Text, resp.ConversationID, resp.Usage = res.Response, res.ConversationID, res.Usage
	}
	err = a.outcome(ctx, runCtx, req, res, waitErr, stderr.String(), dur)
	if err == nil {
		resp.Structured, err = res.structured()
	}
	log.finish(args, rawRes, stderr.String(), cmd.ProcessState.ExitCode(), dur, err)
	return resp, err
}

// NoteInvalid writes validation_error.txt into the run's log dir (best-effort, ai.InvalidNoter).
func (a *Adapter) NoteInvalid(resp *ai.Response, err error) {
	if a.cfg.Logs == nil || resp.RawLogDir == "" {
		return
	}
	rel, rerr := filepath.Rel(a.cfg.Logs.BaseDir(), resp.RawLogDir)
	if rerr != nil {
		return
	}
	_ = a.cfg.Logs.WriteNew(filepath.ToSlash(rel)+"/validation_error.txt", []byte(mask(err.Error())+"\n"), 0o644)
}

func writeSchema(workDir string, schema []byte) error {
	r, err := fsx.OpenRoot(workDir)
	if err != nil {
		return err
	}
	defer r.Close()
	return r.WriteAtomic(schemaFile, schema, 0o644)
}

// decoder turns the CLI's JSON-lines stdout into progress events and one final result.
type decoder interface {
	// line returns the event name to report ("" for none) for one non-empty stdout line.
	line(b []byte) string
	final() (*result, []byte)
}

// agyDecoder: the key is "event" (not "type") and the final payload sits under "result" (agy 1.4.x).
type agyDecoder struct {
	res *result
	raw []byte
}

func (d *agyDecoder) line(b []byte) string {
	var env envelope
	if json.Unmarshal(b, &env) != nil || env.Event == "" {
		return ""
	}
	if env.Event == "result" {
		var rr result
		if json.Unmarshal(env.Result, &rr) == nil {
			d.res, d.raw = &rr, env.Result
		}
		return ""
	}
	return env.Event
}

func (d *agyDecoder) final() (*result, []byte) { return d.res, d.raw }

// waitDelay bounds how long Run keeps reading stdout after the CLI itself has exited.
var waitDelay = 5 * time.Second

// readStream consumes the CLI's stdout, forwarding progress and keeping the final result.
func readStream(ctx context.Context, r io.Reader, events chan<- ai.Event, log *jobLog, dec decoder) (*result, []byte) {
	br := bufio.NewReader(r)
	for {
		line, err := br.ReadBytes('\n')
		if t := bytes.TrimSpace(line); len(t) > 0 {
			log.event(t)
			if ev := dec.line(t); ev != "" && events != nil {
				select {
				case events <- ai.Event{Kind: eventKind(ev), Info: ev}:
				case <-ctx.Done():
				}
			}
		}
		if err != nil {
			return dec.final()
		}
	}
}

func (a *Adapter) outcome(ctx, runCtx context.Context, req ai.Request, res *result, waitErr error, stderr string, dur time.Duration) error {
	// A result that already arrived from a leader that exited 0 wins over a late cancel/timeout.
	if waitErr == nil && res != nil && res.Status == "SUCCESS" {
		return nil
	}
	if err := ctx.Err(); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("%w: %v", ai.ErrTimeout, err)
		}
		return fmt.Errorf("%w: %v", ai.ErrCanceled, err)
	}
	if runCtx.Err() != nil {
		return fmt.Errorf("%w: killed after %s", ai.ErrTimeout, req.Timeout+a.cfg.KillGrace)
	}
	if req.Timeout > 0 && dur >= req.Timeout {
		return fmt.Errorf("%w: %s stopped after %s", ai.ErrTimeout, a.Name(), dur.Round(time.Second))
	}
	text := stderr
	if res != nil && res.Error != "" {
		text = res.Error + "\n" + stderr
	}
	if strings.TrimSpace(text) == "" {
		text = "no result event"
		if waitErr != nil {
			text = waitErr.Error()
		} else if res != nil {
			text = "status " + res.Status
		}
	}
	return classify(a.cfg.Patterns, text)
}

// jobLog writes prompt/events/response/stderr/meta under .app/logs/jobs/<id>/<run>/.
// All methods are no-ops on a nil receiver (logging disabled).
type jobLog struct {
	root   *fsx.Root
	dir    string
	req    ai.Request
	ver    string
	adName string
	events *os.File
}

func (a *Adapter) openLog(req ai.Request) (*jobLog, error) {
	if a.cfg.Logs == nil {
		return nil, nil
	}
	// One dir per Run: a job may call Run more than once (invalid_output retry, parallel evaluate).
	dir := path.Join(".app/logs/jobs", strconv.FormatInt(req.JobID, 10),
		time.Now().UTC().Format("20060102T150405.000000000")+"-"+string(req.Task))
	l := &jobLog{root: a.cfg.Logs, dir: dir, req: req, adName: a.Name()}
	if v := a.version.Load(); v != nil {
		l.ver = *v
	}
	if err := l.root.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("antigravity: log dir: %w", err)
	}
	if err := l.root.WriteNew(dir+"/prompt.txt", []byte(mask(req.Prompt)), 0o644); err != nil {
		return nil, fmt.Errorf("antigravity: log prompt: %w", err)
	}
	f, err := l.root.OpenFile(dir+"/events.ndjson", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return nil, fmt.Errorf("antigravity: log events: %w", err)
	}
	l.events = f
	return l, nil
}

func (l *jobLog) absDir() string {
	if l == nil {
		return ""
	}
	return filepath.Join(l.root.BaseDir(), filepath.FromSlash(l.dir))
}

func (l *jobLog) event(line []byte) {
	if l == nil {
		return
	}
	_, _ = l.events.WriteString(mask(string(line)) + "\n")
}

// finish is best-effort: a log write failure must not hide the run's real outcome.
func (l *jobLog) finish(args []string, rawRes []byte, stderr string, exit int, dur time.Duration, runErr error) {
	if l == nil {
		return
	}
	_ = l.events.Close()
	_ = l.root.WriteNew(l.dir+"/stderr.txt", []byte(mask(stderr)), 0o644)
	if rawRes != nil {
		_ = l.root.WriteNew(l.dir+"/response.json", []byte(mask(string(rawRes))), 0o644)
	}
	masked := make([]string, len(args))
	for i, s := range args {
		masked[i] = mask(s)
	}
	meta := map[string]any{
		"adapter":     l.adName,
		"cli_version": l.ver,
		"job_id":      l.req.JobID,
		"task":        l.req.Task,
		"skill":       l.req.Skill.Name + "@" + l.req.Skill.Version,
		"input_files": l.req.InputFiles,
		"args":        masked,
		"exit_code":   exit,
		"duration_ms": dur.Milliseconds(),
		"error_code":  ai.Code(runErr),
	}
	if runErr != nil {
		meta["error"] = mask(runErr.Error())
	}
	b, _ := json.MarshalIndent(meta, "", "  ")
	_ = l.root.WriteNew(l.dir+"/meta.json", b, 0o644)
}
