package antigravity

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"learninghub/internal/ai"
)

const (
	KindAgy    = "antigravity"
	KindClaude = "claude"
	KindCodex  = "codex"
)

var defaultBinary = map[string]string{KindAgy: "agy", KindClaude: "claude", KindCodex: "codex"}

// readTools is all Claude may use: it reads SKILL.md and inputs/ from WorkDir and nothing else.
const readTools = "Read,Glob,Grep"

// Flags the adapter owns or that widen what the tool may do (dashes stripped, value cut at "=").
var (
	claudeForbidden = names("dangerously-skip-permissions", "allow-dangerously-skip-permissions",
		"permission-mode", "add-dir", "tools", "allowedTools", "allowed-tools", "disallowedTools",
		"disallowed-tools", "p", "print", "output-format", "input-format", "json-schema", "model",
		"effort", "c", "continue", "r", "resume", "session-id", "mcp-config", "settings",
		"setting-sources", "plugin-dir", "verbose")
	codexForbidden = names("dangerously-bypass-approvals-and-sandbox", "yolo", "full-auto", "s",
		"sandbox", "add-dir", "C", "cd", "m", "model", "c", "config", "p", "profile", "json",
		"output-schema", "o", "output-last-message", "skip-git-repo-check")
)

func names(n ...string) map[string]bool {
	m := map[string]bool{}
	for _, s := range n {
		m[s] = true
	}
	return m
}

func checkExtra(extra []string, forbidden map[string]bool) error {
	for _, a := range extra {
		name, _, _ := strings.Cut(strings.TrimSpace(a), "=")
		if forbidden[strings.TrimLeft(name, "-")] || strings.Contains(a, "dangerously") {
			return fmt.Errorf("%w: %q", ErrForbiddenArg, a)
		}
	}
	return nil
}

func checkCommon(s argSpec) error {
	if strings.TrimSpace(s.Prompt) == "" {
		return errors.New("antigravity: empty prompt")
	}
	if strings.HasPrefix(s.Model, "-") {
		return fmt.Errorf("antigravity: invalid model %q", s.Model)
	}
	if s.Effort != "" && !efforts[s.Effort] {
		return fmt.Errorf("antigravity: invalid effort %q", s.Effort)
	}
	return nil
}

// buildClaudeArgs: `claude -p` with stream-json, read-only tools and no user hooks/plugins/MCP.
// Model "" leaves Claude's own default. Timeout is enforced by killing the process.
func buildClaudeArgs(s argSpec, schema []byte) ([]string, error) {
	if err := checkExtra(s.Extra, claudeForbidden); err != nil {
		return nil, err
	}
	if err := checkCommon(s); err != nil {
		return nil, err
	}
	args := []string{"-p", s.Prompt, "--output-format", "stream-json", "--verbose",
		"--tools", readTools, "--allowedTools", readTools,
		"--no-session-persistence", "--strict-mcp-config", "--setting-sources", "project"}
	if len(schema) > 0 {
		args = append(args, "--json-schema", string(schema)) // takes the schema text, not a path
	}
	if s.Model != "" {
		args = append(args, "--model", s.Model)
	}
	if s.Effort != "" {
		args = append(args, "--effort", s.Effort)
	}
	return append(args, s.Extra...), nil
}

// buildCodexArgs: `codex exec --json` in a read-only sandbox. No --output-schema: Codex sends it
// in strict mode, which our schemas (optional fields, min/max) don't satisfy; the prompt points at
// output.schema.json and WatchValid validates and retries instead.
func buildCodexArgs(s argSpec) ([]string, error) {
	if err := checkExtra(s.Extra, codexForbidden); err != nil {
		return nil, err
	}
	if err := checkCommon(s); err != nil {
		return nil, err
	}
	args := []string{"exec", "--json", "--skip-git-repo-check", "--sandbox", "read-only"}
	if s.Model != "" {
		args = append(args, "--model", s.Model)
	}
	if s.Effort != "" {
		e := map[string]string{"max": "xhigh"}[s.Effort]
		if e == "" {
			e = s.Effort
		}
		args = append(args, "-c", "model_reasoning_effort="+e)
	}
	args = append(args, s.Extra...)
	return append(args, "--", s.Prompt), nil
}

func probeClaude(caps ai.Capabilities, out func(...string) (string, error)) ai.Capabilities {
	help, _ := out("--help")
	caps.HeadlessJSON = strings.Contains(help, "stream-json")
	caps.StructuredOutput = strings.Contains(help, "--json-schema")
	st, err := out("auth", "status")
	var v struct {
		LoggedIn bool `json:"loggedIn"`
	}
	if json.Unmarshal([]byte(st), &v) == nil && v.LoggedIn {
		caps.Authenticated = true
	} else {
		caps.Notes = append(caps.Notes, firstLine(st+" "+errString(err)))
	}
	return caps
}

func probeCodex(caps ai.Capabilities, out func(...string) (string, error)) ai.Capabilities {
	help, _ := out("exec", "--help")
	caps.HeadlessJSON = strings.Contains(help, "--json")
	caps.StructuredOutput = caps.HeadlessJSON // JSON comes from the reply text, checked by the validator
	st, err := out("login", "status")
	if err == nil {
		caps.Authenticated = true
	} else {
		caps.Notes = append(caps.Notes, firstLine(st+" "+errString(err)))
	}
	return caps
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// claudeDecoder reads `claude -p --output-format stream-json --verbose` (checked with 2.1.319).
type claudeDecoder struct {
	res *result
	raw []byte
}

func (d *claudeDecoder) line(b []byte) string {
	var m struct {
		Type             string          `json:"type"`
		Subtype          string          `json:"subtype"`
		IsError          bool            `json:"is_error"`
		Result           string          `json:"result"`
		SessionID        string          `json:"session_id"`
		StructuredOutput json.RawMessage `json:"structured_output"`
		Usage            struct {
			Input     int64 `json:"input_tokens"`
			Output    int64 `json:"output_tokens"`
			CacheRead int64 `json:"cache_read_input_tokens"`
			Details   struct {
				Thinking int64 `json:"thinking_tokens"`
			} `json:"output_tokens_details"`
		} `json:"usage"`
	}
	if json.Unmarshal(b, &m) != nil {
		return ""
	}
	switch m.Type {
	case "system":
		if m.Subtype == "init" {
			return "init"
		}
		return "heartbeat"
	case "assistant":
		return "step_update"
	case "result":
		r := &result{ConversationID: m.SessionID, Response: m.Result, StructuredOutput: m.StructuredOutput, Status: "FAILED",
			Usage: ai.Usage{InputTokens: m.Usage.Input, OutputTokens: m.Usage.Output, ThinkingTokens: m.Usage.Details.Thinking,
				CacheReadTokens: m.Usage.CacheRead, TotalTokens: m.Usage.Input + m.Usage.Output}}
		if !m.IsError && m.Subtype == "success" {
			r.Status = "SUCCESS"
		} else {
			r.Error = strings.TrimSpace(m.Subtype + ": " + m.Result)
		}
		d.res, d.raw = r, b
		return ""
	}
	return "heartbeat"
}

func (d *claudeDecoder) final() (*result, []byte) { return d.res, d.raw }

// codexDecoder reads `codex exec --json` events; the last agent_message is the answer.
// shortcut: event shapes follow the Codex docs, not a captured run; recheck when codex is installed.
type codexDecoder struct {
	res result
	ok  bool
}

func (d *codexDecoder) line(b []byte) string {
	var m struct {
		Type     string `json:"type"`
		ThreadID string `json:"thread_id"`
		Message  string `json:"message"`
		Item     struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"item"`
		Usage struct {
			Input     int64 `json:"input_tokens"`
			CacheRead int64 `json:"cached_input_tokens"`
			Output    int64 `json:"output_tokens"`
			Reasoning int64 `json:"reasoning_output_tokens"`
		} `json:"usage"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(b, &m) != nil || m.Type == "" {
		return ""
	}
	d.ok = true
	switch m.Type {
	case "thread.started":
		d.res.ConversationID = m.ThreadID
		return "init"
	case "item.completed":
		if m.Item.Type == "agent_message" {
			d.res.Response = m.Item.Text
		}
		return "step_update"
	case "turn.completed":
		d.res.Status = "SUCCESS"
		d.res.Usage = ai.Usage{InputTokens: m.Usage.Input, OutputTokens: m.Usage.Output, ThinkingTokens: m.Usage.Reasoning,
			CacheReadTokens: m.Usage.CacheRead, TotalTokens: m.Usage.Input + m.Usage.Output}
		d.res.StructuredOutput = extractJSON(d.res.Response)
	case "turn.failed":
		d.res.Status, d.res.Error = "FAILED", m.Error.Message
	case "error":
		d.res.Error = m.Message
	}
	return m.Type
}

func (d *codexDecoder) final() (*result, []byte) {
	if !d.ok {
		return nil, nil
	}
	raw, _ := json.Marshal(d.res)
	return &d.res, raw
}

// extractJSON takes the object from a reply that may wrap it in a ``` fence or a sentence; nil if none.
func extractJSON(s string) json.RawMessage {
	i, j := strings.IndexByte(s, '{'), strings.LastIndexByte(s, '}')
	if i < 0 || j < i {
		return nil
	}
	b := []byte(s[i : j+1])
	if !json.Valid(b) {
		return nil
	}
	return bytes.TrimSpace(b)
}
