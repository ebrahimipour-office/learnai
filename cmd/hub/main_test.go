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

func TestMain(m *testing.M) {
	if os.Getenv("HUB_MOCK_CLAUDE_PROBE") == "1" && len(os.Args) > 1 && !strings.HasPrefix(os.Args[1], "-test.") {
		args := os.Args[1:]
		if len(args) > 0 && args[0] == "--version" {
			fmt.Println("claude-custom-probe-marker 1.2.3")
			os.Exit(0)
		}
		if len(args) > 0 && args[0] == "--help" {
			fmt.Println("--output-format stream-json --json-schema")
			os.Exit(0)
		}
		if len(args) > 1 && args[0] == "auth" && args[1] == "status" {
			fmt.Println(`{"loggedIn": true}`)
			os.Exit(0)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

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

// CR-43: hub doctor probes the saved adapter in the database (meta.adapter_kind)
// and never modifies an existing .doctor_test file in base_dir.
func TestCR43_DoctorProbesSelectedAdapterAndPreservesExistingTestFile(t *testing.T) {
	if os.Getenv("HUB_TEST_DOCTOR_CLAUDE") == "1" {
		os.Args = []string{"hub", "doctor", "--config", os.Getenv("HUB_TEST_CONFIG")}
		main()
		os.Exit(0)
	}

	dir := t.TempDir()
	appDir := filepath.Join(dir, ".app")
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(appDir, "app.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Writer().Exec(`INSERT INTO meta (key, value) VALUES ('adapter_kind', 'claude')`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	// Place an existing .doctor_test file in baseDir to assert it is never overwritten or removed.
	existingTestFile := filepath.Join(dir, ".doctor_test")
	existingContent := "CR43_DO_NOT_OVERWRITE_THIS_FILE"
	if err := os.WriteFile(existingTestFile, []byte(existingContent), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := filepath.Join(dir, "config.toml")
	selfBin, err := os.Executable()
	if err != nil {
		selfBin = os.Args[0]
	}
	body := fmt.Sprintf(`base_dir = %q
[adapter]
kind = "antigravity"
[adapter.claude]
binary = %q
`, dir, selfBin)
	if err := os.WriteFile(cfg, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestCR43_DoctorProbesSelectedAdapterAndPreservesExistingTestFile$")
	cmd.Env = append(os.Environ(),
		"HUB_TEST_DOCTOR_CLAUDE=1",
		"HUB_MOCK_CLAUDE_PROBE=1",
		"HUB_TEST_CONFIG="+cfg,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("expected doctor to succeed, got %v\n%s", err, out)
	}

	// 1. Verify doctor probed claude (version output contains marker from mock claude)
	if !strings.Contains(string(out), "claude-custom-probe-marker 1.2.3") {
		t.Fatalf("expected doctor output to probe claude and show mock marker, got:\n%s", out)
	}

	// 2. Verify existing .doctor_test was untouched
	gotContent, err := os.ReadFile(existingTestFile)
	if err != nil {
		t.Fatalf("reading .doctor_test: %v", err)
	}
	if string(gotContent) != existingContent {
		t.Fatalf(".doctor_test was modified: got %q, want %q", string(gotContent), existingContent)
	}
}

// CR-56: hub version prints version, date, runtime OS/Arch, and VCS revision when available.
func TestCR56_VersionFormat(t *testing.T) {
	gotClean := formatVersion("1.0.0", "2026-11-15", "linux", "amd64", "abc123456789", false)
	wantClean := "LearningHub CLI version 1.0.0 (rev abc123456789, 2026-11-15, linux/amd64)"
	if gotClean != wantClean {
		t.Errorf("got %q, want %q", gotClean, wantClean)
	}

	gotDirty := formatVersion("1.0.0", "2026-11-15", "linux", "amd64", "abc123456789", true)
	wantDirty := "LearningHub CLI version 1.0.0 (rev abc123456789+dirty, 2026-11-15, linux/amd64)"
	if gotDirty != wantDirty {
		t.Errorf("got %q, want %q", gotDirty, wantDirty)
	}

	gotNoRev := formatVersion("1.0.0", "2026-11-15", "linux", "amd64", "", false)
	wantNoRev := "LearningHub CLI version 1.0.0 (2026-11-15, linux/amd64)"
	if gotNoRev != wantNoRev {
		t.Errorf("got %q, want %q", gotNoRev, wantNoRev)
	}
}

func TestCR56_VersionSubcommand(t *testing.T) {
	if os.Getenv("HUB_TEST_VERSION") == "1" {
		os.Args = []string{"hub", "version"}
		main()
		os.Exit(0)
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestCR56_VersionSubcommand$")
	cmd.Env = append(os.Environ(), "HUB_TEST_VERSION=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("expected version to succeed: %v\n%s", err, out)
	}
	s := strings.TrimSpace(string(out))
	if !strings.HasPrefix(s, "LearningHub CLI version ") {
		t.Errorf("unexpected output prefix: %s", s)
	}
	if !strings.Contains(s, "linux/amd64") && !strings.Contains(s, "windows/amd64") {
		t.Errorf("expected OS/Arch in output, got: %s", s)
	}
}

// CR-56: If HUB_VERIFY_DIST=1, verify that dist/ binaries match clean git HEAD and INSTALL.md is synced.
func TestCR56_DistArtifactsVerification(t *testing.T) {
	if os.Getenv("HUB_VERIFY_DIST") != "1" {
		t.Skip("skipping dist artifacts verification; set HUB_VERIFY_DIST=1 to run")
	}

	distDir := filepath.Join("..", "..", "dist")
	distHub := filepath.Join(distDir, "hub")
	distExe := filepath.Join(distDir, "hub.exe")
	distInstall := filepath.Join(distDir, "INSTALL.md")
	origInstall := filepath.Join("..", "..", "INSTALL.md")

	for _, p := range []string{distHub, distExe, distInstall} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("missing dist artifact %s: %v", p, err)
		}
	}

	headBytes, err := exec.Command("git", "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("git rev-parse HEAD failed: %v", err)
	}
	head := strings.TrimSpace(string(headBytes))

	for _, bin := range []string{distHub, distExe} {
		out, err := exec.Command("go", "version", "-m", bin).CombinedOutput()
		if err != nil {
			t.Fatalf("go version -m %s failed: %v\n%s", bin, err, out)
		}
		s := string(out)
		if strings.Contains(s, "vcs.modified=true") {
			t.Errorf("%s was built from dirty tree (vcs.modified=true)", bin)
		}
		if !strings.Contains(s, "vcs.revision="+head) {
			t.Errorf("%s revision does not match HEAD %s\n%s", bin, head, s)
		}
	}

	origBytes, err := os.ReadFile(origInstall)
	if err != nil {
		t.Fatalf("reading %s: %v", origInstall, err)
	}
	distBytes, err := os.ReadFile(distInstall)
	if err != nil {
		t.Fatalf("reading %s: %v", distInstall, err)
	}
	if string(origBytes) != string(distBytes) {
		t.Errorf("dist/INSTALL.md does not match %s", origInstall)
	}
}
