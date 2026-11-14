package procgroup

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"testing"
	"time"
)

// The test binary doubles as the child ("parent" role) and the grandchild.
func TestMain(m *testing.M) {
	switch os.Getenv("PROCGROUP_ROLE") {
	case "grandchild":
		signal.Ignore(syscall.SIGTERM) // forces the SIGKILL escalation on unix
		fmt.Println("ready")
		time.Sleep(time.Hour)
		os.Exit(0)
	case "exit":
		os.Exit(0)
	case "parent", "parent-exit":
		gc := exec.Command(os.Args[0])
		gc.Env = append(os.Environ(), "PROCGROUP_ROLE=grandchild")
		gc.Stdout = os.Stdout // grandchild holds the pipe: EOF on the test side means it is dead
		if err := gc.Start(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		if os.Getenv("PROCGROUP_ROLE") == "parent-exit" {
			time.Sleep(200 * time.Millisecond) // let the grandchild report ready first
			os.Exit(0)                         // leave the grandchild orphaned
		}
		time.Sleep(time.Hour)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// startTree starts parent+grandchild and returns the read end of their shared stdout.
func startTree(t *testing.T, ctx context.Context, role string) (*exec.Cmd, *Group, *os.File) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	cmd := exec.CommandContext(ctx, os.Args[0])
	cmd.Env = append(os.Environ(), "PROCGROUP_ROLE="+role)
	cmd.Stdout = w
	cmd.Stderr = os.Stderr
	g, err := Start(cmd)
	w.Close()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { g.Close() })
	buf := make([]byte, 6)
	if _, err := io.ReadFull(r, buf); err != nil || string(buf) != "ready\n" {
		t.Fatalf("tree did not start: %q %v", buf, err)
	}
	return cmd, g, r
}

// waitEOF fails unless every holder of the pipe (parent and grandchild) is gone within d.
func waitEOF(t *testing.T, r *os.File, d time.Duration) {
	t.Helper()
	done := make(chan error, 1)
	go func() { _, err := io.Copy(io.Discard, r); done <- err }()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("process tree still alive after %s", d)
	}
}

func TestCancelKillsTree(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cmd, _, r := startTree(t, ctx, "parent")
	start := time.Now()
	cancel()
	waitErr := make(chan error, 1)
	go func() { waitErr <- cmd.Wait() }()
	waitEOF(t, r, 5*time.Second)
	select {
	case <-waitErr:
	case <-time.After(5*time.Second - time.Since(start)):
		t.Fatal("Wait did not return within 5s")
	}
}

func TestCloseKillsOrphans(t *testing.T) {
	cmd, g, r := startTree(t, context.Background(), "parent-exit")
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	g.Close()
	waitEOF(t, r, 5*time.Second)
}
