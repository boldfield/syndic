package mcp

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"syscall"
	"testing"
)

type fakeProc struct{}

func (fakeProc) Stdin() io.WriteCloser              { return nopWC{} }
func (fakeProc) Stdout() io.ReadCloser              { return io.NopCloser(strings.NewReader("")) }
func (fakeProc) Stderr() io.ReadCloser              { return io.NopCloser(strings.NewReader("")) }
func (fakeProc) Terminate(syscall.Signal) error     { return nil }
func (fakeProc) ConfirmEmpty(context.Context) error { return nil }

type nopWC struct{}

func (nopWC) Write(p []byte) (int, error) { return len(p), nil }
func (nopWC) Close() error                { return nil }

type fakeJournal struct {
	mu            sync.Mutex
	started, ends int
	dirs          []string
}

func (j *fakeJournal) Started(r InstanceRecord) (string, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.started++
	j.dirs = append(j.dirs, r.WorkspaceDir)
	return "i", nil
}
func (j *fakeJournal) Ended(string) error                  { j.mu.Lock(); j.ends++; j.mu.Unlock(); return nil }
func (j *fakeJournal) ContainmentFailed(_, _ string) error { return nil }

func TestTurnWorkspaceRestartsOnOverlayChange(t *testing.T) {
	j := &fakeJournal{}
	starts := 0
	m := &Instances{
		Journal:   j,
		Start:     func(context.Context, InstanceRequest) (ServerProcess, error) { starts++; return fakeProc{}, nil },
		Terminate: func(ServerProcess) bool { return true },
	}
	overlay, builds := "a", 0
	w := &TurnWorkspace{
		Instances: m,
		Request:   InstanceRequest{InstanceRecord: InstanceRecord{Server: "s", Owner: "p"}},
		Dir:       "/scratch",
		Source: WorkspaceSource{
			Overlay: func() (string, error) { return overlay, nil },
			Build: func(string) (WorkspaceView, error) {
				builds++
				return WorkspaceView{SnapshotVersion: builds, OverlaySHA256: overlay, ModesSHA256: "m"}, nil
			},
		},
	}
	var seen []WorkspaceView
	call := func() {
		r := w.Call(context.Background(), func(_ context.Context, _ *Instance, v WorkspaceView) CallReply {
			seen = append(seen, v)
			return CallReply{Kind: CallResult}
		})
		if r.Kind != CallResult {
			t.Fatalf("reply %v: %v", r.Kind, r.Err)
		}
	}
	call()
	call()
	if starts != 1 || builds != 1 {
		t.Fatalf("same overlay: starts=%d builds=%d, want 1 and 1", starts, builds)
	}
	overlay = "b"
	call()
	call()
	if starts != 2 || builds != 2 || j.ends != 1 {
		t.Fatalf("changed overlay: starts=%d builds=%d ends=%d, want 2, 2, 1", starts, builds, j.ends)
	}
	if seen[1] != seen[0] || seen[2] != seen[3] || seen[0] == seen[2] || seen[2].OverlaySHA256 != "b" {
		t.Fatalf("views not recorded per instance: %+v", seen)
	}
	if j.dirs[0] != "/scratch" {
		t.Fatalf("workspace dir %q", j.dirs[0])
	}
	if err := w.End(); err != nil || j.ends != 2 {
		t.Fatalf("End: %v, ends=%d", err, j.ends)
	}
}

type unconfirmedProc struct{ fakeProc }

func newTestWorkspace(m *Instances, overlay *string, builds *int, buildErr error) *TurnWorkspace {
	return &TurnWorkspace{
		Instances: m,
		Request:   InstanceRequest{InstanceRecord: InstanceRecord{Server: "s", Owner: "p"}},
		Dir:       "/scratch",
		Source: WorkspaceSource{
			Overlay: func() (string, error) { return *overlay, nil },
			Build: func(string) (WorkspaceView, error) {
				*builds++
				if buildErr != nil {
					return WorkspaceView{}, buildErr
				}
				return WorkspaceView{OverlaySHA256: *overlay}, nil
			},
		},
	}
}

func TestTurnWorkspaceRestartsAfterCancelledCall(t *testing.T) {
	starts := 0
	m := &Instances{
		Journal:   &fakeJournal{},
		Start:     func(context.Context, InstanceRequest) (ServerProcess, error) { starts++; return fakeProc{}, nil },
		Terminate: func(ServerProcess) bool { return true },
	}
	overlay, builds := "a", 0
	w := newTestWorkspace(m, &overlay, &builds, nil)
	kinds := []CallKind{CallCancelled, CallResult}
	for _, k := range kinds {
		r := w.Call(context.Background(), func(context.Context, *Instance, WorkspaceView) CallReply { return CallReply{Kind: k} })
		if r.Kind != k {
			t.Fatalf("reply %v: %v", r.Kind, r.Err)
		}
	}
	if starts != 2 || builds != 2 {
		t.Fatalf("starts=%d builds=%d, want 2 and 2", starts, builds)
	}
}

func TestTurnWorkspaceUnverifiedEndNeverRebuilds(t *testing.T) {
	starts := 0
	m := &Instances{
		Journal:   &fakeJournal{},
		Start:     func(context.Context, InstanceRequest) (ServerProcess, error) { starts++; return unconfirmedProc{}, nil },
		Terminate: func(ServerProcess) bool { return false },
	}
	overlay, builds := "a", 0
	w := newTestWorkspace(m, &overlay, &builds, nil)
	ok := func(context.Context, *Instance, WorkspaceView) CallReply { return CallReply{Kind: CallResult} }
	if r := w.Call(context.Background(), ok); r.Kind != CallResult {
		t.Fatalf("first: %v", r.Err)
	}
	overlay = "b"
	for i := 0; i < 3; i++ {
		r := w.Call(context.Background(), ok)
		if r.Kind != CallNotSent || !errors.Is(r.Err, ErrContainmentUnverified) {
			t.Fatalf("call %d: %v %v", i, r.Kind, r.Err)
		}
	}
	if starts != 1 || builds != 1 {
		t.Fatalf("starts=%d builds=%d, want 1 and 1", starts, builds)
	}
}

func TestTurnWorkspaceBuildErrorSendsNothing(t *testing.T) {
	starts := 0
	m := &Instances{
		Journal:   &fakeJournal{},
		Start:     func(context.Context, InstanceRequest) (ServerProcess, error) { starts++; return fakeProc{}, nil },
		Terminate: func(ServerProcess) bool { return true },
	}
	overlay, builds := "a", 0
	w := newTestWorkspace(m, &overlay, &builds, errors.New("boom"))
	sent := false
	r := w.Call(context.Background(), func(context.Context, *Instance, WorkspaceView) CallReply {
		sent = true
		return CallReply{Kind: CallResult}
	})
	if r.Kind != CallNotSent || r.Err == nil || sent || starts != 0 {
		t.Fatalf("reply %v %v sent=%v starts=%d", r.Kind, r.Err, sent, starts)
	}
}

func TestTurnWorkspaceUnverifiedStartNeverRebuilds(t *testing.T) {
	starts := 0
	m := &Instances{
		Journal: &fakeJournal{},
		Start: func(context.Context, InstanceRequest) (ServerProcess, error) {
			starts++
			return unconfirmedProc{}, errors.New("start failed")
		},
		Terminate: func(ServerProcess) bool { return false },
	}
	overlay, builds := "a", 0
	w := newTestWorkspace(m, &overlay, &builds, nil)
	ok := func(context.Context, *Instance, WorkspaceView) CallReply { return CallReply{Kind: CallResult} }
	for i := 0; i < 3; i++ {
		r := w.Call(context.Background(), ok)
		if r.Kind != CallNotSent || !errors.Is(r.Err, ErrContainmentUnverified) {
			t.Fatalf("call %d: %v %v", i, r.Kind, r.Err)
		}
	}
	if starts != 1 || builds != 1 {
		t.Fatalf("starts=%d builds=%d, want 1 and 1", starts, builds)
	}
}
