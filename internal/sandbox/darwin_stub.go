//go:build !darwin || !cgo

package sandbox

import (
	"context"
	"fmt"
	"runtime"
)

// macSupport reports the backend unsupported: it needs macOS, and cgo for
// sandbox_check and libproc.
func macSupport(context.Context, *MacOSSandboxExec) (version, reason string) {
	if runtime.GOOS != "darwin" {
		return "", fmt.Sprintf("%s: runs only on macOS, not %s", MacOSSandboxExecName, runtime.GOOS)
	}
	return "", fmt.Sprintf("%s: this build has cgo disabled; the backend needs it for sandbox_check and libproc", MacOSSandboxExecName)
}
