package procgroup

import "syscall"

// setPdeathsig makes the kernel SIGKILL the leader when hub dies, even by SIGKILL (T12.2).
// The signal follows the OS thread that started the child; Go keeps its threads alive unless a
// goroutine exits while locked to one, which this program never does.
func setPdeathsig(a *syscall.SysProcAttr) { a.Pdeathsig = syscall.SIGKILL }
