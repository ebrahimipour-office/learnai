package signals_test

import (
	"os"
	"testing"

	"learninghub/internal/signals"
)

func TestShutdownSignals(t *testing.T) {
	if len(signals.Shutdown) == 0 {
		t.Fatal("expected at least one shutdown signal")
	}
	hasInterrupt := false
	for _, sig := range signals.Shutdown {
		if sig == os.Interrupt {
			hasInterrupt = true
			break
		}
	}
	if !hasInterrupt {
		t.Fatal("expected os.Interrupt in signals.Shutdown")
	}
}
