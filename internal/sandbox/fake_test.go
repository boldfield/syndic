package sandbox

import (
	"context"
	"errors"
	"io"
	"reflect"
	"sync"
	"syscall"
	"testing"
	"time"
)

func startFake(t *testing.T, f *Fake, spec Spec) Process {
	t.Helper()
	p, err := f.Start(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// readChunks reads the pipe to end of file, returning each read.
func readChunks(t *testing.T, r io.Reader) []string {
	t.Helper()
	var reads []string
	buf := make([]byte, 1024)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			reads = append(reads, string(buf[:n]))
		}
		if err == io.EOF {
			return reads
		}
		if err != nil {
			t.Error(err)
			return reads
		}
	}
}

func confirmWithin(p Process, d time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	return p.ConfirmEmpty(ctx)
}

func TestFakeReplaysScript(t *testing.T) {
	f := NewFake(Caps(F, N1, P, D), FakeScript{
		Chunks: []FakeChunk{
			{Data: []byte("out ")},
			{Delay: 10 * time.Millisecond, Data: []byte("err ")},
			{Delay: 10 * time.Millisecond, Data: []byte("done")},
		},
		Runtime: 50 * time.Millisecond,
		Exit:    ExitStatus{Code: 3},
	})
	if caps, _ := f.Capabilities(); caps != Caps(F, N1, P, D) {
		t.Errorf("capabilities %s", caps)
	}
	spec := Spec{Argv: []string{"/bin/bash", "-c", "x"}, ScratchDir: "/s", TempDir: "/t", Env: []string{"PATH=/bin"}, Timeout: time.Second}
	start := time.Now()
	p := startFake(t, f, spec)
	if got := readChunks(t, p.Output()); !reflect.DeepEqual(got, []string{"out ", "err ", "done"}) {
		t.Errorf("chunks %q", got)
	}
	st, err := p.Wait()
	if err != nil || st != (ExitStatus{Code: 3}) {
		t.Errorf("status %+v, %v", st, err)
	}
	if d := time.Since(start); d < 50*time.Millisecond {
		t.Errorf("exited after %v, before its runtime", d)
	}
	if err := confirmWithin(p, time.Second); err != nil {
		t.Errorf("ConfirmEmpty: %v", err)
	}
	if got := f.Starts(); len(got) != 1 || !reflect.DeepEqual(got[0], spec) {
		t.Errorf("starts %+v", got)
	}
	if _, err := f.Start(context.Background(), spec); !errors.Is(err, ErrFakeExhausted) {
		t.Errorf("exhausted start: %v", err)
	}
}

func TestFakeTerminateRecordsAndEnds(t *testing.T) {
	f := NewFake(0, FakeScript{Runtime: time.Hour, IgnoreTerm: true}, FakeScript{Runtime: time.Hour})
	a := startFake(t, f, Spec{Argv: []string{"a"}})
	b := startFake(t, f, Spec{Argv: []string{"b"}})

	_ = a.Terminate(syscall.SIGTERM)
	select {
	case <-waitChan(a):
		t.Fatal("SIGTERM ended a command that ignores it")
	case <-time.After(20 * time.Millisecond):
	}
	_ = b.Terminate(syscall.SIGTERM)
	_ = a.Terminate(syscall.SIGKILL)
	_ = b.Terminate(syscall.SIGKILL)

	if st, _ := a.Wait(); st != SignalStatus(syscall.SIGKILL, true) || st.Code != 137 {
		t.Errorf("a: %+v", st)
	}
	if st, _ := b.Wait(); st != SignalStatus(syscall.SIGTERM, true) {
		t.Errorf("b: %+v", st)
	}
	want := []FakeTermination{
		{Call: 0, Signal: syscall.SIGTERM},
		{Call: 1, Signal: syscall.SIGTERM},
		{Call: 0, Signal: syscall.SIGKILL},
		{Call: 1, Signal: syscall.SIGKILL},
	}
	if got := f.Terminations(); !reflect.DeepEqual(got, want) {
		t.Errorf("terminations %+v", got)
	}
	for _, p := range []Process{a, b} {
		if got := readChunks(t, p.Output()); len(got) != 0 {
			t.Errorf("output %q", got)
		}
		if err := confirmWithin(p, time.Second); err != nil {
			t.Errorf("ConfirmEmpty: %v", err)
		}
	}
}

func waitChan(p Process) <-chan struct{} {
	ch := make(chan struct{})
	go func() {
		_, _ = p.Wait()
		close(ch)
	}()
	return ch
}

func TestFakeHeldPipe(t *testing.T) {
	f := NewFake(0, FakeScript{Chunks: []FakeChunk{{Data: []byte("x")}}, HoldPipe: true})
	p := startFake(t, f, Spec{})
	eof := make(chan []string, 1)
	go func() { eof <- readChunks(t, p.Output()) }()

	if st, _ := p.Wait(); st != (ExitStatus{}) {
		t.Errorf("status %+v", st)
	}
	select {
	case <-eof:
		t.Fatal("pipe closed while a descendant holds it")
	case <-time.After(50 * time.Millisecond):
	}
	if err := confirmWithin(p, 20*time.Millisecond); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("ConfirmEmpty with a live descendant: %v", err)
	}
	_ = p.Terminate(syscall.SIGTERM)
	select {
	case got := <-eof:
		if !reflect.DeepEqual(got, []string{"x"}) {
			t.Errorf("output %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("pipe still open after termination")
	}
	if err := confirmWithin(p, time.Second); err != nil {
		t.Errorf("ConfirmEmpty: %v", err)
	}
	if st, _ := p.Wait(); st.Terminated {
		t.Errorf("a shell that had exited reports terminated")
	}
}

func TestFakeNeverConfirms(t *testing.T) {
	f := NewFake(0, FakeScript{HoldPipe: true, NeverConfirm: true, Runtime: time.Hour})
	p := startFake(t, f, Spec{})
	_ = p.Terminate(syscall.SIGTERM)
	_ = p.Terminate(syscall.SIGKILL)
	if st, _ := p.Wait(); st != SignalStatus(syscall.SIGTERM, true) {
		t.Errorf("status %+v", st)
	}
	if err := confirmWithin(p, 50*time.Millisecond); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("ConfirmEmpty: %v", err)
	}
	// The held pipe never closes by itself; the engine closes it.
	done := make(chan struct{})
	go func() {
		_, _ = io.ReadAll(p.Output())
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("pipe closed")
	case <-time.After(50 * time.Millisecond):
	}
	_ = p.Output().Close()
	<-done
}

func TestFakeStartError(t *testing.T) {
	boom := errors.New("boom")
	f := NewFake(0, FakeScript{StartErr: boom})
	if _, err := f.Start(context.Background(), Spec{Argv: []string{"x"}}); !errors.Is(err, boom) {
		t.Errorf("err %v", err)
	}
	if len(f.Starts()) != 1 {
		t.Errorf("failed start not recorded")
	}
	f.SetCapabilities(Caps(F), "no namespaces")
	if c, r := f.Capabilities(); c != Caps(F) || r != "no namespaces" {
		t.Errorf("capabilities %s %q", c, r)
	}
}

func TestFakeConcurrent(t *testing.T) {
	const n = 32
	f := NewFake(Caps(F, N1, P, D))
	for i := 0; i < n; i++ {
		f.Push(FakeScript{Chunks: []FakeChunk{{Data: []byte("x")}}, Runtime: time.Millisecond})
	}
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, err := f.Start(context.Background(), Spec{Argv: []string{"x"}})
			if err != nil {
				t.Error(err)
				return
			}
			_, _ = io.ReadAll(p.Output())
			_, _ = p.Wait()
			_ = p.Terminate(syscall.SIGTERM)
			_ = p.Terminate(syscall.SIGKILL)
			_ = confirmWithin(p, time.Second)
		}()
	}
	wg.Wait()
	if len(f.Starts()) != n || len(f.Terminations()) != 2*n {
		t.Errorf("starts %d, terminations %d", len(f.Starts()), len(f.Terminations()))
	}
}
