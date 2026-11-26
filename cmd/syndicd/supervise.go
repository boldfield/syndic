package main

import (
	"io"

	"github.com/boldfield/syndic/internal/sandbox"
)

// isSupervise reports whether args, the arguments after the program name,
// invoke the hidden supervisor subcommand. The subcommand is not in the
// command's help: only the engine starts it, with a control descriptor, and
// it refuses to run without one.
func isSupervise(args []string) bool {
	return len(args) > 0 && args[0] == sandbox.SupervisorArg
}

// supervise runs the supervisor of one command (v7 delta, section 3, P).
func supervise(stderr io.Writer) int {
	return sandbox.Supervise(stderr)
}
