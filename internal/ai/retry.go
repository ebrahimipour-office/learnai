package ai

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
)

// maxRetryNote keeps the retry prompt well under the -p ceiling (prompt.MaxPrompt).
const maxRetryNote = 1500

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
	note := err.Error()
	if len(note) > maxRetryNote {
		note = note[:maxRetryNote] + "…"
	}
	req.Prompt += "\nYour previous reply was rejected by the validator:\n" + note +
		"\nReply again with one corrected JSON object that satisfies output.schema.json and SKILL.md.\n"
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
