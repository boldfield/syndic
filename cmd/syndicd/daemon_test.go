package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/client"
	"github.com/boldfield/syndic/internal/config"
	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/lockfile"
	"github.com/boldfield/syndic/internal/wire"
)

const sentinelSecret = "sentinel-secret-value-9f3a"

// daemon is a running run() against a temp directory and an httptest server.
type daemon struct {
	t           *testing.T
	sig         chan os.Signal
	done        chan int
	stdout      threadSafeBuffer
	stderr      threadSafeBuffer
	socket      string
	journalPath string
	runtimeDir  string
	rosterPath  string
	secretsDir  string
}

func daemonRoster(baseURL, stopGrace string, withAuth bool) string {
	toml := strings.Replace(enginetest.RosterTOML, "http://localhost:11434/v1", baseURL, 1)
	if withAuth {
		toml = strings.Replace(toml, "tokens_per_byte_max = 1.0\n",
			"tokens_per_byte_max = 1.0\n\n[endpoints.auth]\nsource = \"file\"\nname = \"openai.key\"\n", 1)
	}
	if stopGrace != "" {
		toml += "\n[engine]\nstop_grace = \"" + stopGrace + "\"\n"
	}
	return toml
}

// newDaemonDirs prepares the directories, the roster and a seeded journal.
func newDaemonDirs(t *testing.T, rosterTOML string) *daemon {
	t.Helper()
	dir := t.TempDir()
	d := &daemon{
		t:           t,
		sig:         make(chan os.Signal, 2),
		done:        make(chan int, 1),
		journalPath: filepath.Join(dir, "journal.db"),
		runtimeDir:  filepath.Join(dir, "runtime"),
		rosterPath:  filepath.Join(dir, "roster.toml"),
		secretsDir:  filepath.Join(dir, "secrets"),
	}
	for _, p := range []string{d.runtimeDir, d.secretsDir} {
		if err := os.MkdirAll(p, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(d.secretsDir, "openai.key"), []byte(sentinelSecret), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(d.rosterPath, []byte(rosterTOML), 0o600); err != nil {
		t.Fatal(err)
	}
	roster, err := config.Load(d.rosterPath)
	if err != nil {
		t.Fatal(err)
	}
	db, err := journal.Open(d.journalPath)
	if err != nil {
		t.Fatal(err)
	}
	seedProbeRecords(t, journal.NewStore(db), roster)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_RUNTIME_DIR", d.runtimeDir)
	t.Setenv("XDG_DATA_HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)
	return d
}

// start launches run and waits for its startup line.
func (d *daemon) start() {
	d.t.Helper()
	d.done = make(chan int, 1)
	go func() {
		d.done <- run([]string{
			"--roster", d.rosterPath,
			"--journal", d.journalPath,
			"--secrets-dir", d.secretsDir,
		}, &d.stdout, &d.stderr, d.sig)
	}()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if f := strings.Fields(d.stdout.String()); len(f) >= 3 {
			d.socket = f[0]
			return
		}
		select {
		case code := <-d.done:
			d.t.Fatalf("run exited %d before start: %s", code, d.stderr.String())
		case <-time.After(20 * time.Millisecond):
		}
	}
	d.t.Fatal("daemon did not start")
}

func (d *daemon) dial() *client.Client {
	d.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := client.Dial(ctx, d.socket, "test", "1.0")
	if err != nil {
		d.t.Fatalf("dial: %v", err)
	}
	return c
}

func (d *daemon) wait(within time.Duration) int {
	d.t.Helper()
	select {
	case code := <-d.done:
		return code
	case <-time.After(within):
		d.t.Fatal("run did not return in time")
		return -1
	}
}

func (d *daemon) events() []journal.Event {
	d.t.Helper()
	db, err := journal.Open(d.journalPath)
	if err != nil {
		d.t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	evs, err := journal.NewStore(db).EventsSince(0, 100000)
	if err != nil {
		d.t.Fatal(err)
	}
	return evs
}

func (d *daemon) lockFree() bool {
	paths, err := lockfile.Resolve(func(key string) string {
		if key == "XDG_RUNTIME_DIR" {
			return d.runtimeDir
		}
		return ""
	}, os.Getenv("HOME"))
	if err != nil {
		d.t.Fatal(err)
	}
	l, err := lockfile.Acquire(paths.Lock, time.Now())
	if err != nil {
		return false
	}
	_ = l.Release()
	return true
}

// chatServer answers chat completions, recording each request body. When
// block is non-nil the handler holds until it is closed or the request ends.
type chatServer struct {
	*httptest.Server
	mu       sync.Mutex
	bodies   []map[string]any
	auths    []string
	received chan struct{}
}

func newChatServer(t *testing.T, block chan struct{}) *chatServer {
	t.Helper()
	cs := &chatServer{received: make(chan struct{}, 16)}
	cs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		cs.mu.Lock()
		cs.bodies = append(cs.bodies, body)
		cs.auths = append(cs.auths, r.Header.Get("Authorization"))
		cs.mu.Unlock()
		cs.received <- struct{}{}
		if block != nil {
			select {
			case <-block:
			case <-r.Context().Done():
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"` + enginetest.DefaultReportedModel + `","choices":[{"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`))
	}))
	t.Cleanup(func() {
		if block != nil {
			select {
			case <-block:
			default:
				close(block)
			}
		}
		cs.Close()
	})
	return cs
}

func (cs *chatServer) waitRequest(t *testing.T) {
	t.Helper()
	select {
	case <-cs.received:
	case <-time.After(10 * time.Second):
		t.Fatal("server never received a request")
	}
}

func submitSolo(t *testing.T, c *client.Client) string {
	t.Helper()
	preset := "solo"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	reply, err := c.Submit(ctx, wire.SubmitRequest{SubmissionID: "sub-1", Input: "say hello", Preset: &preset})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	return reply.TurnID
}

func waitTurnState(t *testing.T, c *client.Client, turnID string, states ...string) wire.TurnReply {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var last wire.TurnReply
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		reply, err := c.Turn(ctx, turnID)
		cancel()
		if err == nil {
			last = reply
			for _, s := range states {
				if reply.State == s {
					return reply
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("turn never reached %v; last state %q", states, last.State)
	return last
}

func TestSoloTurnAndSecret(t *testing.T) {
	cs := newChatServer(t, nil)
	d := newDaemonDirs(t, daemonRoster(cs.URL, "", true))
	d.start()

	c := d.dial()
	turnID := submitSolo(t, c)
	reply := waitTurnState(t, c, turnID, "solo_complete", "incomplete", "failed")
	if reply.State == "incomplete" || reply.State == "failed" {
		t.Fatalf("turn ended %q (reason %v)", reply.State, reply.TerminalReason)
	}
	_ = c.Close()

	cs.mu.Lock()
	if len(cs.bodies) == 0 {
		cs.mu.Unlock()
		t.Fatal("server received no request")
	}
	body := cs.bodies[0]
	auth := cs.auths[0]
	cs.mu.Unlock()
	if body["model"] != "llama2" {
		t.Fatalf("request model = %v, want llama2", body["model"])
	}
	if _, ok := body["max_tokens"]; !ok {
		t.Fatalf("request lacks max_tokens: %v", body)
	}
	if auth != "Bearer "+sentinelSecret {
		t.Fatalf("secret was not resolved into the request: %q", auth)
	}

	d.sig <- os.Interrupt
	if code := d.wait(10 * time.Second); code != 0 {
		t.Fatalf("exit %d: %s", code, d.stderr.String())
	}
	if all := d.stdout.String() + d.stderr.String(); strings.Contains(all, sentinelSecret) {
		t.Fatalf("secret leaked to output: %q", all)
	}
}

func TestStopBlockedTurn(t *testing.T) {
	cs := newChatServer(t, make(chan struct{}))
	d := newDaemonDirs(t, daemonRoster(cs.URL, "300ms", false))
	d.start()

	c := d.dial()
	defer func() { _ = c.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := c.Subscribe(ctx, 0); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	turnID := submitSolo(t, c)
	cs.waitRequest(t)

	start := time.Now()
	d.sig <- os.Interrupt

	var gotStopping bool
	for {
		_, err := c.NextEvent(ctx)
		if err == nil {
			continue
		}
		var werr *wire.Error
		gotStopping = errors.As(err, &werr) && werr.Code == wire.ErrorCodeStopping
		break
	}
	if !gotStopping {
		t.Fatal("client did not receive error{code: stopping}")
	}

	if code := d.wait(10 * time.Second); code != 0 {
		t.Fatalf("exit %d: %s", code, d.stderr.String())
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("run took %v with a 300ms stop_grace", elapsed)
	}
	if !d.lockFree() {
		t.Fatal("lock not released")
	}

	var incomplete bool
	for _, ev := range d.events() {
		if ev.Kind != wire.EventKindTurnState || ev.TurnID != turnID {
			continue
		}
		var st wire.EvTurnState
		if err := json.Unmarshal(ev.Data, &st); err != nil {
			t.Fatal(err)
		}
		if st.State == "incomplete" && st.Reason != nil && *st.Reason == "engine_stop" {
			incomplete = true
		}
	}
	if !incomplete {
		t.Fatal("turn was not ended incomplete with engine_stop")
	}
}

func TestDoubleSignalExits130AndRecovers(t *testing.T) {
	cs := newChatServer(t, make(chan struct{}))
	d := newDaemonDirs(t, daemonRoster(cs.URL, "30s", false))
	d.start()

	c := d.dial()
	submitSolo(t, c)
	cs.waitRequest(t)
	_ = c.Close()

	start := time.Now()
	d.sig <- os.Interrupt
	d.sig <- os.Interrupt
	if code := d.wait(10 * time.Second); code != 130 {
		t.Fatalf("exit %d, want 130: %s", code, d.stderr.String())
	}
	if time.Since(start) > 10*time.Second {
		t.Fatal("second signal did not cut the grace short")
	}
	if !d.lockFree() {
		t.Fatal("lock not released")
	}

	// A fresh run on the same journal starts.
	d.stdout = threadSafeBuffer{}
	d.start()
	d.sig <- os.Interrupt
	if code := d.wait(10 * time.Second); code != 0 {
		t.Fatalf("second run exit %d: %s", code, d.stderr.String())
	}

	var recovered []int
	for _, ev := range d.events() {
		if ev.Kind != wire.EventKindEngineStarted {
			continue
		}
		var st wire.EvEngineStarted
		if err := json.Unmarshal(ev.Data, &st); err != nil {
			t.Fatal(err)
		}
		recovered = append(recovered, st.RecoveredAttempts)
	}
	if len(recovered) < 2 || recovered[len(recovered)-1] < 1 {
		t.Fatalf("engine_started recovered attempts = %v, want the last >= 1", recovered)
	}
}

func TestOneSignalLastEventEngineStopping(t *testing.T) {
	cs := newChatServer(t, nil)
	d := newDaemonDirs(t, daemonRoster(cs.URL, "", false))
	d.start()
	d.sig <- os.Interrupt
	if code := d.wait(10 * time.Second); code != 0 {
		t.Fatalf("exit %d: %s", code, d.stderr.String())
	}
	evs := d.events()
	if last := evs[len(evs)-1].Kind; last != wire.EventKindEngineStopping {
		t.Fatalf("last event %q, want engine_stopping", last)
	}
}
