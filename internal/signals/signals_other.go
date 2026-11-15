//go:build !unix && !windows

package signals

import (
	"os"
)

// Shutdown contains the fallback OS signals that initiate graceful process shutdown.
var Shutdown = []os.Signal{os.Interrupt}
