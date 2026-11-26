package sandbox

import (
	"context"
	"io"
	"sync"
	"time"
)

// FakeCall scripts one command run on a Fake. No real process exists.
type FakeCall struct {
	// Chunks are the output bytes, one Read returning at most one chunk.
	Chunks [][]byte
	// ChunkDelay separates consecutive chunks.
	ChunkDelay time.Duration
	// Status is the exit status once the command ends on its own.
	Status ExitStatus
	// RunFor is how long the command "runs" from Start before it exits.
	RunFor time.Duration
	// HoldPipe keeps the output pipe open after exit, as a descendant would,
	// until Terminate(Forced) or the end of the Start context.
	HoldPipe bool
	// ConfirmNever makes ConfirmEmpty block until its context ends.
	ConfirmNever bool
	// StartErr makes Start fail with this error.
	StartErr error
}

// Fake is a scriptable Backend for engine tests. It is safe for concurrent
// use. Calls are consumed in order; once the script is exhausted a call that
// exits at once with status 0 and no output is used.
type Fake struct {
	// NameValue and ConfigValue answer Name and Config.
	NameValue   string
	ConfigValue map[string]string

	mu     sync.Mutex
	report Report
	script []FakeCall
	specs  []Spec
	terms  []Termination
}

// NewFake returns a Fake named "fake" that enforces every capability.
func NewFake() *Fake {
	return &Fake{
		NameValue:   "fake",
		ConfigValue: map[string]string{},
		report:      Report{Enforced: NewCapSet(F, N1, N2, P, D)},
	}
}

// SetReport sets what Capabilities returns.
func (f *Fake) SetReport(r Report) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.report = r
}

// Script appends calls to the script.
func (f *Fake) Script(calls ...FakeCall) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.script = append(f.script, calls...)
}

// Specs returns every Spec Start received, in order.
func (f *Fake) Specs() []Spec {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Spec(nil), f.specs...)
}

// Terminations returns every Terminate call, in order, across processes.
func (f *Fake) Terminations() []Termination {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Termination(nil), f.terms...)
}

// Name implements Backend.
func (f *Fake) Name() string { return f.NameValue }

// Config implements Backend.
func (f *Fake) Config() map[string]string { return f.ConfigValue }

// Capabilities implements Backend.
func (f *Fake) Capabilities() Report {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.report
}

// Start implements Backend.
func (f *Fake) Start(ctx context.Context, spec Spec) (Process, error) {
	f.mu.Lock()
	f.specs = append(f.specs, spec)
	var call FakeCall
	if len(f.script) > 0 {
		call = f.script[0]
		f.script = f.script[1:]
	}
	f.mu.Unlock()
	if call.StartErr != nil {
		return nil, call.StartErr
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p := &fakeProcess{
		fake:     f,
		call:     call,
		pipe:     newFakePipe(),
		exited:   make(chan struct{}),
		killed:   make(chan struct{}),
		termOnce: make(chan struct{}),
	}
	go p.run(ctx)
	return p, nil
}

type fakeProcess struct {
	fake *Fake
	call FakeCall
	pipe *fakePipe

	exited   chan struct{} // closed when the shell has ended
	killed   chan struct{} // closed by Terminate(Forced)
	termOnce chan struct{} // closed by the first Terminate

	mu         sync.Mutex
	status     ExitStatus
	termClosed bool
	killClosed bool
	termSig    int
}

func (p *fakeProcess) run(ctx context.Context) {
	start := time.Now()
	for i, c := range p.call.Chunks {
		if i > 0 && p.call.ChunkDelay > 0 && !p.sleep(ctx, p.call.ChunkDelay) {
			break
		}
		p.pipe.write(c)
	}
	if rest := p.call.RunFor - time.Since(start); rest > 0 {
		p.sleep(ctx, rest)
	}
	p.mu.Lock()
	select {
	case <-p.termOnce:
		p.status = ExitStatus{Signal: p.termSig, Terminated: true}
	default:
		if ctx.Err() != nil {
			p.status = ExitStatus{Signal: 9, Terminated: true}
		} else {
			p.status = p.call.Status
		}
	}
	p.mu.Unlock()
	close(p.exited)
	if p.call.HoldPipe {
		select {
		case <-p.killed:
		case <-ctx.Done():
		}
	}
	p.pipe.close()
}

// sleep waits d; false if the process was terminated or ctx ended first.
func (p *fakeProcess) sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-p.termOnce:
		return false
	case <-ctx.Done():
		return false
	}
}

func (p *fakeProcess) Output() io.Reader { return p.pipe }

func (p *fakeProcess) Wait() (ExitStatus, error) {
	<-p.exited
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.status, nil
}

func (p *fakeProcess) Terminate(t Termination) {
	p.fake.mu.Lock()
	p.fake.terms = append(p.fake.terms, t)
	p.fake.mu.Unlock()
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.termClosed {
		p.termClosed = true
		p.termSig = 15
		if t == Forced {
			p.termSig = 9
		}
		close(p.termOnce)
	}
	if t == Forced && !p.killClosed {
		p.killClosed = true
		close(p.killed)
	}
}

func (p *fakeProcess) ConfirmEmpty(ctx context.Context) error {
	if p.call.ConfirmNever {
		<-ctx.Done()
		return ctx.Err()
	}
	return ctx.Err()
}

// fakePipe is an unbounded chunked in-memory pipe.
type fakePipe struct {
	mu     sync.Mutex
	cond   *sync.Cond
	chunks [][]byte
	closed bool
}

func newFakePipe() *fakePipe {
	p := &fakePipe{}
	p.cond = sync.NewCond(&p.mu)
	return p
}

func (p *fakePipe) write(b []byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	p.chunks = append(p.chunks, append([]byte(nil), b...))
	p.cond.Broadcast()
}

func (p *fakePipe) close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	p.cond.Broadcast()
}

func (p *fakePipe) Read(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for len(p.chunks) == 0 {
		if p.closed {
			return 0, io.EOF
		}
		p.cond.Wait()
	}
	n := copy(b, p.chunks[0])
	if n < len(p.chunks[0]) {
		p.chunks[0] = p.chunks[0][n:]
	} else {
		p.chunks = p.chunks[1:]
	}
	return n, nil
}
