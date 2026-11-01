// Package ai is the only boundary between the hub and an AI tool (TDD §6.1).
// It must not depend on store or domain.
package ai

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

type Task string

const (
	TaskLesson         Task = "lesson"
	TaskExercises      Task = "exercises"
	TaskEvaluate       Task = "evaluate"
	TaskRoadmapAnalyze Task = "roadmap_analyze"
)

type Adapter interface {
	// Name is recorded on the job and in output files (e.g. "antigravity").
	Name() string
	// Probe checks feasibility (R7.3): version, auth, headless, structured output.
	Probe(ctx context.Context) (Capabilities, error)
	// Run executes one task, sending progress on events (may be nil). Cancelling ctx
	// must stop the process and return ErrCanceled/ErrTimeout.
	Run(ctx context.Context, req Request, events chan<- Event) (*Response, error)
}

type SkillRef struct {
	Name    string
	Version string
	Text    string
}

type Request struct {
	JobID        int64
	Task         Task
	Skill        SkillRef
	Prompt       string   // short; large data lives in WorkDir files
	WorkDir      string   // absolute temporary sandbox
	InputFiles   []string // relative to WorkDir (for logs)
	OutputSchema []byte   // JSON Schema of the output (for --json-schema)
	Model        string
	Effort       string
	Timeout      time.Duration
	StallTimeout time.Duration
}

type Usage struct {
	InputTokens     int64 `json:"input_tokens"`
	OutputTokens    int64 `json:"output_tokens"`
	ThinkingTokens  int64 `json:"thinking_tokens"`
	CacheReadTokens int64 `json:"cache_read_tokens"`
	TotalTokens     int64 `json:"total_tokens"`
}

type Response struct {
	Structured     json.RawMessage // not yet schema-validated
	Text           string
	ConversationID string
	Usage          Usage
	Duration       time.Duration
	RawLogDir      string
}

type Event struct {
	Kind string // "started"|"step"|"text_delta"|"tool"|"heartbeat"
	Info string
}

type Capabilities struct {
	Version             string
	Authenticated       bool
	HeadlessJSON        bool
	StructuredOutput    bool
	ExitCodesDocumented bool
	Models              []string
	Notes               []string
}

var (
	ErrAuthRequired   = errors.New("ai: authentication required")
	ErrBinaryNotFound = errors.New("ai: adapter binary not found")
	ErrTimeout        = errors.New("ai: timeout")
	ErrStalled        = errors.New("ai: stalled")
	ErrCanceled       = errors.New("ai: canceled")
	ErrRateLimited    = errors.New("ai: rate limited")
	ErrToolFailed     = errors.New("ai: tool failed")
	ErrInvalidOutput  = errors.New("ai: invalid output")
)

// Code maps an adapter error to jobs.error_code (TDD §6.1 table); "" for nil.
func Code(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrAuthRequired):
		return "auth_required"
	case errors.Is(err, ErrBinaryNotFound):
		return "adapter_missing"
	case errors.Is(err, ErrTimeout):
		return "timeout"
	case errors.Is(err, ErrStalled):
		return "stalled"
	case errors.Is(err, ErrCanceled):
		return "canceled"
	case errors.Is(err, ErrRateLimited):
		return "rate_limited"
	case errors.Is(err, ErrInvalidOutput):
		return "invalid_output"
	default:
		return "tool_error"
	}
}
