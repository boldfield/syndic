//go:build linux

package main

import (
	"context"

	"github.com/boldfield/syndic/internal/sandbox"
)

// platformBackend is the sandbox backend of this platform (v7 delta, section 3).
func platformBackend(ctx context.Context, sup sandbox.Supervisor, sys sandbox.SystemLocations) sandbox.Backend {
	return sandbox.NewLinuxNamespaces(ctx, sup, sys)
}
