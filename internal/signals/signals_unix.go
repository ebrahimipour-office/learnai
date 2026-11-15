//go:build unix

package signals

import (
	"os"
	"syscall"
)

// Shutdown contains the OS signals that initiate graceful process shutdown on Unix.
// SIGHUP (terminal closed) shuts down like SIGTERM so running AI process trees are killed (CR-23).
var Shutdown = []os.Signal{os.Interrupt, syscall.SIGTERM, syscall.SIGHUP}
