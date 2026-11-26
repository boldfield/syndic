//go:build !darwin || !cgo

package sandbox

import (
	"context"
	"strings"
	"testing"
)

func TestMacOSSandboxExecStubUnsupported(t *testing.T) {
	b := NewMacOSSandboxExec(context.Background(), Supervisor{}, SystemLocations{})
	enforced, reason := b.Capabilities()
	if enforced != 0 {
		t.Errorf("enforced %v, want none", enforced)
	}
	explained := strings.Contains(reason, "only on macOS") || strings.Contains(reason, "cgo disabled")
	if !strings.HasPrefix(reason, "macos-sandbox-exec: ") || !explained {
		t.Errorf("reason %q, want the backend named and why", reason)
	}
	if b.Name() != "macos-sandbox-exec" {
		t.Errorf("Name() = %q", b.Name())
	}
	if _, err := b.Start(context.Background(), Spec{Argv: []string{"true"}}); err == nil || !strings.Contains(err.Error(), reason) {
		t.Errorf("Start: %v, want the reason", err)
	}
}
