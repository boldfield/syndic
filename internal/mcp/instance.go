package mcp

// This file owns the local server process (v9 delta, section 6,
// "Instances", "Cancelling a call" and "Termination"). An instance is one
// server process with one owner. It starts when its owner first needs it and
// ends when its owner does; the start is journaled before the process starts
// and the end only once the backend has confirmed that nothing of the
// instance remains. The protocol is carried by the instance's standard input
// and output; its standard error goes to a bounded sink and to no model.
//
// This package imports no network or process package, so the sandbox, the
// journal and the run's gate arrive as the small interfaces and functions
// below; package local binds them to the real ones. State directories, the
// workspace copy, the proxy and what follows an unverified containment are
// other tasks': the caller hands in the directories and the start.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"syscall"
	"time"
)

// Instance kinds (section 6); the strings are the journal's.
const (
	TurnInstance    = "turn"
	ApplyInstance   = "apply"
	ListingInstance = "listing"
	RunInstance     = "run"
)

// ServerProcess is a started server: its standard input, standard output and
// standard error are three separate pipes to the engine, and everything it
// started is contained until ConfirmEmpty returns nil.
type ServerProcess interface {
	// Stdin is the pipe to the server's standard input.
	Stdin() io.WriteCloser
	// Stdout is the pipe from the server's standard output.
	Stdout() io.ReadCloser
	// Stderr is the pipe from the server's standard error.
	Stderr() io.ReadCloser
	// Terminate signals everything contained with sig, without waiting.
	Terminate(sig syscall.Signal) error
	// ConfirmEmpty returns nil only once nothing contained remains.
	ConfirmEmpty(ctx context.Context) error
}

// InstanceRecord is what is journaled of an instance when it starts.
type InstanceRecord struct {
	Server string
	// Kind is TurnInstance, ApplyInstance, ListingInstance or RunInstance.
	Kind string
	// Owner is the participant, apply operation or listing that owns the
	// instance; empty for the run instance.
	Owner string
	// StateDir, TempDir and WorkspaceDir are the directories the instance
	// uses; one it has none of is empty.
	StateDir, TempDir, WorkspaceDir string
}

// InstanceJournal records instances.
type InstanceJournal interface {
	// Started journals mcp_instance_started and returns the instance's id.
	Started(rec InstanceRecord) (id string, err error)
	// Ended journals mcp_instance_ended.
	Ended(id string) error
	// ContainmentFailed journals that the instance was not confirmed gone.
	ContainmentFailed(id, detail string) error
}

// Gate is the run's execution gate (v7, section 3, "Containment
// unverified").
type Gate interface {
	// Enter returns nil while the gate is open.
	Enter() error
}

var (
	// ErrNotOffered is wrapped by the error of a server that is not offered
	// for the turn: it failed to start, or did not answer discovery and
	// listing within its timeout.
	ErrNotOffered = errors.New("mcp: server not offered")
	// ErrInstanceEnded is the error of a call to an instance that has ended.
	ErrInstanceEnded = errors.New("mcp: instance has ended")
	// ErrContainmentUnverified is wrapped by the error of an end that the
	// backend did not confirm within the cleanup deadline. The failure is
	// journaled and the instance stays unresolved.
	ErrContainmentUnverified = errors.New("mcp: instance containment unverified")
)

// Instances starts and ends the instances of one run. It is safe for
// concurrent use. Journal and Start are required.
type Instances struct {
	// Journal records every instance.
	Journal InstanceJournal
	// Gate is checked immediately before every server process starts and
	// before every call sent to one; nil is a gate that stays open.
	Gate Gate
	// Start starts the process of one instance, under the server policy,
	// with standard input, output and error as three pipes. It is called
	// after the instance's start is journaled and the gate entered.
	Start func(ctx context.Context, req InstanceRequest) (ServerProcess, error)
	// Terminate runs v7's one sequence on p (SIGTERM, the grace period,
	// SIGKILL, the cleanup deadline) and reports whether the backend
	// confirmed that nothing remains.
	Terminate func(p ServerProcess) (confirmed bool)
	// Stderr makes the bounded sink the server's standard error is copied
	// to (mcp.max_stderr_bytes); nil discards it.
	Stderr func(server string) io.Writer
	// Unconfined is v7's displayed line when the run is under
	// sandbox = "none", and empty otherwise. A server then runs unconfined
	// and Notice receives that line naming the server, once for each
	// server.
	Unconfined string
	// Notice receives UnconfinedLine; nil drops it.
	Notice func(line string)

	mu      sync.Mutex
	run     map[string]*runSlot
	noticed map[string]bool
}

// runSlot holds the run instance of one server.
type runSlot struct {
	mu   sync.Mutex // held while the instance is being started
	inst *Instance
}

// InstanceRequest says what to start.
type InstanceRequest struct {
	InstanceRecord
	// Args is passed through to Instances.Start unchanged, for the caller's
	// spec: the argument vector, environment and system locations.
	Args any
}

// UnconfinedLine is the line shown for a server that runs unconfined: v7's
// line, naming the server.
func UnconfinedLine(server, line string) string {
	return fmt.Sprintf("mcp server %q: %s", server, line)
}

func (m *Instances) enter() error {
	if m.Gate == nil {
		return nil
	}
	return m.Gate.Enter()
}

// Open starts one instance. The gate is entered immediately before the
// process starts; a closed gate starts nothing. The start is journaled
// first. For the run instance Open returns the one already running for the
// server, if there is one.
func (m *Instances) Open(ctx context.Context, req InstanceRequest) (*Instance, error) {
	if req.Kind != RunInstance {
		return m.open(ctx, req)
	}
	m.mu.Lock()
	if m.run == nil {
		m.run = map[string]*runSlot{}
	}
	slot := m.run[req.Server]
	if slot == nil {
		slot = &runSlot{}
		m.run[req.Server] = slot
	}
	m.mu.Unlock()
	slot.mu.Lock()
	defer slot.mu.Unlock()
	if slot.inst != nil && !slot.inst.Ended() {
		return slot.inst, nil
	}
	inst, err := m.open(ctx, req)
	if err != nil {
		return nil, err
	}
	slot.inst = inst
	return inst, nil
}

func (m *Instances) open(ctx context.Context, req InstanceRequest) (*Instance, error) {
	if m.Journal == nil || m.Start == nil || m.Terminate == nil {
		return nil, errors.New("mcp: instances need a journal, a start and a termination")
	}
	if err := m.enter(); err != nil {
		return nil, err
	}
	id, err := m.Journal.Started(req.InstanceRecord)
	if err != nil {
		return nil, fmt.Errorf("mcp: journal instance start: %w", err)
	}
	inst := &Instance{mgr: m, id: id, server: req.Server, kind: req.Kind, done: make(chan struct{})}
	// The gate is entered again immediately before the start: it may have
	// closed while the start was being recorded.
	if err := m.enter(); err != nil {
		_ = inst.finish()
		return nil, err
	}
	proc, err := m.Start(ctx, req)
	if err != nil {
		// Nothing started, so nothing remains.
		_ = inst.finish()
		return nil, fmt.Errorf("mcp: start server %q: %w", req.Server, err)
	}
	inst.proc = proc
	inst.stdin, inst.stdout = proc.Stdin(), proc.Stdout()
	if m.Stderr != nil {
		inst.stderr = m.Stderr(req.Server)
	}
	if inst.stderr == nil {
		inst.stderr = io.Discard
	}
	go func(r io.Reader) { _, _ = io.Copy(inst.stderr, r) }(proc.Stderr())
	if m.Unconfined != "" {
		inst.unconfined = true
		m.mu.Lock()
		if m.noticed == nil {
			m.noticed = map[string]bool{}
		}
		first := !m.noticed[req.Server]
		m.noticed[req.Server] = true
		m.mu.Unlock()
		if first && m.Notice != nil {
			m.Notice(UnconfinedLine(req.Server, m.Unconfined))
		}
	}
	return inst, nil
}

// Offer opens a listing instance for the server, runs list on it, and ends
// it. A server that fails to start, or does not finish list within timeout
// (zero waits without bound), is not offered: the error wraps ErrNotOffered.
// At the timeout the instance is ended, which ends any read still waiting.
func Offer[T any](ctx context.Context, m *Instances, req InstanceRequest, timeout time.Duration, list func(ctx context.Context, inst *Instance) (T, error)) (T, error) {
	var zero T
	req.Kind = ListingInstance
	if req.Owner == "" {
		req.Owner = "listing"
	}
	inst, err := m.Open(ctx, req)
	if err != nil {
		return zero, fmt.Errorf("%w: %s: %w", ErrNotOffered, req.Server, err)
	}
	lctx, cancel := ctx, context.CancelFunc(func() {})
	if timeout > 0 {
		lctx, cancel = context.WithTimeout(ctx, timeout)
	}
	defer cancel()
	type result struct {
		v   T
		err error
	}
	ch := make(chan result, 1)
	go func() {
		v, err := list(lctx, inst)
		ch <- result{v, err}
	}()
	var res result
	select {
	case res = <-ch:
	case <-lctx.Done():
		// A result that raced the bound still counts.
		select {
		case res = <-ch:
		default:
			res.err = lctx.Err()
		}
	}
	endErr := inst.End()
	if res.err != nil {
		return zero, fmt.Errorf("%w: %s: %w", ErrNotOffered, req.Server, res.err)
	}
	if endErr != nil {
		return zero, endErr
	}
	return res.v, nil
}

// EndRun ends every run instance, at engine stop.
func (m *Instances) EndRun() error {
	m.mu.Lock()
	var all []*Instance
	for _, s := range m.run {
		if s.inst != nil {
			all = append(all, s.inst)
		}
	}
	m.mu.Unlock()
	var errs []error
	for _, i := range all {
		errs = append(errs, i.End())
	}
	return errors.Join(errs...)
}

// Instance is one server process with one owner.
type Instance struct {
	mgr        *Instances
	id, server string
	kind       string
	proc       ServerProcess
	stdin      io.WriteCloser
	stdout     io.ReadCloser
	stderr     io.Writer
	unconfined bool

	mu     sync.Mutex
	ended  bool
	done   chan struct{}
	endErr error
}

// ID is the instance's journal id.
func (i *Instance) ID() string { return i.id }

// Server is the server's name.
func (i *Instance) Server() string { return i.server }

// Kind is TurnInstance, ApplyInstance, ListingInstance or RunInstance.
func (i *Instance) Kind() string { return i.kind }

// Unconfined reports whether the server runs under sandbox = "none".
func (i *Instance) Unconfined() bool { return i.unconfined }

// Stdin is the pipe the engine writes protocol messages to.
func (i *Instance) Stdin() io.Writer { return i.stdin }

// Stdout is the server's standard output, which carries the protocol and
// nothing else.
func (i *Instance) Stdout() io.Reader { return i.stdout }

// Stderr is the sink the server's standard error is copied to. It is for the
// engine's log; no model is given it.
func (i *Instance) Stderr() io.Writer { return i.stderr }

// Ended reports whether the instance has ended, confirmed or not.
func (i *Instance) Ended() bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.ended
}

// Call sends one call through send, which carries it over the instance's
// pipes. The run's gate is checked immediately before: a closed gate sends
// nothing and the reply is CallNotSent. A call that times out or is
// abandoned is cancelled by send as section 3 has it; a turn or apply
// instance is then ended, since its state is unknown, and a later call needs
// a new one. The run instance is left running; a listing instance is the
// listing's to end.
func (i *Instance) Call(ctx context.Context, send func(ctx context.Context) CallReply) CallReply {
	if err := i.mgr.enter(); err != nil {
		return CallReply{Kind: CallNotSent, Err: err}
	}
	if i.Ended() {
		return CallReply{Kind: CallNotSent, Err: ErrInstanceEnded}
	}
	reply := send(ctx)
	if reply.Kind == CallCancelled && (i.kind == TurnInstance || i.kind == ApplyInstance) {
		_ = i.End()
	}
	return reply
}

// End ends the instance by v7's one sequence. Only a confirmation is
// journaled as ended; without one the failure is journaled, the instance
// stays unresolved and the error wraps ErrContainmentUnverified. End may be
// called again and returns the first outcome.
func (i *Instance) End() error {
	i.mu.Lock()
	if i.ended {
		i.mu.Unlock()
		<-i.done
		return i.endErr
	}
	i.ended = true
	i.mu.Unlock()
	defer close(i.done)

	if i.stdin != nil {
		_ = i.stdin.Close()
	}
	var err error
	if i.proc != nil {
		if i.mgr.Terminate(i.proc) {
			err = i.finish()
		} else {
			err = errors.Join(
				fmt.Errorf("%w: %s", ErrContainmentUnverified, i.server),
				i.mgr.Journal.ContainmentFailed(i.id, "containment not confirmed within the cleanup deadline"),
			)
		}
	}
	i.endErr = err
	return err
}

// finish journals the end of an instance nothing of which remains.
func (i *Instance) finish() error {
	if err := i.mgr.Journal.Ended(i.id); err != nil {
		return fmt.Errorf("mcp: journal instance end: %w", err)
	}
	return nil
}
