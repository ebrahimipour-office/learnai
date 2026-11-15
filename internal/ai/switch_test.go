package ai_test

import (
	"context"
	"testing"

	"learninghub/internal/ai"
	"learninghub/internal/ai/fake"
)

type namedAdapter struct {
	ai.Adapter
	name  string
	model string
}

func (n namedAdapter) Name() string  { return n.name }
func (n namedAdapter) Model() string { return n.model }

func TestSwitch(t *testing.T) {
	a1 := namedAdapter{Adapter: &fake.Adapter{}, name: "tool1", model: "m1"}
	a2 := namedAdapter{Adapter: &fake.Adapter{}, name: "tool2", model: "m2"}

	sw := ai.NewSwitch("tool1", a1, a2)

	if names := sw.Names(); len(names) != 2 || names[0] != "tool1" || names[1] != "tool2" {
		t.Fatalf("unexpected Names: %v", names)
	}

	if sw.Name() != "tool1" {
		t.Fatalf("expected tool1, got %s", sw.Name())
	}
	if sw.Model() != "m1" {
		t.Fatalf("expected m1, got %s", sw.Model())
	}
	if sw.Current().Name() != "tool1" {
		t.Fatalf("expected Current() to be tool1, got %s", sw.Current().Name())
	}

	if got := sw.Get("tool2"); got == nil || got.Name() != "tool2" {
		t.Fatalf("expected Get(tool2) to return tool2 adapter, got: %v", got)
	}
	if got := sw.Get("nonexistent"); got != nil {
		t.Fatalf("expected Get(nonexistent) to be nil, got: %v", got)
	}

	if err := sw.Set("tool2"); err != nil {
		t.Fatalf("unexpected error setting tool2: %v", err)
	}
	if sw.Name() != "tool2" || sw.Model() != "m2" {
		t.Fatalf("expected tool2/m2 after switch, got %s/%s", sw.Name(), sw.Model())
	}

	if err := sw.Set("unknown"); err == nil {
		t.Fatal("expected error setting unknown adapter, got nil")
	}

	// Probe delegates to current adapter
	caps, err := sw.Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe failed: %v", err)
	}
	if !caps.HeadlessJSON {
		t.Fatal("expected fake adapter to support HeadlessJSON")
	}
}
