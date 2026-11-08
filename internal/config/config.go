package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

var (
	ErrForbiddenListenAddr   = errors.New("listen address must strictly bind to loopback (127.0.0.1 or [::1])")
	ErrForbiddenDangerousFlag = errors.New("--dangerously-skip-permissions is strictly prohibited")
	ErrInvalidBaseDir        = errors.New("base_dir must be an absolute path")
)

type Config struct {
	BaseDir          string          `toml:"base_dir"`
	Listen           string          `toml:"listen"`
	Language         string          `toml:"language"`
	LogRetentionDays int             `toml:"log_retention_days"`
	Adapter          AdapterConfig   `toml:"adapter"`
	Jobs             JobsConfig      `toml:"jobs"`
	Exercises        ExercisesConfig `toml:"exercises"`
	Limits           LimitsConfig    `toml:"limits"`
	Editor           EditorConfig    `toml:"editor"`
}

type AdapterConfig struct {
	Kind      string   `toml:"kind"`
	Binary    string   `toml:"binary"`
	Model     string   `toml:"model"`
	Effort    string   `toml:"effort"`
	Sandbox   bool     `toml:"sandbox"`
	ExtraArgs []string `toml:"extra_args"`
}

type JobsConfig struct {
	MaxConcurrent       int           `toml:"max_concurrent"`
	EvaluateParallelism int           `toml:"evaluate_parallelism"`
	TimeoutLesson       time.Duration `toml:"timeout_lesson"`
	TimeoutExercises    time.Duration `toml:"timeout_exercises"`
	TimeoutEvaluateItem time.Duration `toml:"timeout_evaluate_item"`
	TimeoutAnalyze      time.Duration `toml:"timeout_analyze"`
	StallTimeout        time.Duration `toml:"stall_timeout"`
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
		Adapter: AdapterConfig{
			Kind:      "antigravity",
			Binary:    "agy",
			Model:     "gemini-3.8-flash-medium",
			Effort:    "medium",
			Sandbox:   true,
			ExtraArgs: nil,
		},
		Jobs: JobsConfig{
			MaxConcurrent:       1,
			EvaluateParallelism: 1,
			TimeoutLesson:       10 * time.Minute,
			TimeoutExercises:    10 * time.Minute,
			TimeoutEvaluateItem: 5 * time.Minute,
			TimeoutAnalyze:      3 * time.Minute,
			StallTimeout:        6 * time.Minute, // agy sends nothing while writing a long final answer (B4); keep below lesson/exercises timeouts
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
		},
		Editor: EditorConfig{
			Command: "",
		},
	}
}

// DefaultConfigPath returns the standard configuration file path (%APPDATA%\LearningHub\config.toml on Windows).
func DefaultConfigPath() (string, error) {
	appData := os.Getenv("APPDATA")
	if appData == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".config", "learninghub", "config.toml"), nil
	}
	return filepath.Join(appData, "LearningHub", "config.toml"), nil
}

// Load loads and validates configuration from a file or falls back to defaults.
func Load(cfgPath string, fallbackBaseDir string) (Config, error) {
	cfg := DefaultConfig(fallbackBaseDir)

	if cfgPath != "" {
		if _, err := os.Stat(cfgPath); err == nil {
			if _, err := toml.DecodeFile(cfgPath, &cfg); err != nil {
				return cfg, fmt.Errorf("decoding config file %s: %w", cfgPath, err)
			}
		} else if !os.IsNotExist(err) {
			return cfg, err
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

	// 2. Reject prohibited dangerous flags
	for _, arg := range c.Adapter.ExtraArgs {
		if strings.Contains(arg, "dangerously-skip-permissions") {
			return ErrForbiddenDangerousFlag
		}
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

	// 4. Validate limits
	if c.Limits.MaxRoadmapBytes <= 0 {
		c.Limits.MaxRoadmapBytes = 1048576
	}
	if c.Limits.MaxAnswerBytes <= 0 {
		c.Limits.MaxAnswerBytes = 204800
	}
	if c.Limits.MaxLessonBytes <= 0 {
		c.Limits.MaxLessonBytes = 204800
	}

	// 5. Validate jobs concurrency
	if c.Jobs.MaxConcurrent < 1 {
		c.Jobs.MaxConcurrent = 1
	}
	if c.Jobs.EvaluateParallelism < 1 {
		c.Jobs.EvaluateParallelism = 1
	}
	c.Jobs.EvaluateParallelism = min(c.Jobs.EvaluateParallelism, 3) // TDD §6.5: at most 3 agy processes per evaluation

	return nil
}
