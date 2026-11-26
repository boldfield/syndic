package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/boldfield/syndic/internal/sandbox"
)

// TestMain dispatches the hidden subcommand as main does, so that the test
// binary run with it behaves as syndicd would.
func TestMain(m *testing.M) {
	if isSupervise(os.Args[1:]) {
		os.Exit(supervise(os.Stderr))
	}
	os.Exit(m.Run())
}

func TestSuperviseRefusesWithoutControlDescriptor(t *testing.T) {
	cmd := exec.Command(os.Args[0], sandbox.SupervisorArg)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 2 {
		t.Fatalf("%s without a control descriptor: %v, want exit status 2; stderr %q", sandbox.SupervisorArg, err, stderr.String())
	}
	if !strings.Contains(stderr.String(), "internal to the engine") {
		t.Errorf("stderr %q", stderr.String())
	}
}

func TestSuperviseNotInHelp(t *testing.T) {
	var stdout, stderr threadSafeBuffer
	sigChan := make(chan os.Signal)
	defer close(sigChan)
	if code := run([]string{"-h"}, &stdout, &stderr, sigChan); code != 2 {
		t.Fatalf("-h: exit code %d", code)
	}
	help := stdout.String() + stderr.String()
	if !strings.Contains(help, "-roster") {
		t.Fatalf("help text not printed: %q", help)
	}
	if strings.Contains(help, sandbox.SupervisorArg) || strings.Contains(help, "supervis") {
		t.Errorf("help mentions the hidden subcommand: %q", help)
	}
}
