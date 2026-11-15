package ai

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"unicode/utf8"
)

// maxRetryNote keeps the retry prompt well under the -p ceiling (prompt.MaxPrompt).
const maxRetryNote = 1500

// Truncate cuts s to at most maxBytes bytes (plus "…") without splitting a UTF-8
// rune, so Persian text in error messages stays valid (CR-28).
func Truncate(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	n := maxBytes
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}

// InvalidNoter is optionally implemented by adapters that keep per-run logs: WatchValid
// calls it on every validator rejection so the reason sits next to that run's response.
type InvalidNoter interface {
	NoteInvalid(resp *Response, err error)
}

// WatchValid is Watch plus check on the structured output. If the output is invalid
// (ErrInvalidOutput from the adapter or from check), it runs once more with the errors
// appended to the prompt (TDD T4.1), then gives up. Usage is summed over both runs.
func WatchValid(ctx context.Context, a Adapter, req Request, events chan<- Event, check func(json.RawMessage) error) (*Response, error) {
	resp, err := watchChecked(ctx, a, req, events, check, 1)
	if !errors.Is(err, ErrInvalidOutput) {
		return resp, err
	}
	if cerr := CtxErr(ctx); cerr != nil { // CR-26: a cancel after the first run must not start a second
		return resp, cerr
	}
	const (
		prefix         = "\nYour previous reply was rejected by the validator:\n"
		suffix         = "\nReply again with one corrected JSON object that satisfies output.schema.json and SKILL.md.\n"
		maxPromptLimit = 4096 // ceiling for -p prompt (CR-I10)
	)
	avail := maxPromptLimit - len(req.Prompt) - len(prefix) - len(suffix) - len("…")
	noteCap := maxRetryNote
	if avail < noteCap {
		noteCap = avail
	}
	if noteCap < 0 {
		noteCap = 0
	}
	note := Truncate(err.Error(), noteCap)
	req.Prompt += prefix + note + suffix
	first := resp
	resp, err = watchChecked(ctx, a, req, events, check, 2)
	if first != nil && resp != nil {
		resp.Usage = addUsage(first.Usage, resp.Usage)
	} else if resp == nil {
		resp = first
	}
	return resp, err
}

func watchChecked(ctx context.Context, a Adapter, req Request, events chan<- Event, check func(json.RawMessage) error, attempt int) (*Response, error) {
	resp, err := Watch(ctx, a, req, events)
	if err == nil {
		err = check(resp.Structured)
	}
	if errors.Is(err, ErrInvalidOutput) {
		slog.Warn("AI output rejected by validator", "job", req.JobID, "task", req.Task, "attempt", attempt, "error", err)
		if n, ok := a.(InvalidNoter); ok && resp != nil {
			n.NoteInvalid(resp, err)
		}
	}
	return resp, err
}

func addUsage(a, b Usage) Usage {
	return Usage{a.InputTokens + b.InputTokens, a.OutputTokens + b.OutputTokens, a.ThinkingTokens + b.ThinkingTokens,
		a.CacheReadTokens + b.CacheReadTokens, a.TotalTokens + b.TotalTokens}
}
