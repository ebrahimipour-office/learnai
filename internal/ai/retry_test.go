package ai_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"learninghub/internal/ai"
)

// noter returns {"n":<call>} and records every NoteInvalid call.
type noter struct {
	calls int
	notes []string
}

func (n *noter) Name() string                                   { return "noter" }
func (n *noter) Probe(context.Context) (ai.Capabilities, error) { return ai.Capabilities{}, nil }
func (n *noter) Run(context.Context, ai.Request, chan<- ai.Event) (*ai.Response, error) {
	n.calls++
	return &ai.Response{Structured: json.RawMessage(fmt.Sprintf(`{"n":%d}`, n.calls))}, nil
}
func (n *noter) NoteInvalid(resp *ai.Response, err error) {
	n.notes = append(n.notes, string(resp.Structured)+" "+err.Error())
}

func TestWatchValidNotesEveryRejection(t *testing.T) {
	rejectFirst := func(raw json.RawMessage) error {
		if string(raw) == `{"n":1}` {
			return fmt.Errorf("%w: bad id", ai.ErrInvalidOutput)
		}
		return nil
	}
	a := &noter{}
	if _, err := ai.WatchValid(context.Background(), a, ai.Request{}, nil, rejectFirst); err != nil {
		t.Fatalf("retry should succeed: %v", err)
	}
	// Rejection recorded even though the retry succeeded (B6).
	if len(a.notes) != 1 || a.notes[0] != `{"n":1} ai: invalid output: bad id` {
		t.Fatalf("notes = %q", a.notes)
	}

	a = &noter{}
	rejectAll := func(json.RawMessage) error { return fmt.Errorf("%w: nope", ai.ErrInvalidOutput) }
	if _, err := ai.WatchValid(context.Background(), a, ai.Request{}, nil, rejectAll); !errors.Is(err, ai.ErrInvalidOutput) {
		t.Fatalf("want invalid output, got %v", err)
	}
	if len(a.notes) != 2 {
		t.Fatalf("both attempts must be noted, got %q", a.notes)
	}
}
