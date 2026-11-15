//go:build windows

package signals

import (
	"os"
	"syscall"
)

// Shutdown contains the OS signals that initiate graceful process shutdown on Windows.
var Shutdown = []os.Signal{os.Interrupt, syscall.SIGTERM}
