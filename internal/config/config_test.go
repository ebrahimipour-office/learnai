package config_test

import (
	"errors"
	"testing"

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
