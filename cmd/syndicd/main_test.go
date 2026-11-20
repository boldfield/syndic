package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/config"
	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/evidence"
	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/transport"
)

func TestVersion(t *testing.T) {
	var stdout, stderr bytes.Buffer
	sigChan := make(chan os.Signal)
	defer close(sigChan)

	exitCode := run([]string{"--version"}, &stdout, &stderr, sigChan)

	if exitCode != 0 {
		t.Fatalf("want exit code 0, got %d", exitCode)
	}

	output := stdout.String()
	if !strings.Contains(output, "syndicd version") {
		t.Fatalf("want 'syndicd version', got %q", output)
	}
}

func TestNormalStart(t *testing.T) {
	dir := t.TempDir()
	rosterPath := filepath.Join(dir, "roster.toml")
	journalPath := filepath.Join(dir, "journal.db")
	runtimeDir := filepath.Join(dir, "runtime")
	if err := os.MkdirAll(runtimeDir, 0700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(rosterPath, []byte(enginetest.RosterTOML), 0o600); err != nil {
		t.Fatal(err)
	}

	roster, err := config.Load(rosterPath)
	if err != nil {
		t.Fatal(err)
	}

	db, err := journal.Open(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = db.Close()
	}()

	store := journal.NewStore(db)

	// Seed probe records
	seedProbeRecords(t, store, roster)

	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	// Start syndicd in a goroutine
	sigChan := make(chan os.Signal, 2)
	var stdout, stderr bytes.Buffer

	go func() {
		time.Sleep(100 * time.Millisecond)
		sigChan <- os.Interrupt
	}()

	if err := os.Setenv("XDG_RUNTIME_DIR", runtimeDir); err != nil {
		t.Fatal(err)
	}
	if err := os.Setenv("XDG_DATA_HOME", dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Setenv("XDG_CONFIG_HOME", dir); err != nil {
		t.Fatal(err)
	}

	exitCode := run(
		[]string{
			"--roster", rosterPath,
			"--journal", journalPath,
			"--secrets-dir", filepath.Join(dir, "secrets"),
		},
		&stdout,
		&stderr,
		sigChan,
	)

	if exitCode != 0 {
		t.Fatalf("want exit code 0, got %d; stderr: %s", exitCode, stderr.String())
	}

	// Check that startup info was printed
	output := stdout.String()
	parts := strings.Fields(output)
	if len(parts) < 3 {
		t.Fatalf("want at least 3 fields in startup info, got %d: %q", len(parts), output)
	}

	socketPath := parts[0]
	runID := parts[1]

	if !strings.Contains(socketPath, "engine.sock") {
		t.Fatalf("want socket path to contain 'engine.sock', got %q", socketPath)
	}

	if runID == "" {
		t.Fatal("want non-empty run ID")
	}
}

func TestSecondRun(t *testing.T) {
	dir := t.TempDir()
	rosterPath := filepath.Join(dir, "roster.toml")
	journalPath := filepath.Join(dir, "journal.db")
	runtimeDir := filepath.Join(dir, "runtime")
	if err := os.MkdirAll(runtimeDir, 0700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(rosterPath, []byte(enginetest.RosterTOML), 0o600); err != nil {
		t.Fatal(err)
	}

	roster, err := config.Load(rosterPath)
	if err != nil {
		t.Fatal(err)
	}

	db, err := journal.Open(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = db.Close()
	}()

	store := journal.NewStore(db)

	// Seed probe records
	seedProbeRecords(t, store, roster)

	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	if err := os.Setenv("XDG_RUNTIME_DIR", runtimeDir); err != nil {
		t.Fatal(err)
	}
	if err := os.Setenv("XDG_DATA_HOME", dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Setenv("XDG_CONFIG_HOME", dir); err != nil {
		t.Fatal(err)
	}

	// Start first instance
	sigChan1 := make(chan os.Signal, 2)
	var stdout1, stderr1 bytes.Buffer

	firstDone := make(chan int, 1)
	go func() {
		exitCode := run(
			[]string{
				"--roster", rosterPath,
				"--journal", journalPath,
				"--secrets-dir", filepath.Join(dir, "secrets"),
			},
			&stdout1,
			&stderr1,
			sigChan1,
		)
		firstDone <- exitCode
	}()

	// Give the first instance time to start
	time.Sleep(500 * time.Millisecond)

	// Try to start a second instance
	sigChan2 := make(chan os.Signal)
	var stdout2, stderr2 bytes.Buffer

	exitCode := run(
		[]string{
			"--roster", rosterPath,
			"--journal", journalPath,
			"--secrets-dir", filepath.Join(dir, "secrets"),
		},
		&stdout2,
		&stderr2,
		sigChan2,
	)

	if exitCode != 3 {
		t.Fatalf("want exit code 3, got %d", exitCode)
	}

	// Check that stderr mentions PID
	if !strings.Contains(stderr2.String(), "PID") {
		t.Fatalf("want stderr to mention PID, got %q", stderr2.String())
	}

	// Stop the first instance
	sigChan1 <- os.Interrupt
	firstExitCode := <-firstDone

	if firstExitCode != 0 {
		t.Fatalf("first instance: want exit code 0, got %d; stderr: %s", firstExitCode, stderr1.String())
	}
}

func TestBadRoster(t *testing.T) {
	dir := t.TempDir()
	rosterPath := filepath.Join(dir, "roster.toml")
	journalPath := filepath.Join(dir, "journal.db")
	runtimeDir := filepath.Join(dir, "runtime")
	if err := os.MkdirAll(runtimeDir, 0700); err != nil {
		t.Fatal(err)
	}

	// Write an invalid roster (bad default_preset)
	badRoster := strings.Replace(
		enginetest.RosterTOML,
		`default_preset = "review"`,
		`default_preset = "nope"`,
		1,
	)

	if err := os.WriteFile(rosterPath, []byte(badRoster), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.Setenv("XDG_RUNTIME_DIR", runtimeDir); err != nil {
		t.Fatal(err)
	}
	if err := os.Setenv("XDG_DATA_HOME", dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Setenv("XDG_CONFIG_HOME", dir); err != nil {
		t.Fatal(err)
	}

	sigChan := make(chan os.Signal)
	defer close(sigChan)

	var stdout, stderr bytes.Buffer

	exitCode := run(
		[]string{
			"--roster", rosterPath,
			"--journal", journalPath,
			"--secrets-dir", filepath.Join(dir, "secrets"),
		},
		&stdout,
		&stderr,
		sigChan,
	)

	if exitCode != 2 {
		t.Fatalf("want exit code 2, got %d", exitCode)
	}

	// Check that validation errors were printed
	if stderr.String() == "" {
		t.Fatal("want stderr with validation errors")
	}

	// Check that lock can be acquired and no engine run was written
	db, err := journal.Open(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = db.Close()
	}()

	var runs int
	if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM runs WHERE kind = 'engine'`).Scan(&runs); err != nil {
		t.Fatal(err)
	}

	if runs != 0 {
		t.Fatalf("want 0 engine runs, got %d", runs)
	}
}

func TestCliParticipantFails(t *testing.T) {
	dir := t.TempDir()
	rosterPath := filepath.Join(dir, "roster.toml")
	journalPath := filepath.Join(dir, "journal.db")
	runtimeDir := filepath.Join(dir, "runtime")
	if err := os.MkdirAll(runtimeDir, 0700); err != nil {
		t.Fatal(err)
	}

	// Create a roster with a CLI participant that has a binary field
	cliRoster := strings.Replace(
		enginetest.RosterTOML,
		`route = "api"`,
		`route = "cli"
binary = "/usr/bin/cli"`,
		1,
	)
	// Also remove the endpoint reference since CLI participants don't have endpoints
	cliRoster = strings.Replace(
		cliRoster,
		`endpoint       = "openai"`,
		``,
		1,
	)

	if err := os.WriteFile(rosterPath, []byte(cliRoster), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.Setenv("XDG_RUNTIME_DIR", runtimeDir); err != nil {
		t.Fatal(err)
	}
	if err := os.Setenv("XDG_DATA_HOME", dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Setenv("XDG_CONFIG_HOME", dir); err != nil {
		t.Fatal(err)
	}

	sigChan := make(chan os.Signal)
	defer close(sigChan)

	var stdout, stderr bytes.Buffer

	exitCode := run(
		[]string{
			"--roster", rosterPath,
			"--journal", journalPath,
			"--secrets-dir", filepath.Join(dir, "secrets"),
		},
		&stdout,
		&stderr,
		sigChan,
	)

	if exitCode != 1 {
		t.Fatalf("want exit code 1, got %d; stderr: %s", exitCode, stderr.String())
	}

	stderrOutput := stderr.String()
	if !strings.Contains(stderrOutput, "unsupported route") {
		t.Fatalf("want stderr to mention unsupported route, got %q", stderrOutput)
	}
}

func TestStaleSocket(t *testing.T) {
	dir := t.TempDir()
	rosterPath := filepath.Join(dir, "roster.toml")
	journalPath := filepath.Join(dir, "journal.db")
	runtimeDir := filepath.Join(dir, "runtime")
	if err := os.MkdirAll(runtimeDir, 0700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(rosterPath, []byte(enginetest.RosterTOML), 0o600); err != nil {
		t.Fatal(err)
	}

	roster, err := config.Load(rosterPath)
	if err != nil {
		t.Fatal(err)
	}

	db, err := journal.Open(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = db.Close()
	}()

	store := journal.NewStore(db)
	seedProbeRecords(t, store, roster)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	// Create a stale socket file
	syndirPath := filepath.Join(runtimeDir, "syndic")
	if err := os.MkdirAll(syndirPath, 0700); err != nil {
		t.Fatal(err)
	}
	staleSocket := filepath.Join(syndirPath, "engine.sock")
	if err := os.WriteFile(staleSocket, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.Setenv("XDG_RUNTIME_DIR", runtimeDir); err != nil {
		t.Fatal(err)
	}
	if err := os.Setenv("XDG_DATA_HOME", dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Setenv("XDG_CONFIG_HOME", dir); err != nil {
		t.Fatal(err)
	}

	sigChan := make(chan os.Signal, 2)
	var stdout, stderr bytes.Buffer

	done := make(chan int, 1)
	go func() {
		exitCode := run(
			[]string{
				"--roster", rosterPath,
				"--journal", journalPath,
				"--secrets-dir", filepath.Join(dir, "secrets"),
			},
			&stdout,
			&stderr,
			sigChan,
		)
		done <- exitCode
	}()

	// Give the daemon time to start and create the socket
	time.Sleep(500 * time.Millisecond)

	// Stop the daemon
	sigChan <- os.Interrupt

	exitCode := <-done

	if exitCode != 0 {
		t.Fatalf("want exit code 0, got %d; stderr: %s", exitCode, stderr.String())
	}

	// Just verify the stale socket was handled without error
	// The socket will be recreated during start
}

func seedProbeRecords(t *testing.T, store *journal.Store, roster *config.Roster) {
	t.Helper()
	seeded := map[[2]string]bool{}
	for _, p := range roster.Participants {
		if p.Route != "api" {
			continue
		}

		key := [2]string{p.Endpoint, p.Model}
		if seeded[key] {
			continue
		}
		seeded[key] = true

		var profile config.EndpointProfile
		for _, ep := range roster.Endpoints {
			if ep.Name == p.Endpoint {
				profile = ep
				break
			}
		}

		export := evidence.Export{
			Redacted:                 true,
			RedactionRule:            "redaction/v1",
			EndpointName:             profile.Name,
			DeclaredLocation:         profile.DeclaredLocation,
			DeclaredExecution:        profile.DeclaredExecution,
			DeclaredBilling:          profile.DeclaredBilling,
			NetworkDependency:        profile.NetworkDependency,
			Model:                    p.Model,
			AuthMode:                 "none",
			Identity:                 transport.NewUnknownIdentity(),
			ReportedModel:            enginetest.DefaultReportedModel,
			Streaming:                transport.TriUnknown,
			CancelObservable:         transport.TriUnknown,
			UsageAvailable:           transport.TriUnknown,
			ModelsEndpoint:           transport.TriUnknown,
			ModelListed:              transport.TriUnknown,
			SystemRoleHonored:        transport.TriUnknown,
			EligibleRequiredReviewer: transport.TriUnknown,
			AdapterVersion:           enginetest.AdapterVersion,
			ObservedAt:               time.Date(2026, time.November, 18, 12, 0, 0, 0, time.UTC),
		}

		contextBound := enginetest.DefaultContextBound
		export.ContextBound = &contextBound

		calibration := transport.Calibration{Available: true, BytesPerToken: enginetest.DefaultCalibration}

		pm, err := evidence.NewProbeManifest(export, profile, p.Model, enginetest.AdapterVersion, calibration)
		if err != nil {
			t.Fatalf("probe manifest for %s: %v", p.ID, err)
		}

		data, err := json.Marshal(pm)
		if err != nil {
			t.Fatalf("marshal probe manifest for %s: %v", p.ID, err)
		}

		if _, _, err := store.CreateRun(data); err != nil {
			t.Fatalf("seed probe record for %s: %v", p.ID, err)
		}
	}
}
