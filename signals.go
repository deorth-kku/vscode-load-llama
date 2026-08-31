package main

import (
	"os"
	"syscall"
)

// signals that trigger a graceful shutdown. On Windows, Go delivers
// both Ctrl+C and Ctrl+Break as os.Interrupt.
var signals = []os.Signal{os.Interrupt, syscall.SIGTERM}
