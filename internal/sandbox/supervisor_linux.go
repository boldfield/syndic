package sandbox

import "syscall"

// setParentDeathSignal asks the kernel to send sig to the started process
// when its parent dies.
func setParentDeathSignal(attr *syscall.SysProcAttr, sig syscall.Signal) {
	attr.Pdeathsig = sig
}
