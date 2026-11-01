// Package antigravity runs the agy CLI headless with stream-json output (TDD §6.2).
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
	"learninghub/internal/fsx"
)

type Config struct {
	Binary    string
	Model     string
	Effort    string
	Sandbox   bool
	ExtraArgs []string
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
	cfg.Binary = cmp.Or(cfg.Binary, "agy")
	cfg.KillGrace = cmp.Or(cfg.KillGrace, 30*time.Second)
	if cfg.Patterns == nil {
		cfg.Patterns = DefaultPatterns
	}
	return &Adapter{cfg: cfg}
}

func (a *Adapter) Name() string { return "antigravity" }

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

	help, _ := out("--help") // flag usage may exit non-zero; only the text matters
	caps.HeadlessJSON = strings.Contains(help, "stream-json")
	caps.StructuredOutput = strings.Contains(help, "--json-schema")

	m, err := out("models")
	if perr := classify(a.cfg.Patterns, m); err != nil || !errors.Is(perr, ai.ErrToolFailed) {
		caps.Notes = append(caps.Notes, firstLine(lastLine(m)))
		return caps, nil
	}
	caps.Authenticated = true
	// shortcut: `agy models` output format unseen (location-blocked); one model per line assumed.
	for _, l := range strings.Split(m, "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasSuffix(l, "...") {
			caps.Models = append(caps.Models, l)
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
	args, err := buildArgs(argSpec{
		Prompt:    req.Prompt,
		WorkDir:   req.WorkDir,
		HasSchema: len(req.OutputSchema) > 0,
		Model:     cmp.Or(req.Model, a.cfg.Model),
		Effort:    cmp.Or(req.Effort, a.cfg.Effort),
		Timeout:   req.Timeout,
		Sandbox:   a.cfg.Sandbox,
		Extra:     a.cfg.ExtraArgs,
	})
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

	// shortcut: CommandContext kills only the direct child; T3.2 procgroup swaps in a tree kill.
	cmd := exec.CommandContext(runCtx, bin, args...)
	cmd.Dir = req.WorkDir
	cmd.WaitDelay = 5 * time.Second
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ai.ErrToolFailed, err)
	}

	start := time.Now()
	if err := cmd.Start(); err != nil {
		log.finish(args, nil, stderr.String(), -1, 0, err)
		return nil, fmt.Errorf("%w: start: %v", ai.ErrToolFailed, err)
	}
	res, rawRes := readStream(runCtx, stdout, events, log)
	waitErr := cmd.Wait()
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

func writeSchema(workDir string, schema []byte) error {
	r, err := fsx.OpenRoot(workDir)
	if err != nil {
		return err
	}
	defer r.Close()
	return r.WriteAtomic(schemaFile, schema, 0o644)
}

// readStream consumes agy's NDJSON stdout, forwarding progress and keeping the final result.
func readStream(ctx context.Context, r io.Reader, events chan<- ai.Event, log *jobLog) (*result, []byte) {
	var res *result
	var raw []byte
	br := bufio.NewReader(r)
	for {
		line, err := br.ReadBytes('\n')
		if t := bytes.TrimSpace(line); len(t) > 0 {
			log.event(t)
			var env envelope
			if json.Unmarshal(t, &env) == nil && env.Event != "" {
				if env.Event == "result" {
					var rr result
					if json.Unmarshal(env.Result, &rr) == nil {
						res, raw = &rr, env.Result
					}
				} else if events != nil {
					select {
					case events <- ai.Event{Kind: eventKind(env.Event), Info: env.Event}:
					case <-ctx.Done():
					}
				}
			}
		}
		if err != nil {
			return res, raw
		}
	}
}

func (a *Adapter) outcome(ctx, runCtx context.Context, req ai.Request, res *result, waitErr error, stderr string, dur time.Duration) error {
	if err := ctx.Err(); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("%w: %v", ai.ErrTimeout, err)
		}
		return fmt.Errorf("%w: %v", ai.ErrCanceled, err)
	}
	if runCtx.Err() != nil {
		return fmt.Errorf("%w: killed after %s", ai.ErrTimeout, req.Timeout+a.cfg.KillGrace)
	}
	if waitErr == nil && res != nil && res.Status == "SUCCESS" {
		return nil
	}
	if req.Timeout > 0 && dur >= req.Timeout {
		return fmt.Errorf("%w: agy stopped after %s", ai.ErrTimeout, dur.Round(time.Second))
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
