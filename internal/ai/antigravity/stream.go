package antigravity

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"learninghub/internal/ai"
)

// envelope is one stream-json line. Captured from agy 1.4.x: the key is "event"
// (not "type") and the final payload sits under "result".
type envelope struct {
	Event  string          `json:"event"`
	Result json.RawMessage `json:"result"`
}

type result struct {
	ConversationID   string          `json:"conversation_id"`
	Status           string          `json:"status"`
	Response         string          `json:"response"`
	Error            string          `json:"error"`
	DurationSeconds  float64         `json:"duration_seconds"`
	Usage            ai.Usage        `json:"usage"`
	FinishedReason   string          `json:"finished_reason"`
	StructuredOutput json.RawMessage `json:"structured_output"`
}

// eventKind maps agy event names onto ai.Event kinds; unknown events still count as liveness.
func eventKind(name string) string {
	switch name {
	case "init":
		return "started"
	case "step_update":
		return "step"
	default:
		return "heartbeat"
	}
}

// structured picks the JSON object from a SUCCESS result. Confirmed against agy 1.4.12
// (2026-11-01): it lives in "structured_output"; if missing, fallback extracts bare JSON
// or JSON enclosed in markdown fences/text from "response".
func (r *result) structured() (json.RawMessage, error) {
	if s := bytes.TrimSpace(r.StructuredOutput); len(s) > 0 && !bytes.Equal(s, []byte("null")) && !bytes.Equal(s, []byte("{}")) {
		return s, nil
	}
	if s := strings.TrimSpace(r.Response); s != "" {
		if strings.HasPrefix(s, "{") && json.Valid([]byte(s)) {
			return json.RawMessage(s), nil
		}
		if raw := extractJSON(s); raw != nil {
			return raw, nil
		}
	}
	return nil, fmt.Errorf("%w: no structured output in result", ai.ErrInvalidOutput)
}

// Pattern maps a lower-case substring of agy's error text to a typed error.
// Exact messages are undocumented, so the list is configurable (TDD §6.1).
type Pattern struct {
	Substr string
	Err    error
}

var DefaultPatterns = []Pattern{
	{"authentication required", ai.ErrAuthRequired},
	{"not authenticated", ai.ErrAuthRequired},
	{"please log in", ai.ErrAuthRequired},
	{"eligibility check failed", ai.ErrAuthRequired}, // account-level block, seen 2026-11-01
	{"not eligible", ai.ErrAuthRequired},
	{"rate limit", ai.ErrRateLimited},
	{"resource_exhausted", ai.ErrRateLimited},
	{"quota", ai.ErrRateLimited},
	{"too many requests", ai.ErrRateLimited},
	{"usage limit", ai.ErrRateLimited}, // claude and codex plan limits
	{"not logged in", ai.ErrAuthRequired},
	{"/login", ai.ErrAuthRequired},
	{"invalid api key", ai.ErrAuthRequired},
	{"unauthorized", ai.ErrAuthRequired},
}

func classify(patterns []Pattern, text string) error {
	low := strings.ToLower(text)
	for _, p := range patterns {
		if strings.Contains(low, p.Substr) {
			return fmt.Errorf("%w: %s", p.Err, firstLine(text))
		}
	}
	return fmt.Errorf("%w: %s", ai.ErrToolFailed, firstLine(text))
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}

var secrets = []struct {
	re   *regexp.Regexp
	repl string
}{
	{regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9._~+/=-]+`), "${1}***"},
	{regexp.MustCompile(`ya29\.[A-Za-z0-9._-]+`), "***"},
	{regexp.MustCompile(`AIza[0-9A-Za-z_-]{35}`), "***"},
	{regexp.MustCompile(`sk-[A-Za-z0-9_-]{16,}`), "***"},
	{regexp.MustCompile(`(?i)("?\b(?:access_token|refresh_token|id_token|api_key|apikey|token|secret|password)"?\s*[:=]\s*"?)[^"\s,}]+`), "${1}***"},
}

// mask redacts tokens and keys before anything is written to the job log.
func mask(s string) string {
	for _, p := range secrets {
		s = p.re.ReplaceAllString(s, p.repl)
	}
	return s
}
