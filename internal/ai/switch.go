package ai

import (
	"context"
	"fmt"
	"sync/atomic"
)

// Modeler is implemented by adapters that know which model they run with ("" = the tool's default).
type Modeler interface{ Model() string }

// ModelFor is the model a job records: the adapter's own when it knows it, else fallback.
func ModelFor(a Adapter, fallback string) string {
	if m, ok := a.(Modeler); ok {
		return m.Model()
	}
	return fallback
}

// Switch delegates to one of several adapters (agy, claude, codex); the settings page picks which.
// Jobs read Name/Model/Run at different moments, so callers must not Set while a job is active.
type Switch struct {
	names    []string
	adapters map[string]Adapter
	cur      atomic.Pointer[string]
}

var _ Adapter = (*Switch)(nil)

// NewSwitch selects current, or the first adapter when current is unknown.
func NewSwitch(current string, as ...Adapter) *Switch {
	s := &Switch{adapters: map[string]Adapter{}}
	for _, a := range as {
		s.names = append(s.names, a.Name())
		s.adapters[a.Name()] = a
	}
	if s.Set(current) != nil {
		s.cur.Store(&s.names[0])
	}
	return s
}

// Names lists the selectable adapters in construction order.
func (s *Switch) Names() []string { return s.names }

func (s *Switch) Set(name string) error {
	if _, ok := s.adapters[name]; !ok {
		return fmt.Errorf("ai: unknown adapter %q", name)
	}
	s.cur.Store(&name)
	return nil
}

func (s *Switch) current() Adapter { return s.adapters[*s.cur.Load()] }

func (s *Switch) Name() string  { return *s.cur.Load() }
func (s *Switch) Model() string { return ModelFor(s.current(), "") }

func (s *Switch) Probe(ctx context.Context) (Capabilities, error) { return s.current().Probe(ctx) }

func (s *Switch) Run(ctx context.Context, req Request, events chan<- Event) (*Response, error) {
	return s.current().Run(ctx, req, events)
}

func (s *Switch) NoteInvalid(resp *Response, err error) {
	if n, ok := s.current().(InvalidNoter); ok {
		n.NoteInvalid(resp, err)
	}
}
