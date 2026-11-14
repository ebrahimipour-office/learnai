package ai_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

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

// CR-26: a cancel after the first invalid output must not start a second run.
func TestWatchValidNoRetryAfterCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	a := &noter{}
	_, err := ai.WatchValid(ctx, a, ai.Request{}, nil, func(json.RawMessage) error {
		cancel()
		return fmt.Errorf("%w: bad", ai.ErrInvalidOutput)
	})
	if !errors.Is(err, ai.ErrCanceled) || a.calls != 1 {
		t.Fatalf("want ErrCanceled after 1 call, got %v after %d", err, a.calls)
	}
}

// CR-28: every cut point of a Persian string yields valid UTF-8 within the byte cap.
func TestTruncateRuneBoundary(t *testing.T) {
	s := "خطای اعتبارسنجی: فیلد عنوان خالی است"
	for n := 0; n <= len(s)+1; n++ {
		got := ai.Truncate(s, n)
		if !utf8.ValidString(got) {
			t.Fatalf("Truncate(%d) = %q: invalid UTF-8", n, got)
		}
		if body := strings.TrimSuffix(got, "…"); len(body) > n {
			t.Fatalf("Truncate(%d) kept %d bytes", n, len(body))
		}
	}
	if got := ai.Truncate(s, len(s)); got != s {
		t.Fatalf("no-op truncate changed string: %q", got)
	}
}
