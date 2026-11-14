package main

import (
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
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
