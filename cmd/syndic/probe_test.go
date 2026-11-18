package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/boldfield/syndic/internal/evidence"
	"github.com/boldfield/syndic/internal/journal"
)

func TestProbeSuccess(t *testing.T) {
	// Create temporary directories
	tmpDir := t.TempDir()
	rawDir := filepath.Join(tmpDir, "raw")
	exportDir := filepath.Join(tmpDir, "export")
	journalPath := filepath.Join(tmpDir, "journal.sqlite")
	secretsDir := filepath.Join(tmpDir, "secrets")
	rosterPath := filepath.Join(tmpDir, "roster.toml")

	if err := os.MkdirAll(secretsDir, 0700); err != nil {
		t.Fatalf("failed to create secrets dir: %v", err)
	}

	// Create a test server that behaves like an OpenAI-compatible endpoint
	sentinelValue := "sentinel-header-value-12345"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			w.Header().Set("X-Sentinel", sentinelValue)
			if err := json.NewEncoder(w).Encode(map[string]interface{}{
				"object": "list",
				"data": []map[string]string{
					{"id": "test-model", "object": "model"},
				},
			}); err != nil {
				t.Logf("failed to encode models response: %v", err)
			}
		} else if strings.HasSuffix(r.URL.Path, "/chat/completions") {
			w.Header().Set("X-Sentinel", sentinelValue)
			if err := json.NewEncoder(w).Encode(map[string]interface{}{
				"id":      "chatcmpl-123",
				"object":  "chat.completion",
				"model":   "test-model",
				"choices": []map[string]interface{}{{"message": map[string]string{"role": "assistant", "content": "test"}, "finish_reason": "stop"}},
				"usage":   map[string]int{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
			}); err != nil {
				t.Logf("failed to encode completion response: %v", err)
			}
		} else {
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	// Create roster
	rosterContent := fmt.Sprintf(`
default_preset = "test-preset"

[[participants]]
id = "author"
model = "test-model"
route = "api"
endpoint = "test-endpoint"
eligible_roles = ["author"]
pool = "default"

[[endpoints]]
name = "test-endpoint"
base_url = "%s"
dialect = "chat_completions_v1"
models = ["test-model"]
declared_location = "local"
declared_execution = "remote"
declared_billing = "none"
network_dependency = "none"
max_tokens_field = "max_tokens"

[[participants]]
id = "reviewer"
model = "test-model"
route = "api"
endpoint = "test-endpoint"
eligible_roles = ["reviewer"]
pool = "default"

[presets.test-preset]
mode = "review"
author = "author"
required_reviewers = ["reviewer"]
`, server.URL)

	if err := os.WriteFile(rosterPath, []byte(rosterContent), 0600); err != nil {
		t.Fatalf("failed to write roster: %v", err)
	}

	// Run the probe command
	oldStdout := os.Stdout
	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stdout = w
	os.Stderr = w

	fs := flag.NewFlagSet("probe", flag.ContinueOnError)
	exitCode := cmdProbe(fs, []string{
		"--roster", rosterPath,
		"--endpoint", "test-endpoint",
		"--model", "test-model",
		"--raw-dir", rawDir,
		"--export-dir", exportDir,
		"--journal", journalPath,
		"--secrets-dir", secretsDir,
	})

	if err := w.Close(); err != nil {
		t.Logf("failed to close pipe writer: %v", err)
	}
	os.Stdout = oldStdout
	os.Stderr = oldStderr

	output, _ := io.ReadAll(r)
	outputStr := string(output)

	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d\nOutput: %s", exitCode, outputStr)
	}

	// Check that exactly one file was written to each directory
	rawFiles, err := os.ReadDir(rawDir)
	if err != nil {
		t.Fatalf("failed to read raw dir: %v", err)
	}
	if len(rawFiles) != 1 {
		t.Fatalf("expected 1 raw file, got %d", len(rawFiles))
	}

	exportFiles, err := os.ReadDir(exportDir)
	if err != nil {
		t.Fatalf("failed to read export dir: %v", err)
	}
	if len(exportFiles) != 1 {
		t.Fatalf("expected 1 export file, got %d", len(exportFiles))
	}

	// Verify export file is valid JSON and has redacted=true
	exportPath := filepath.Join(exportDir, exportFiles[0].Name())
	exportData, err := os.ReadFile(exportPath)
	if err != nil {
		t.Fatalf("failed to read export: %v", err)
	}

	var exp evidence.Export
	if err := json.Unmarshal(exportData, &exp); err != nil {
		t.Fatalf("failed to parse export JSON: %v", err)
	}

	if !exp.Redacted {
		t.Fatal("export should have redacted=true")
	}

	if strings.Contains(string(exportData), sentinelValue) {
		t.Fatal("export contains sentinel value from test server")
	}

	// Verify journal has one run and one probe_completed event
	db, err := journal.Open(journalPath)
	if err != nil {
		t.Fatalf("failed to open journal: %v", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Logf("failed to close journal: %v", err)
		}
	}()

	var runCount int
	if err := db.Conn().QueryRow("SELECT COUNT(*) FROM runs").Scan(&runCount); err != nil {
		t.Fatalf("failed to query runs: %v", err)
	}
	if runCount != 1 {
		t.Fatalf("expected 1 run, got %d", runCount)
	}

	var runManifest sql.NullString
	if err := db.Conn().QueryRow("SELECT manifest FROM runs").Scan(&runManifest); err != nil {
		t.Fatalf("failed to query run manifest: %v", err)
	}

	if strings.Contains(runManifest.String, sentinelValue) {
		t.Fatal("run manifest contains sentinel value")
	}

	var eventCount int
	if err := db.Conn().QueryRow("SELECT COUNT(*) FROM events WHERE event_type = ?", "probe_completed").Scan(&eventCount); err != nil {
		t.Fatalf("failed to query events: %v", err)
	}
	if eventCount != 1 {
		t.Fatalf("expected 1 probe_completed event, got %d", eventCount)
	}
}

func TestProbeRawDirInGitWorktree(t *testing.T) {
	// Create temporary directory with .git subdirectory
	tmpDir := t.TempDir()
	gitDir := filepath.Join(tmpDir, ".git")
	if err := os.Mkdir(gitDir, 0700); err != nil {
		t.Fatalf("failed to create .git: %v", err)
	}

	rawDir := filepath.Join(tmpDir, "raw")
	secretsDir := filepath.Join(tmpDir, "secrets")
	rosterPath := filepath.Join(tmpDir, "roster.toml")

	if err := os.MkdirAll(secretsDir, 0700); err != nil {
		t.Fatalf("failed to create secrets dir: %v", err)
	}

	// Create minimal roster
	rosterContent := `
default_preset = "test-preset"

[[participants]]
id = "author"
model = "test-model"
route = "api"
endpoint = "test-endpoint"
eligible_roles = ["author"]
pool = "default"

[[endpoints]]
name = "test-endpoint"
base_url = "http://localhost:9999"
dialect = "chat_completions_v1"
models = ["test-model"]
declared_location = "local"
declared_execution = "remote"
declared_billing = "none"
network_dependency = "none"

[[participants]]
id = "reviewer"
model = "test-model"
route = "api"
endpoint = "test-endpoint"
eligible_roles = ["reviewer"]
pool = "default"

[presets.test-preset]
mode = "review"
author = "author"
required_reviewers = ["reviewer"]
`
	if err := os.WriteFile(rosterPath, []byte(rosterContent), 0600); err != nil {
		t.Fatalf("failed to write roster: %v", err)
	}

	// Run the probe command
	fs := flag.NewFlagSet("probe", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	exitCode := cmdProbe(fs, []string{
		"--roster", rosterPath,
		"--endpoint", "test-endpoint",
		"--model", "test-model",
		"--raw-dir", rawDir,
		"--secrets-dir", secretsDir,
	})

	if exitCode != 4 {
		t.Fatalf("expected exit code 4, got %d", exitCode)
	}

	// Verify no files were written
	if _, err := os.Stat(rawDir); err == nil {
		t.Fatal("raw dir should not exist")
	}
}

func TestProbeUnreachable(t *testing.T) {
	tmpDir := t.TempDir()
	secretsDir := filepath.Join(tmpDir, "secrets")
	rosterPath := filepath.Join(tmpDir, "roster.toml")

	if err := os.MkdirAll(secretsDir, 0700); err != nil {
		t.Fatalf("failed to create secrets dir: %v", err)
	}

	// Create roster with unreachable endpoint
	rosterContent := `
default_preset = "test-preset"

[[participants]]
id = "author"
model = "test-model"
route = "api"
endpoint = "test-endpoint"
eligible_roles = ["author"]
pool = "default"

[[endpoints]]
name = "test-endpoint"
base_url = "http://localhost:1"
dialect = "chat_completions_v1"
models = ["test-model"]
declared_location = "local"
declared_execution = "remote"
declared_billing = "none"
network_dependency = "none"

[[participants]]
id = "reviewer"
model = "test-model"
route = "api"
endpoint = "test-endpoint"
eligible_roles = ["reviewer"]
pool = "default"

[presets.test-preset]
mode = "review"
author = "author"
required_reviewers = ["reviewer"]
`
	if err := os.WriteFile(rosterPath, []byte(rosterContent), 0600); err != nil {
		t.Fatalf("failed to write roster: %v", err)
	}

	// Run the probe command and capture stderr
	var stderr bytes.Buffer
	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	fs := flag.NewFlagSet("probe", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	exitCode := cmdProbe(fs, []string{
		"--roster", rosterPath,
		"--endpoint", "test-endpoint",
		"--model", "test-model",
		"--secrets-dir", secretsDir,
	})

	if err := w.Close(); err != nil {
		t.Logf("failed to close pipe writer: %v", err)
	}
	os.Stderr = oldStderr
	if _, err := io.Copy(&stderr, r); err != nil {
		t.Logf("failed to copy from pipe: %v", err)
	}

	if exitCode != 3 {
		t.Fatalf("expected exit code 3, got %d", exitCode)
	}

	output := stderr.String()
	if !strings.Contains(output, "unreachable") {
		t.Fatalf("expected 'unreachable' in output, got: %s", output)
	}
}

func TestProbeMissingEndpoint(t *testing.T) {
	tmpDir := t.TempDir()
	rosterPath := filepath.Join(tmpDir, "roster.toml")

	rosterContent := `
default_preset = "test-preset"

[[participants]]
id = "author"
model = "test-model"
route = "api"
endpoint = "test-endpoint"
eligible_roles = ["author"]
pool = "default"

[[endpoints]]
name = "test-endpoint"
base_url = "http://localhost:9999"
dialect = "chat_completions_v1"
models = ["test-model"]
declared_location = "local"
declared_execution = "remote"
declared_billing = "none"
network_dependency = "none"

[[participants]]
id = "reviewer"
model = "test-model"
route = "api"
endpoint = "test-endpoint"
eligible_roles = ["reviewer"]
pool = "default"

[presets.test-preset]
mode = "review"
author = "author"
required_reviewers = ["reviewer"]
`
	if err := os.WriteFile(rosterPath, []byte(rosterContent), 0600); err != nil {
		t.Fatalf("failed to write roster: %v", err)
	}

	// Run without --model flag
	fs := flag.NewFlagSet("probe", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	exitCode := cmdProbe(fs, []string{
		"--roster", rosterPath,
		"--endpoint", "test-endpoint",
	})

	if exitCode != 2 {
		t.Fatalf("expected exit code 2, got %d", exitCode)
	}
}

func TestProbeMissingModel(t *testing.T) {
	tmpDir := t.TempDir()
	rosterPath := filepath.Join(tmpDir, "roster.toml")

	rosterContent := `
default_preset = "test-preset"

[[participants]]
id = "author"
model = "test-model"
route = "api"
endpoint = "test-endpoint"
eligible_roles = ["author"]
pool = "default"

[[endpoints]]
name = "test-endpoint"
base_url = "http://localhost:9999"
dialect = "chat_completions_v1"
models = ["test-model"]
declared_location = "local"
declared_execution = "remote"
declared_billing = "none"
network_dependency = "none"

[[participants]]
id = "reviewer"
model = "test-model"
route = "api"
endpoint = "test-endpoint"
eligible_roles = ["reviewer"]
pool = "default"

[presets.test-preset]
mode = "review"
author = "author"
required_reviewers = ["reviewer"]
`
	if err := os.WriteFile(rosterPath, []byte(rosterContent), 0600); err != nil {
		t.Fatalf("failed to write roster: %v", err)
	}

	// Run without --endpoint flag
	fs := flag.NewFlagSet("probe", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	exitCode := cmdProbe(fs, []string{
		"--roster", rosterPath,
		"--model", "test-model",
	})

	if exitCode != 2 {
		t.Fatalf("expected exit code 2, got %d", exitCode)
	}
}

func TestProbeInvalidRoster(t *testing.T) {
	tmpDir := t.TempDir()
	rosterPath := filepath.Join(tmpDir, "roster.toml")

	// Write invalid TOML
	if err := os.WriteFile(rosterPath, []byte("this is not valid [toml"), 0600); err != nil {
		t.Fatalf("failed to write roster: %v", err)
	}

	fs := flag.NewFlagSet("probe", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	exitCode := cmdProbe(fs, []string{
		"--roster", rosterPath,
		"--endpoint", "test-endpoint",
		"--model", "test-model",
	})

	if exitCode != 2 {
		t.Fatalf("expected exit code 2, got %d", exitCode)
	}
}

func TestProbeEndpointNotFound(t *testing.T) {
	tmpDir := t.TempDir()
	secretsDir := filepath.Join(tmpDir, "secrets")
	rosterPath := filepath.Join(tmpDir, "roster.toml")

	if err := os.MkdirAll(secretsDir, 0700); err != nil {
		t.Fatalf("failed to create secrets dir: %v", err)
	}

	rosterContent := `
default_preset = "test-preset"

[[participants]]
id = "author"
model = "test-model"
route = "api"
endpoint = "test-endpoint"
eligible_roles = ["author"]
pool = "default"

[[endpoints]]
name = "test-endpoint"
base_url = "http://localhost:9999"
dialect = "chat_completions_v1"
models = ["test-model"]
declared_location = "local"
declared_execution = "remote"
declared_billing = "none"
network_dependency = "none"

[[participants]]
id = "reviewer"
model = "test-model"
route = "api"
endpoint = "test-endpoint"
eligible_roles = ["reviewer"]
pool = "default"

[presets.test-preset]
mode = "review"
author = "author"
required_reviewers = ["reviewer"]
`
	if err := os.WriteFile(rosterPath, []byte(rosterContent), 0600); err != nil {
		t.Fatalf("failed to write roster: %v", err)
	}

	fs := flag.NewFlagSet("probe", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	exitCode := cmdProbe(fs, []string{
		"--roster", rosterPath,
		"--endpoint", "nonexistent-endpoint",
		"--model", "test-model",
		"--secrets-dir", secretsDir,
	})

	if exitCode != 2 {
		t.Fatalf("expected exit code 2, got %d", exitCode)
	}
}

func TestProbeUnauthorized(t *testing.T) {
	tmpDir := t.TempDir()
	rawDir := filepath.Join(tmpDir, "raw")
	exportDir := filepath.Join(tmpDir, "export")
	secretsDir := filepath.Join(tmpDir, "secrets")
	rosterPath := filepath.Join(tmpDir, "roster.toml")

	if err := os.MkdirAll(secretsDir, 0700); err != nil {
		t.Fatalf("failed to create secrets dir: %v", err)
	}

	// Create a test server that returns 401
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	// Create roster
	rosterContent := fmt.Sprintf(`
default_preset = "test-preset"

[[participants]]
id = "author"
model = "test-model"
route = "api"
endpoint = "test-endpoint"
eligible_roles = ["author"]
pool = "default"

[[endpoints]]
name = "test-endpoint"
base_url = "%s"
dialect = "chat_completions_v1"
models = ["test-model"]
declared_location = "local"
declared_execution = "remote"
declared_billing = "none"
network_dependency = "none"

[[participants]]
id = "reviewer"
model = "test-model"
route = "api"
endpoint = "test-endpoint"
eligible_roles = ["reviewer"]
pool = "default"

[presets.test-preset]
mode = "review"
author = "author"
required_reviewers = ["reviewer"]
`, server.URL)

	if err := os.WriteFile(rosterPath, []byte(rosterContent), 0600); err != nil {
		t.Fatalf("failed to write roster: %v", err)
	}

	// Run the probe command and capture stderr
	var stderr bytes.Buffer
	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	fs := flag.NewFlagSet("probe", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	exitCode := cmdProbe(fs, []string{
		"--roster", rosterPath,
		"--endpoint", "test-endpoint",
		"--model", "test-model",
		"--raw-dir", rawDir,
		"--export-dir", exportDir,
		"--secrets-dir", secretsDir,
	})

	if err := w.Close(); err != nil {
		t.Logf("failed to close pipe writer: %v", err)
	}
	os.Stderr = oldStderr
	if _, err := io.Copy(&stderr, r); err != nil {
		t.Logf("failed to copy from pipe: %v", err)
	}

	if exitCode != 3 {
		t.Fatalf("expected exit code 3, got %d", exitCode)
	}

	output := stderr.String()
	if !strings.Contains(output, "unauthorized") {
		t.Fatalf("expected 'unauthorized' in output, got: %s", output)
	}

	// Verify no export was written
	exportFiles, err := os.ReadDir(exportDir)
	if err == nil && len(exportFiles) > 0 {
		t.Fatalf("expected no export files, got %d", len(exportFiles))
	}
}

func TestProbeRawDirInGitWorktreeWithHttptest(t *testing.T) {
	// Create temporary directory with .git subdirectory
	tmpDir := t.TempDir()
	gitDir := filepath.Join(tmpDir, ".git")
	if err := os.Mkdir(gitDir, 0700); err != nil {
		t.Fatalf("failed to create .git: %v", err)
	}

	rawDir := filepath.Join(tmpDir, "raw")
	secretsDir := filepath.Join(tmpDir, "secrets")
	rosterPath := filepath.Join(tmpDir, "roster.toml")

	if err := os.MkdirAll(secretsDir, 0700); err != nil {
		t.Fatalf("failed to create secrets dir: %v", err)
	}

	// Create a test server
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		http.NotFound(w, r)
	}))
	defer server.Close()

	// Create roster
	rosterContent := fmt.Sprintf(`
default_preset = "test-preset"

[[participants]]
id = "author"
model = "test-model"
route = "api"
endpoint = "test-endpoint"
eligible_roles = ["author"]
pool = "default"

[[endpoints]]
name = "test-endpoint"
base_url = "%s"
dialect = "chat_completions_v1"
models = ["test-model"]
declared_location = "local"
declared_execution = "remote"
declared_billing = "none"
network_dependency = "none"

[[participants]]
id = "reviewer"
model = "test-model"
route = "api"
endpoint = "test-endpoint"
eligible_roles = ["reviewer"]
pool = "default"

[presets.test-preset]
mode = "review"
author = "author"
required_reviewers = ["reviewer"]
`, server.URL)

	if err := os.WriteFile(rosterPath, []byte(rosterContent), 0600); err != nil {
		t.Fatalf("failed to write roster: %v", err)
	}

	// Run the probe command
	var stderr bytes.Buffer
	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	fs := flag.NewFlagSet("probe", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	exitCode := cmdProbe(fs, []string{
		"--roster", rosterPath,
		"--endpoint", "test-endpoint",
		"--model", "test-model",
		"--raw-dir", rawDir,
		"--secrets-dir", secretsDir,
	})

	if err := w.Close(); err != nil {
		t.Logf("failed to close pipe writer: %v", err)
	}
	os.Stderr = oldStderr
	if _, err := io.Copy(&stderr, r); err != nil {
		t.Logf("failed to copy from pipe: %v", err)
	}

	if exitCode != 4 {
		t.Fatalf("expected exit code 4, got %d", exitCode)
	}

	// Verify server received zero requests
	if requestCount > 0 {
		t.Fatalf("expected zero requests to server, got %d", requestCount)
	}

	// Verify no files were written
	if _, err := os.Stat(rawDir); err == nil {
		t.Fatal("raw dir should not exist")
	}
}

func TestProbeInvalidRosterValidation(t *testing.T) {
	tmpDir := t.TempDir()
	secretsDir := filepath.Join(tmpDir, "secrets")
	rosterPath := filepath.Join(tmpDir, "roster.toml")

	if err := os.MkdirAll(secretsDir, 0700); err != nil {
		t.Fatalf("failed to create secrets dir: %v", err)
	}

	// Create roster with validation violations
	rosterContent := `
[[endpoints]]
name = "test-endpoint"
base_url = "http://localhost:9999"
dialect = "bogus_dialect"
models = []
declared_location = "mars"
`
	if err := os.WriteFile(rosterPath, []byte(rosterContent), 0600); err != nil {
		t.Fatalf("failed to write roster: %v", err)
	}

	// Run the probe command and capture stderr
	var stderr bytes.Buffer
	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	fs := flag.NewFlagSet("probe", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	exitCode := cmdProbe(fs, []string{
		"--roster", rosterPath,
		"--endpoint", "test-endpoint",
		"--model", "test-model",
		"--secrets-dir", secretsDir,
	})

	if err := w.Close(); err != nil {
		t.Logf("failed to close pipe writer: %v", err)
	}
	os.Stderr = oldStderr
	if _, err := io.Copy(&stderr, r); err != nil {
		t.Logf("failed to copy from pipe: %v", err)
	}

	if exitCode != 2 {
		t.Fatalf("expected exit code 2, got %d, stderr: %s", exitCode, stderr.String())
	}
}
