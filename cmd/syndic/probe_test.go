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
	// Create HEAD file to mark it as a real repository
	headPath := filepath.Join(gitDir, "HEAD")
	if err := os.WriteFile(headPath, []byte("ref: refs/heads/main\n"), 0600); err != nil {
		t.Fatalf("failed to create HEAD: %v", err)
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
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(tmpDir, "runtime"))

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

	t.Setenv("HOME", tmpDir)
	t.Setenv("XDG_DATA_HOME", filepath.Join(tmpDir, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpDir, "config"))
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(tmpDir, "runtime"))

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

	t.Setenv("HOME", tmpDir)
	t.Setenv("XDG_DATA_HOME", filepath.Join(tmpDir, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpDir, "config"))
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(tmpDir, "runtime"))

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
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(tmpDir, "runtime"))

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
	// Create HEAD file to mark it as a real repository
	headPath := filepath.Join(gitDir, "HEAD")
	if err := os.WriteFile(headPath, []byte("ref: refs/heads/main\n"), 0600); err != nil {
		t.Fatalf("failed to create HEAD: %v", err)
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
	// Create HEAD file to mark it as a real repository
	headPath := filepath.Join(gitDir, "HEAD")
	if err := os.WriteFile(headPath, []byte("ref: refs/heads/main\n"), 0600); err != nil {
		t.Fatalf("failed to create HEAD: %v", err)
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
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(tmpDir, "runtime"))

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

func TestProbeRawDirUnderGitDirWithHead(t *testing.T) {
	// A raw directory under a directory whose .git is a directory containing HEAD: exit 4, nothing written, and test server received zero requests
	tmpDir := t.TempDir()
	runtimeDir := filepath.Join(tmpDir, "runtime")
	gitDir := filepath.Join(tmpDir, ".git")
	if err := os.Mkdir(gitDir, 0700); err != nil {
		t.Fatalf("failed to create .git: %v", err)
	}
	headPath := filepath.Join(gitDir, "HEAD")
	if err := os.WriteFile(headPath, []byte("ref: refs/heads/main\n"), 0600); err != nil {
		t.Fatalf("failed to create HEAD: %v", err)
	}

	if err := os.MkdirAll(runtimeDir, 0700); err != nil {
		t.Fatalf("failed to create runtime dir: %v", err)
	}
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)

	rawDir := filepath.Join(tmpDir, "raw")
	secretsDir := filepath.Join(tmpDir, "secrets")
	rosterPath := filepath.Join(tmpDir, "roster.toml")
	exportDir := filepath.Join(tmpDir, "export")
	journalPath := filepath.Join(tmpDir, "journal.sqlite")

	if err := os.MkdirAll(secretsDir, 0700); err != nil {
		t.Fatalf("failed to create secrets dir: %v", err)
	}

	// Create a test server with an atomic request counter
	requestCount := atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		http.NotFound(w, r)
	}))
	defer server.Close()

	t.Setenv("HOME", tmpDir)
	t.Setenv("XDG_DATA_HOME", filepath.Join(tmpDir, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpDir, "config"))

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

	// Verify server received zero requests
	if requestCount.Load() > 0 {
		t.Fatalf("expected zero requests to server, got %d", requestCount.Load())
	}

	// Verify no files were written
	if _, err := os.Stat(rawDir); err == nil {
		t.Fatal("raw dir should not exist")
	}
}

func TestProbeRawDirUnderGitFile(t *testing.T) {
	// A raw directory under a directory whose .git is a file containing gitdir:: exit 4
	tmpDir := t.TempDir()
	gitPath := filepath.Join(tmpDir, ".git")
	gitdirContent := "gitdir: /some/path/to/real/repo"
	if err := os.WriteFile(gitPath, []byte(gitdirContent), 0600); err != nil {
		t.Fatalf("failed to create .git file: %v", err)
	}

	rawDir := filepath.Join(tmpDir, "raw")
	secretsDir := filepath.Join(tmpDir, "secrets")
	rosterPath := filepath.Join(tmpDir, "roster.toml")
	exportDir := filepath.Join(tmpDir, "export")
	journalPath := filepath.Join(tmpDir, "journal.sqlite")

	if err := os.MkdirAll(secretsDir, 0700); err != nil {
		t.Fatalf("failed to create secrets dir: %v", err)
	}

	t.Setenv("HOME", tmpDir)
	t.Setenv("XDG_DATA_HOME", filepath.Join(tmpDir, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpDir, "config"))
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(tmpDir, "runtime"))

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
}

func TestProbeRawDirUnderFakeGitDirWithHooksOnly(t *testing.T) {
	// A raw directory under a directory whose .git is a directory containing only hooks: pass
	tmpDir := t.TempDir()
	runtimeDir := filepath.Join(tmpDir, "runtime")
	gitDir := filepath.Join(tmpDir, ".git")
	if err := os.Mkdir(gitDir, 0700); err != nil {
		t.Fatalf("failed to create .git: %v", err)
	}
	// Create only a hooks subdirectory, no HEAD
	hooksDir := filepath.Join(gitDir, "hooks")
	if err := os.Mkdir(hooksDir, 0700); err != nil {
		t.Fatalf("failed to create hooks: %v", err)
	}

	if err := os.MkdirAll(runtimeDir, 0700); err != nil {
		t.Fatalf("failed to create runtime dir: %v", err)
	}
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)

	rawDir := filepath.Join(tmpDir, "raw")
	secretsDir := filepath.Join(tmpDir, "secrets")
	rosterPath := filepath.Join(tmpDir, "roster.toml")
	exportDir := filepath.Join(tmpDir, "export")
	journalPath := filepath.Join(tmpDir, "journal.sqlite")

	if err := os.MkdirAll(secretsDir, 0700); err != nil {
		t.Fatalf("failed to create secrets dir: %v", err)
	}

	// Create a test server with request counter
	requestCount := atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		http.NotFound(w, r)
	}))
	defer server.Close()

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

	// Check should pass (not 4), and probe should proceed (at least one request)
	if exitCode == 4 {
		t.Fatalf("expected non-4 exit code for fake .git directory, got %d", exitCode)
	}

	// Verify the probe proceeded by checking that at least one request was made to the server
	if requestCount.Load() < 1 {
		t.Fatalf("expected at least one request to server (probe proceeded), got %d", requestCount.Load())
	}
}

func TestProbeRawDirUnderEmptyGitFile(t *testing.T) {
	// A raw directory under a directory whose .git is an empty regular file: pass
	tmpDir := t.TempDir()
	runtimeDir := filepath.Join(tmpDir, "runtime")
	gitPath := filepath.Join(tmpDir, ".git")
	if err := os.WriteFile(gitPath, []byte(""), 0600); err != nil {
		t.Fatalf("failed to create empty .git file: %v", err)
	}

	if err := os.MkdirAll(runtimeDir, 0700); err != nil {
		t.Fatalf("failed to create runtime dir: %v", err)
	}
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)

	rawDir := filepath.Join(tmpDir, "raw")
	secretsDir := filepath.Join(tmpDir, "secrets")
	rosterPath := filepath.Join(tmpDir, "roster.toml")
	exportDir := filepath.Join(tmpDir, "export")
	journalPath := filepath.Join(tmpDir, "journal.sqlite")

	if err := os.MkdirAll(secretsDir, 0700); err != nil {
		t.Fatalf("failed to create secrets dir: %v", err)
	}

	// Create a test server with request counter
	requestCount := atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		http.NotFound(w, r)
	}))
	defer server.Close()

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

	// Check should pass (not 4), and probe should proceed (at least one request)
	if exitCode == 4 {
		t.Fatalf("expected non-4 exit code for empty .git file, got %d", exitCode)
	}

	// Verify the probe proceeded by checking that at least one request was made to the server
	if requestCount.Load() < 1 {
		t.Fatalf("expected at least one request to server (probe proceeded), got %d", requestCount.Load())
	}
}

func TestProbeRawDirUnderGitDirWithHeadDirectory(t *testing.T) {
	// A .git directory containing a directory named HEAD (not a file) should not qualify as a repository: pass
	tmpDir := t.TempDir()
	runtimeDir := filepath.Join(tmpDir, "runtime")
	gitDir := filepath.Join(tmpDir, ".git")
	if err := os.Mkdir(gitDir, 0700); err != nil {
		t.Fatalf("failed to create .git: %v", err)
	}
	// Create a directory named HEAD instead of a file
	headPath := filepath.Join(gitDir, "HEAD")
	if err := os.Mkdir(headPath, 0700); err != nil {
		t.Fatalf("failed to create HEAD directory: %v", err)
	}

	if err := os.MkdirAll(runtimeDir, 0700); err != nil {
		t.Fatalf("failed to create runtime dir: %v", err)
	}
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)

	rawDir := filepath.Join(tmpDir, "raw")
	secretsDir := filepath.Join(tmpDir, "secrets")
	rosterPath := filepath.Join(tmpDir, "roster.toml")
	exportDir := filepath.Join(tmpDir, "export")
	journalPath := filepath.Join(tmpDir, "journal.sqlite")

	if err := os.MkdirAll(secretsDir, 0700); err != nil {
		t.Fatalf("failed to create secrets dir: %v", err)
	}

	// Create a test server with request counter
	requestCount := atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		http.NotFound(w, r)
	}))
	defer server.Close()

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

	// Should not be 4 since HEAD is a directory, not a regular file
	if exitCode == 4 {
		t.Fatalf("expected non-4 exit code for HEAD directory, got %d", exitCode)
	}

	// Verify the probe proceeded (at least one request was made)
	if requestCount.Load() < 1 {
		t.Fatalf("expected at least one request to server (probe proceeded), got %d", requestCount.Load())
	}
}

func TestProbeRawDirUnderFakeGitDirWithRealRepoAbove(t *testing.T) {
	// A non-repository .git directory nearer the raw directory, with a real repository two levels further up: exit 4
	tmpDir := t.TempDir()
	runtimeDir := filepath.Join(tmpDir, "runtime")

	// Create a real repository at the top level
	topGitDir := filepath.Join(tmpDir, ".git")
	if err := os.Mkdir(topGitDir, 0700); err != nil {
		t.Fatalf("failed to create top .git: %v", err)
	}
	topHeadPath := filepath.Join(topGitDir, "HEAD")
	if err := os.WriteFile(topHeadPath, []byte("ref: refs/heads/main\n"), 0600); err != nil {
		t.Fatalf("failed to create top HEAD: %v", err)
	}

	// Create a fake .git directory at a nested level
	nestedDir := filepath.Join(tmpDir, "nested")
	if err := os.Mkdir(nestedDir, 0700); err != nil {
		t.Fatalf("failed to create nested dir: %v", err)
	}
	fakeGitDir := filepath.Join(nestedDir, ".git")
	if err := os.Mkdir(fakeGitDir, 0700); err != nil {
		t.Fatalf("failed to create fake .git: %v", err)
	}
	// Create only hooks, no HEAD
	hooksDir := filepath.Join(fakeGitDir, "hooks")
	if err := os.Mkdir(hooksDir, 0700); err != nil {
		t.Fatalf("failed to create hooks: %v", err)
	}

	if err := os.MkdirAll(runtimeDir, 0700); err != nil {
		t.Fatalf("failed to create runtime dir: %v", err)
	}
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)

	rawDir := filepath.Join(nestedDir, "raw")
	secretsDir := filepath.Join(tmpDir, "secrets")
	rosterPath := filepath.Join(tmpDir, "roster.toml")
	exportDir := filepath.Join(tmpDir, "export")
	journalPath := filepath.Join(tmpDir, "journal.sqlite")

	if err := os.MkdirAll(secretsDir, 0700); err != nil {
		t.Fatalf("failed to create secrets dir: %v", err)
	}

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
		"--endpoint", "test-endpoint",
		"--model", "test-model",
		"--raw-dir", rawDir,
		"--export-dir", exportDir,
		"--journal", journalPath,
		"--secrets-dir", secretsDir,
	})

	// Should exit 4 because the real repository at the top level is found after skipping the fake one
	if exitCode != 4 {
		t.Fatalf("expected exit code 4 for real repo above fake .git, got %d", exitCode)
	}
}

func TestProbeRawDirUnderUnreadableGitDir(t *testing.T) {
	// A .git directory that cannot be read (mode 0000): exit 4
	// Skip if running as root
	if os.Geteuid() == 0 {
		t.Skip("skipping test when running as root")
	}

	tmpDir := t.TempDir()
	runtimeDir := filepath.Join(tmpDir, "runtime")
	gitDir := filepath.Join(tmpDir, ".git")
	if err := os.Mkdir(gitDir, 0700); err != nil {
		t.Fatalf("failed to create .git: %v", err)
	}

	// Make it unreadable
	if err := os.Chmod(gitDir, 0000); err != nil {
		t.Fatalf("failed to chmod .git: %v", err)
	}
	t.Cleanup(func() {
		// Restore permissions for cleanup
		_ = os.Chmod(gitDir, 0700)
	})

	if err := os.MkdirAll(runtimeDir, 0700); err != nil {
		t.Fatalf("failed to create runtime dir: %v", err)
	}
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)

	rawDir := filepath.Join(tmpDir, "raw")
	secretsDir := filepath.Join(tmpDir, "secrets")
	rosterPath := filepath.Join(tmpDir, "roster.toml")
	exportDir := filepath.Join(tmpDir, "export")
	journalPath := filepath.Join(tmpDir, "journal.sqlite")

	if err := os.MkdirAll(secretsDir, 0700); err != nil {
		t.Fatalf("failed to create secrets dir: %v", err)
	}

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
		"--endpoint", "test-endpoint",
		"--model", "test-model",
		"--raw-dir", rawDir,
		"--export-dir", exportDir,
		"--journal", journalPath,
		"--secrets-dir", secretsDir,
	})

	// Should exit 4 because we fail closed when can't inspect .git
	if exitCode != 4 {
		t.Fatalf("expected exit code 4 for unreadable .git dir, got %d", exitCode)
	}
}

func TestProbeRawDirThroughSymlinkToRepo(t *testing.T) {
	// A raw directory reached through a symbolic link into a real repository: exit 4
	tmpDir := t.TempDir()
	runtimeDir := filepath.Join(tmpDir, "runtime")

	// Create a real repository
	repoDir := filepath.Join(tmpDir, "repo")
	if err := os.Mkdir(repoDir, 0700); err != nil {
		t.Fatalf("failed to create repo dir: %v", err)
	}
	gitDir := filepath.Join(repoDir, ".git")
	if err := os.Mkdir(gitDir, 0700); err != nil {
		t.Fatalf("failed to create .git: %v", err)
	}
	headPath := filepath.Join(gitDir, "HEAD")
	if err := os.WriteFile(headPath, []byte("ref: refs/heads/main\n"), 0600); err != nil {
		t.Fatalf("failed to create HEAD: %v", err)
	}

	// Create a symlink to the repo
	linkDir := filepath.Join(tmpDir, "link")
	if err := os.Symlink(repoDir, linkDir); err != nil {
		t.Fatalf("failed to create symlink: %v", err)
	}

	if err := os.MkdirAll(runtimeDir, 0700); err != nil {
		t.Fatalf("failed to create runtime dir: %v", err)
	}
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)

	rawDir := filepath.Join(linkDir, "raw")
	secretsDir := filepath.Join(tmpDir, "secrets")
	rosterPath := filepath.Join(tmpDir, "roster.toml")
	exportDir := filepath.Join(tmpDir, "export")
	journalPath := filepath.Join(tmpDir, "journal.sqlite")

	if err := os.MkdirAll(secretsDir, 0700); err != nil {
		t.Fatalf("failed to create secrets dir: %v", err)
	}

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
		"--endpoint", "test-endpoint",
		"--model", "test-model",
		"--raw-dir", rawDir,
		"--export-dir", exportDir,
		"--journal", journalPath,
		"--secrets-dir", secretsDir,
	})

	if exitCode != 4 {
		t.Fatalf("expected exit code 4 for raw dir through symlink to repo, got %d", exitCode)
	}
}

func TestProbeJournalFailureLockRelease(t *testing.T) {
	tmpDir := t.TempDir()
	runtimeDir := filepath.Join(tmpDir, "runtime")
	rawDir := filepath.Join(tmpDir, "raw")
	secretsDir := filepath.Join(tmpDir, "secrets")
	rosterPath := filepath.Join(tmpDir, "roster.toml")
	exportDir := filepath.Join(tmpDir, "export")

	if err := os.MkdirAll(runtimeDir, 0700); err != nil {
		t.Fatalf("failed to create runtime dir: %v", err)
	}
	if err := os.MkdirAll(secretsDir, 0700); err != nil {
		t.Fatalf("failed to create secrets dir: %v", err)
	}

	// Create a file at the journal parent path to force journal creation to fail
	journalParent := filepath.Join(tmpDir, "journal-parent")
	if err := os.WriteFile(journalParent, []byte("not-a-directory"), 0600); err != nil {
		t.Fatalf("failed to create blocking file: %v", err)
	}
	journalPath := filepath.Join(journalParent, "journal.sqlite")

	t.Setenv("HOME", tmpDir)
	t.Setenv("XDG_DATA_HOME", filepath.Join(tmpDir, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpDir, "config"))
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/models") {
			_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"test-model","object":"model"}]}`)
		} else {
			_, _ = io.WriteString(w, `{
				"model": "test-model",
				"choices": [{"message":{"role":"assistant","content":"test"},"finish_reason":"stop"}],
				"usage":{"prompt_tokens":10,"completion_tokens":5}
			}`)
		}
	}))
	defer server.Close()

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

	// Run the probe - should fail because journal directory can't be created
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

	if exitCode != 1 {
		t.Fatalf("expected exit code 1 (error), got %d", exitCode)
	}

	// Verify lock was released by acquiring it
	paths, err := lockfile.Resolve(os.Getenv, tmpDir)
	if err != nil {
		t.Fatalf("failed to resolve paths: %v", err)
	}

	lock, err := lockfile.Acquire(paths.Lock, time.Now())
	if err != nil {
		t.Fatalf("lock was not released after journal failure: %v", err)
	}
	defer func() {
		if err := lock.Release(); err != nil {
			t.Logf("failed to release lock: %v", err)
		}
	}()
}

func TestProbeMeteredEndpointWithoutSpendingControl(t *testing.T) {
	tmpDir := t.TempDir()
	rosterPath := filepath.Join(tmpDir, "roster.toml")

	// Create roster with metered endpoint but no spending control
	rosterContent := `
default_preset = "test-preset"

[[participants]]
id = "author"
model = "test-model"
route = "api"
endpoint = "metered-endpoint"
eligible_roles = ["author"]
pool = "default"

[[endpoints]]
name = "metered-endpoint"
base_url = "http://localhost:9999"
dialect = "chat_completions_v1"
models = ["test-model"]
declared_location = "local"
declared_execution = "remote"
declared_billing = "metered"
network_dependency = "other"
max_tokens_field = "max_tokens"
tokens_per_byte_max = 1.0

[[participants]]
id = "reviewer"
model = "test-model"
route = "api"
endpoint = "metered-endpoint"
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
	var stderr bytes.Buffer
	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	fs := flag.NewFlagSet("probe", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	exitCode := cmdProbe(fs, []string{
		"--roster", rosterPath,
		"--endpoint", "metered-endpoint",
		"--model", "test-model",
		"--raw-dir", filepath.Join(tmpDir, "raw"),
		"--export-dir", filepath.Join(tmpDir, "export"),
		"--journal", filepath.Join(tmpDir, "journal.sqlite"),
		"--secrets-dir", filepath.Join(tmpDir, "secrets"),
	})

	if err := w.Close(); err != nil {
		t.Logf("failed to close pipe writer: %v", err)
	}
	os.Stderr = oldStderr
	if _, err := io.Copy(&stderr, r); err != nil {
		t.Fatalf("failed to copy from pipe: %v", err)
	}

	// Should fail with exit code 2 because metered endpoint lacks external spending control
	if exitCode != 2 {
		t.Fatalf("expected exit code 2, got %d\nStderr: %s", exitCode, stderr.String())
	}

	output := stderr.String()
	if !strings.Contains(output, "metered") || !strings.Contains(output, "spending ceiling") {
		t.Fatalf("expected error message about metered billing and spending ceiling, got: %s", output)
	}
}

func TestProbeMeteredEndpointWithExternalSpendingControl(t *testing.T) {
	tmpDir := t.TempDir()
	runtimeDir := filepath.Join(tmpDir, "runtime")
	rawDir := filepath.Join(tmpDir, "raw")
	exportDir := filepath.Join(tmpDir, "export")
	journalPath := filepath.Join(tmpDir, "journal.sqlite")
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

	// Create roster with metered endpoint and external spending control
	rosterContent := fmt.Sprintf(`
default_preset = "test-preset"

[[participants]]
id = "author"
model = "test-model"
route = "api"
endpoint = "metered-endpoint"
eligible_roles = ["author"]
pool = "default"

[[endpoints]]
name = "metered-endpoint"
base_url = "%s"
dialect = "chat_completions_v1"
models = ["test-model"]
declared_location = "local"
declared_execution = "remote"
declared_billing = "metered"
spending_control = "external"
network_dependency = "other"
max_tokens_field = "max_tokens"
tokens_per_byte_max = 1.0

[[participants]]
id = "reviewer"
model = "test-model"
route = "api"
endpoint = "metered-endpoint"
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

	// Run the probe command with output draining in a goroutine to prevent deadlock
	oldStdout := os.Stdout
	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stdout = w
	os.Stderr = w

	var outputStr string
	done := make(chan struct{})
	go func() {
		defer close(done)
		output, _ := io.ReadAll(r)
		outputStr = string(output)
	}()

	fs := flag.NewFlagSet("probe", flag.ContinueOnError)
	exitCode := cmdProbe(fs, []string{
		"--roster", rosterPath,
		"--endpoint", "metered-endpoint",
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
	<-done

	// Should succeed with exit code 0
	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d\nOutput: %s", exitCode, outputStr)
	}

	// Verify files were written
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

	// Verify export file is valid JSON and declares metered billing truthfully
	exportPath := filepath.Join(exportDir, exportFiles[0].Name())
	exportData, err := os.ReadFile(exportPath)
	if err != nil {
		t.Fatalf("failed to read export: %v", err)
	}

	var exp evidence.Export
	if err := json.Unmarshal(exportData, &exp); err != nil {
		t.Fatalf("failed to parse export JSON: %v", err)
	}

	if exp.DeclaredBilling != "metered" {
		t.Fatalf("expected declared_billing 'metered', got %q", exp.DeclaredBilling)
	}

	// Verify the probe manifest was written with fingerprint
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

	// Parse the probe manifest to verify it's valid
	probeManifest, hasFingerprint, err := evidence.ParseProbeManifest([]byte(runManifest.String))
	if err != nil {
		t.Fatalf("failed to parse probe manifest: %v", err)
	}

	if !hasFingerprint {
		t.Fatalf("probe manifest should have fingerprint")
	}

	if probeManifest.ConfigFingerprint == "" {
		t.Fatalf("probe manifest should have non-empty config_fingerprint")
	}
}

func TestProbeIncompleteWritesDiagnosisAndFails(t *testing.T) {
	tmpDir := t.TempDir()
	rawDir := filepath.Join(tmpDir, "raw")
	secretsDir := filepath.Join(tmpDir, "secrets")
	exportDir := filepath.Join(tmpDir, "export")
	rosterPath := filepath.Join(tmpDir, "roster.toml")
	journalPath := filepath.Join(tmpDir, "journal.sqlite")
	if err := os.MkdirAll(secretsDir, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", tmpDir)
	t.Setenv("XDG_DATA_HOME", filepath.Join(tmpDir, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpDir, "config"))
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(tmpDir, "runtime"))

	// The server never reports a model, so the probe is incomplete.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/models") {
			_, _ = io.WriteString(w, `{"object":"list","data":[]}`)
			return
		}
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"test"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10}}`)
	}))
	defer server.Close()

	roster := fmt.Sprintf(`
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
	if err := os.WriteFile(rosterPath, []byte(roster), 0600); err != nil {
		t.Fatal(err)
	}

	fs := flag.NewFlagSet("probe", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	exitCode := cmdProbe(fs, []string{
		"--roster", rosterPath, "--endpoint", "test-endpoint", "--model", "test-model",
		"--raw-dir", rawDir, "--export-dir", exportDir, "--journal", journalPath, "--secrets-dir", secretsDir,
	})
	if exitCode != 1 {
		t.Fatalf("exit code = %d, want 1 for an incomplete probe", exitCode)
	}
	for _, dir := range []string{rawDir, exportDir} {
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) == 0 {
			t.Fatalf("expected diagnosis artefacts in %s (err=%v)", dir, err)
		}
	}
	if _, err := os.Stat(journalPath); err == nil {
		t.Fatalf("incomplete probe must not create a usable journal record")
	}
}
