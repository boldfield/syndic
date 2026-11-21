package main

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestTUIRefusesRedirectedStdout runs the tui command with standard output
// in a file: it exits 2 with a one-line message and writes no control
// sequences to the file.
func TestTUIRefusesRedirectedStdout(t *testing.T) {
	dir := t.TempDir()
	open := func(name string) *os.File {
		f, err := os.Create(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = f.Close() })
		return f
	}
	stdout, stderr := open("stdout"), open("stderr")
	oldOut, oldErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = stdout, stderr
	code := cmdTUI(flag.NewFlagSet("tui", flag.ContinueOnError), nil)
	os.Stdout, os.Stderr = oldOut, oldErr

	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	out, err := os.ReadFile(stdout.Name())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "\x1b") {
		t.Errorf("stdout holds an escape sequence: %q", out)
	}
	msg, err := os.ReadFile(stderr.Name())
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(msg)); strings.Contains(got, "\n") || !strings.Contains(got, "not a terminal") {
		t.Errorf("message = %q, want one line saying standard output is not a terminal", got)
	}
}
