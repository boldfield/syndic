package sandbox

import (
	"context"
	"errors"
	"io"
	"sync"
	"syscall"
	"time"
)

// FakeChunk is one write to a fake command's pipe.
type FakeChunk struct {
	// Delay is how long the command runs before this write.
	Delay time.Duration
	// Data is written to the pipe in one write.
	Data []byte
}

// FakeScript scripts one fake command.
type FakeScript struct {
	// StartErr, when set, makes Start fail with it.
	StartErr error
	// Chunks are written to the pipe in order.
	Chunks []FakeChunk
	// Runtime is how long the shell runs from start to exit, counting the
	// chunk delays; the shell exits after its last chunk if that is later.
	Runtime time.Duration
	// Exit is the status of a shell that exits by itself.
	Exit ExitStatus
	// IgnoreTerm makes SIGTERM end nothing; SIGKILL still does.
	IgnoreTerm bool
	// HoldPipe makes a descendant keep the pipe open after the shell exits,
	// until a Terminate that ends it.
	HoldPipe bool
	// NeverConfirm makes the descendant survive every Terminate, so that
	// ConfirmEmpty never returns nil and a held pipe never closes.
	NeverConfirm bool
}

// FakeTermination records one Terminate call.
type FakeTermination struct {
	// Call is the index of the Start call whose process was signalled.
	Call int
	// Signal is the signal passed.
	Signal syscall.Signal
}

// Fake is a scriptable Backend for engine tests. It runs no process: each
// Start replays the next queued FakeScript. It is safe for concurrent use.
type Fake struct {
	mu       sync.Mutex
	enforced CapSet
	reason   string
	scripts  []FakeScript
	starts   []Spec
	terms    []FakeTermination
}

// ErrFakeExhausted is returned by Start when no script is queued.
var ErrFakeExhausted = errors.New("sandbox: fake backend has no script queued")

// NewFake returns a fake backend that reports the given capabilities and
// replays scripts in order, one per Start.
func NewFake(enforced CapSet, scripts ...FakeScript) *Fake {
	return &Fake{enforced: enforced, scripts: append([]FakeScript{}, scripts...)}
}

// SetCapabilities changes what the backend reports.
func (f *Fake) SetCapabilities(enforced CapSet, reason string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.enforced, f.reason = enforced, reason
}

// Push queues further scripts.
func (f *Fake) Push(scripts ...FakeScript) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.scripts = append(f.scripts, scripts...)
}

// Starts returns every Spec the backend was started with, in order.
func (f *Fake) Starts() []Spec {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Spec{}, f.starts...)
}

// Terminations returns every Terminate call, in order.
func (f *Fake) Terminations() []FakeTermination {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]FakeTermination{}, f.terms...)
}

// Name implements Backend.
func (f *Fake) Name() string { return "fake" }

// Config implements Backend.
func (f *Fake) Config() map[string]string { return map[string]string{} }

// Capabilities implements Backend.
func (f *Fake) Capabilities() (CapSet, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.enforced, f.reason
}

// Start implements Backend. The spec is recorded even when the script makes
// Start fail.
func (f *Fake) Start(ctx context.Context, spec Spec) (Process, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	call := len(f.starts)
	f.starts = append(f.starts, cloneSpec(spec))
	if len(f.scripts) == 0 {
		return nil, ErrFakeExhausted
	}
	script := f.scripts[0]
	f.scripts = f.scripts[1:]
	if script.StartErr != nil {
		return nil, script.StartErr
	}
	pr, pw := io.Pipe()
	p := &fakeProcess{
		backend:  f,
		call:     call,
		script:   script,
		out:      pr,
		pw:       pw,
		killed:   make(chan struct{}),
		descDead: make(chan struct{}),
		exited:   make(chan struct{}),
	}
	p.pending.cond = sync.NewCond(&p.pending.mu)
	go p.run(time.Now())
	go p.drain()
	return p, nil
}

func cloneSpec(s Spec) Spec {
	s.Argv = append([]string(nil), s.Argv...)
	s.Env = append([]string(nil), s.Env...)
	s.System.Path = append([]string(nil), s.System.Path...)
	s.System.ReadOnlyPaths = append([]string(nil), s.System.ReadOnlyPaths...)
	s.System.Devices = append([]string(nil), s.System.Devices...)
	if s.Proxy != nil {
		l := *s.Proxy
		s.Proxy = &l
	}
	return s
}

type fakeProcess struct {
	backend *Fake
	call    int
	script  FakeScript
	out     *io.PipeReader
	pw      *io.PipeWriter

	mu       sync.Mutex
	killSig  syscall.Signal
	killed   chan struct{} // closed by the first Terminate that ends the shell
	descDead chan struct{} // closed when the holding descendant ends
	exited   chan struct{} // closed when the shell has exited
	status   ExitStatus

	// pending stands in for the kernel pipe buffer: the shell's writes land
	// here and never block, so shell exit, Wait and Terminate do not depend
	// on anyone reading Output. drain delivers the chunks to the pipe in
	// order, one Write each.
	pending struct {
		mu     sync.Mutex
		cond   *sync.Cond
		chunks [][]byte
		closed bool // every writer has gone; close the pipe once drained
	}
}

func (p *fakeProcess) run(start time.Time) {
	for _, c := range p.script.Chunks {
		if !p.sleep(c.Delay) {
			break
		}
		p.emit(c.Data)
	}
	if rest := p.script.Runtime - time.Since(start); rest > 0 {
		p.sleep(rest)
	}

	p.mu.Lock()
	select {
	case <-p.killed:
		p.status = SignalStatus(p.killSig, true)
	default:
		p.status = p.script.Exit
	}
	p.mu.Unlock()
	close(p.exited)

	if p.script.HoldPipe {
		<-p.descDead
	}
	p.pending.mu.Lock()
	p.pending.closed = true
	p.pending.mu.Unlock()
	p.pending.cond.Signal()
}

// emit buffers one write by the shell without waiting for a reader.
func (p *fakeProcess) emit(data []byte) {
	p.pending.mu.Lock()
	p.pending.chunks = append(p.pending.chunks, append([]byte(nil), data...))
	p.pending.mu.Unlock()
	p.pending.cond.Signal()
}

// drain delivers buffered writes to the pipe, and closes it after the last
// writer has gone and everything buffered has been delivered, as a reader
// of a real pipe sees end of file only after the buffered bytes. If the
// reader closes Output, what remains is discarded.
func (p *fakeProcess) drain() {
	for {
		p.pending.mu.Lock()
		for len(p.pending.chunks) == 0 && !p.pending.closed {
			p.pending.cond.Wait()
		}
		if len(p.pending.chunks) == 0 {
			p.pending.mu.Unlock()
			_ = p.pw.Close()
			return
		}
		c := p.pending.chunks[0]
		p.pending.chunks = p.pending.chunks[1:]
		p.pending.mu.Unlock()
		if _, err := p.pw.Write(c); err != nil {
			return
		}
	}
}

// sleep waits d, and reports false when the shell was killed first.
func (p *fakeProcess) sleep(d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-p.killed:
		return false
	}
}

func (p *fakeProcess) Output() io.ReadCloser { return p.out }

func (p *fakeProcess) Wait() (ExitStatus, error) {
	<-p.exited
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.status, nil
}

func (p *fakeProcess) Terminate(sig syscall.Signal) error {
	p.backend.mu.Lock()
	p.backend.terms = append(p.backend.terms, FakeTermination{Call: p.call, Signal: sig})
	p.backend.mu.Unlock()

	if sig == syscall.SIGTERM && p.script.IgnoreTerm {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	select {
	case <-p.killed:
		return nil
	default:
	}
	p.killSig = sig
	close(p.killed)
	if !p.script.NeverConfirm {
		close(p.descDead)
	}
	return nil
}

func (p *fakeProcess) ConfirmEmpty(ctx context.Context) error {
	if p.script.NeverConfirm {
		<-ctx.Done()
		return ctx.Err()
	}
	select {
	case <-p.exited:
	case <-ctx.Done():
		return ctx.Err()
	}
	if p.script.HoldPipe {
		select {
		case <-p.descDead:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}
