package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// runContextProbe runs `syndic probe` against a fake endpoint whose usage
// scales with prompt size, returning the largest user message the server saw
// and the exported over_length outcome and classifier.
func runContextProbe(t *testing.T, participantCtx string, extraArgs ...string) (int64, string, string) {
	t.Helper()
	tmpDir := t.TempDir()
	rawDir := filepath.Join(tmpDir, "raw")
	exportDir := filepath.Join(tmpDir, "export")
	secretsDir := filepath.Join(tmpDir, "secrets")
	rosterPath := filepath.Join(tmpDir, "roster.toml")
	t.Setenv("XDG_RUNTIME_DIR", tmpDir)
	if err := os.MkdirAll(secretsDir, 0700); err != nil {
		t.Fatal(err)
	}

	var maxLen atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": []map[string]string{{"id": "test-model"}}})
			return
		}
		var body struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		n := 0
		for _, m := range body.Messages {
			if m.Role == "user" {
				n = len(m.Content)
			}
		}
		if int64(n) > maxLen.Load() {
			maxLen.Store(int64(n))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model":   "test-model",
			"choices": []map[string]any{{"message": map[string]string{"role": "assistant", "content": "ok"}, "finish_reason": "stop"}},
			"usage":   map[string]int{"prompt_tokens": 10 + n/4, "completion_tokens": 1},
		})
	}))
	defer server.Close()

	roster := fmt.Sprintf(`
default_preset = "p"

[[participants]]
id = "author"
model = "test-model"
route = "api"
endpoint = "test-endpoint"
eligible_roles = ["author"]
pool = "default"
%s

[[participants]]
id = "reviewer"
model = "test-model"
route = "api"
endpoint = "test-endpoint"
eligible_roles = ["reviewer"]
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

[presets.p]
mode = "review"
author = "author"
required_reviewers = ["reviewer"]
`, participantCtx, server.URL)
	if err := os.WriteFile(rosterPath, []byte(roster), 0600); err != nil {
		t.Fatal(err)
	}

	args := append([]string{
		"--roster", rosterPath, "--endpoint", "test-endpoint", "--model", "test-model",
		"--raw-dir", rawDir, "--export-dir", exportDir,
		"--journal", filepath.Join(tmpDir, "journal.sqlite"), "--secrets-dir", secretsDir,
	}, extraArgs...)
	if code := cmdProbe(flag.NewFlagSet("probe", flag.ContinueOnError), args); code != 0 {
		t.Fatalf("probe exit code %d", code)
	}

	files, _ := filepath.Glob(filepath.Join(exportDir, "*.json"))
	if len(files) != 1 {
		t.Fatalf("expected one export, got %v", files)
	}
	data, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	var exp struct {
		OverLength           string `json:"over_length"`
		OverLengthClassifier string `json:"over_length_classifier"`
	}
	if err := json.Unmarshal(data, &exp); err != nil {
		t.Fatal(err)
	}
	return maxLen.Load(), exp.OverLength, exp.OverLengthClassifier
}

func TestProbeContextTokensResolution(t *testing.T) {
	// Participant context_tokens is used when --context-tokens is absent.
	partMax, partOutcome, _ := runContextProbe(t, "context_tokens = 4000")
	if strings.HasPrefix(partOutcome, "not_run") {
		t.Fatalf("participant context_tokens ignored: outcome %q", partOutcome)
	}

	// --context-tokens overrides the participant value.
	flagMax, flagOutcome, _ := runContextProbe(t, "context_tokens = 4000", "--context-tokens", "400")
	if strings.HasPrefix(flagOutcome, "not_run") {
		t.Fatalf("--context-tokens ignored: outcome %q", flagOutcome)
	}
	if flagMax*3 >= partMax {
		t.Errorf("flag should override participant: largest prompt %d bytes with flag, %d with participant only", flagMax, partMax)
	}

	// No flag and no participant context_tokens: not_run:no_context.
	_, noneOutcome, rule := runContextProbe(t, "")
	if noneOutcome != "not_run:no_context" {
		t.Errorf("no context configured: got %q, want not_run:no_context", noneOutcome)
	}
	if rule == "" {
		t.Error("classification rule missing from export")
	}
}

func TestProbeSkipOverLengthExport(t *testing.T) {
	_, outcome, rule := runContextProbe(t, "context_tokens = 4000", "--skip-overlength")
	if outcome != "not_run:skipped" {
		t.Errorf("got %q, want not_run:skipped", outcome)
	}
	if rule == "" {
		t.Error("classification rule must be exported even when over-length is skipped")
	}
}
