//go:build windows

package antigravity

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"learninghub/internal/ai"
)

// CR-13: On Windows, npm creates claude.cmd and codex.cmd shims.
// LookPath resolving to a .cmd or .bat file must be refused with ErrBatchShimUnsupported.
func TestWindowsBatchShimRefused(t *testing.T) {
	dir := t.TempDir()
	shimPath := filepath.Join(dir, "echo_shim.cmd")
	content := "@echo off\r\necho %*\r\n"
	if err := os.WriteFile(shimPath, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}

	// Prepend dir to PATH so exec.LookPath("echo_shim") finds echo_shim.cmd
	origPath := os.Getenv("PATH")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+origPath)

	a := New(Config{Kind: KindClaude, Binary: "echo_shim"})

	bin, err := a.lookPath()
	if err == nil {
		t.Fatalf("expected batch shim to be refused, got resolved bin: %q", bin)
	}
	if !errors.Is(err, ai.ErrBatchShimUnsupported) {
		t.Fatalf("expected ErrBatchShimUnsupported, got: %v", err)
	}
	if !errors.Is(err, ai.ErrBinaryNotFound) {
		t.Fatalf("expected ErrBinaryNotFound, got: %v", err)
	}
	if !strings.Contains(err.Error(), "Windows batch shims .cmd/.bat are not supported") {
		t.Fatalf("expected actionable hint, got: %v", err)
	}

	// Probe and Run must also be refused
	if _, err := a.Probe(context.Background()); !errors.Is(err, ai.ErrBatchShimUnsupported) {
		t.Fatalf("Probe: expected ErrBatchShimUnsupported, got: %v", err)
	}
	req := ai.Request{
		Prompt:  "line 1\nline 2",
		WorkDir: dir,
	}
	if _, err := a.Run(context.Background(), req, nil); !errors.Is(err, ai.ErrBatchShimUnsupported) {
		t.Fatalf("Run: expected ErrBatchShimUnsupported, got: %v", err)
	}
}
