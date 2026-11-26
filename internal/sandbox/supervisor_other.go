//go:build !linux

package sandbox

import "syscall"

// setParentDeathSignal does nothing: only Linux has a parent-death signal.
// The control pipe's end of file covers the engine's death everywhere.
func setParentDeathSignal(*syscall.SysProcAttr, syscall.Signal) {}
