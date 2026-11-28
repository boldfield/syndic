package mcp

// This file owns the workspace copy of a turn instance (v9 delta, section 6,
// "The workspace copy"). With workspace = "scratch" the instance gets a
// read-only copy of the workspace, built before its process starts as v7
// section 5 builds a command's scratch directory. The three view values taken
// when the build completes are recorded on every call the instance serves.
// The copy is never rebuilt under a running process: before each call the
// caller's present overlay_sha256 is compared with the instance's, and when
// they differ the instance is ended and a new one started on a fresh copy.
//
// This package imports neither the scratch builder nor the journal's
// staging, so the build and the overlay hash arrive as the two functions of
// WorkspaceSource; the caller binds them to package scratch.

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// WorkspaceView is v7's three view values, taken when a copy's build
// completes.
type WorkspaceView struct {
	SnapshotVersion int    `json:"snapshot_version"`
	OverlaySHA256   string `json:"overlay_sha256"`
	ModesSHA256     string `json:"modes_sha256"`
}

// WorkspaceSource is the caller's side of the copy.
type WorkspaceSource struct {
	// Overlay returns the overlay_sha256 of the caller's present overlay,
	// without building anything.
	Overlay func() (string, error)
	// Build replaces dir with the copy for the caller's present overlay and
	// returns the view taken when the build completes.
	Build func(dir string) (WorkspaceView, error)
}

// TurnWorkspace serves the calls of one participant to one server with
// workspace = "scratch". It holds at most one live instance, and is safe for
// concurrent use; calls are served one at a time so that a restart never
// runs under a call in flight.
type TurnWorkspace struct {
	// Instances starts the instances.
	Instances *Instances
	// Request is the instance to start; its WorkspaceDir is replaced by Dir.
	Request InstanceRequest
	// Dir is the directory the copy is built in.
	Dir string
	// Source builds the copy and reports the caller's present overlay.
	Source WorkspaceSource

	mu   sync.Mutex
	inst *Instance
	view WorkspaceView
}

// Call serves one call. send carries it over inst's pipes, as for
// Instance.Call, and is given the view the instance's process was started on,
// which the caller records with the call. A failure to compare, end or start
// sends nothing: the reply is CallNotSent.
func (w *TurnWorkspace) Call(ctx context.Context, send func(ctx context.Context, inst *Instance, view WorkspaceView) CallReply) CallReply {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.Source.Overlay == nil || w.Source.Build == nil || w.Instances == nil {
		return CallReply{Kind: CallNotSent, Err: errors.New("mcp: workspace copy needs instances and a source")}
	}
	present, err := w.Source.Overlay()
	if err != nil {
		return CallReply{Kind: CallNotSent, Err: fmt.Errorf("mcp: overlay of the workspace copy: %w", err)}
	}
	if w.inst != nil && !w.inst.Ended() && w.view.OverlaySHA256 == present {
		return w.inst.Call(ctx, func(ctx context.Context) CallReply { return send(ctx, w.inst, w.view) })
	}
	if w.inst != nil {
		// Ended after a cancelled call, or the overlay moved: the old process
		// must be confirmed gone before another reads the directory.
		// An end that is not confirmed keeps the instance here: its copy stays
		// in place, and no call rebuilds it or starts another until it is.
		if err := w.inst.End(); err != nil {
			return CallReply{Kind: CallNotSent, Err: err}
		}
		w.inst = nil
	}
	// A closed gate starts nothing, and must not wipe the directory either.
	if err := w.Instances.enter(); err != nil {
		return CallReply{Kind: CallNotSent, Err: err}
	}
	view, err := w.Source.Build(w.Dir)
	if err != nil {
		return CallReply{Kind: CallNotSent, Err: fmt.Errorf("mcp: build workspace copy: %w", err)}
	}
	req := w.Request
	req.Kind = TurnInstance
	req.WorkspaceDir = w.Dir
	inst, err := w.Instances.Open(ctx, req)
	if err != nil {
		return CallReply{Kind: CallNotSent, Err: err}
	}
	w.inst, w.view = inst, view
	return inst.Call(ctx, func(ctx context.Context) CallReply { return send(ctx, inst, view) })
}

// End ends the live instance, if any.
func (w *TurnWorkspace) End() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.inst == nil {
		return nil
	}
	if err := w.inst.End(); err != nil {
		return err
	}
	w.inst = nil
	return nil
}
