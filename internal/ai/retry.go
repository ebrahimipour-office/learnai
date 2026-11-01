package ai

import (
	"context"
	"encoding/json"
	"errors"
)

// maxRetryNote keeps the retry prompt well under the -p ceiling (prompt.MaxPrompt).
const maxRetryNote = 1500

// WatchValid is Watch plus check on the structured output. If the output is invalid
// (ErrInvalidOutput from the adapter or from check), it runs once more with the errors
// appended to the prompt (TDD T4.1), then gives up. Usage is summed over both runs.
func WatchValid(ctx context.Context, a Adapter, req Request, events chan<- Event, check func(json.RawMessage) error) (*Response, error) {
	resp, err := watchChecked(ctx, a, req, events, check)
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
	resp, err = watchChecked(ctx, a, req, events, check)
	if first != nil && resp != nil {
		resp.Usage = addUsage(first.Usage, resp.Usage)
	} else if resp == nil {
		resp = first
	}
	return resp, err
}

func watchChecked(ctx context.Context, a Adapter, req Request, events chan<- Event, check func(json.RawMessage) error) (*Response, error) {
	resp, err := Watch(ctx, a, req, events)
	if err == nil {
		err = check(resp.Structured)
	}
	return resp, err
}

func addUsage(a, b Usage) Usage {
	return Usage{a.InputTokens + b.InputTokens, a.OutputTokens + b.OutputTokens, a.ThinkingTokens + b.ThinkingTokens,
		a.CacheReadTokens + b.CacheReadTokens, a.TotalTokens + b.TotalTokens}
}
