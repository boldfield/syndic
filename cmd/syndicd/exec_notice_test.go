package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/config"
	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/journal"
)

// execNoneRosterTOML grants exec in a review preset under sandbox = "none";
// exec in a solo preset is refused under it.
func execNoneRosterTOML() string {
	r := strings.Replace(enginetest.RosterTOML, `models = ["llama2", "llama3"]`, `models = ["llama2", "llama3", "refmodel"]
referee_model = "refmodel"`, 1)
	return r + `
[presets.execreview]
mode = "review"
author = "author"
required_reviewers = ["reviewer"]
tools = ["read", "exec"]

[tools.exec]
sandbox = "none"
`
}

// startStderr starts syndicd on roster, waits until its start log contains
// want or the deadline passes, stops it and returns the log.
func startStderr(t *testing.T, roster, want string) string {
	t.Helper()
	dir := t.TempDir()
	rosterPath := filepath.Join(dir, "roster.toml")
	journalPath := filepath.Join(dir, "journal.db")
	runtimeDir := filepath.Join(dir, "runtime")
	if err := os.MkdirAll(runtimeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
	t.Setenv("XDG_DATA_HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)
	if err := os.WriteFile(rosterPath, []byte(roster), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.Load(rosterPath)
	if err != nil {
		t.Fatal(err)
	}
	db, err := journal.Open(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	seedProbeRecords(t, journal.NewStore(db), loaded)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	sigChan := make(chan os.Signal, 2)
	var stdout, stderr threadSafeBuffer
	done := make(chan int, 1)
	go func() {
		done <- run([]string{
			"--roster", rosterPath,
			"--journal", journalPath,
			"--secrets-dir", filepath.Join(dir, "secrets"),
		}, &stdout, &stderr, sigChan)
	}()
	// The startup line on stdout comes after the start log.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && !strings.Contains(stdout.String(), rosterPath) && !strings.Contains(stderr.String(), want) {
		time.Sleep(50 * time.Millisecond)
	}
	sigChan <- os.Interrupt
	if code := <-done; code != 0 {
		t.Fatalf("exit code %d; stderr: %s", code, stderr.String())
	}
	return stderr.String()
}

func TestStartLogCarriesSandboxNoneLine(t *testing.T) {
	const line = "exec: sandbox none; filesystem and network confinement are unenforced"
	if got := startStderr(t, execNoneRosterTOML(), line); !strings.Contains(got, line+"\n") {
		t.Fatalf("start log lacks %q: %q", line, got)
	}
}

func TestStartLogOmitsSandboxNoneLineUnderRequired(t *testing.T) {
	const line = "exec: sandbox none"
	if got := startStderr(t, enginetest.RosterTOML, line); strings.Contains(got, line) {
		t.Fatalf("start log carries %q without sandbox = \"none\": %q", line, got)
	}
}
