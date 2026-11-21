package acceptance

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/transport/fake"
)

func init() {
	cover("TUI", "scripted tmux run with detach/reattach and binary interactivity", "TestTMUX_EndToEnd")
}

// TestTMUX_EndToEnd tests the whole alpha in a real tmux session with real binaries.
// It runs only when tmux is on the path and the environment variable SYNDIC_TMUX_TEST=1 is set.
func TestTMUX_EndToEnd(t *testing.T) {
	if os.Getenv("SYNDIC_TMUX_TEST") != "1" {
		gap(t, "SYNDIC_TMUX_TEST not set")
	}

	// Check tmux is available
	if _, err := exec.LookPath("tmux"); err != nil {
		gap(t, fmt.Sprintf("tmux not on path: %v", err))
	}

	tmpDir := t.TempDir()
	t.Logf("using temp directory: %s", tmpDir)

	// Set up environment
	runtimeDir := filepath.Join(tmpDir, "run")
	if err := os.MkdirAll(runtimeDir, 0700); err != nil {
		t.Fatalf("mkdir runtimeDir: %v", err)
	}
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)

	binDir := filepath.Join(tmpDir, "bin")
	if err := os.MkdirAll(binDir, 0700); err != nil {
		t.Fatalf("mkdir binDir: %v", err)
	}

	// Build binaries
	syndicdPath := filepath.Join(binDir, "syndicd")
	syndicPath := filepath.Join(binDir, "syndic")

	cmd := exec.Command("go", "build", "-o", syndicdPath, "./cmd/syndicd")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build syndicd: %v\n%s", err, out)
	}

	cmd = exec.Command("go", "build", "-o", syndicPath, "./cmd/syndic")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build syndic: %v\n%s", err, out)
	}

	// Create fake OpenAI-compatible server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"object": "list",
				"data": []map[string]string{
					{"id": "test-model", "object": "model"},
				},
			})
		} else if strings.HasSuffix(r.URL.Path, "/chat/completions") {
			w.Header().Set("Content-Type", "application/json")

			var body map[string]interface{}
			_ = json.NewDecoder(r.Body).Decode(&body)

			msgs, _ := body["messages"].([]interface{})
			role := ""
			for _, m := range msgs {
				mm, ok := m.(map[string]interface{})
				if ok && mm["role"] == "user" {
					role = "author"
					break
				}
			}

			if role == "author" {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"id":     "chatcmpl-123",
					"object": "chat.completion",
					"model":  "test-model",
					"choices": []map[string]interface{}{{
						"message":       map[string]string{"role": "assistant", "content": "This is a test answer to your question."},
						"finish_reason": "stop",
					}},
					"usage": map[string]int{"prompt_tokens": 10, "completion_tokens": 5},
				})
			} else {
				w.Header().Set("Content-Type", "application/json")
				content := fake.VerdictApprove("", "")
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"id":     "chatcmpl-456",
					"object": "chat.completion",
					"model":  "test-model",
					"choices": []map[string]interface{}{{
						"message":       map[string]string{"role": "assistant", "content": content},
						"finish_reason": "stop",
					}},
					"usage": map[string]int{"prompt_tokens": 20, "completion_tokens": 10},
				})
			}
		} else {
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	// Create roster
	journalPath := filepath.Join(tmpDir, "journal.db")
	secretsDir := filepath.Join(tmpDir, "secrets")
	if err := os.MkdirAll(secretsDir, 0700); err != nil {
		t.Fatalf("mkdir secretsDir: %v", err)
	}

	rosterPath := filepath.Join(tmpDir, "roster.toml")
	rosterContent := fmt.Sprintf(`
default_preset = "test-preset"

[[presets]]
name = "test-preset"
mode = "governed"
author = "author"
required_reviewers = ["reviewer"]

[[participants]]
id = "author"
model = "test-model"
route = "api"
endpoint = "test-endpoint"
eligible_roles = ["author"]
pool = "default"

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

[[pools]]
name = "default"
concurrency = 10

[[deadlines]]
phase = "attempt"
duration_ms = 30000

[[deadlines]]
phase = "turn"
duration_ms = 60000
`, server.URL)

	if err := os.WriteFile(rosterPath, []byte(rosterContent), 0600); err != nil {
		t.Fatalf("write roster: %v", err)
	}

	// Run syndic probe for author
	rawDir := filepath.Join(tmpDir, "evidence-raw")
	exportDir := filepath.Join(tmpDir, "evidence")
	if err := os.MkdirAll(rawDir, 0700); err != nil {
		t.Fatalf("mkdir rawDir: %v", err)
	}
	if err := os.MkdirAll(exportDir, 0700); err != nil {
		t.Fatalf("mkdir exportDir: %v", err)
	}

	cmd = exec.Command(syndicPath, "probe",
		"--roster", rosterPath,
		"--endpoint", "test-endpoint",
		"--model", "test-model",
		"--raw-dir", rawDir,
		"--export-dir", exportDir,
		"--journal", journalPath,
		"--secrets-dir", secretsDir,
	)
	cmd.Env = append(os.Environ(),
		"XDG_RUNTIME_DIR="+runtimeDir,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("probe: %v\n%s", err, out)
	}

	// Start syndicd in background
	syndicdCmd := exec.Command(syndicdPath,
		"-roster", rosterPath,
		"-journal", journalPath,
		"-secrets-dir", secretsDir,
	)
	syndicdCmd.Env = append(os.Environ(),
		"XDG_RUNTIME_DIR="+runtimeDir,
	)
	syndicdCmd.Stdout = os.Stderr
	syndicdCmd.Stderr = os.Stderr

	if err := syndicdCmd.Start(); err != nil {
		t.Fatalf("start syndicd: %v", err)
	}
	defer func() {
		_ = syndicdCmd.Process.Signal(os.Interrupt)
		_ = syndicdCmd.Wait()
	}()

	// Give syndicd time to start
	time.Sleep(500 * time.Millisecond)

	// Test scenario 1: submit a question and wait for approval
	sessionName := "test-" + strconv.Itoa(os.Getpid())
	tmuxNewSession := []string{"tmux", "new-session", "-d", "-s", sessionName, "-x", "80", "-y", "24"}
	if out, err := exec.Command(tmuxNewSession[0], tmuxNewSession[1:]...).CombinedOutput(); err != nil {
		t.Fatalf("tmux new-session: %v\n%s", err, out)
	}
	defer func() { _ = exec.Command("tmux", "kill-session", "-t", sessionName).Run() }()

	// Start syndic in the tmux session
	cmd = exec.Command("tmux", "send-keys", "-t", sessionName, syndicPath, "Enter")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("tmux send-keys syndic: %v\n%s", err, out)
	}

	// Wait for TUI to start
	time.Sleep(500 * time.Millisecond)

	// Type a question
	cmd = exec.Command("tmux", "send-keys", "-t", sessionName, "What is 2+2?", "Enter")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("tmux send question: %v\n%s", err, out)
	}

	// Wait for the submit and approval
	maxWait := time.Now().Add(10 * time.Second)
	for time.Now().Before(maxWait) {
		cmd := exec.Command("tmux", "capture-pane", "-t", sessionName, "-p")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Logf("tmux capture-pane: %v", err)
			time.Sleep(100 * time.Millisecond)
			continue
		}

		paneContent := string(out)
		if strings.Contains(paneContent, "approved by") && strings.Contains(paneContent, "This is a test answer") {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Verify pane content
	cmd = exec.Command("tmux", "capture-pane", "-t", sessionName, "-p")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("final capture-pane: %v\n%s", err, out)
	}

	paneContent := string(out)
	t.Logf("final pane content:\n%s", paneContent)

	if !strings.Contains(paneContent, "approved by") {
		t.Errorf("expected 'approved by' in pane content")
	}

	// Check line lengths
	for _, line := range strings.Split(paneContent, "\n") {
		if len(line) > 80 {
			t.Errorf("line too long (%d cells): %q", len(line), line)
		}
	}

	// Test scenario 2: Kill TUI and reattach
	cmd = exec.Command("tmux", "send-keys", "-t", sessionName, "C-c")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Logf("tmux kill syndic: %v\n%s", err, out)
	}

	time.Sleep(500 * time.Millisecond)

	// Start a new TUI session in the same tmux window
	cmd = exec.Command("tmux", "send-keys", "-t", sessionName, syndicPath, "Enter")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("tmux restart syndic: %v\n%s", err, out)
	}

	time.Sleep(1 * time.Second)

	// Verify the finished turn is shown
	cmd = exec.Command("tmux", "capture-pane", "-t", sessionName, "-p")
	out, err = cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("reattach capture-pane: %v\n%s", err, out)
	}

	paneContent = string(out)
	t.Logf("after reattach, pane content:\n%s", paneContent)

	if !strings.Contains(paneContent, "This is a test answer") {
		t.Errorf("expected test answer in reattached TUI")
	}

	// Kill tmux session
	_ = exec.Command("tmux", "kill-session", "-t", sessionName).Run()

	// Stop syndicd with SIGTERM
	_ = syndicdCmd.Process.Signal(syscall.SIGTERM)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- syndicdCmd.Wait()
	}()

	select {
	case err := <-done:
		if err != nil && !strings.Contains(err.Error(), "signal: terminated") {
			t.Logf("syndicd exit: %v", err)
		}
	case <-ctx.Done():
		t.Fatalf("syndicd did not stop within timeout")
	}
}
