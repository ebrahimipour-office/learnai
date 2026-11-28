package ai_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"learninghub/internal/ai"
	"learninghub/internal/ai/fake"
)

func TestWatchStalls(t *testing.T) {
	start := time.Now()
	_, err := ai.Watch(context.Background(), &fake.Adapter{Scenario: fake.Hang}, ai.Request{StallTimeout: 50 * time.Millisecond}, nil)
	if !errors.Is(err, ai.ErrStalled) || ai.Code(err) != "stalled" {
		t.Fatalf("want ErrStalled, got %v", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("stall detected after %s", d)
	}
}

func TestWatchEventsKeepItAlive(t *testing.T) {
	a := &fake.Adapter{Scenario: fake.Slow, Delay: 300 * time.Millisecond, Tick: 10 * time.Millisecond, Output: []byte(`{}`)}
	events := make(chan ai.Event, 100)
	resp, err := ai.Watch(context.Background(), a, ai.Request{StallTimeout: 80 * time.Millisecond}, events)
	if err != nil || string(resp.Structured) != `{}` {
		t.Fatalf("slow but chatty run must succeed: %v", err)
	}
	if len(events) == 0 {
		t.Error("events were not forwarded")
	}
}

func TestWatchCancelIsNotStall(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := ai.Watch(ctx, &fake.Adapter{Scenario: fake.Hang}, ai.Request{StallTimeout: time.Minute}, nil)
	if !errors.Is(err, ai.ErrCanceled) {
		t.Fatalf("want ErrCanceled, got %v", err)
	}
}

// T13.7: a stall that fires just as Run returns a result must not replace it.
func TestWatchLateStallKeepsResult(t *testing.T) {
	resp, err := ai.Watch(context.Background(), lateOK{}, ai.Request{StallTimeout: 20 * time.Millisecond}, nil)
	if err != nil || resp == nil {
		t.Fatalf("want the result, got %v", err)
	}
}

// lateOK ignores ctx, outlives the stall timeout, then succeeds.
type lateOK struct{ ai.Adapter }

func (lateOK) Run(context.Context, ai.Request, chan<- ai.Event) (*ai.Response, error) {
	time.Sleep(100 * time.Millisecond)
	return &ai.Response{}, nil
}
