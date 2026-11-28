package main

import (
	"os"
	"path/filepath"

	"github.com/boldfield/syndic/internal/engine"
	"github.com/boldfield/syndic/internal/sandbox"
)

// execOptions is the daemon's wiring of command execution: the supervisor is
// this binary's hidden subcommand, the backend the platform's, and the
// self-test keeps its check-2 file in the engine's state directory. The
// self-test's network fixture is selfTestNetwork, for check 6 under a
// network list.
func execOptions(stateDir string) (engine.ExecOptions, error) {
	exe, err := os.Executable()
	if err != nil {
		return engine.ExecOptions{}, err
	}
	if exe, err = filepath.Abs(exe); err != nil {
		return engine.ExecOptions{}, err
	}
	return engine.ExecOptions{
		Supervisor: sandbox.Supervisor{Command: []string{exe, sandbox.SupervisorArg}},
		NewBackend: platformBackend,
		SelfTest:   sandbox.SelfTestConfig{StateDir: stateDir, Helper: exe, Network: selfTestNetwork},
	}, nil
}
