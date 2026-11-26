package sandbox

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"
)

func TestFakeReplaysScript(t *testing.T) {
	f := NewFake()
	f.Script(FakeCall{
		Chunks: [][]byte{[]byte("ab"), []byte("cde")},
		Status: ExitStatus{Code: 3},
		RunFor: 20 * time.Millisecond,
	})
	spec := Spec{Argv: []string{"sh", "-c", "x"}, Scratch: "/s"}
	p, err := f.Start(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 16)
	var chunks []string
	for {
		n, err := p.Output().Read(buf)
		if n > 0 {
			chunks = append(chunks, string(buf[:n]))
		}
		if err == io.EOF {
			break
		}
	}
	if len(chunks) != 2 || chunks[0] != "ab" || chunks[1] != "cde" {
		t.Errorf("chunks %q", chunks)
	}
	st, _ := p.Wait()
	if st.Code != 3 || st.Terminated {
		t.Errorf("status %+v", st)
	}
	if err := p.ConfirmEmpty(context.Background()); err != nil {
		t.Error(err)
	}
	if got := f.Specs(); len(got) != 1 || got[0].Scratch != "/s" {
		t.Errorf("specs %+v", got)
	}
}

func TestFakeHeldPipeAndTerminate(t *testing.T) {
	f := NewFake()
	f.Script(FakeCall{Chunks: [][]byte{[]byte("x")}, HoldPipe: true})
	p, _ := f.Start(context.Background(), Spec{})
	if _, err := p.Wait(); err != nil {
		t.Fatal(err)
	}
	eof := make(chan struct{})
	go func() { _, _ = io.Copy(io.Discard, p.Output()); close(eof) }()
	select {
	case <-eof:
		t.Fatal("pipe closed while a descendant holds it")
	case <-time.After(50 * time.Millisecond):
	}
	p.Terminate(Graceful)
	select {
	case <-eof:
		t.Fatal("graceful terminate released a held pipe")
	case <-time.After(30 * time.Millisecond):
	}
	p.Terminate(Forced)
	select {
	case <-eof:
	case <-time.After(time.Second):
		t.Fatal("pipe still held after forced terminate")
	}
	if got := f.Terminations(); len(got) != 2 || got[0] != Graceful || got[1] != Forced {
		t.Errorf("terminations %v", got)
	}
}

func TestFakeTerminateCutsRunShort(t *testing.T) {
	f := NewFake()
	f.Script(FakeCall{RunFor: time.Hour})
	p, _ := f.Start(context.Background(), Spec{})
	p.Terminate(Graceful)
	st, _ := p.Wait()
	if !st.Terminated || st.Signal != 15 {
		t.Errorf("status %+v", st)
	}
}

func TestFakeConfirmNeverReturns(t *testing.T) {
	f := NewFake()
	f.Script(FakeCall{ConfirmNever: true})
	p, _ := f.Start(context.Background(), Spec{})
	_, _ = p.Wait()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := p.ConfirmEmpty(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err %v", err)
	}
}

func TestFakeReportAndStartError(t *testing.T) {
	f := NewFake()
	f.SetReport(Report{Enforced: NewCapSet(F), Reason: "old kernel"})
	if r := f.Capabilities(); Supported(r.Enforced, false) || r.Reason == "" {
		t.Errorf("report %+v", r)
	}
	boom := errors.New("boom")
	f.Script(FakeCall{StartErr: boom})
	if _, err := f.Start(context.Background(), Spec{}); !errors.Is(err, boom) {
		t.Errorf("err %v", err)
	}
}

func TestFakeConcurrent(t *testing.T) {
	f := NewFake()
	done := make(chan struct{})
	for i := 0; i < 20; i++ {
		go func() {
			p, _ := f.Start(context.Background(), Spec{})
			p.Terminate(Forced)
			_, _ = p.Wait()
			done <- struct{}{}
		}()
	}
	for i := 0; i < 20; i++ {
		<-done
	}
	if len(f.Specs()) != 20 || len(f.Terminations()) != 20 {
		t.Error("lost records")
	}
}
