//go:build windows

package procgroup

import (
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"syscall"
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
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	// The child starts suspended and runs only once it is in the job, so nothing it spawns can escape (T12.2).
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_SUSPENDED
	return &Group{cmd: cmd, job: job}, nil
}

var beforeAttachForTest func(g *Group)

// attach puts the suspended child in the job, then lets it run; its descendants join automatically.
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
	return resume(uint32(g.cmd.Process.Pid))
}

// resume starts the threads of a process created with CREATE_SUSPENDED. os/exec closes the thread
// handle CreateProcess returns, so the threads are found with a Toolhelp snapshot.
func resume(pid uint32) error {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return fmt.Errorf("procgroup: thread snapshot: %w", err)
	}
	defer windows.CloseHandle(snap)
	te := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	resumed := 0
	for err = windows.Thread32First(snap, &te); err == nil; err = windows.Thread32Next(snap, &te) {
		if te.OwnerProcessID != pid {
			continue
		}
		h, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, te.ThreadID)
		if err != nil {
			return fmt.Errorf("procgroup: open thread %d: %w", te.ThreadID, err)
		}
		_, err = windows.ResumeThread(h)
		windows.CloseHandle(h)
		if err != nil {
			return fmt.Errorf("procgroup: resume thread %d: %w", te.ThreadID, err)
		}
		resumed++
	}
	if resumed == 0 {
		return fmt.Errorf("procgroup: no thread found for process %d", pid)
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
