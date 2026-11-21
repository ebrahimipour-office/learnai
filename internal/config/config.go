package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

var (
	ErrForbiddenListenAddr    = errors.New("listen address must strictly bind to loopback (127.0.0.1 or [::1])")
	ErrForbiddenDangerousFlag = errors.New("--dangerously-skip-permissions is not allowed in extra_args; use adapter.skip_permissions")
	ErrSkipWithoutSandbox     = errors.New("adapter.skip_permissions requires adapter.sandbox = true")
	ErrInvalidBaseDir         = errors.New("base_dir must be an absolute path")
	ErrInvalidDuration        = errors.New("duration must be at least 1s")
	ErrInvalidRetention       = errors.New("log_retention_days must be at least 1")
	ErrUnknownConfigKey       = errors.New("config file contains unknown keys")
)

type Config struct {
	BaseDir          string          `toml:"base_dir"`
	Listen           string          `toml:"listen"`
	Language         string          `toml:"language"`
	LogRetentionDays int             `toml:"log_retention_days"`
	BackupKeep       int             `toml:"backup_keep"` // daily DB backups kept in .app/backup
	DraftTTL         time.Duration   `toml:"draft_ttl"`   // how long an unconfirmed import preview lives
	Adapter          AdapterConfig   `toml:"adapter"`
	Jobs             JobsConfig      `toml:"jobs"`
	Exercises        ExercisesConfig `toml:"exercises"`
	Limits           LimitsConfig    `toml:"limits"`
	Editor           EditorConfig    `toml:"editor"`
	Review           ReviewConfig    `toml:"review"`
}

// ReviewConfig picks the topics shown under «برای مرور» on /topics (T10.17).
type ReviewConfig struct {
	ScoreBelow float64 `toml:"score_below"` // latest score under this is "low"
	StaleDays  int     `toml:"stale_days"`  // latest evaluation older than this is "stale"
}

// AdapterConfig: the top-level keys configure agy; [adapter.claude] and [adapter.codex] the other
// tools. Kind is the tool used until one is picked on the settings page (saved in the database).
type AdapterConfig struct {
	Kind    string `toml:"kind"` // antigravity | claude | codex
	Binary  string `toml:"binary"`
	Model   string `toml:"model"`
	Effort  string `toml:"effort"`
	Sandbox bool   `toml:"sandbox"`
	// SkipPermissions passes --dangerously-skip-permissions to agy: headless mode denies every
	// permission prompt and the turn then ends with no output (T8.9). Requires Sandbox.
	SkipPermissions bool       `toml:"skip_permissions"`
	ExtraArgs       []string   `toml:"extra_args"`
	Claude          ToolConfig `toml:"claude"`
	Codex           ToolConfig `toml:"codex"`
}

// ToolConfig is one more AI CLI; Model/Effort "" use the tool's own default.
type ToolConfig struct {
	Binary    string   `toml:"binary"`
	Model     string   `toml:"model"`
	Effort    string   `toml:"effort"`
	ExtraArgs []string `toml:"extra_args"`
}

type JobsConfig struct {
	MaxConcurrent       int           `toml:"max_concurrent"`
	EvaluateParallelism int           `toml:"evaluate_parallelism"`
	TimeoutLesson       time.Duration `toml:"timeout_lesson"`
	TimeoutExercises    time.Duration `toml:"timeout_exercises"`
	TimeoutEvaluateItem time.Duration `toml:"timeout_evaluate_item"`
	TimeoutEvaluate     time.Duration `toml:"timeout_evaluate"` // whole evaluation job (TDD §6.5)
	TimeoutAnalyze      time.Duration `toml:"timeout_analyze"`
	StallTimeout        time.Duration `toml:"stall_timeout"`
	AutoRetry           int           `toml:"auto_retry"`  // T10.8: automatic retries of rate_limited/interrupted jobs; 0 = off
	RetryDelay          time.Duration `toml:"retry_delay"` // wait before each automatic retry
}

type ExercisesConfig struct {
	CountDefault      int      `toml:"count_default"`
	CountMin          int      `toml:"count_min"`
	CountMax          int      `toml:"count_max"`
	AllowedAnswerExts []string `toml:"allowed_answer_exts"`
}

type LimitsConfig struct {
	MaxRoadmapBytes int64 `toml:"max_roadmap_bytes"`
	MaxAnswerBytes  int64 `toml:"max_answer_bytes"`
	MaxLessonBytes  int64 `toml:"max_lesson_bytes"`
	MaxSourceBytes  int64 `toml:"max_source_bytes"` // chapter source.md sent to the AI (TDD §12.8)
	MaxTreeTopics   int   `toml:"max_tree_topics"`  // records one "tree" import may create (R12)
}

type EditorConfig struct {
	Command string `toml:"command"`
}

// DefaultConfig returns safe, pre-configured defaults.
func DefaultConfig(defaultBaseDir string) Config {
	return Config{
		BaseDir:          defaultBaseDir,
		Listen:           "127.0.0.1:8787",
		Language:         "fa",
		LogRetentionDays: 30,
		BackupKeep:       7,
		DraftTTL:         24 * time.Hour,
		Adapter: AdapterConfig{
			Kind:            "antigravity",
			Binary:          "agy",
			Model:           "gemini-3.8-flash-medium",
			Effort:          "medium",
			Sandbox:         true,
			SkipPermissions: true,
			ExtraArgs:       nil,
			Claude:          ToolConfig{Binary: "claude"},
			Codex:           ToolConfig{Binary: "codex"},
		},
		Jobs: JobsConfig{
			MaxConcurrent:       1,
			EvaluateParallelism: 1,
			TimeoutLesson:       10 * time.Minute,
			TimeoutExercises:    10 * time.Minute,
			TimeoutEvaluateItem: 5 * time.Minute,
			TimeoutEvaluate:     30 * time.Minute,
			TimeoutAnalyze:      10 * time.Minute,
			StallTimeout:        6 * time.Minute, // agy sends nothing while writing a long final answer (B4); keep below lesson/exercises timeouts
			AutoRetry:           2,
			RetryDelay:          2 * time.Minute,
		},
		Exercises: ExercisesConfig{
			CountDefault: 6,
			CountMin:     3,
			CountMax:     12,
			AllowedAnswerExts: []string{
				".go", ".py", ".js", ".ts", ".java", ".c", ".cpp",
				".rs", ".sql", ".sh", ".md", ".txt", ".json", ".yaml", ".yml",
			},
		},
		Limits: LimitsConfig{
			MaxRoadmapBytes: 1048576, // 1 MB
			MaxAnswerBytes:  204800,  // 200 KB
			MaxLessonBytes:  204800,  // 200 KB
			MaxSourceBytes:  204800,  // 200 KB
			MaxTreeTopics:   200,
		},
		Editor: EditorConfig{
			Command: "",
		},
		Review: ReviewConfig{ScoreBelow: 70, StaleDays: 14},
	}
}

// DefaultConfigPath returns the standard configuration file path (%APPDATA%\LearningHub\config.toml on Windows,
// or ~/.config/learninghub/config.toml on Unix/macOS).
func DefaultConfigPath() (string, error) {
	appData := os.Getenv("APPDATA")
	if appData != "" {
		return filepath.Join(appData, "LearningHub", "config.toml"), nil
	}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "learninghub", "config.toml"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "learninghub", "config.toml"), nil
}

// Load loads and validates configuration from a file or falls back to defaults.
// When cfgPath is empty, it first checks for config.toml in fallbackBaseDir (current working directory).
// If not found, it checks DefaultConfigPath(); if that default file exists, it is loaded.
// If no config file exists, built-in defaults with fallbackBaseDir are used.
func Load(cfgPath string, fallbackBaseDir string) (Config, error) {
	cfg := DefaultConfig(fallbackBaseDir)

	actualPath := cfgPath
	if actualPath == "" {
		if fallbackBaseDir != "" {
			localPath := filepath.Join(fallbackBaseDir, "config.toml")
			if _, statErr := os.Stat(localPath); statErr == nil {
				actualPath = localPath
			} else if !errors.Is(statErr, os.ErrNotExist) {
				return cfg, fmt.Errorf("reading local config file %s: %w", localPath, statErr)
			}
		}
		if actualPath == "" {
			if defPath, err := DefaultConfigPath(); err == nil {
				if _, statErr := os.Stat(defPath); statErr == nil {
					actualPath = defPath
				} else if !errors.Is(statErr, os.ErrNotExist) {
					return cfg, fmt.Errorf("reading default config file %s: %w", defPath, statErr)
				}
			}
		}
	}

	if actualPath != "" {
		md, err := toml.DecodeFile(actualPath, &cfg)
		if err != nil {
			return cfg, fmt.Errorf("reading config file %s: %w", actualPath, err)
		}
		if undecoded := md.Undecoded(); len(undecoded) > 0 {
			keys := make([]string, len(undecoded))
			for i, k := range undecoded {
				keys[i] = k.String()
			}
			slices.Sort(keys)
			return cfg, fmt.Errorf("%w in %s: %s", ErrUnknownConfigKey, actualPath, strings.Join(keys, ", "))
		}
	}

	if err := cfg.Validate(); err != nil {
		return cfg, err
	}

	return cfg, nil
}

// Validate applies strict security and integrity checks on the configuration (D1, Section 8.1).
func (c *Config) Validate() error {
	// 1. Strictly verify loopback listen address
	host, _, err := net.SplitHostPort(c.Listen)
	if err != nil {
		return fmt.Errorf("parsing listen address %q: %w", c.Listen, err)
	}

	if host != "127.0.0.1" && host != "::1" && host != "localhost" {
		return fmt.Errorf("%w: listen is configured as %q", ErrForbiddenListenAddr, c.Listen)
	}

	// 2. Reject prohibited dangerous flags and unknown tools
	for _, arg := range slices.Concat(c.Adapter.ExtraArgs, c.Adapter.Claude.ExtraArgs, c.Adapter.Codex.ExtraArgs) {
		if strings.Contains(arg, "dangerously") {
			return ErrForbiddenDangerousFlag
		}
	}
	if c.Adapter.SkipPermissions && !c.Adapter.Sandbox {
		return ErrSkipWithoutSandbox
	}
	if !slices.Contains([]string{"antigravity", "claude", "codex"}, c.Adapter.Kind) {
		return fmt.Errorf("adapter.kind must be antigravity, claude or codex, not %q", c.Adapter.Kind)
	}

	// 3. Ensure base directory is absolute
	if c.BaseDir == "" {
		return errors.New("base_dir must not be empty")
	}
	if !filepath.IsAbs(c.BaseDir) {
		abs, err := filepath.Abs(c.BaseDir)
		if err != nil {
			return fmt.Errorf("%w: %s", ErrInvalidBaseDir, c.BaseDir)
		}
		c.BaseDir = abs
	}

	// 4. Validate retention and timeouts (CR-40)
	if c.LogRetentionDays < 1 {
		return fmt.Errorf("%w: got %d", ErrInvalidRetention, c.LogRetentionDays)
	}
	for _, tc := range []struct {
		name string
		d    time.Duration
	}{
		{"draft_ttl", c.DraftTTL},
		{"jobs.timeout_lesson", c.Jobs.TimeoutLesson},
		{"jobs.timeout_exercises", c.Jobs.TimeoutExercises},
		{"jobs.timeout_evaluate_item", c.Jobs.TimeoutEvaluateItem},
		{"jobs.timeout_evaluate", c.Jobs.TimeoutEvaluate},
		{"jobs.timeout_analyze", c.Jobs.TimeoutAnalyze},
		{"jobs.stall_timeout", c.Jobs.StallTimeout},
		{"jobs.retry_delay", c.Jobs.RetryDelay},
	} {
		if tc.d < time.Second {
			return fmt.Errorf("%w: %s must be at least 1s, got %s", ErrInvalidDuration, tc.name, tc.d)
		}
	}

	// 5. Validate limits
	if c.Limits.MaxRoadmapBytes <= 0 {
		c.Limits.MaxRoadmapBytes = 1048576
	}
	if c.Limits.MaxAnswerBytes <= 0 {
		c.Limits.MaxAnswerBytes = 204800
	}
	if c.Limits.MaxLessonBytes <= 0 {
		c.Limits.MaxLessonBytes = 204800
	}
	if c.Limits.MaxSourceBytes <= 0 {
		c.Limits.MaxSourceBytes = 204800
	}
	if c.Limits.MaxTreeTopics <= 0 {
		c.Limits.MaxTreeTopics = 200
	}
	if c.BackupKeep < 1 {
		c.BackupKeep = 7
	}
	if c.Review.ScoreBelow <= 0 || c.Review.ScoreBelow > 100 {
		c.Review.ScoreBelow = 70
	}
	if c.Review.StaleDays < 1 {
		c.Review.StaleDays = 14
	}

	// 6. Validate jobs concurrency
	if c.Jobs.MaxConcurrent < 1 {
		c.Jobs.MaxConcurrent = 1
	}
	c.Jobs.AutoRetry = max(c.Jobs.AutoRetry, 0)
	if c.Jobs.EvaluateParallelism < 1 {
		c.Jobs.EvaluateParallelism = 1
	}
	c.Jobs.EvaluateParallelism = min(c.Jobs.EvaluateParallelism, 3) // TDD §6.5: at most 3 agy processes per evaluation

	return nil
}
