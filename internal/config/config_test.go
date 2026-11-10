package config_test

import (
	"errors"
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
