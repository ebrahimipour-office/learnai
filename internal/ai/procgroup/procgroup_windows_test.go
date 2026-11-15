//go:build windows

package procgroup

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"testing"

	"golang.org/x/sys/windows"
)

// CR-24: if attaching to the Windows Job Object fails (e.g. closed job handle),
// Start must fail, clean up child process, and leave no process running.
func TestStartFailsOnClosedJobHandle(t *testing.T) {
	beforeAttachForTest = func(g *Group) {
		g.mu.Lock()
		defer g.mu.Unlock()
		if g.job != 0 {
			_ = windows.CloseHandle(g.job)
			// Keep g.job with the now-invalid handle so AssignProcessToJobObject
			// will be called with it and return ERROR_INVALID_HANDLE.
		}
	}
	defer func() {
		beforeAttachForTest = nil
	}()

	cmd := exec.CommandContext(context.Background(), os.Args[0])
	cmd.Env = append(os.Environ(), "PROCGROUP_ROLE=grandchild")

	g, err := Start(cmd)
	if err == nil {
		if g != nil {
			g.Close()
		}
		t.Fatal("Start succeeded, want error due to closed job handle")
	}

	if g != nil {
		t.Fatalf("Start returned group %v, want nil on error", g)
	}

	if !errors.Is(err, windows.ERROR_INVALID_HANDLE) {
		t.Fatalf("Start error = %v, want ERROR_INVALID_HANDLE", err)
	}

	// Verify child process was killed and reaped; no running process remains.
	if cmd.Process == nil {
		t.Fatal("cmd.Process is nil")
	}
	pid := cmd.Process.Pid
	if cmd.ProcessState == nil || !cmd.ProcessState.Exited() {
		t.Fatalf("child process %d was not waited on / did not exit", pid)
	}

	// Double check with OS handle that process is terminated
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, uint32(pid))
	if err == nil {
		defer windows.CloseHandle(h)
		ev, waitErr := windows.WaitForSingleObject(h, 2000)
		if waitErr != nil || ev != windows.WAIT_OBJECT_0 {
			t.Fatalf("child process %d is still running: ev=%d, err=%v", pid, ev, waitErr)
		}
	}
}

func TestStartFailsOnZeroJobHandle(t *testing.T) {
	beforeAttachForTest = func(g *Group) {
		g.mu.Lock()
		defer g.mu.Unlock()
		if g.job != 0 {
			_ = windows.CloseHandle(g.job)
			g.job = 0
		}
	}
	defer func() {
		beforeAttachForTest = nil
	}()

	cmd := exec.CommandContext(context.Background(), os.Args[0])
	cmd.Env = append(os.Environ(), "PROCGROUP_ROLE=grandchild")

	g, err := Start(cmd)
	if err == nil {
		if g != nil {
			g.Close()
		}
		t.Fatal("Start succeeded, want error due to zero job handle")
	}

	if g != nil {
		t.Fatalf("Start returned group %v, want nil on error", g)
	}

	if cmd.ProcessState == nil || !cmd.ProcessState.Exited() {
		t.Fatalf("child process was not waited on")
	}
}
