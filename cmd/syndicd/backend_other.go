//go:build !linux

package main

import (
	"context"
	"runtime"

	"github.com/boldfield/syndic/internal/sandbox"
)

// platformBackend is the sandbox backend of this platform (v7 delta, section 3);
// nil where there is none, and exec is then unoffered under sandbox = "required".
func platformBackend(ctx context.Context, sup sandbox.Supervisor, sys sandbox.SystemLocations) sandbox.Backend {
	if runtime.GOOS == "darwin" {
		return sandbox.NewMacOSSandboxExec(ctx, sup, sys)
	}
	return nil
}

// fixtureUnix is false here: the call's proxy listener is a loopback TCP
// listener, named by its own address.
const (
	fixtureUnix      = false
	fixtureProxyAddr = ""
)
