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
	TaskChapterDetect  Task = "chapter_detect"
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
	// Chapter source.md problems found while preparing inputs (TDD §12.8); never truncated or skipped.
	ErrSourceTooLarge = errors.New("ai: chapter source too large")
	ErrSourceMissing  = errors.New("ai: chapter source missing")
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
	case errors.Is(err, ErrSourceTooLarge):
		return "source_too_large"
	case errors.Is(err, ErrSourceMissing):
		return "source_missing"
	default:
		return "tool_error"
	}
}

// Message is the actionable Persian text shown for an error_code (TDD §6.1); "" when the
// code is not an adapter error (the job's own error_message is shown instead).
func Message(code string) string {
	return messages[code]
}

var messages = map[string]string{
	"auth_required":    "ابتدا یک‌بار در ترمینال `agy` را اجرا و وارد شوید؛ سپس در صفحه تنظیمات بررسی را دوباره اجرا کنید.",
	"adapter_missing":  "فایل `agy` پیدا نشد؛ آن را نصب کنید یا مسیرش را در `adapter.binary` فایل config.toml اصلاح کنید.",
	"timeout":          "زمان اجرا از سقف مجاز گذشت.",
	"stalled":          "ابزار AI مدتی پاسخ نداد؛ دوباره تلاش کنید. اگر تکرار شد، `jobs.stall_timeout` را در config.toml افزایش دهید.",
	"rate_limited":     "سهمیه یا محدودیت نرخ؛ بعداً دوباره تلاش کنید.",
	"tool_error":       "ابزار AI با خطا پایان یافت (جزئیات در لاگ).",
	"invalid_output":   "خروجی AI معتبر نبود.",
	"source_too_large": "متن فصل بزرگ‌تر از سقف است؛ فصل را در پیش‌نمایش کوچک‌تر کنید یا سقف `limits.max_source_bytes` را در config.toml افزایش دهید.",
	"source_missing":   "فایل متن فصل پیدا نشد؛ `source.md` فصل را بازگردانید یا نقشه راه را دوباره وارد کنید (جزئیات فنی مسیر را نشان می‌دهد).",
}
