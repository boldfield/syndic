//go:build darwin && !cgo

package sandbox_test

import (
	"context"
	"os"
	"testing"

	"github.com/boldfield/syndic/internal/sandbox"
)

// TestMacOSSandboxExecEnforcement fails when SYNDIC_SANDBOX_TEST=1 is set
// on a build without cgo, where the backend is unsupported.
func TestMacOSSandboxExecEnforcement(t *testing.T) {
	if os.Getenv("SYNDIC_SANDBOX_TEST") != "1" {
		t.Skip("SYNDIC_SANDBOX_TEST=1 is not set; the macOS enforcement suite is opt-in")
	}
	b := sandbox.NewMacOSSandboxExec(context.Background(), sandbox.Supervisor{}, sandbox.SystemLocations{})
	enforced, reason := b.Capabilities()
	if ok, missing := sandbox.Supported(enforced, true); !ok {
		t.Fatalf("backend unsupported (missing %s): %s", missing, reason)
	}
}
