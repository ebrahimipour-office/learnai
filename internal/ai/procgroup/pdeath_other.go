//go:build unix && !linux

package procgroup

import "syscall"

func setPdeathsig(*syscall.SysProcAttr) {}
