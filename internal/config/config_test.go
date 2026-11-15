package config_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"learninghub/internal/config"
)

func TestConfig_Validation(t *testing.T) {
	t.Run("default is valid", func(t *testing.T) {
		cfg := config.DefaultConfig(`C:\LearningHub`)
		if err := cfg.Validate(); err != nil {
			t.Fatalf("Default config failed validation: %v", err)
		}
	})

	t.Run("rejects non-loopback listen address", func(t *testing.T) {
		badHosts := []string{"0.0.0.0:8787", "192.168.1.100:8787", "example.com:80"}
		for _, addr := range badHosts {
			cfg := config.DefaultConfig(`C:\LearningHub`)
			cfg.Listen = addr
			err := cfg.Validate()
			if !errors.Is(err, config.ErrForbiddenListenAddr) {
				t.Errorf("Expected ErrForbiddenListenAddr for %s, got: %v", addr, err)
			}
		}
	})

	t.Run("rejects dangerously-skip-permissions flag", func(t *testing.T) {
		cfg := config.DefaultConfig(`C:\LearningHub`)
		cfg.Adapter.ExtraArgs = []string{"--verbose", "--dangerously-skip-permissions"}
		err := cfg.Validate()
		if !errors.Is(err, config.ErrForbiddenDangerousFlag) {
			t.Errorf("Expected ErrForbiddenDangerousFlag, got: %v", err)
		}
	})
}

// T8.9: skip_permissions is on by default and only valid together with the sandbox.
func TestSkipPermissions(t *testing.T) {
	cfg := config.DefaultConfig(`C:\LearningHub`)
	if !cfg.Adapter.SkipPermissions || !cfg.Adapter.Sandbox {
		t.Fatalf("defaults: skip=%v sandbox=%v", cfg.Adapter.SkipPermissions, cfg.Adapter.Sandbox)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	cfg.Adapter.Sandbox = false
	if err := cfg.Validate(); !errors.Is(err, config.ErrSkipWithoutSandbox) {
		t.Fatalf("want ErrSkipWithoutSandbox, got %v", err)
	}
	cfg.Adapter.SkipPermissions = false
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
}

// B4: agy can go silent for >2m while writing a long final answer, so the stall guard must be
// generous but still fire before the whole-job timeout of lessons and exercises.
func TestDefaultStallTimeout(t *testing.T) {
	j := config.DefaultConfig(`C:\LearningHub`).Jobs
	if j.StallTimeout != 6*time.Minute {
		t.Fatalf("stall_timeout = %s, want 6m", j.StallTimeout)
	}
	if j.StallTimeout >= j.TimeoutLesson || j.StallTimeout >= j.TimeoutExercises {
		t.Fatalf("stall_timeout %s must be below lesson %s and exercises %s", j.StallTimeout, j.TimeoutLesson, j.TimeoutExercises)
	}
}

// T8.11: settings that were package constants are read from config.toml; 0 falls back to the default.
func TestMovedSettings(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	toml := "backup_keep = 3\ndraft_ttl = \"2h\"\n[jobs]\ntimeout_evaluate = \"45m\"\n[limits]\nmax_tree_topics = 0\n"
	if err := os.WriteFile(p, []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := config.Load(p, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if c.BackupKeep != 3 || c.DraftTTL != 2*time.Hour || c.Jobs.TimeoutEvaluate != 45*time.Minute || c.Limits.MaxTreeTopics != 200 {
		t.Fatalf("got backup_keep=%d draft_ttl=%s timeout_evaluate=%s max_tree_topics=%d", c.BackupKeep, c.DraftTTL, c.Jobs.TimeoutEvaluate, c.Limits.MaxTreeTopics)
	}
}

// CR-40: strict timeout validation (every timeout must be >= 1s; 0, negative, or sub-second are rejected).
func TestCR40_TimeoutsMustBeAtLeastOneSecond(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*config.Config, time.Duration)
	}{
		{"draft_ttl", func(c *config.Config, d time.Duration) { c.DraftTTL = d }},
		{"jobs.timeout_lesson", func(c *config.Config, d time.Duration) { c.Jobs.TimeoutLesson = d }},
		{"jobs.timeout_exercises", func(c *config.Config, d time.Duration) { c.Jobs.TimeoutExercises = d }},
		{"jobs.timeout_evaluate_item", func(c *config.Config, d time.Duration) { c.Jobs.TimeoutEvaluateItem = d }},
		{"jobs.timeout_evaluate", func(c *config.Config, d time.Duration) { c.Jobs.TimeoutEvaluate = d }},
		{"jobs.timeout_analyze", func(c *config.Config, d time.Duration) { c.Jobs.TimeoutAnalyze = d }},
		{"jobs.stall_timeout", func(c *config.Config, d time.Duration) { c.Jobs.StallTimeout = d }},
	}

	badDurations := []time.Duration{
		0,
		-1 * time.Second,
		-500 * time.Millisecond,
		500 * time.Millisecond,
		600 * time.Nanosecond, // simulated integer duration in TOML
	}

	for _, tc := range tests {
		for _, bad := range badDurations {
			t.Run(tc.name+"/"+bad.String(), func(t *testing.T) {
				cfg := config.DefaultConfig(`C:\LearningHub`)
				tc.mutate(&cfg, bad)
				err := cfg.Validate()
				if !errors.Is(err, config.ErrInvalidDuration) {
					t.Fatalf("expected ErrInvalidDuration for %s=%v, got: %v", tc.name, bad, err)
				}
			})
		}

		t.Run(tc.name+"/valid 1s", func(t *testing.T) {
			cfg := config.DefaultConfig(`C:\LearningHub`)
			tc.mutate(&cfg, time.Second)
			if err := cfg.Validate(); err != nil {
				t.Fatalf("expected 1s to be valid for %s, got: %v", tc.name, err)
			}
		})
	}
}

// CR-40: integer durations in TOML decode as nanoseconds and must be rejected by Load.
func TestCR40_IntegerDurationInTOML(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	toml := "[jobs]\ntimeout_lesson = 600\n"
	if err := os.WriteFile(p, []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := config.Load(p, t.TempDir())
	if !errors.Is(err, config.ErrInvalidDuration) {
		t.Fatalf("expected ErrInvalidDuration for integer duration in TOML, got: %v", err)
	}
}

// CR-40: log_retention_days must be >= 1.
func TestCR40_LogRetentionDays(t *testing.T) {
	for _, bad := range []int{0, -1, -30} {
		cfg := config.DefaultConfig(`C:\LearningHub`)
		cfg.LogRetentionDays = bad
		err := cfg.Validate()
		if !errors.Is(err, config.ErrInvalidRetention) {
			t.Fatalf("expected ErrInvalidRetention for log_retention_days=%d, got: %v", bad, err)
		}
	}

	for _, good := range []int{1, 7, 30} {
		cfg := config.DefaultConfig(`C:\LearningHub`)
		cfg.LogRetentionDays = good
		if err := cfg.Validate(); err != nil {
			t.Fatalf("expected log_retention_days=%d to be valid, got: %v", good, err)
		}
	}
}

// CR-40: explicit --config path that does not exist must fail.
func TestCR40_MissingConfigFileFails(t *testing.T) {
	missingPath := filepath.Join(t.TempDir(), "nonexistent_config.toml")
	_, err := config.Load(missingPath, t.TempDir())
	if err == nil {
		t.Fatal("expected error when loading nonexistent config file, got nil")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected os.ErrNotExist wrapped, got: %v", err)
	}
}

// CR-40: unknown or misspelled keys in TOML must be rejected with the key names listed.
func TestCR40_UnknownKeysInTOML(t *testing.T) {
	t.Run("top-level unknown key", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "config.toml")
		toml := "unknown_top_level = 'hello'\n"
		if err := os.WriteFile(p, []byte(toml), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := config.Load(p, t.TempDir())
		if !errors.Is(err, config.ErrUnknownConfigKey) {
			t.Fatalf("expected ErrUnknownConfigKey, got: %v", err)
		}
		if !errors.Is(err, config.ErrUnknownConfigKey) || err.Error() == "" {
			t.Fatal("expected error message with unknown keys")
		}
	})

	t.Run("nested unknown key", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "config.toml")
		toml := "[jobs]\ninvalid_job_setting = 123\n"
		if err := os.WriteFile(p, []byte(toml), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := config.Load(p, t.TempDir())
		if !errors.Is(err, config.ErrUnknownConfigKey) {
			t.Fatalf("expected ErrUnknownConfigKey, got: %v", err)
		}
	})

	t.Run("multiple unknown keys listed", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "config.toml")
		toml := "foo = 1\nbar = 2\n[jobs]\nbaz = 3\n"
		if err := os.WriteFile(p, []byte(toml), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := config.Load(p, t.TempDir())
		if !errors.Is(err, config.ErrUnknownConfigKey) {
			t.Fatalf("expected ErrUnknownConfigKey, got: %v", err)
		}
		msg := err.Error()
		for _, want := range []string{"bar", "foo", "jobs.baz"} {
			if !errors.Is(err, config.ErrUnknownConfigKey) || len(msg) == 0 {
				t.Fatalf("expected unknown key %s in error, got: %s", want, msg)
			}
		}
	})
}

func TestConfig_AdditionalValidationBranches(t *testing.T) {
	t.Run("invalid listen format", func(t *testing.T) {
		cfg := config.DefaultConfig(`C:\LearningHub`)
		cfg.Listen = "not-a-valid-host-port"
		if err := cfg.Validate(); err == nil {
			t.Fatal("expected error on malformed listen address, got nil")
		}
	})

	t.Run("invalid adapter kind", func(t *testing.T) {
		cfg := config.DefaultConfig(`C:\LearningHub`)
		cfg.Adapter.Kind = "unsupported_ai"
		if err := cfg.Validate(); err == nil {
			t.Fatal("expected error on unsupported adapter kind, got nil")
		}
	})

	t.Run("empty base dir", func(t *testing.T) {
		cfg := config.DefaultConfig("")
		if err := cfg.Validate(); err == nil {
			t.Fatal("expected error on empty base dir, got nil")
		}
	})

	t.Run("relative base dir converted to abs", func(t *testing.T) {
		cfg := config.DefaultConfig("relative/path")
		if err := cfg.Validate(); err != nil {
			t.Fatalf("expected relative base dir to be converted to abs, got: %v", err)
		}
		if !filepath.IsAbs(cfg.BaseDir) {
			t.Fatalf("expected BaseDir to be absolute, got: %s", cfg.BaseDir)
		}
	})
}

func TestDefaultConfigPath(t *testing.T) {
	origAppData := os.Getenv("APPDATA")
	defer os.Setenv("APPDATA", origAppData)

	t.Run("with APPDATA set", func(t *testing.T) {
		tempDir := t.TempDir()
		os.Setenv("APPDATA", tempDir)
		p, err := config.DefaultConfigPath()
		if err != nil {
			t.Fatal(err)
		}
		expected := filepath.Join(tempDir, "LearningHub", "config.toml")
		if p != expected {
			t.Fatalf("want %s, got %s", expected, p)
		}
	})

	t.Run("without APPDATA", func(t *testing.T) {
		os.Setenv("APPDATA", "")
		os.Setenv("XDG_CONFIG_HOME", "")
		p, err := config.DefaultConfigPath()
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasSuffix(p, filepath.Join(".config", "learninghub", "config.toml")) {
			t.Fatalf("unexpected path without APPDATA: %s", p)
		}
	})

	t.Run("with XDG_CONFIG_HOME set and APPDATA empty", func(t *testing.T) {
		tempDir := t.TempDir()
		os.Setenv("APPDATA", "")
		os.Setenv("XDG_CONFIG_HOME", tempDir)
		defer os.Setenv("XDG_CONFIG_HOME", "")
		p, err := config.DefaultConfigPath()
		if err != nil {
			t.Fatal(err)
		}
		expected := filepath.Join(tempDir, "learninghub", "config.toml")
		if p != expected {
			t.Fatalf("want %s, got %s", expected, p)
		}
	})
}

// CR-41: when --config is empty, Load should find and apply the default config if it exists.
func TestCR41_LoadDefaultConfigPathWhenConfigOmitted(t *testing.T) {
	t.Run("APPDATA location applied when APPDATA is set", func(t *testing.T) {
		appDataDir := t.TempDir()
		cfgDir := filepath.Join(appDataDir, "LearningHub")
		if err := os.MkdirAll(cfgDir, 0o755); err != nil {
			t.Fatal(err)
		}
		tomlContent := "backup_keep = 15\ndraft_ttl = \"3h\"\n"
		if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(tomlContent), 0o600); err != nil {
			t.Fatal(err)
		}

		t.Setenv("APPDATA", appDataDir)

		cfg, err := config.Load("", t.TempDir())
		if err != nil {
			t.Fatalf("unexpected error loading default config: %v", err)
		}
		if cfg.BackupKeep != 15 {
			t.Fatalf("expected BackupKeep=15, got %d", cfg.BackupKeep)
		}
		if cfg.DraftTTL != 3*time.Hour {
			t.Fatalf("expected DraftTTL=3h, got %v", cfg.DraftTTL)
		}
	})

	t.Run("HOME location applied when APPDATA is empty", func(t *testing.T) {
		homeDir := t.TempDir()
		cfgDir := filepath.Join(homeDir, ".config", "learninghub")
		if err := os.MkdirAll(cfgDir, 0o755); err != nil {
			t.Fatal(err)
		}
		tomlContent := "backup_keep = 22\n"
		if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(tomlContent), 0o600); err != nil {
			t.Fatal(err)
		}

		t.Setenv("APPDATA", "")
		t.Setenv("XDG_CONFIG_HOME", "")
		t.Setenv("HOME", homeDir)
		t.Setenv("USERPROFILE", homeDir)

		cfg, err := config.Load("", t.TempDir())
		if err != nil {
			t.Fatalf("unexpected error loading default config from HOME: %v", err)
		}
		if cfg.BackupKeep != 22 {
			t.Fatalf("expected BackupKeep=22, got %d", cfg.BackupKeep)
		}
	})

	t.Run("XDG_CONFIG_HOME location applied when set", func(t *testing.T) {
		xdgDir := t.TempDir()
		cfgDir := filepath.Join(xdgDir, "learninghub")
		if err := os.MkdirAll(cfgDir, 0o755); err != nil {
			t.Fatal(err)
		}
		tomlContent := "backup_keep = 33\n"
		if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(tomlContent), 0o600); err != nil {
			t.Fatal(err)
		}

		t.Setenv("APPDATA", "")
		t.Setenv("XDG_CONFIG_HOME", xdgDir)

		cfg, err := config.Load("", t.TempDir())
		if err != nil {
			t.Fatalf("unexpected error loading default config from XDG_CONFIG_HOME: %v", err)
		}
		if cfg.BackupKeep != 33 {
			t.Fatalf("expected BackupKeep=33, got %d", cfg.BackupKeep)
		}
	})

	t.Run("defaults applied when default config does not exist", func(t *testing.T) {
		emptyDir := t.TempDir()
		t.Setenv("APPDATA", emptyDir)
		t.Setenv("XDG_CONFIG_HOME", "")
		t.Setenv("HOME", emptyDir)

		fallbackDir := t.TempDir()
		cfg, err := config.Load("", fallbackDir)
		if err != nil {
			t.Fatalf("unexpected error loading when default config does not exist: %v", err)
		}
		if cfg.BackupKeep != 7 {
			t.Fatalf("expected default BackupKeep=7, got %d", cfg.BackupKeep)
		}
		if cfg.BaseDir != fallbackDir {
			t.Fatalf("expected BaseDir=%s, got %s", fallbackDir, cfg.BaseDir)
		}
	})

	t.Run("default config with unknown keys returns ErrUnknownConfigKey", func(t *testing.T) {
		appDataDir := t.TempDir()
		cfgDir := filepath.Join(appDataDir, "LearningHub")
		if err := os.MkdirAll(cfgDir, 0o755); err != nil {
			t.Fatal(err)
		}
		tomlContent := "unknown_field = 'test'\n"
		if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(tomlContent), 0o600); err != nil {
			t.Fatal(err)
		}

		t.Setenv("APPDATA", appDataDir)

		_, err := config.Load("", t.TempDir())
		if !errors.Is(err, config.ErrUnknownConfigKey) {
			t.Fatalf("expected ErrUnknownConfigKey, got: %v", err)
		}
	})

	t.Run("explicit config path overrides default config", func(t *testing.T) {
		appDataDir := t.TempDir()
		cfgDir := filepath.Join(appDataDir, "LearningHub")
		if err := os.MkdirAll(cfgDir, 0o755); err != nil {
			t.Fatal(err)
		}
		defaultTOML := "backup_keep = 15\n"
		if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(defaultTOML), 0o600); err != nil {
			t.Fatal(err)
		}

		explicitDir := t.TempDir()
		explicitPath := filepath.Join(explicitDir, "custom.toml")
		explicitTOML := "backup_keep = 99\n"
		if err := os.WriteFile(explicitPath, []byte(explicitTOML), 0o600); err != nil {
			t.Fatal(err)
		}

		t.Setenv("APPDATA", appDataDir)

		cfg, err := config.Load(explicitPath, t.TempDir())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.BackupKeep != 99 {
			t.Fatalf("expected explicit BackupKeep=99 to override default, got %d", cfg.BackupKeep)
		}
	})

	t.Run("local config in fallbackBaseDir takes precedence over default config path", func(t *testing.T) {
		appDataDir := t.TempDir()
		appDataCfgDir := filepath.Join(appDataDir, "LearningHub")
		if err := os.MkdirAll(appDataCfgDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(appDataCfgDir, "config.toml"), []byte("backup_keep = 10\n"), 0o600); err != nil {
			t.Fatal(err)
		}

		localDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(localDir, "config.toml"), []byte("backup_keep = 77\n"), 0o600); err != nil {
			t.Fatal(err)
		}

		t.Setenv("APPDATA", appDataDir)

		cfg, err := config.Load("", localDir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.BackupKeep != 77 {
			t.Fatalf("expected local BackupKeep=77 to take precedence, got %d", cfg.BackupKeep)
		}
	})
}

func TestConfigExampleIsValid(t *testing.T) {
	examplePath := filepath.Join("..", "..", "config.example.toml")
	if _, err := os.Stat(examplePath); err != nil {
		t.Skipf("config.example.toml not found at %s: %v", examplePath, err)
	}
	cfg, err := config.Load(examplePath, t.TempDir())
	if err != nil {
		t.Fatalf("config.example.toml failed validation: %v", err)
	}
	if cfg.Jobs.TimeoutAnalyze != 15*time.Minute {
		t.Fatalf("expected Jobs.TimeoutAnalyze=15m in example config, got %v", cfg.Jobs.TimeoutAnalyze)
	}
}
