package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"learninghub/internal/store"

	"github.com/gofrs/flock"
)

// CR-44: a fatal serve error (port already taken) must exit 1, not 0.
func TestServeExitsNonZeroWhenPortTaken(t *testing.T) {
	if os.Getenv("HUB_TEST_SERVE") == "1" {
		os.Args = []string{"hub", "serve", "--config", os.Getenv("HUB_TEST_CONFIG")}
		main()
		os.Exit(0)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.toml")
	body := "base_dir = '" + dir + "'\nlisten = '" + ln.Addr().String() + "'\n"
	if err := os.WriteFile(cfg, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestServeExitsNonZeroWhenPortTaken$")
	cmd.Env = append(os.Environ(), "HUB_TEST_SERVE=1", "HUB_TEST_CONFIG="+cfg)
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 {
		t.Fatalf("want exit status 1, got %v\n%s", err, out)
	}
}

// CR-39: hub migrate must refuse to run while hub.lock is held and exit with status 1.
func TestMigrateExitsNonZeroWhenLocked(t *testing.T) {
	if os.Getenv("HUB_TEST_MIGRATE_LOCKED") == "1" {
		os.Args = []string{"hub", "migrate", "--config", os.Getenv("HUB_TEST_CONFIG")}
		main()
		os.Exit(0)
	}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".app"), 0o755); err != nil {
		t.Fatal(err)
	}
	lockFile := filepath.Join(dir, ".app", "hub.lock")
	lock := flock.New(lockFile)
	locked, err := lock.TryLock()
	if err != nil || !locked {
		t.Fatalf("failed to acquire test lock: %v (locked=%v)", err, locked)
	}
	defer lock.Unlock()

	cfg := filepath.Join(dir, "config.toml")
	body := "base_dir = '" + dir + "'\n"
	if err := os.WriteFile(cfg, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestMigrateExitsNonZeroWhenLocked$")
	cmd.Env = append(os.Environ(), "HUB_TEST_MIGRATE_LOCKED=1", "HUB_TEST_CONFIG="+cfg)
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 {
		t.Fatalf("want exit status 1, got %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "LearningHub is running; stop it before migrating") {
		t.Fatalf("expected output to mention LearningHub is running, got: %s", out)
	}
}

// CR-39: hub migrate succeeds and reports schema status when lock is not held.
func TestMigrateSucceedsWhenUnlocked(t *testing.T) {
	if os.Getenv("HUB_TEST_MIGRATE_OK") == "1" {
		os.Args = []string{"hub", "migrate", "--config", os.Getenv("HUB_TEST_CONFIG")}
		main()
		os.Exit(0)
	}
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.toml")
	body := "base_dir = '" + dir + "'\n"
	if err := os.WriteFile(cfg, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestMigrateSucceedsWhenUnlocked$")
	cmd.Env = append(os.Environ(), "HUB_TEST_MIGRATE_OK=1", "HUB_TEST_CONFIG="+cfg)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("expected migrate to succeed, got %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "Database schema is up to date") {
		t.Fatalf("expected output to contain up to date message, got: %s", out)
	}
}

// CR-39: hub migrate must fail and exit 1 if the database user_version is newer than this binary.
func TestMigrateExitsNonZeroWhenNewerSchema(t *testing.T) {
	if os.Getenv("HUB_TEST_MIGRATE_NEWER") == "1" {
		os.Args = []string{"hub", "migrate", "--config", os.Getenv("HUB_TEST_CONFIG")}
		main()
		os.Exit(0)
	}
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "app.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Writer().Exec("PRAGMA user_version = 99;"); err != nil {
		t.Fatal(err)
	}
	db.Close()

	cfg := filepath.Join(dir, "config.toml")
	body := "base_dir = '" + dir + "'\n"
	if err := os.WriteFile(cfg, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestMigrateExitsNonZeroWhenNewerSchema$")
	cmd.Env = append(os.Environ(), "HUB_TEST_MIGRATE_NEWER=1", "HUB_TEST_CONFIG="+cfg)
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 {
		t.Fatalf("want exit status 1, got %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "database schema v99 is newer than this hub") {
		t.Fatalf("expected output to mention newer schema, got: %s", out)
	}
}

// CR-40: hub serve must fail with exit status 1 when an explicit --config file is missing.
func TestServeExitsNonZeroWhenConfigMissing(t *testing.T) {
	if os.Getenv("HUB_TEST_CONFIG_MISSING") == "1" {
		os.Args = []string{"hub", "serve", "--config", os.Getenv("HUB_TEST_CONFIG")}
		main()
		os.Exit(0)
	}
	missingCfg := filepath.Join(t.TempDir(), "missing_config.toml")
	cmd := exec.Command(os.Args[0], "-test.run=^TestServeExitsNonZeroWhenConfigMissing$")
	cmd.Env = append(os.Environ(), "HUB_TEST_CONFIG_MISSING=1", "HUB_TEST_CONFIG="+missingCfg)
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 {
		t.Fatalf("want exit status 1, got %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "reading config file") {
		t.Fatalf("expected error output about reading config file, got: %s", out)
	}
}

// CR-41: CLI commands without --config must load default config if present in APPDATA/XDG.
func TestCR41_CLIAppliesDefaultConfigWhenConfigOmitted(t *testing.T) {
	if os.Getenv("HUB_TEST_DEFAULT_CONFIG_INVALID") == "1" {
		os.Args = []string{"hub", "doctor"}
		main()
		os.Exit(0)
	}
	if os.Getenv("HUB_TEST_DEFAULT_CONFIG_MIGRATE") == "1" {
		os.Args = []string{"hub", "migrate"}
		main()
		os.Exit(0)
	}

	t.Run("invalid default config causes doctor to fail with exit status 1", func(t *testing.T) {
		appDataDir := t.TempDir()
		cfgDir := filepath.Join(appDataDir, "LearningHub")
		if err := os.MkdirAll(cfgDir, 0o755); err != nil {
			t.Fatal(err)
		}
		badTOML := "unknown_cli_key = 'fail'\n"
		if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(badTOML), 0o600); err != nil {
			t.Fatal(err)
		}

		cmd := exec.Command(os.Args[0], "-test.run=^TestCR41_CLIAppliesDefaultConfigWhenConfigOmitted$")
		cmd.Env = append(os.Environ(), "HUB_TEST_DEFAULT_CONFIG_INVALID=1", "APPDATA="+appDataDir)
		out, err := cmd.CombinedOutput()
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 1 {
			t.Fatalf("want exit status 1, got %v\n%s", err, out)
		}
		if !strings.Contains(string(out), "unknown keys") {
			t.Fatalf("expected error mentioning unknown keys from default config, got: %s", out)
		}
	})

	t.Run("valid default config base_dir is used by migrate", func(t *testing.T) {
		appDataDir := t.TempDir()
		baseDir := t.TempDir()
		cfgDir := filepath.Join(appDataDir, "LearningHub")
		if err := os.MkdirAll(cfgDir, 0o755); err != nil {
			t.Fatal(err)
		}
		validTOML := fmt.Sprintf("base_dir = %q\n", baseDir)
		if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(validTOML), 0o600); err != nil {
			t.Fatal(err)
		}

		cmd := exec.Command(os.Args[0], "-test.run=^TestCR41_CLIAppliesDefaultConfigWhenConfigOmitted$")
		cmd.Env = append(os.Environ(), "HUB_TEST_DEFAULT_CONFIG_MIGRATE=1", "APPDATA="+appDataDir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("migrate with default config failed: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), "user_version =") {
			t.Fatalf("expected migrate success output, got: %s", out)
		}
		// Confirm app.db was created in baseDir from default config
		if _, err := os.Stat(filepath.Join(baseDir, "app.db")); err != nil {
			t.Fatalf("expected app.db to be created in baseDir %s: %v", baseDir, err)
		}
	})
}
