package acceptance

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/transport/fake"
	"github.com/mattn/go-runewidth"
)

// The scripted host-pane acceptance: real syndicd and syndic binaries, a real
// tmux server, and a stub OpenAI-compatible endpoint that is the only server
// the test talks to. It runs only when SYNDIC_TMUX_TEST=1 and tmux is on the
// path.

const (
	tmuxAuthorModel   = "stub-author"
	tmuxReviewerModel = "stub-reviewer"

	// blockTrigger in a question makes the stub's reviewer block. The scripted
	// test never uses it; the manual checklist does.
	blockTrigger = "[block]"

	// slowTrigger in a question makes the stub's reviewer wait slowDelay before
	// it answers, so a person has time to detach during the turn.
	slowTrigger = "[slow]"
	slowDelay   = 30 * time.Second

	tmuxPaneWait = 45 * time.Second
	tmuxStepWait = 30 * time.Second
)

// tmuxRoster has one author and one reviewer on two endpoints of the stub,
// told apart by their URL path, with non-provisional deadlines.
const tmuxRoster = `default_preset = "review"

[deadlines]
attempt     = "60s"
turn        = "120s"
delivery    = "10s"
provisional = false

[budgets]
calibration_safety         = 0.75
message_overhead_tokens    = 8
request_overhead_tokens    = 16
context_margin             = 0.10
author_max_output_tokens   = 1024
reviewer_max_output_tokens = 1024
delivery_max_tokens        = 64
turn_max_estimated_tokens  = 100000

[engine]
stop_grace = "5s"

[pools.default]
concurrency             = 2
uncertain_release_after = "0s"

[[participants]]
id = "author"
model = "stub-author"
route = "api"
endpoint = "author-ep"
eligible_roles = ["author"]
pool = "default"
context_tokens = 32768
bytes_per_token = 4.0

[[participants]]
id = "reviewer"
model = "stub-reviewer"
route = "api"
endpoint = "reviewer-ep"
eligible_roles = ["reviewer"]
pool = "default"
context_tokens = 32768
bytes_per_token = 4.0
accept_unknown_truncation = true

[[endpoints]]
name = "author-ep"
base_url = "%[1]s/author/v1"
declared_location = "local"
declared_execution = "local"
declared_billing = "none"
network_dependency = "none"
dialect = "chat_completions_v1"
models = ["stub-author"]
tokens_per_byte_max = 1.0

[[endpoints]]
name = "reviewer-ep"
base_url = "%[1]s/reviewer/v1"
declared_location = "local"
declared_execution = "local"
declared_billing = "none"
network_dependency = "none"
dialect = "chat_completions_v1"
models = ["stub-reviewer"]
tokens_per_byte_max = 1.0

[presets.review]
mode = "review"
author = "author"
required_reviewers = ["reviewer"]

[policy]
local_only = true
allowed_destinations = []
`

var (
	reCandidateHash = regexp.MustCompile(`The candidate hash is: (\S+)`)
	reAttemptID     = regexp.MustCompile(`The attempt ID is: (\S+)`)
	reHeadMarker    = regexp.MustCompile(`<<<HEAD_\d+>>>`)
	reTailMarker    = regexp.MustCompile(`<<<TAIL_\d+>>>`)
)

// holdGate holds reviewer verdict requests open until released.
type holdGate struct {
	arrived     chan struct{} // closed when the first request is held
	release     chan struct{} // closed to let held requests answer
	arrivedOnce sync.Once
}

// chatStub is the stub endpoint. It answers /models, the probe's requests, an
// author request with a fixed answer, and a reviewer request with an approve
// envelope that echoes the candidate hash and attempt id in the request.
type chatStub struct {
	srv  *httptest.Server
	mu   sync.Mutex
	gate *holdGate
}

type stubMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func newChatStub(t *testing.T) *chatStub {
	t.Helper()
	s := &chatStub{}
	s.srv = httptest.NewServer(http.HandlerFunc(s.handle))
	t.Cleanup(s.srv.Close)
	return s
}

// TestTMUX_ServeStub serves the stub for the manual host-pane checklist in
// docs/acceptance/host-panes.md. It runs only with SYNDIC_STUB_SERVE set to a
// directory: it writes a roster there, prints the roster path and blocks until
// interrupted.
func TestTMUX_ServeStub(t *testing.T) {
	dir := os.Getenv("SYNDIC_STUB_SERVE")
	if dir == "" {
		gap(t, "SYNDIC_STUB_SERVE is not set; serving the stub for the manual checklist is opt-in")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := &chatStub{}
	s.srv = httptest.NewUnstartedServer(http.HandlerFunc(s.handle))
	_ = s.srv.Listener.Close()
	s.srv.Listener = ln
	s.srv.Start()
	defer s.srv.Close()

	rosterPath := filepath.Join(dir, "syndic.toml")
	if err := os.WriteFile(rosterPath, []byte(fmt.Sprintf(tmuxRoster, s.srv.URL)), 0o600); err != nil {
		t.Fatalf("write roster: %v", err)
	}
	fmt.Printf("stub serving at %s\nroster written to %s\nPress Ctrl+C to stop.\n", s.srv.URL, rosterPath)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
}

// hold makes reviewer verdict requests block until the returned gate's
// release channel is closed.
func (s *chatStub) hold() *holdGate {
	g := &holdGate{arrived: make(chan struct{}), release: make(chan struct{})}
	s.mu.Lock()
	s.gate = g
	s.mu.Unlock()
	return g
}

func (s *chatStub) currentGate() *holdGate {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gate
}

func (s *chatStub) handle(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/models"):
		_ = json.NewEncoder(w).Encode(map[string]any{
			"object": "list",
			"data": []map[string]string{
				{"id": tmuxAuthorModel, "object": "model"},
				{"id": tmuxReviewerModel, "object": "model"},
			},
		})
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/chat/completions"):
		s.chat(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (s *chatStub) chat(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Model    string        `json:"model"`
		Messages []stubMessage `json:"messages"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var system, user strings.Builder
	total := 0
	for _, m := range req.Messages {
		total += len(m.Content)
		switch m.Role {
		case "system":
			system.WriteString(m.Content)
		case "user":
			user.WriteString(m.Content)
		}
	}

	var reply string
	switch {
	// A reviewer's verdict request carries the hash and attempt id to echo.
	case strings.HasPrefix(r.URL.Path, "/reviewer/") && reCandidateHash.MatchString(system.String()):
		if g := s.currentGate(); g != nil {
			g.arrivedOnce.Do(func() { close(g.arrived) })
			select {
			case <-g.release:
			case <-r.Context().Done():
				return
			}
		}
		if strings.Contains(user.String(), slowTrigger) {
			select {
			case <-time.After(slowDelay):
			case <-r.Context().Done():
				return
			}
		}
		hash := reCandidateHash.FindStringSubmatch(system.String())[1]
		attempt := reAttemptID.FindStringSubmatch(system.String())[1]
		if strings.Contains(user.String(), blockTrigger) {
			reply = fake.VerdictBlock(hash, attempt, "stub-block", "high",
				"The request asks the stub to block.", "Withdraw the block trigger from the request.", "The stub blocks on request.")
			break
		}
		reply = strings.Join([]string{
			"VERDICT v1",
			"candidate: " + hash,
			"attempt: " + attempt,
			"verdict: approve",
			"reason: The stub approves.",
			"END VERDICT",
		}, "\n")
	// The probe's system-role check.
	case strings.Contains(system.String(), "PING"):
		reply = "PING"
	// The probe's over-length check wants both markers echoed back.
	case reHeadMarker.MatchString(user.String()):
		reply = reHeadMarker.FindString(user.String()) + " " + reTailMarker.FindString(user.String())
	// An author request.
	case strings.HasPrefix(r.URL.Path, "/author/") && strings.Contains(system.String(), "author of a document"):
		reply = "Stub answer to: " + strings.TrimSpace(user.String())
	default:
		reply = "ok"
	}

	// Usage grows with the prompt, which is what the probe's calibration reads.
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id":      "stub",
		"object":  "chat.completion",
		"model":   req.Model,
		"choices": []map[string]any{{"index": 0, "finish_reason": "stop", "message": map[string]string{"role": "assistant", "content": reply}}},
		"usage":   map[string]int{"prompt_tokens": 5 + total/4, "completion_tokens": len(reply)/4 + 1, "total_tokens": 6 + total/4 + len(reply)/4},
	})
}

// proc is a child process with its output captured and a single Wait.
type proc struct {
	name string
	cmd  *exec.Cmd
	out  *lockedBuffer
	done chan struct{}
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func startProc(t *testing.T, name string, env []string, bin string, args ...string) *proc {
	t.Helper()
	p := &proc{name: name, out: &lockedBuffer{}, done: make(chan struct{})}
	p.cmd = exec.Command(bin, args...)
	p.cmd.Env = env
	p.cmd.Stdout = p.out
	p.cmd.Stderr = p.out
	if err := p.cmd.Start(); err != nil {
		t.Fatalf("start %s: %v", name, err)
	}
	go func() {
		_ = p.cmd.Wait()
		close(p.done)
	}()
	t.Cleanup(func() {
		select {
		case <-p.done:
		default:
			_ = p.cmd.Process.Kill()
			<-p.done
		}
	})
	return p
}

func (p *proc) exited() bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

// tmuxHost is a tmux server on a private socket.
type tmuxHost struct {
	t       *testing.T
	sock    string
	env     string // the env(1) prefix that gives a child the test's directories
	syndic  string
	paneIDs []int
}

func (h *tmuxHost) run(args ...string) (string, error) {
	cmd := exec.Command("tmux", append([]string{"-S", h.sock}, args...)...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (h *tmuxHost) mustRun(args ...string) string {
	h.t.Helper()
	out, err := h.run(args...)
	if err != nil {
		h.t.Fatalf("tmux %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

// start opens a detached 80x24 session running syndic and records the pid of
// the process in its pane.
func (h *tmuxHost) start(session string, extraEnv ...string) int {
	h.t.Helper()
	args := []string{"new-session", "-d", "-s", session, "-x", "80", "-y", "24", "-P", "-F", "#{pane_pid}"}
	args = append(args, "env "+h.env+" "+strings.Join(extraEnv, " ")+" "+h.syndic)
	pid, err := strconv.Atoi(strings.TrimSpace(h.mustRun(args...)))
	if err != nil {
		h.t.Fatalf("pane pid: %v", err)
	}
	h.paneIDs = append(h.paneIDs, pid)
	return pid
}

func (h *tmuxHost) capture(session string) string {
	out, err := h.run("capture-pane", "-p", "-t", session)
	if err != nil {
		return "(no pane: " + strings.TrimSpace(out) + ")"
	}
	return out
}

// typeLine types text literally and presses Enter.
func (h *tmuxHost) typeLine(session, text string) {
	h.t.Helper()
	h.mustRun("send-keys", "-t", session, "-l", text)
	h.mustRun("send-keys", "-t", session, "Enter")
}

// waitPane polls the pane until ok is true or the wait is over, and on
// timeout fails with the last pane and the daemon's output.
func (h *tmuxHost) waitPane(session, what string, d time.Duration, ok func(pane string) bool, daemon *proc) string {
	h.t.Helper()
	deadline := time.Now().Add(d)
	var pane string
	for {
		pane = h.capture(session)
		if ok(pane) {
			return pane
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("timeout waiting for %s\n--- pane ---\n%s\n--- syndicd output ---\n%s", what, pane, daemon.out.String())
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// killSession kills the whole session, which is how the test closes the
// terminal without /quit, and waits for the TUI process to be gone.
func (h *tmuxHost) killSession(session string, pid int) {
	h.t.Helper()
	h.mustRun("kill-session", "-t", session)
	waitFor(h.t, "the TUI process to exit", tmuxStepWait, func() bool { return !pidAlive(pid) })
}

func pidAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

func waitFor(t *testing.T, what string, d time.Duration, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// checkWidth fails if any line of the pane is wider than 80 cells.
func checkWidth(t *testing.T, pane string) {
	t.Helper()
	for i, line := range strings.Split(pane, "\n") {
		if w := runewidth.StringWidth(line); w > 80 {
			t.Errorf("pane line %d is %d cells wide, want at most 80: %q", i+1, w, line)
		}
	}
}

// turnStates reads the journal's turns, oldest first, as terminal states; an
// open turn is the empty string.
func turnStates(t *testing.T, path string) []string {
	t.Helper()
	db, err := journal.Open(path)
	if err != nil {
		t.Fatalf("open journal: %v", err)
	}
	defer func() { _ = db.Close() }()
	rows, err := db.Conn().Query(`SELECT COALESCE(terminal_state, '') FROM turns ORDER BY rowid`)
	if err != nil {
		t.Fatalf("query turns: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var states []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatalf("scan turn: %v", err)
		}
		states = append(states, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("turns: %v", err)
	}
	return states
}

func buildBinary(t *testing.T, root, out, pkg string) {
	t.Helper()
	cmd := exec.Command("go", "build", "-o", out, pkg)
	cmd.Dir = root
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build %s: %v\n%s", pkg, err, b)
	}
}

func TestTMUX_EndToEnd(t *testing.T) {
	if os.Getenv("SYNDIC_TMUX_TEST") != "1" {
		gap(t, "SYNDIC_TMUX_TEST=1 is not set; the scripted tmux acceptance run is opt-in")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		gap(t, "tmux is not on the path; the scripted tmux acceptance run needs it")
	}

	// A socket path must fit in 108 bytes, so the directory is short.
	dir, err := os.MkdirTemp("", "syn")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	root, err := moduleRoot(wd)
	if err != nil {
		t.Fatalf("module root: %v", err)
	}
	bin := filepath.Join(dir, "bin")
	runtimeDir := filepath.Join(dir, "run")
	dataDir := filepath.Join(dir, "data")
	configDir := filepath.Join(dir, "cfg")
	for _, d := range []string{bin, runtimeDir, dataDir, configDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	syndicd := filepath.Join(bin, "syndicd")
	syndic := filepath.Join(bin, "syndic")
	buildBinary(t, root, syndicd, "./cmd/syndicd")
	buildBinary(t, root, syndic, "./cmd/syndic")

	stub := newChatStub(t)
	rosterPath := filepath.Join(configDir, "syndic.toml")
	if err := os.WriteFile(rosterPath, []byte(fmt.Sprintf(tmuxRoster, stub.srv.URL)), 0o600); err != nil {
		t.Fatalf("write roster: %v", err)
	}

	envPairs := []string{
		"HOME=" + dir,
		"PATH=" + os.Getenv("PATH"),
		"XDG_RUNTIME_DIR=" + runtimeDir,
		"XDG_DATA_HOME=" + dataDir,
		"XDG_CONFIG_HOME=" + configDir,
		"XDG_STATE_HOME=" + filepath.Join(dir, "state"),
	}
	journalPath := filepath.Join(dataDir, "syndic", "journal.sqlite")

	// Probe each participant's endpoint and model before the engine starts.
	for _, p := range []struct{ endpoint, model string }{{"author-ep", tmuxAuthorModel}, {"reviewer-ep", tmuxReviewerModel}} {
		cmd := exec.Command(syndic, "probe",
			"--roster", rosterPath, "--endpoint", p.endpoint, "--model", p.model,
			"--raw-dir", filepath.Join(dataDir, "raw"), "--export-dir", filepath.Join(dir, "export"),
			"--journal", journalPath, "--secrets-dir", filepath.Join(configDir, "secrets"),
			"--context-tokens", "4096")
		cmd.Env = envPairs
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("syndic probe %s: %v\n%s", p.endpoint, err, out)
		}
	}

	daemon := startProc(t, "syndicd", envPairs, syndicd, "--roster", rosterPath, "--journal", journalPath)
	socket := filepath.Join(runtimeDir, "syndic", "engine.sock")
	waitFor(t, "syndicd to accept connections\n"+daemon.out.String(), tmuxStepWait, func() bool {
		if daemon.exited() {
			t.Fatalf("syndicd exited early:\n%s", daemon.out.String())
		}
		c, err := net.Dial("unix", socket)
		if err != nil {
			return false
		}
		_ = c.Close()
		return true
	})

	host := &tmuxHost{
		t:      t,
		sock:   filepath.Join(dir, "tmux.sock"),
		env:    strings.Join(append([]string{"TERM=xterm-256color"}, envPairs...), " "),
		syndic: syndic,
	}
	t.Cleanup(func() {
		_, _ = host.run("kill-server")
		for _, pid := range host.paneIDs {
			if pidAlive(pid) {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	})

	// Scenario 1: a question gets an approved answer.
	pid := host.start("s1")
	host.waitPane("s1", "the TUI prompt", tmuxPaneWait, func(p string) bool { return regexp.MustCompile(`(?m)^>`).MatchString(p) }, daemon)
	host.typeLine("s1", "first question")
	pane := host.waitPane("s1", "the approved answer", tmuxPaneWait, func(p string) bool {
		return strings.Contains(p, "approved by")
	}, daemon)
	if !strings.Contains(pane, "Stub answer to: first question") {
		t.Errorf("the answer text is not on screen:\n%s", pane)
	}
	checkWidth(t, pane)
	t.Logf("pane after the first approved turn:\n%s", pane)

	// Scenario 2: kill the terminal, start another, and the finished turn is shown.
	host.killSession("s1", pid)
	pid = host.start("s2")
	pane = host.waitPane("s2", "the finished turn after reattach", tmuxPaneWait, func(p string) bool {
		return strings.Contains(p, "approved by") && strings.Contains(p, "Stub answer to: first question")
	}, daemon)
	checkWidth(t, pane)

	// Scenario 3: kill the TUI during a turn; the turn is not cancelled.
	gate := stub.hold()
	host.typeLine("s2", "second question")
	select {
	case <-gate.arrived:
	case <-time.After(tmuxPaneWait):
		t.Fatalf("the reviewer request never arrived\n%s\n%s", host.capture("s2"), daemon.out.String())
	}
	if got := turnStates(t, journalPath); len(got) != 2 || got[1] != "" {
		t.Fatalf("with the reviewer held, journal turn states = %q, want the second turn open", got)
	}
	host.killSession("s2", pid)
	close(gate.release)
	waitFor(t, "the journal to show the second turn approved", tmuxStepWait, func() bool {
		got := turnStates(t, journalPath)
		return len(got) == 2 && got[1] == "approved"
	})
	host.start("s3")
	pane = host.waitPane("s3", "the second turn's approved result", tmuxPaneWait, func(p string) bool {
		// The second question continues the conversation the first opened, so
		// the stub echoes the history with it; the chat header proves s3
		// reattached to the conversation holding both turns.
		return strings.Contains(p, "approved by") && strings.Contains(p, ", 2 turns")
	}, daemon)
	checkWidth(t, pane)
	if got := turnStates(t, journalPath); len(got) != 2 || got[0] != "approved" || got[1] != "approved" {
		t.Errorf("journal turn states = %q, want two approved turns", got)
	}

	// SIGTERM stops the engine with exit code 0 and leaves no socket listening.
	_, _ = host.run("kill-server")
	if err := daemon.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signal syndicd: %v", err)
	}
	select {
	case <-daemon.done:
	case <-time.After(tmuxStepWait):
		t.Fatalf("syndicd did not exit after SIGTERM\n%s", daemon.out.String())
	}
	if code := daemon.cmd.ProcessState.ExitCode(); code != 0 {
		t.Errorf("syndicd exit code = %d, want 0\n%s", code, daemon.out.String())
	}
	if c, err := net.Dial("unix", socket); err == nil {
		_ = c.Close()
		t.Errorf("the engine socket still accepts connections after syndicd exited")
	}
}
