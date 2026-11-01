package ai

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Watch runs a.Run under the stall watchdog (TDD §6.5): if no event arrives for req.StallTimeout,
// the run is cancelled (killing the process tree) and ErrStalled is returned. Events are still
// forwarded to events (may be nil). StallTimeout <= 0 disables the watchdog.
func Watch(ctx context.Context, a Adapter, req Request, events chan<- Event) (*Response, error) {
	if req.StallTimeout <= 0 {
		return a.Run(ctx, req, events)
	}
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	stall := time.AfterFunc(req.StallTimeout, func() { cancel(ErrStalled) })
	defer stall.Stop()

	in := make(chan Event)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range in {
			stall.Reset(req.StallTimeout)
			if events != nil {
				select {
				case events <- ev:
				case <-ctx.Done():
				}
			}
		}
	}()
	resp, err := a.Run(ctx, req, in)
	close(in) // adapters never send after Run returns
	<-done
	if errors.Is(context.Cause(ctx), ErrStalled) {
		return resp, fmt.Errorf("%w: no event for %s", ErrStalled, req.StallTimeout)
	}
	return resp, err
}
