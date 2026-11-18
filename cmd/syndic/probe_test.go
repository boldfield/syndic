package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/config"
	"github.com/boldfield/syndic/internal/evidence"
	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/lockfile"
	"github.com/boldfield/syndic/internal/transport"
)

func TestProbeSuccess(t *testing.T) {
	// Create temporary directories
	tmpDir := t.TempDir()
	runtimeDir := filepath.Join(tmpDir, "runtime")
	rawDir := filepath.Join(tmpDir, "raw")
	exportDir := filepath.Join(tmpDir, "export")
	journalPath := filepath.Join(tmpDir, "journal.sqlite")
	secretsDir := filepath.Join(tmpDir, "secrets")
	rosterPath := filepath.Join(tmpDir, "roster.toml")

	// Set XDG_RUNTIME_DIR to temp directory
	if err := os.MkdirAll(runtimeDir, 0700); err != nil {
		t.Fatalf("failed to create runtime dir: %v", err)
	}
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)

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

			// Parse request to detect calibration requests
			var body map[string]interface{}
			if err := json.NewDecoder(r.Body).Decode(&body); err == nil {
				msgs, _ := body["messages"].([]interface{})
				var userMsg string
				for _, m := range msgs {
					mm, ok := m.(map[string]interface{})
					if ok && mm["role"] == "user" {
						userMsg, _ = mm["content"].(string)
						break
					}
				}

				// Handle calibration requests
				isBaseline := userMsg == ""
				isCalibrationText := len(userMsg) == 4096

				if isBaseline {
					// Baseline calibration: 1 prompt token
					if err := json.NewEncoder(w).Encode(map[string]interface{}{
						"model":   "test-model",
						"choices": []map[string]interface{}{{"message": map[string]string{"role": "assistant", "content": ""}, "finish_reason": "stop"}},
						"usage":   map[string]int{"prompt_tokens": 1, "completion_tokens": 1},
					}); err != nil {
						t.Logf("failed to encode baseline response: %v", err)
					}
					return
				} else if isCalibrationText {
					// Calibration text: 1034 prompt tokens (matches acceptance test)
					if err := json.NewEncoder(w).Encode(map[string]interface{}{
						"model":   "test-model",
						"choices": []map[string]interface{}{{"message": map[string]string{"role": "assistant", "content": "ok"}, "finish_reason": "stop"}},
						"usage":   map[string]int{"prompt_tokens": 1034, "completion_tokens": 1},
					}); err != nil {
						t.Logf("failed to encode calibration text response: %v", err)
					}
					return
				}
			}

			// Regular chat request
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
tokens_per_byte_max = 1.0

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

	// Verify raw record contains the sentinel
	rawPath := filepath.Join(rawDir, rawFiles[0].Name())
	rawData, err := os.ReadFile(rawPath)
	if err != nil {
		t.Fatalf("failed to read raw record: %v", err)
	}

	if !strings.Contains(string(rawData), sentinelValue) {
		t.Fatal("raw record should contain sentinel value from test server")
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

	// Verify manifest is a ProbeManifest with fingerprint and calibration
	var manifest evidence.ProbeManifest
	if err := json.Unmarshal([]byte(runManifest.String), &manifest); err != nil {
		t.Fatalf("failed to parse manifest JSON: %v", err)
	}

	if manifest.ConfigFingerprint == "" {
		t.Fatal("manifest should have config_fingerprint")
	}

	if manifest.FingerprintRule != evidence.FingerprintRule {
		t.Fatalf("expected fingerprint_rule %q, got %q", evidence.FingerprintRule, manifest.FingerprintRule)
	}

	if !manifest.Calibration.Available {
		t.Fatal("manifest should have available calibration")
	}

	// Verify exact fingerprint value
	if manifest.ConfigFingerprint == "" {
		t.Fatal("manifest should have non-empty config_fingerprint")
	}

	// Verify exact bytes per token: baseline=1, calibration=1034, so 4096 bytes / (1034 - 1) = ~4.0 bytes/token
	expectedBytesPerToken := float64(4096) / float64(1034-1)
	if math.Abs(manifest.Calibration.BytesPerToken-expectedBytesPerToken) > 0.01 {
		t.Fatalf("expected bytes/token ~%.2f, got %.2f", expectedBytesPerToken, manifest.Calibration.BytesPerToken)
	}

	// Verify export file doesn't contain config_fingerprint
	if strings.Contains(string(exportData), "config_fingerprint") {
		t.Fatal("export file should not contain config_fingerprint")
	}

	// Verify manifest can be parsed with ParseProbeManifest and hasFingerprint is true
	parsedManifest, hasFingerprint, err := evidence.ParseProbeManifest([]byte(runManifest.String))
	if err != nil {
		t.Fatalf("failed to parse manifest with ParseProbeManifest: %v", err)
	}
	if !hasFingerprint {
		t.Fatal("ParseProbeManifest should return hasFingerprint == true")
	}
	if parsedManifest.ConfigFingerprint != manifest.ConfigFingerprint {
		t.Fatalf("parsed manifest fingerprint mismatch")
	}

	// Verify exact fingerprint value by extracting adapter version from raw record
	var rawRecord transport.CapabilityRecord
	if err := json.Unmarshal(rawData, &rawRecord); err != nil {
		t.Fatalf("failed to parse raw record JSON: %v", err)
	}

	// Reconstruct the endpoint profile to compute expected fingerprint
	tokensPerByteMax := 1.0
	testEndpointProfile := config.EndpointProfile{
		Name:              "test-endpoint",
		BaseURL:           server.URL,
		Dialect:           "chat_completions_v1",
		Models:            []string{"test-model"},
		DeclaredLocation:  "local",
		DeclaredExecution: "remote",
		DeclaredBilling:   "none",
		NetworkDependency: "none",
		MaxTokensField:    "max_tokens",
		TokensPerByteMax:  &tokensPerByteMax,
	}

	expectedFingerprint, err := evidence.ConfigFingerprint(testEndpointProfile, "test-model", rawRecord.AdapterVersion)
	if err != nil {
		t.Fatalf("failed to compute expected fingerprint: %v", err)
	}

	if manifest.ConfigFingerprint != expectedFingerprint {
		t.Fatalf("config_fingerprint mismatch: expected %q, got %q", expectedFingerprint, manifest.ConfigFingerprint)
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
	exportDir := filepath.Join(tmpDir, "export")
	journalPath := filepath.Join(tmpDir, "journal.sqlite")

	if err := os.MkdirAll(secretsDir, 0700); err != nil {
		t.Fatalf("failed to create secrets dir: %v", err)
	}

	// Set environment to temp directory to avoid home directory pollution
	t.Setenv("HOME", tmpDir)
	t.Setenv("XDG_DATA_HOME", filepath.Join(tmpDir, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpDir, "config"))

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
		"--export-dir", exportDir,
		"--journal", journalPath,
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
	runtimeDir := filepath.Join(tmpDir, "runtime")
	secretsDir := filepath.Join(tmpDir, "secrets")
	rosterPath := filepath.Join(tmpDir, "roster.toml")
	rawDir := filepath.Join(tmpDir, "raw")
	exportDir := filepath.Join(tmpDir, "export")
	journalPath := filepath.Join(tmpDir, "journal.sqlite")

	if err := os.MkdirAll(runtimeDir, 0700); err != nil {
		t.Fatalf("failed to create runtime dir: %v", err)
	}
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)

	if err := os.MkdirAll(secretsDir, 0700); err != nil {
		t.Fatalf("failed to create secrets dir: %v", err)
	}

	// Set environment to temp directory to avoid home directory pollution
	t.Setenv("HOME", tmpDir)
	t.Setenv("XDG_DATA_HOME", filepath.Join(tmpDir, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpDir, "config"))

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
		"--raw-dir", rawDir,
		"--export-dir", exportDir,
		"--journal", journalPath,
		"--secrets-dir", secretsDir,
	})

	if err := w.Close(); err != nil {
		t.Logf("failed to close pipe writer: %v", err)
	}
	os.Stderr = oldStderr
	if _, err := io.Copy(&stderr, r); err != nil {
		t.Fatalf("failed to copy from pipe: %v", err)
	}

	if exitCode != 3 {
		t.Fatalf("expected exit code 3, got %d", exitCode)
	}

	output := stderr.String()
	if !strings.Contains(output, "unreachable") {
		t.Fatalf("expected 'unreachable' in output, got: %s", output)
	}

	// Verify no export was written
	exportFiles, err := os.ReadDir(exportDir)
	if err == nil && len(exportFiles) > 0 {
		t.Fatalf("expected no export files, got %d", len(exportFiles))
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
	secretsDir := filepath.Join(tmpDir, "secrets")

	if err := os.MkdirAll(secretsDir, 0700); err != nil {
		t.Fatalf("failed to create secrets dir: %v", err)
	}

	// Set environment to temp directory to avoid home directory pollution
	t.Setenv("HOME", tmpDir)
	t.Setenv("XDG_DATA_HOME", filepath.Join(tmpDir, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpDir, "config"))

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
		"--secrets-dir", secretsDir,
	})

	if exitCode != 2 {
		t.Fatalf("expected exit code 2, got %d", exitCode)
	}
}

func TestProbeEndpointNotFound(t *testing.T) {
	tmpDir := t.TempDir()
	runtimeDir := filepath.Join(tmpDir, "runtime")
	secretsDir := filepath.Join(tmpDir, "secrets")
	rosterPath := filepath.Join(tmpDir, "roster.toml")
	rawDir := filepath.Join(tmpDir, "raw")
	exportDir := filepath.Join(tmpDir, "export")
	journalPath := filepath.Join(tmpDir, "journal.sqlite")

	if err := os.MkdirAll(runtimeDir, 0700); err != nil {
		t.Fatalf("failed to create runtime dir: %v", err)
	}
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)

	if err := os.MkdirAll(secretsDir, 0700); err != nil {
		t.Fatalf("failed to create secrets dir: %v", err)
	}

	// Set environment to temp directory to avoid home directory pollution
	t.Setenv("HOME", tmpDir)
	t.Setenv("XDG_DATA_HOME", filepath.Join(tmpDir, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpDir, "config"))

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
		"--raw-dir", rawDir,
		"--export-dir", exportDir,
		"--journal", journalPath,
		"--secrets-dir", secretsDir,
	})

	if exitCode != 2 {
		t.Fatalf("expected exit code 2, got %d", exitCode)
	}
}

func TestProbeUnauthorized(t *testing.T) {
	tmpDir := t.TempDir()
	runtimeDir := filepath.Join(tmpDir, "runtime")
	rawDir := filepath.Join(tmpDir, "raw")
	exportDir := filepath.Join(tmpDir, "export")
	secretsDir := filepath.Join(tmpDir, "secrets")
	rosterPath := filepath.Join(tmpDir, "roster.toml")
	journalPath := filepath.Join(tmpDir, "journal.sqlite")

	if err := os.MkdirAll(runtimeDir, 0700); err != nil {
		t.Fatalf("failed to create runtime dir: %v", err)
	}
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)

	if err := os.MkdirAll(secretsDir, 0700); err != nil {
		t.Fatalf("failed to create secrets dir: %v", err)
	}

	// Set environment to temp directory to avoid home directory pollution
	t.Setenv("HOME", tmpDir)
	t.Setenv("XDG_DATA_HOME", filepath.Join(tmpDir, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpDir, "config"))

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
		"--journal", journalPath,
		"--secrets-dir", secretsDir,
	})

	if err := w.Close(); err != nil {
		t.Logf("failed to close pipe writer: %v", err)
	}
	os.Stderr = oldStderr
	if _, err := io.Copy(&stderr, r); err != nil {
		t.Fatalf("failed to copy from pipe: %v", err)
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
	runtimeDir := filepath.Join(tmpDir, "runtime")
	gitDir := filepath.Join(tmpDir, ".git")
	if err := os.Mkdir(gitDir, 0700); err != nil {
		t.Fatalf("failed to create .git: %v", err)
	}

	rawDir := filepath.Join(tmpDir, "raw")
	secretsDir := filepath.Join(tmpDir, "secrets")
	rosterPath := filepath.Join(tmpDir, "roster.toml")

	if err := os.MkdirAll(runtimeDir, 0700); err != nil {
		t.Fatalf("failed to create runtime dir: %v", err)
	}
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)

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
		t.Fatalf("failed to copy from pipe: %v", err)
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
	runtimeDir := filepath.Join(tmpDir, "runtime")
	secretsDir := filepath.Join(tmpDir, "secrets")
	rosterPath := filepath.Join(tmpDir, "roster.toml")
	rawDir := filepath.Join(tmpDir, "raw")
	exportDir := filepath.Join(tmpDir, "export")
	journalPath := filepath.Join(tmpDir, "journal.sqlite")

	if err := os.MkdirAll(runtimeDir, 0700); err != nil {
		t.Fatalf("failed to create runtime dir: %v", err)
	}
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)

	if err := os.MkdirAll(secretsDir, 0700); err != nil {
		t.Fatalf("failed to create secrets dir: %v", err)
	}

	// Set environment to temp directory to avoid home directory pollution
	t.Setenv("HOME", tmpDir)
	t.Setenv("XDG_DATA_HOME", filepath.Join(tmpDir, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpDir, "config"))

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
		"--raw-dir", rawDir,
		"--export-dir", exportDir,
		"--journal", journalPath,
		"--secrets-dir", secretsDir,
	})

	if err := w.Close(); err != nil {
		t.Logf("failed to close pipe writer: %v", err)
	}
	os.Stderr = oldStderr
	if _, err := io.Copy(&stderr, r); err != nil {
		t.Fatalf("failed to copy from pipe: %v", err)
	}

	if exitCode != 2 {
		t.Fatalf("expected exit code 2, got %d, stderr: %s", exitCode, stderr.String())
	}

	output := stderr.String()
	// Assert that the captured output contains expected violations
	expectedViolations := []string{"unknown declared_location", "unknown dialect", "default_preset is empty"}
	for _, violation := range expectedViolations {
		if !strings.Contains(output, violation) {
			t.Fatalf("expected violation '%s' in output, got: %s", violation, output)
		}
	}
}

func TestProbeRawDirRelativePathFromNestedWorkingDirectory(t *testing.T) {
	// Create a temporary repository structure
	tmpDir := t.TempDir()
	repoDir := filepath.Join(tmpDir, "repo")
	nestedDir := filepath.Join(repoDir, "nested")
	gitDir := filepath.Join(repoDir, ".git")

	if err := os.MkdirAll(nestedDir, 0700); err != nil {
		t.Fatalf("failed to create nested dir: %v", err)
	}
	if err := os.Mkdir(gitDir, 0700); err != nil {
		t.Fatalf("failed to create .git: %v", err)
	}

	// Set working directory to the nested directory
	t.Chdir(nestedDir)

	// Create relative raw dir
	secretsDir := filepath.Join(tmpDir, "secrets")
	rosterPath := filepath.Join(tmpDir, "roster.toml")
	exportDir := filepath.Join(tmpDir, "export")
	journalPath := filepath.Join(tmpDir, "journal.sqlite")

	if err := os.MkdirAll(secretsDir, 0700); err != nil {
		t.Fatalf("failed to create secrets dir: %v", err)
	}

	// Set environment to temp directory
	t.Setenv("HOME", tmpDir)
	t.Setenv("XDG_DATA_HOME", filepath.Join(tmpDir, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpDir, "config"))

	// Create roster
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

	// Run the probe command with relative raw directory
	fs := flag.NewFlagSet("probe", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	exitCode := cmdProbe(fs, []string{
		"--roster", rosterPath,
		"--endpoint", "test-endpoint",
		"--model", "test-model",
		"--raw-dir", "relative-raw",
		"--export-dir", exportDir,
		"--journal", journalPath,
		"--secrets-dir", secretsDir,
	})

	// Should exit 4 because raw dir is inside worktree
	if exitCode != 4 {
		t.Fatalf("expected exit code 4 for raw dir inside worktree, got %d", exitCode)
	}
}

func TestProbeMeteringEndpointRefused(t *testing.T) {
	tmpDir := t.TempDir()
	runtimeDir := filepath.Join(tmpDir, "runtime")
	rawDir := filepath.Join(tmpDir, "raw")
	exportDir := filepath.Join(tmpDir, "export")
	secretsDir := filepath.Join(tmpDir, "secrets")
	rosterPath := filepath.Join(tmpDir, "roster.toml")
	journalPath := filepath.Join(tmpDir, "journal.sqlite")

	if err := os.MkdirAll(runtimeDir, 0700); err != nil {
		t.Fatalf("failed to create runtime dir: %v", err)
	}
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)

	if err := os.MkdirAll(secretsDir, 0700); err != nil {
		t.Fatalf("failed to create secrets dir: %v", err)
	}

	// Create a test server that behaves like an OpenAI-compatible endpoint
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{})
	}))
	defer server.Close()

	// Create roster with metered endpoint
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
declared_billing = "metered"
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

	// Set environment to temp directory
	t.Setenv("HOME", tmpDir)
	t.Setenv("XDG_DATA_HOME", filepath.Join(tmpDir, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpDir, "config"))

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
		"--journal", journalPath,
		"--secrets-dir", secretsDir,
	})

	if err := w.Close(); err != nil {
		t.Logf("failed to close pipe writer: %v", err)
	}
	os.Stderr = oldStderr
	if _, err := io.Copy(&stderr, r); err != nil {
		t.Fatalf("failed to copy from pipe: %v", err)
	}

	if exitCode != 2 {
		t.Fatalf("expected exit code 2 for metered endpoint, got %d", exitCode)
	}

	output := stderr.String()
	if !strings.Contains(output, "metered") || !strings.Contains(output, "spending ceiling") || !strings.Contains(output, "test-endpoint") {
		t.Fatalf("expected metered/spending ceiling/test-endpoint error message, got: %s", output)
	}

	// Verify the test server received zero requests (probe was rejected before adapter was built)
	if requestCount > 0 {
		t.Fatalf("expected zero requests to server, got %d", requestCount)
	}

	// Verify no export was written
	if _, err := os.Stat(exportDir); err == nil {
		exportFiles, _ := os.ReadDir(exportDir)
		if len(exportFiles) > 0 {
			t.Fatalf("expected no export files, got %d", len(exportFiles))
		}
	}
}

func TestProbeJournalParentDirDoesNotExist(t *testing.T) {
	tmpDir := t.TempDir()
	runtimeDir := filepath.Join(tmpDir, "runtime")
	rawDir := filepath.Join(tmpDir, "raw")
	exportDir := filepath.Join(tmpDir, "export")
	journalPath := filepath.Join(tmpDir, "nonexistent", "subdir", "journal.sqlite")
	secretsDir := filepath.Join(tmpDir, "secrets")
	rosterPath := filepath.Join(tmpDir, "roster.toml")

	if err := os.MkdirAll(runtimeDir, 0700); err != nil {
		t.Fatalf("failed to create runtime dir: %v", err)
	}
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)

	if err := os.MkdirAll(secretsDir, 0700); err != nil {
		t.Fatalf("failed to create secrets dir: %v", err)
	}

	// Create a test server that behaves like an OpenAI-compatible endpoint
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			if err := json.NewEncoder(w).Encode(map[string]interface{}{
				"object": "list",
				"data": []map[string]string{
					{"id": "test-model", "object": "model"},
				},
			}); err != nil {
				t.Logf("failed to encode models response: %v", err)
			}
		} else if strings.HasSuffix(r.URL.Path, "/chat/completions") {
			if err := json.NewEncoder(w).Encode(map[string]interface{}{
				"id":      "chatcmpl-123",
				"object":  "chat.completion",
				"model":   "test-model",
				"choices": []map[string]interface{}{{"message": map[string]string{"role": "assistant", "content": "test"}, "finish_reason": "stop"}},
				"usage":   map[string]int{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
			}); err != nil {
				t.Logf("failed to encode completion response: %v", err)
			}
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

	// Verify journal was created and has the data
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
}

func TestProbeNoUsage(t *testing.T) {
	// Create temporary directories
	tmpDir := t.TempDir()
	runtimeDir := filepath.Join(tmpDir, "runtime")
	rawDir := filepath.Join(tmpDir, "raw")
	exportDir := filepath.Join(tmpDir, "export")
	journalPath := filepath.Join(tmpDir, "journal.sqlite")
	secretsDir := filepath.Join(tmpDir, "secrets")
	rosterPath := filepath.Join(tmpDir, "roster.toml")

	// Set XDG_RUNTIME_DIR to temp directory
	if err := os.MkdirAll(runtimeDir, 0700); err != nil {
		t.Fatalf("failed to create runtime dir: %v", err)
	}
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)

	if err := os.MkdirAll(secretsDir, 0700); err != nil {
		t.Fatalf("failed to create secrets dir: %v", err)
	}

	// Create a test server that returns no usage
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			if err := json.NewEncoder(w).Encode(map[string]interface{}{
				"object": "list",
				"data": []map[string]string{
					{"id": "test-model", "object": "model"},
				},
			}); err != nil {
				t.Logf("failed to encode models response: %v", err)
			}
		} else if strings.HasSuffix(r.URL.Path, "/chat/completions") {
			// Return no usage field
			if err := json.NewEncoder(w).Encode(map[string]interface{}{
				"model":   "test-model",
				"choices": []map[string]interface{}{{"message": map[string]string{"role": "assistant", "content": "test"}, "finish_reason": "stop"}},
			}); err != nil {
				t.Logf("failed to encode completion response: %v", err)
			}
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
tokens_per_byte_max = 1.0

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

	// Verify manifest calibration is unavailable
	db, err := journal.Open(journalPath)
	if err != nil {
		t.Fatalf("failed to open journal: %v", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Logf("failed to close journal: %v", err)
		}
	}()

	var runManifest sql.NullString
	if err := db.Conn().QueryRow("SELECT manifest FROM runs").Scan(&runManifest); err != nil {
		t.Fatalf("failed to query run manifest: %v", err)
	}

	var manifest evidence.ProbeManifest
	if err := json.Unmarshal([]byte(runManifest.String), &manifest); err != nil {
		t.Fatalf("failed to parse manifest JSON: %v", err)
	}

	if manifest.Calibration.Available {
		t.Fatal("manifest calibration should be unavailable")
	}

	// Verify manifest marshals calibration as "unavailable"
	var manifestMap map[string]interface{}
	if err := json.Unmarshal([]byte(runManifest.String), &manifestMap); err != nil {
		t.Fatalf("failed to parse manifest map: %v", err)
	}

	calibrationValue, ok := manifestMap["calibration"]
	if !ok {
		t.Fatal("manifest should have calibration field")
	}

	if calibrationValue != "unavailable" {
		t.Fatalf("expected calibration 'unavailable', got %v", calibrationValue)
	}
}

func TestProbeLockContention(t *testing.T) {
	// Create temporary directories
	tmpDir := t.TempDir()
	runtimeDir := filepath.Join(tmpDir, "runtime")
	rawDir := filepath.Join(tmpDir, "raw")
	exportDir := filepath.Join(tmpDir, "export")
	journalPath := filepath.Join(tmpDir, "journal.sqlite")
	secretsDir := filepath.Join(tmpDir, "secrets")
	rosterPath := filepath.Join(tmpDir, "roster.toml")

	// Set XDG_RUNTIME_DIR to temp directory
	if err := os.MkdirAll(runtimeDir, 0700); err != nil {
		t.Fatalf("failed to create runtime dir: %v", err)
	}
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)

	if err := os.MkdirAll(secretsDir, 0700); err != nil {
		t.Fatalf("failed to create secrets dir: %v", err)
	}

	// Create a test server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			if err := json.NewEncoder(w).Encode(map[string]interface{}{
				"object": "list",
				"data": []map[string]string{
					{"id": "test-model", "object": "model"},
				},
			}); err != nil {
				t.Logf("failed to encode models response: %v", err)
			}
		} else if strings.HasSuffix(r.URL.Path, "/chat/completions") {
			if err := json.NewEncoder(w).Encode(map[string]interface{}{
				"model":   "test-model",
				"choices": []map[string]interface{}{{"message": map[string]string{"role": "assistant", "content": "test"}, "finish_reason": "stop"}},
				"usage":   map[string]int{"prompt_tokens": 10, "completion_tokens": 5},
			}); err != nil {
				t.Logf("failed to encode completion response: %v", err)
			}
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
tokens_per_byte_max = 1.0

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

	// Acquire lock in test to simulate engine holding it
	paths, err := lockfile.Resolve(os.Getenv, os.Getenv("HOME"))
	if err != nil {
		t.Fatalf("failed to resolve runtime paths: %v", err)
	}

	testLock, err := lockfile.Acquire(paths.Lock, time.Now())
	if err != nil {
		t.Fatalf("failed to acquire test lock: %v", err)
	}
	t.Cleanup(func() {
		if err := testLock.Release(); err != nil {
			t.Errorf("failed to release test lock: %v", err)
		}
	})

	// Run the probe command
	var stderr bytes.Buffer
	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
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
	os.Stderr = oldStderr
	if _, err := io.Copy(&stderr, r); err != nil {
		t.Fatalf("failed to copy from pipe: %v", err)
	}

	// Should exit with code 5
	if exitCode != 5 {
		t.Fatalf("expected exit code 5, got %d", exitCode)
	}

	output := stderr.String()
	expectedPIDStr := fmt.Sprintf("%d", os.Getpid())
	if !strings.Contains(output, expectedPIDStr) {
		t.Fatalf("expected PID %s in stderr, got: %s", expectedPIDStr, output)
	}

	// Verify export and raw record exist after exit 5
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

	// Verify journal has no new run
	_, err = os.Stat(journalPath)
	if err == nil {
		db, err := journal.Open(journalPath)
		if err != nil {
			t.Fatalf("failed to open journal: %v", err)
		}
		defer func() {
			if err := db.Close(); err != nil {
				t.Errorf("failed to close journal: %v", err)
			}
		}()

		var runCount int
		if err := db.Conn().QueryRow("SELECT COUNT(*) FROM runs").Scan(&runCount); err != nil {
			t.Fatalf("failed to query runs: %v", err)
		}
		if runCount > 0 {
			t.Fatalf("expected no runs in journal when lock is held, got %d", runCount)
		}
	}
}

func TestProbeLockRelease(t *testing.T) {
	// Create temporary directories
	tmpDir := t.TempDir()
	runtimeDir := filepath.Join(tmpDir, "runtime")
	rawDir := filepath.Join(tmpDir, "raw")
	exportDir := filepath.Join(tmpDir, "export")
	journalPath := filepath.Join(tmpDir, "journal.sqlite")
	secretsDir := filepath.Join(tmpDir, "secrets")
	rosterPath := filepath.Join(tmpDir, "roster.toml")

	// Set XDG_RUNTIME_DIR to temp directory
	if err := os.MkdirAll(runtimeDir, 0700); err != nil {
		t.Fatalf("failed to create runtime dir: %v", err)
	}
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)

	if err := os.MkdirAll(secretsDir, 0700); err != nil {
		t.Fatalf("failed to create secrets dir: %v", err)
	}

	// Create a test server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			if err := json.NewEncoder(w).Encode(map[string]interface{}{
				"object": "list",
				"data": []map[string]string{
					{"id": "test-model", "object": "model"},
				},
			}); err != nil {
				t.Logf("failed to encode models response: %v", err)
			}
		} else if strings.HasSuffix(r.URL.Path, "/chat/completions") {
			var body map[string]interface{}
			if err := json.NewDecoder(r.Body).Decode(&body); err == nil {
				msgs, _ := body["messages"].([]interface{})
				var userMsg string
				for _, m := range msgs {
					mm, ok := m.(map[string]interface{})
					if ok && mm["role"] == "user" {
						userMsg, _ = mm["content"].(string)
						break
					}
				}

				isBaseline := userMsg == ""
				isCalibrationText := len(userMsg) == 4096

				if isBaseline {
					if err := json.NewEncoder(w).Encode(map[string]interface{}{
						"model":   "test-model",
						"choices": []map[string]interface{}{{"message": map[string]string{"role": "assistant", "content": ""}, "finish_reason": "stop"}},
						"usage":   map[string]int{"prompt_tokens": 1, "completion_tokens": 1},
					}); err != nil {
						t.Logf("failed to encode baseline response: %v", err)
					}
					return
				} else if isCalibrationText {
					if err := json.NewEncoder(w).Encode(map[string]interface{}{
						"model":   "test-model",
						"choices": []map[string]interface{}{{"message": map[string]string{"role": "assistant", "content": "ok"}, "finish_reason": "stop"}},
						"usage":   map[string]int{"prompt_tokens": 1034, "completion_tokens": 1},
					}); err != nil {
						t.Logf("failed to encode calibration text response: %v", err)
					}
					return
				}
			}

			if err := json.NewEncoder(w).Encode(map[string]interface{}{
				"model":   "test-model",
				"choices": []map[string]interface{}{{"message": map[string]string{"role": "assistant", "content": "test"}, "finish_reason": "stop"}},
				"usage":   map[string]int{"prompt_tokens": 10, "completion_tokens": 5},
			}); err != nil {
				t.Logf("failed to encode completion response: %v", err)
			}
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
tokens_per_byte_max = 1.0

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

	// Now verify that the lock can be acquired
	paths, err := lockfile.Resolve(os.Getenv, os.Getenv("HOME"))
	if err != nil {
		t.Fatalf("failed to resolve runtime paths: %v", err)
	}

	lock, err := lockfile.Acquire(paths.Lock, time.Now())
	if err != nil {
		t.Fatalf("failed to acquire lock after probe run: %v", err)
	}
	t.Cleanup(func() {
		if err := lock.Release(); err != nil {
			t.Errorf("failed to release lock: %v", err)
		}
	})
}

func TestProbeSkipCalibration(t *testing.T) {
	// Create temporary directories
	tmpDir := t.TempDir()
	runtimeDir := filepath.Join(tmpDir, "runtime")
	rawDir := filepath.Join(tmpDir, "raw")
	exportDir := filepath.Join(tmpDir, "export")
	journalPath := filepath.Join(tmpDir, "journal.sqlite")
	secretsDir := filepath.Join(tmpDir, "secrets")
	rosterPath := filepath.Join(tmpDir, "roster.toml")

	// Set XDG_RUNTIME_DIR to temp directory
	if err := os.MkdirAll(runtimeDir, 0700); err != nil {
		t.Fatalf("failed to create runtime dir: %v", err)
	}
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)

	if err := os.MkdirAll(secretsDir, 0700); err != nil {
		t.Fatalf("failed to create secrets dir: %v", err)
	}

	// Create a test server that tracks calibration requests
	var calibrationRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			if err := json.NewEncoder(w).Encode(map[string]interface{}{
				"object": "list",
				"data": []map[string]string{
					{"id": "test-model", "object": "model"},
				},
			}); err != nil {
				t.Logf("failed to encode models response: %v", err)
			}
		} else if strings.HasSuffix(r.URL.Path, "/chat/completions") {
			var body map[string]interface{}
			if err := json.NewDecoder(r.Body).Decode(&body); err == nil {
				msgs, _ := body["messages"].([]interface{})
				for _, m := range msgs {
					mm, ok := m.(map[string]interface{})
					if ok && mm["role"] == "user" {
						userMsg, _ := mm["content"].(string)
						if userMsg == "" || len(userMsg) == 4096 {
							calibrationRequests.Add(1)
						}
						break
					}
				}
			}

			if err := json.NewEncoder(w).Encode(map[string]interface{}{
				"model":   "test-model",
				"choices": []map[string]interface{}{{"message": map[string]string{"role": "assistant", "content": "test"}, "finish_reason": "stop"}},
				"usage":   map[string]int{"prompt_tokens": 10, "completion_tokens": 5},
			}); err != nil {
				t.Logf("failed to encode completion response: %v", err)
			}
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
tokens_per_byte_max = 1.0

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

	// Run the probe command with --skip-calibration
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
		"--skip-calibration",
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

	// Verify no calibration requests were made
	if calibrationRequests.Load() > 0 {
		t.Fatalf("expected no calibration requests with --skip-calibration, got %d", calibrationRequests.Load())
	}

	// Verify manifest has unavailable calibration
	db, err := journal.Open(journalPath)
	if err != nil {
		t.Fatalf("failed to open journal: %v", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Logf("failed to close journal: %v", err)
		}
	}()

	var runManifest sql.NullString
	if err := db.Conn().QueryRow("SELECT manifest FROM runs").Scan(&runManifest); err != nil {
		t.Fatalf("failed to query run manifest: %v", err)
	}

	var manifest evidence.ProbeManifest
	if err := json.Unmarshal([]byte(runManifest.String), &manifest); err != nil {
		t.Fatalf("failed to parse manifest JSON: %v", err)
	}

	if manifest.Calibration.Available {
		t.Fatal("manifest calibration should be unavailable with --skip-calibration")
	}
}

func TestProbeResolverSecretNotLeaked(t *testing.T) {
	// Create temporary directories
	tmpDir := t.TempDir()
	runtimeDir := filepath.Join(tmpDir, "runtime")
	rawDir := filepath.Join(tmpDir, "raw")
	exportDir := filepath.Join(tmpDir, "export")
	journalPath := filepath.Join(tmpDir, "journal.sqlite")
	secretsDir := filepath.Join(tmpDir, "secrets")
	rosterPath := filepath.Join(tmpDir, "roster.toml")

	// Set XDG_RUNTIME_DIR to temp directory
	if err := os.MkdirAll(runtimeDir, 0700); err != nil {
		t.Fatalf("failed to create runtime dir: %v", err)
	}
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)

	if err := os.MkdirAll(secretsDir, 0700); err != nil {
		t.Fatalf("failed to create secrets dir: %v", err)
	}

	// Write a sentinel secret to a file
	sentinelSecret := "secret-sentinel-value-99999"
	secretFilePath := filepath.Join(secretsDir, "test-auth-secret")
	if err := os.WriteFile(secretFilePath, []byte(sentinelSecret), 0600); err != nil {
		t.Fatalf("failed to write secret file: %v", err)
	}

	// Create a test server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			if err := json.NewEncoder(w).Encode(map[string]interface{}{
				"object": "list",
				"data": []map[string]string{
					{"id": "test-model", "object": "model"},
				},
			}); err != nil {
				t.Logf("failed to encode models response: %v", err)
			}
		} else if strings.HasSuffix(r.URL.Path, "/chat/completions") {
			if err := json.NewEncoder(w).Encode(map[string]interface{}{
				"model":   "test-model",
				"choices": []map[string]interface{}{{"message": map[string]string{"role": "assistant", "content": "test"}, "finish_reason": "stop"}},
				"usage":   map[string]int{"prompt_tokens": 10, "completion_tokens": 5},
			}); err != nil {
				t.Logf("failed to encode completion response: %v", err)
			}
		}
	}))
	defer server.Close()

	// Create roster with auth reference to the secret file
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
tokens_per_byte_max = 1.0

[endpoints.auth]
source = "file"
name = "test-auth-secret"

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

	// Verify manifest does not contain the sentinel secret
	db, err := journal.Open(journalPath)
	if err != nil {
		t.Fatalf("failed to open journal: %v", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Logf("failed to close journal: %v", err)
		}
	}()

	var runManifest sql.NullString
	if err := db.Conn().QueryRow("SELECT manifest FROM runs").Scan(&runManifest); err != nil {
		t.Fatalf("failed to query run manifest: %v", err)
	}

	if strings.Contains(runManifest.String, sentinelSecret) {
		t.Fatalf("manifest should not contain resolver secret, but got: %s", runManifest.String)
	}
}
