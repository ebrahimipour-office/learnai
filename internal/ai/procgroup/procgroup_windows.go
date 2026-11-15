//go:build windows

package procgroup

import (
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

type Group struct {
	cmd *exec.Cmd
	mu  sync.Mutex
	job windows.Handle
}

func newGroup(cmd *exec.Cmd) (*Group, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
	}
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(job)
		return nil, err
	}
	return &Group{cmd: cmd, job: job}, nil
}

var beforeAttachForTest func(g *Group)

// attach puts the started child in the job; its later descendants join automatically.
// shortcut: a grandchild spawned between CreateProcess and this call escapes the job; upgrade to CREATE_SUSPENDED + resume if ever seen.
func (g *Group) attach() error {
	if beforeAttachForTest != nil {
		beforeAttachForTest(g)
	}
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(g.cmd.Process.Pid))
	if err != nil {
		return fmt.Errorf("procgroup: open child process %d: %w", g.cmd.Process.Pid, err)
	}
	defer windows.CloseHandle(h)
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.job == 0 {
		return errors.New("procgroup: job object handle is closed")
	}
	if err := windows.AssignProcessToJobObject(g.job, h); err != nil {
		return fmt.Errorf("procgroup: assign child process %d to job: %w", g.cmd.Process.Pid, err)
	}
	return nil
}

// Kill terminates every process in the job, plus the child directly in case attach has not run yet.
func (g *Group) Kill() error {
	g.mu.Lock()
	if g.job != 0 {
		windows.TerminateJobObject(g.job, 1)
	}
	g.mu.Unlock()
	if g.cmd.Process == nil {
		return nil
	}
	return ignoreDone(g.cmd.Process.Kill())
}

// release closes the job handle; KILL_ON_JOB_CLOSE ends any survivor, and the OS does the same if hub dies.
func (g *Group) release() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.job != 0 {
		windows.CloseHandle(g.job)
		g.job = 0
	}
}
