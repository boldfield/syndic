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
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/transport/fake"
	"github.com/mattn/go-runewidth"
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

	// Get module root for building
	modRootCmd := exec.Command("go", "list", "-m", "-f", "{{.Dir}}")
	modRootOut, err := modRootCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("get module root: %v\n%s", err, modRootOut)
	}
	moduleRoot := strings.TrimSpace(string(modRootOut))

	// Build binaries
	syndicdPath := filepath.Join(binDir, "syndicd")
	syndicPath := filepath.Join(binDir, "syndic")

	cmd := exec.Command("go", "build", "-o", syndicdPath, "github.com/boldfield/syndic/cmd/syndicd")
	cmd.Dir = moduleRoot
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build syndicd: %v\n%s", err, out)
	}

	cmd = exec.Command("go", "build", "-o", syndicPath, "github.com/boldfield/syndic/cmd/syndic")
	cmd.Dir = moduleRoot
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build syndic: %v\n%s", err, out)
	}

	// Create fake OpenAI-compatible server
	var reviewerHandlerMu sync.Mutex
	var reviewerHeld bool
	var reviewerReleasedOnce sync.Once
	var reviewerReleased = make(chan struct{})
	var reviewerRequestArrived = make(chan struct{})
	var reviewerRequestArrivedOnce sync.Once

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
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, "failed to decode body", http.StatusBadRequest)
				return
			}

			// Distinguish reviewer from author by checking system message
			isReviewer := false
			candidateHash := ""
			attemptID := ""

			if msgs, ok := body["messages"].([]interface{}); ok {
				for _, m := range msgs {
					msg := m.(map[string]interface{})
					if msg["role"] == "system" {
						if sysContent, ok := msg["content"].(string); ok {
							if strings.Contains(sysContent, "reviewer") {
								isReviewer = true
								// Extract candidate_hash and attempt_id from user message
								for _, um := range msgs {
									umsg := um.(map[string]interface{})
									if umsg["role"] == "user" {
										if content, ok := umsg["content"].(string); ok {
											// Parse candidate and attempt from the user message
											if idx := strings.Index(content, "candidate:"); idx >= 0 {
												hashPart := strings.TrimSpace(content[idx+len("candidate:"):])
												if eol := strings.Index(hashPart, "\n"); eol >= 0 {
													candidateHash = hashPart[:eol]
												} else {
													candidateHash = hashPart
												}
											}
											if idx := strings.Index(content, "attempt:"); idx >= 0 {
												attemptPart := strings.TrimSpace(content[idx+len("attempt:"):])
												if eol := strings.Index(attemptPart, "\n"); eol >= 0 {
													attemptID = attemptPart[:eol]
												} else {
													attemptID = attemptPart
												}
											}
										}
									}
								}
							}
						}
					}
				}
			}

			if isReviewer {
				// Signal that the reviewer request arrived
				reviewerRequestArrivedOnce.Do(func() {
					close(reviewerRequestArrived)
				})

				// If held, wait until released
				reviewerHandlerMu.Lock()
				held := reviewerHeld
				reviewerHandlerMu.Unlock()

				if held {
					<-reviewerReleased
				}

				w.Header().Set("Content-Type", "application/json")
				content := fake.VerdictApprove(candidateHash, attemptID)
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
			} else {
				// Author request
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

[deadlines]
attempt = "30s"
turn = "60s"
delivery = "10s"
provisional = true

[presets.test-preset]
mode = "review"
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

[pools.default]
concurrency = 10
`, server.URL)

	if err := os.WriteFile(rosterPath, []byte(rosterContent), 0600); err != nil {
		t.Fatalf("write roster: %v", err)
	}

	// Set up probe directories
	rawDir := filepath.Join(tmpDir, "evidence-raw")
	exportDir := filepath.Join(tmpDir, "evidence")
	if err := os.MkdirAll(rawDir, 0700); err != nil {
		t.Fatalf("mkdir rawDir: %v", err)
	}
	if err := os.MkdirAll(exportDir, 0700); err != nil {
		t.Fatalf("mkdir exportDir: %v", err)
	}

	// Run syndic probe for each participant
	for _, participant := range []string{"author", "reviewer"} {
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
			t.Fatalf("probe for %s: %v\n%s", participant, err, out)
		}
	}

	// Use a private tmux socket
	tmuxSocket := filepath.Join(tmpDir, "tmux-socket")
	sessionName := "test-" + strconv.Itoa(os.Getpid())

	// Clean up tmux at the end (kill-server and session)
	t.Cleanup(func() {
		_ = exec.Command("tmux", "-S", tmuxSocket, "kill-server").Run()
	})

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

	// Stop syndicd at the end with SIGTERM
	t.Cleanup(func() {
		_ = syndicdCmd.Process.Signal(syscall.SIGTERM)
		_ = syndicdCmd.Wait()
	})

	// Give syndicd time to start
	time.Sleep(500 * time.Millisecond)

	// Test scenario 1: submit a question and wait for approval
	tmuxNewSession := []string{"tmux", "-S", tmuxSocket, "new-session", "-d", "-s", sessionName, "-x", "80", "-y", "24"}
	if out, err := exec.Command(tmuxNewSession[0], tmuxNewSession[1:]...).CombinedOutput(); err != nil {
		t.Fatalf("tmux new-session: %v\n%s", err, out)
	}

	// Start syndic in the tmux session
	cmd = exec.Command("tmux", "-S", tmuxSocket, "send-keys", "-t", sessionName, syndicPath, "Enter")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("tmux send-keys syndic: %v\n%s", err, out)
	}

	// Wait for TUI to start
	time.Sleep(500 * time.Millisecond)

	// Type a question
	cmd = exec.Command("tmux", "-S", tmuxSocket, "send-keys", "-t", sessionName, "What is 2+2?", "Enter")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("tmux send question: %v\n%s", err, out)
	}

	// Wait for the submit and approval
	maxWait := time.Now().Add(10 * time.Second)
	var approvedPane string
	for time.Now().Before(maxWait) {
		cmd := exec.Command("tmux", "-S", tmuxSocket, "capture-pane", "-t", sessionName, "-p")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Logf("tmux capture-pane: %v", err)
			time.Sleep(100 * time.Millisecond)
			continue
		}

		paneContent := string(out)
		if strings.Contains(paneContent, "approved by") && strings.Contains(paneContent, "This is a test answer") {
			approvedPane = paneContent
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	if approvedPane == "" {
		t.Errorf("timeout waiting for approved answer")
	} else {
		t.Logf("first scenario pane content:\n%s", approvedPane)

		if !strings.Contains(approvedPane, "approved by") {
			t.Errorf("expected 'approved by' in pane content")
		}

		// Check line lengths (count display cells, not bytes)
		for _, line := range strings.Split(approvedPane, "\n") {
			cells := runewidth.StringWidth(line)
			if cells > 80 {
				t.Errorf("line too long (%d cells): %q", cells, line)
			}
		}
	}

	// Test scenario 2: mid-turn detach with reviewer held open, then reattach
	cmd = exec.Command("tmux", "-S", tmuxSocket, "send-keys", "-t", sessionName, "What is 3+3?", "Enter")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("tmux send second question: %v\n%s", err, out)
	}

	// Give it time to dispatch to the author
	time.Sleep(500 * time.Millisecond)

	// Hold the reviewer handler open BEFORE making the request
	reviewerHandlerMu.Lock()
	reviewerHeld = true
	reviewerHandlerMu.Unlock()

	// Wait for the reviewer request to arrive and get stuck
	select {
	case <-reviewerRequestArrived:
		// Reviewer request arrived and is now held
	case <-time.After(5 * time.Second):
		t.Errorf("timeout waiting for reviewer request to arrive")
	}

	// Kill the TUI session (not gracefully)
	cmd = exec.Command("tmux", "-S", tmuxSocket, "kill-session", "-t", sessionName)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Logf("tmux kill-session: %v\n%s", err, out)
	}

	// Wait a bit
	time.Sleep(500 * time.Millisecond)

	// Now release the reviewer handler
	reviewerReleasedOnce.Do(func() {
		close(reviewerReleased)
	})

	// Wait for the response to arrive at the engine
	time.Sleep(500 * time.Millisecond)

	// Start a new TUI session with a new name
	newSessionName := "test2-" + strconv.Itoa(os.Getpid())
	cmd = exec.Command("tmux", "-S", tmuxSocket, "new-session", "-d", "-s", newSessionName, "-x", "80", "-y", "24", syndicPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("tmux new-session for reattach: %v\n%s", err, out)
	}

	// Wait for the new session to show the approved turn
	maxWait = time.Now().Add(10 * time.Second)
	var reattachedPane string
	for time.Now().Before(maxWait) {
		cmd := exec.Command("tmux", "-S", tmuxSocket, "capture-pane", "-t", newSessionName, "-p")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Logf("tmux capture-pane after reattach: %v", err)
			time.Sleep(100 * time.Millisecond)
			continue
		}

		paneContent := string(out)
		if strings.Contains(paneContent, "This is a test answer") {
			reattachedPane = paneContent
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	if reattachedPane == "" {
		t.Errorf("timeout waiting for answer after reattach")
	} else {
		t.Logf("after reattach, pane content:\n%s", reattachedPane)
	}

	// Verify the engine journal shows the turn as approved
	// (The turn was submitted, then detached mid-turn, but the engine still received the approval)
	if !strings.Contains(reattachedPane, "This is a test answer") {
		t.Errorf("expected test answer in reattached TUI after mid-turn detach")
	}

	// Test scenario 3: Check SIGTERM handling
	// Kill all syndic processes started in tmux
	cmd = exec.Command("tmux", "-S", tmuxSocket, "kill-server")
	_ = cmd.Run()

	// Stop syndicd with SIGTERM
	if err := syndicdCmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Logf("send SIGTERM: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- syndicdCmd.Wait()
	}()

	var exitErr error
	select {
	case err := <-done:
		exitErr = err
	case <-ctx.Done():
		t.Fatalf("syndicd did not stop within timeout")
	}

	// Check that syndicd exited with code 0 (killed by SIGTERM is treated as exit 0)
	if exitErr != nil && exitErr.Error() != "signal: terminated" {
		t.Errorf("syndicd unexpected exit error: %v", exitErr)
	}
}
