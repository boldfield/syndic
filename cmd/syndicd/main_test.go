package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/client"
	"github.com/boldfield/syndic/internal/config"
	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/evidence"
	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/lockfile"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/transport/openai"
)

// threadSafeBuffer wraps a bytes.Buffer with a mutex for thread-safe access
type threadSafeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *threadSafeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *threadSafeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestVersion(t *testing.T) {
	var stdout, stderr threadSafeBuffer
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
	var stdout, stderr threadSafeBuffer
	done := make(chan int, 1)

	go func() {
		done <- run(
			[]string{
				"--roster", rosterPath,
				"--journal", journalPath,
				"--secrets-dir", filepath.Join(dir, "secrets"),
			},
			&stdout,
			&stderr,
			sigChan,
		)
	}()

	// Give the daemon time to start and open the socket
	time.Sleep(500 * time.Millisecond)

	// Get the socket path before it might change
	// We need to be careful about race conditions when reading output
	var socketPath, runID string
	for i := 0; i < 10; i++ {
		output := stdout.String()
		parts := strings.Fields(output)
		if len(parts) >= 3 {
			socketPath = parts[0]
			runID = parts[1]
			break
		}
		if i < 9 {
			time.Sleep(50 * time.Millisecond)
		}
	}

	if socketPath == "" {
		t.Fatalf("want socket path, got empty")
	}

	if !strings.Contains(socketPath, "engine.sock") {
		t.Fatalf("want socket path to contain 'engine.sock', got %q", socketPath)
	}

	if runID == "" {
		t.Fatal("want non-empty run ID")
	}

	// Connect with client and verify status
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	c, err := client.Dial(ctx, socketPath, "test", "1.0")
	if err != nil {
		sigChan <- os.Interrupt
		exitCode := <-done
		t.Fatalf("dial socket: %v (exit code %d)", err, exitCode)
	}
	defer func() {
		_ = c.Close()
	}()

	status, err := c.Status(ctx)
	if err != nil {
		sigChan <- os.Interrupt
		exitCode := <-done
		t.Fatalf("get status: %v (exit code %d)", err, exitCode)
	}

	// Verify that participants are listed
	if len(status.Participants) == 0 {
		t.Fatal("want non-empty participants list")
	}

	// Close the client and send signal to stop
	_ = c.Close()
	sigChan <- os.Interrupt
	exitCode := <-done

	if exitCode != 0 {
		t.Fatalf("want exit code 0, got %d; stderr: %s", exitCode, stderr.String())
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

	var stdout, stderr threadSafeBuffer

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

	// Check that lock can be acquired
	paths, err := lockfile.Resolve(func(key string) string {
		switch key {
		case "XDG_RUNTIME_DIR":
			return runtimeDir
		default:
			return ""
		}
	}, os.Getenv("HOME"))
	if err != nil {
		t.Fatalf("resolve paths: %v", err)
	}

	lock, err := lockfile.Acquire(paths.Lock, time.Now())
	if err != nil {
		t.Fatalf("want lock to be free after bad roster, but could not acquire: %v", err)
	}
	_ = lock.Release()

	// Check that no engine run was written
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

	var stdout, stderr threadSafeBuffer

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

func TestSignalWithNoTurn(t *testing.T) {
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
	var stdout, stderr threadSafeBuffer
	done := make(chan int, 1)

	go func() {
		done <- run(
			[]string{
				"--roster", rosterPath,
				"--journal", journalPath,
				"--secrets-dir", filepath.Join(dir, "secrets"),
			},
			&stdout,
			&stderr,
			sigChan,
		)
	}()

	// Give the daemon time to start
	time.Sleep(500 * time.Millisecond)

	// Get the socket path from stdout (with retry to handle race)
	var socketPath string
	for i := 0; i < 10; i++ {
		output := stdout.String()
		parts := strings.Fields(output)
		if len(parts) >= 1 {
			socketPath = parts[0]
			break
		}
		if i < 9 {
			time.Sleep(50 * time.Millisecond)
		}
	}
	if socketPath == "" {
		t.Fatalf("want socket path, got empty")
	}

	// Verify socket accepts connections before signal
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	c, err := client.Dial(ctx, socketPath, "test", "1.0")
	cancel()
	if err != nil {
		sigChan <- os.Interrupt
		<-done
		t.Fatalf("dial socket before signal: %v", err)
	}
	_ = c.Close()

	// Send signal with no turn in progress
	sigChan <- os.Interrupt
	exitCode := <-done

	if exitCode != 0 {
		t.Fatalf("want exit code 0, got %d", exitCode)
	}

	// Verify socket no longer accepts connections
	ctx, cancel = context.WithTimeout(context.Background(), 1*time.Second)
	_, err = client.Dial(ctx, socketPath, "test", "1.0")
	cancel()
	if err == nil {
		t.Fatal("want socket to be closed after signal, but dial succeeded")
	}

	// Verify lock is free
	paths, err := lockfile.Resolve(func(key string) string {
		switch key {
		case "XDG_RUNTIME_DIR":
			return runtimeDir
		default:
			return ""
		}
	}, os.Getenv("HOME"))
	if err != nil {
		t.Fatalf("resolve paths: %v", err)
	}

	lock, err := lockfile.Acquire(paths.Lock, time.Now())
	if err != nil {
		t.Fatalf("want lock to be free, but could not acquire: %v", err)
	}
	_ = lock.Release()
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
	var stdout, stderr threadSafeBuffer

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

func TestLogExternalSpendingControl(t *testing.T) {
	dir := t.TempDir()
	rosterPath := filepath.Join(dir, "roster.toml")
	journalPath := filepath.Join(dir, "journal.db")
	runtimeDir := filepath.Join(dir, "runtime")
	if err := os.MkdirAll(runtimeDir, 0700); err != nil {
		t.Fatal(err)
	}

	// Set up isolated environment for the daemon
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
	t.Setenv("XDG_DATA_HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)

	// Create roster with external spending control on a metered endpoint
	rosterWithExternal := strings.Replace(
		enginetest.RosterTOML,
		`declared_billing = "none"`,
		`declared_billing = "metered"
spending_control = "external"`,
		1,
	)

	if err := os.WriteFile(rosterPath, []byte(rosterWithExternal), 0o600); err != nil {
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

	// Start syndicd in a goroutine
	sigChan := make(chan os.Signal, 2)
	var stdout, stderr threadSafeBuffer
	done := make(chan int, 1)

	go func() {
		done <- run(
			[]string{
				"--roster", rosterPath,
				"--journal", journalPath,
				"--secrets-dir", filepath.Join(dir, "secrets"),
			},
			&stdout,
			&stderr,
			sigChan,
		)
	}()

	// Poll for the log message indicating daemon is ready and has logged external spending control
	var stderrOutput string
	var hasMeteredExternal bool
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		stderrOutput = stderr.String()
		hasMeteredExternal = strings.Contains(stderrOutput, "external spending control:") &&
			strings.Contains(stderrOutput, "(metered)")
		if hasMeteredExternal {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Stop the daemon
	sigChan <- os.Interrupt
	exitCode := <-done

	if exitCode != 0 {
		t.Fatalf("want exit code 0, got %d; stderr: %s", exitCode, stderr.String())
	}

	if !hasMeteredExternal {
		t.Fatalf("want stderr to contain 'external spending control: ... (metered)', got: %q", stderrOutput)
	}
}

func TestLogExternalSpendingControlSharedEndpoint(t *testing.T) {
	dir := t.TempDir()
	rosterPath := filepath.Join(dir, "roster.toml")
	journalPath := filepath.Join(dir, "journal.db")
	runtimeDir := filepath.Join(dir, "runtime")
	if err := os.MkdirAll(runtimeDir, 0700); err != nil {
		t.Fatal(err)
	}

	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
	t.Setenv("XDG_DATA_HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)

	// Create roster with multiple participants on same metered endpoint with external spending control
	rosterWithExternal := strings.Replace(
		enginetest.RosterTOML,
		`declared_billing = "none"`,
		`declared_billing = "metered"
spending_control = "external"`,
		1,
	)

	if err := os.WriteFile(rosterPath, []byte(rosterWithExternal), 0o600); err != nil {
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

	sigChan := make(chan os.Signal, 2)
	var stdout, stderr threadSafeBuffer
	done := make(chan int, 1)

	go func() {
		done <- run(
			[]string{
				"--roster", rosterPath,
				"--journal", journalPath,
				"--secrets-dir", filepath.Join(dir, "secrets"),
			},
			&stdout,
			&stderr,
			sigChan,
		)
	}()

	// Poll for the log message indicating daemon is ready
	var stderrOutput string
	var foundLog bool
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		stderrOutput = stderr.String()
		foundLog = strings.Contains(stderrOutput, "external spending control:")
		if foundLog {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	sigChan <- os.Interrupt
	exitCode := <-done

	if exitCode != 0 {
		t.Fatalf("want exit code 0, got %d; stderr: %s", exitCode, stderr.String())
	}

	if !foundLog {
		t.Fatalf("want stderr to contain 'external spending control:', got: %q", stderrOutput)
	}

	// Count occurrences of the log message per endpoint to ensure deduplication
	for _, endpoint := range roster.Endpoints {
		if endpoint.DeclaredBilling == "metered" {
			searchStr := fmt.Sprintf("external spending control: %s (metered)", endpoint.Name)
			count := strings.Count(stderrOutput, searchStr)
			if count != 1 {
				t.Fatalf("want exactly 1 log entry for endpoint %s, got %d (stderr: %q)", endpoint.Name, count, stderrOutput)
			}
		}
	}
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
			AdapterVersion:           openai.AdapterVersion,
			ObservedAt:               time.Date(2026, time.November, 18, 12, 0, 0, 0, time.UTC),
		}

		contextBound := enginetest.DefaultContextBound
		export.ContextBound = &contextBound

		calibration := transport.Calibration{Available: true, BytesPerToken: enginetest.DefaultCalibration}

		pm, err := evidence.NewProbeManifest(export, profile, p.Model, openai.AdapterVersion, calibration)
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
