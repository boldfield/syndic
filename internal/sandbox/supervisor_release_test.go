package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// containTokenGroup names a process-group container that holds a token file
// and releases it, as the macOS profile container does.
// callBound bounds every step of a forked test call.
const callBound = 10 * time.Second

const containTokenGroup = "test_token_group"

type tokenGroup struct {
	processGroup
	token string
}

func init() {
	containers[containTokenGroup] = func(config json.RawMessage) (Container, error) {
		var c struct{ Token string }
		if err := json.Unmarshal(config, &c); err != nil {
			return nil, err
		}
		return &tokenGroup{token: c.Token}, nil
	}
}

func (g *tokenGroup) release() { _ = os.Remove(g.token) }

// startTokenCall creates the token exclusively, as writeMacCall does, and
// starts a command whose container releases it.
func startTokenCall(t *testing.T, dir, script string) (*supervisedProcess, error) {
	t.Helper()
	token := filepath.Join(dir, ".member")
	f, err := os.OpenFile(token, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o444)
	if err != nil {
		return nil, err
	}
	_ = f.Close()
	ctx, cancel := context.WithTimeout(context.Background(), callBound)
	defer cancel()
	p, err := testSupervisor(t, Sequence{KillGrace: time.Second, CleanupDeadline: 10 * time.Second}).start(
		ctx,
		launch{container: containTokenGroup, config: map[string]string{"Token": token}},
		shellSpec(dir, script))
	if err != nil {
		_ = os.Remove(token)
		t.Fatalf("start: %v", err)
	}
	return p, nil
}

// TestReleaseBeforeEmptyIsReported runs many calls in turn on one directory;
// each start must find the previous call's token gone once that call has
// been confirmed empty.
func TestReleaseBeforeEmptyIsReported(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 50; i++ {
		p, err := startTokenCall(t, dir, "echo hi")
		if err != nil {
			t.Fatalf("call %d: start refused: %v", i, err)
		}
		done := make(chan error, 1)
		go func() {
			_, _ = io.Copy(io.Discard, p.Output())
			_, err := p.Wait()
			done <- err
		}()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("call %d: wait: %v", i, err)
			}
		case <-time.After(callBound):
			_ = p.Terminate(9)
			t.Fatalf("call %d: output and wait did not finish within %s", i, callBound)
		}
		ctx, cancel := context.WithTimeout(context.Background(), callBound)
		err = p.ConfirmEmpty(ctx)
		cancel()
		if err != nil {
			t.Fatalf("call %d: confirm empty: %v", i, err)
		}
		if _, err := os.Stat(filepath.Join(dir, ".member")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("call %d: token remains after the call was confirmed empty: %v", i, err)
		}
	}
}

// TestLiveCallRefusesStartOnSameDirectory shows the token of a live call
// still refuses another start.
func TestLiveCallRefusesStartOnSameDirectory(t *testing.T) {
	dir := t.TempDir()
	p, err := startTokenCall(t, dir, "sleep 1000")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = p.Terminate(9)
		_ = p.Output().Close()
		select {
		case <-p.reaped:
		case <-time.After(callBound):
			t.Errorf("live call was not reaped within %s", callBound)
		}
	})
	if _, err := startTokenCall(t, dir, "true"); !errors.Is(err, os.ErrExist) {
		t.Fatalf("second start on a live call's directory: %v, want a refusal", err)
	}
}
