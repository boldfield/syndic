package command

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/sandbox"
)

// TestMain lets the test binary stand in for syndicd as the unsandboxed
// backend's supervisor.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == sandbox.SupervisorArg {
		os.Exit(sandbox.Supervise(os.Stderr))
	}
	captureCutoff = 200 * time.Millisecond
	os.Exit(m.Run())
}

var fastSequence = sandbox.Sequence{KillGrace: 50 * time.Millisecond, CleanupDeadline: time.Second}

func fakeRunner(t *testing.T, bounds Bounds, scripts ...sandbox.FakeScript) (*Runner, *sandbox.Fake) {
	t.Helper()
	fake := sandbox.NewFake(sandbox.Caps(sandbox.F, sandbox.N1, sandbox.P, sandbox.D), scripts...)
	return &Runner{Backend: fake, Shell: Bash, Sequence: fastSequence, Bounds: bounds}, fake
}

func fakeCall(command string) Call {
	return Call{Command: command, Spec: sandbox.Spec{ScratchDir: "/scratch", TempDir: "/tmp/call"}}
}

func intp(v int) *int { return &v }

func output(data string) sandbox.FakeScript {
	return sandbox.FakeScript{Chunks: []sandbox.FakeChunk{{Data: []byte(data)}}}
}

func TestSelectShell(t *testing.T) {
	for _, tc := range []struct {
		present []string
		want    Shell
		err     error
	}{
		{[]string{"/bin/bash", "/bin/sh"}, Bash, nil},
		{[]string{"/bin/sh"}, Sh, nil},
		{nil, "", ErrNoShell},
	} {
		got, err := SelectShell(func(p string) bool { return slices.Contains(tc.present, p) })
		if got != tc.want || !errors.Is(err, tc.err) {
			t.Errorf("present %v: %q, %v; want %q, %v", tc.present, got, err, tc.want, tc.err)
		}
	}
}

func TestShellArgv(t *testing.T) {
	if got, want := Bash.Argv("true"), []string{"/bin/bash", "--noprofile", "--norc", "-c", "true"}; !slices.Equal(got, want) {
		t.Errorf("bash: %q", got)
	}
	if got, want := Sh.Argv("true"), []string{"/bin/sh", "-c", "true"}; !slices.Equal(got, want) {
		t.Errorf("sh: %q", got)
	}
}

func TestRunStartsShell(t *testing.T) {
	r, fake := fakeRunner(t, Bounds{}, output(""))
	r.Shell = Sh
	call := fakeCall("echo hi")
	call.Spec.Env = []string{"BASH_ENV=/x", "ENV=/y", "HOME=/tmp/call", "PATH=/bin"}
	res, err := r.Run(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	if res.Shell != "/bin/sh" {
		t.Errorf("shell %q", res.Shell)
	}
	spec := fake.Starts()[0]
	if !slices.Equal(spec.Argv, []string{"/bin/sh", "-c", "echo hi"}) {
		t.Errorf("argv %q", spec.Argv)
	}
	if !slices.Equal(spec.Env, []string{"HOME=/tmp/call", "PATH=/bin"}) {
		t.Errorf("env %q", spec.Env)
	}
	if spec.ScratchDir != "/scratch" {
		t.Errorf("scratch %q", spec.ScratchDir)
	}
}

func TestRunNoShell(t *testing.T) {
	r, fake := fakeRunner(t, Bounds{}, output(""))
	r.Shell = ""
	if _, err := r.Run(context.Background(), fakeCall("true")); !errors.Is(err, ErrNoShell) {
		t.Fatalf("err %v", err)
	}
	if len(fake.Starts()) != 0 {
		t.Error("a command was started")
	}
}

func TestCommandLength(t *testing.T) {
	r, fake := fakeRunner(t, Bounds{}, output(""))
	if _, err := r.Run(context.Background(), fakeCall(strings.Repeat("a", MaxCommandBytes))); err != nil {
		t.Fatalf("at the bound: %v", err)
	}
	_, err := r.Run(context.Background(), fakeCall(strings.Repeat("a", MaxCommandBytes+1)))
	if !errors.Is(err, ErrCommandTooLong) {
		t.Fatalf("over the bound: %v", err)
	}
	if n := len(fake.Starts()); n != 1 {
		t.Errorf("%d starts, want 1", n)
	}
}

func TestEffectiveTimeout(t *testing.T) {
	r, fake := fakeRunner(t, Bounds{MaxTimeoutS: 600}, output(""), output(""))
	call := fakeCall("true")
	call.TimeoutS = intp(900)
	res, err := r.Run(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	if res.TimeoutSEffective != 600 || fake.Starts()[0].Timeout != 600*time.Second {
		t.Errorf("900 of 600: effective %d, spec %v", res.TimeoutSEffective, fake.Starts()[0].Timeout)
	}
	res, err = r.Run(context.Background(), fakeCall("true"))
	if err != nil {
		t.Fatal(err)
	}
	if res.TimeoutSEffective != DefaultTimeoutS {
		t.Errorf("absent: effective %d", res.TimeoutSEffective)
	}

	b := Bounds{DefaultTimeoutS: 30, MaxTimeoutS: 60}
	for _, tc := range []struct {
		req  *int
		want int
	}{{nil, 30}, {intp(45), 45}, {intp(60), 60}, {intp(61), 60}} {
		if got, err := b.EffectiveTimeoutS(tc.req); err != nil || got != tc.want {
			t.Errorf("%v: %d, %v; want %d", tc.req, got, err, tc.want)
		}
	}
	if _, err := b.EffectiveTimeoutS(intp(0)); !errors.Is(err, ErrInvalidTimeout) {
		t.Errorf("zero: %v", err)
	}
}

func TestTimedOut(t *testing.T) {
	r, fake := fakeRunner(t, Bounds{}, sandbox.FakeScript{
		Chunks:  []sandbox.FakeChunk{{Data: []byte("started\n")}},
		Runtime: time.Hour,
	})
	call := fakeCall("sleep 3600")
	call.TimeoutS = intp(1)
	begin := time.Now()
	res, err := r.Run(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	if !res.TimedOut || res.Signal != int(syscall.SIGTERM) || res.ExitCode != 143 {
		t.Errorf("timed_out %v signal %d exit %d", res.TimedOut, res.Signal, res.ExitCode)
	}
	if res.Output != "started\n" || !res.CaptureComplete {
		t.Errorf("output %q complete %v", res.Output, res.CaptureComplete)
	}
	if res.DurationMS < 1000 || time.Since(begin) > 10*time.Second {
		t.Errorf("duration %dms", res.DurationMS)
	}
	if terms := fake.Terminations(); len(terms) == 0 || terms[0].Signal != syscall.SIGTERM {
		t.Errorf("terminations %v", terms)
	}
}

func TestExitStatus(t *testing.T) {
	r, _ := fakeRunner(t, Bounds{},
		sandbox.FakeScript{Exit: sandbox.SignalStatus(syscall.SIGKILL, false)},
		sandbox.FakeScript{Exit: sandbox.ExitStatus{Code: 3}},
	)
	res, err := r.Run(context.Background(), fakeCall("kill -9 $$"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Signal != 9 || res.ExitCode != 137 || res.TimedOut {
		t.Errorf("signal %d exit %d timed_out %v", res.Signal, res.ExitCode, res.TimedOut)
	}
	res, err = r.Run(context.Background(), fakeCall("exit 3"))
	if err != nil {
		t.Fatalf("a non-zero exit is a result: %v", err)
	}
	if res.ExitCode != 3 || res.Signal != 0 {
		t.Errorf("exit %d signal %d", res.ExitCode, res.Signal)
	}
}

func checkSHA(t *testing.T, res *Result) {
	t.Helper()
	sum := sha256.Sum256(res.Kept)
	if res.OutputSHA256 != hex.EncodeToString(sum[:]) {
		t.Errorf("output_sha256 %s does not match the kept bytes %x", res.OutputSHA256, res.Kept)
	}
	if res.OutputBytesKept != len(res.Kept) {
		t.Errorf("output_bytes_kept %d, kept %d", res.OutputBytesKept, len(res.Kept))
	}
}

func TestInvalidUTF8(t *testing.T) {
	raw := "ok \xff\xfe € \xe2\x82\n"
	r, _ := fakeRunner(t, Bounds{}, output(raw))
	res, err := r.Run(context.Background(), fakeCall("printf"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "ok \uFFFD\uFFFD € \uFFFD\uFFFD\n"; res.Output != want {
		t.Errorf("output %q, want %q", res.Output, want)
	}
	if string(res.Kept) != raw || res.Truncated {
		t.Errorf("kept %q truncated %v", res.Kept, res.Truncated)
	}
	checkSHA(t, res)
	if want := sha256.Sum256([]byte(raw)); res.OutputSHA256 != hex.EncodeToString(want[:]) {
		t.Error("output_sha256 is not of the raw bytes")
	}
}

func TestTruncated(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		kept string
		text string
	}{
		{"ascii", strings.Repeat("a", 100), strings.Repeat("a", 10), strings.Repeat("a", 10)},
		{"invalid bytes render longer", strings.Repeat("\xff", 100), "\xff\xff\xff", "\uFFFD\uFFFD\uFFFD"},
		{"a cut splits a sequence", "abcdefg😀😀", "abcdefg\xf0", "abcdefg\uFFFD"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Two writes, so that the cut does not depend on one read.
			half := len(tc.raw) / 2
			r, _ := fakeRunner(t, Bounds{MaxOutputBytes: 10}, sandbox.FakeScript{Chunks: []sandbox.FakeChunk{
				{Data: []byte(tc.raw[:half])}, {Delay: 10 * time.Millisecond, Data: []byte(tc.raw[half:])},
			}})
			res, err := r.Run(context.Background(), fakeCall("yes"))
			if err != nil {
				t.Fatal(err)
			}
			if !res.Truncated || res.OutputBytesTotal != int64(len(tc.raw)) || res.OutputBytesKept != len(tc.kept) {
				t.Errorf("truncated %v total %d kept %d", res.Truncated, res.OutputBytesTotal, res.OutputBytesKept)
			}
			if string(res.Kept) != tc.kept || res.Output != tc.text {
				t.Errorf("kept %q output %q", res.Kept, res.Output)
			}
			if len(res.Output) > 10 {
				t.Errorf("rendering %d bytes", len(res.Output))
			}
			if !res.CaptureComplete {
				t.Error("capture incomplete")
			}
			checkSHA(t, res)
		})
	}
}

// heldBackend wraps the fake so that the pipe, once the fake's output has
// been delivered, stays open until the engine closes it, as a pipe held by
// a descendant outside the containment does.
type heldBackend struct{ *sandbox.Fake }

func (b heldBackend) Start(ctx context.Context, spec sandbox.Spec) (sandbox.Process, error) {
	p, err := b.Fake.Start(ctx, spec)
	if err != nil {
		return nil, err
	}
	return heldProcess{Process: p, out: &heldPipe{r: p.Output(), closed: make(chan struct{})}}, nil
}

type heldProcess struct {
	sandbox.Process
	out *heldPipe
}

func (p heldProcess) Output() io.ReadCloser { return p.out }

type heldPipe struct {
	r      io.ReadCloser
	once   sync.Once
	closed chan struct{}
}

func (h *heldPipe) Read(b []byte) (int, error) {
	n, err := h.r.Read(b)
	if errors.Is(err, io.EOF) {
		<-h.closed
		return n, io.ErrClosedPipe
	}
	return n, err
}

func (h *heldPipe) Close() error {
	h.once.Do(func() { close(h.closed) })
	return h.r.Close()
}

func TestCaptureCutoff(t *testing.T) {
	fake := sandbox.NewFake(sandbox.Caps(sandbox.D), output("held\n"))
	r := &Runner{Backend: heldBackend{fake}, Shell: Bash, Sequence: fastSequence}
	begin := time.Now()
	res, err := r.Run(context.Background(), fakeCall("hold"))
	if err != nil {
		t.Fatal(err)
	}
	if res.CaptureComplete || res.Truncated || res.Output != "held\n" {
		t.Errorf("complete %v truncated %v output %q", res.CaptureComplete, res.Truncated, res.Output)
	}
	if d := time.Since(begin); d < captureCutoff {
		t.Errorf("returned after %v, before the cutoff", d)
	}

	r, _ = fakeRunner(t, Bounds{}, output("closed\n"))
	res, err = r.Run(context.Background(), fakeCall("echo closed"))
	if err != nil {
		t.Fatal(err)
	}
	if !res.CaptureComplete || res.Truncated {
		t.Errorf("complete %v truncated %v", res.CaptureComplete, res.Truncated)
	}
}

func TestCancelled(t *testing.T) {
	r, fake := fakeRunner(t, Bounds{}, sandbox.FakeScript{Runtime: time.Hour})
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	revoked := false
	call := fakeCall("sleep 3600")
	call.Revoke = func() { revoked = true }
	res, err := r.Run(ctx, call)
	if !errors.Is(err, ErrTerminatedByCaller) || res != nil {
		t.Fatalf("res %v err %v", res, err)
	}
	if !revoked || len(fake.Terminations()) == 0 {
		t.Errorf("revoked %v terminations %v", revoked, fake.Terminations())
	}

	// A context that has already ended starts nothing.
	if _, err := r.Run(ctx, fakeCall("true")); !errors.Is(err, ErrTerminatedByCaller) {
		t.Errorf("ended context: %v", err)
	}
	if n := len(fake.Starts()); n != 1 {
		t.Errorf("%d starts", n)
	}
}

func TestContainmentUnverified(t *testing.T) {
	for _, tc := range []struct {
		name   string
		script sandbox.FakeScript
		cancel bool
	}{
		{"shell exit", sandbox.FakeScript{NeverConfirm: true}, false},
		{"timeout", sandbox.FakeScript{NeverConfirm: true, Runtime: time.Hour}, false},
		{"cancellation", sandbox.FakeScript{NeverConfirm: true, Runtime: time.Hour}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := fakeRunner(t, Bounds{}, tc.script)
			r.Sequence = sandbox.Sequence{KillGrace: 50 * time.Millisecond, CleanupDeadline: 100 * time.Millisecond}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.cancel {
				time.AfterFunc(50*time.Millisecond, cancel)
			}
			call := fakeCall("sleep 3600")
			call.TimeoutS = intp(1)
			begin := time.Now()
			res, err := r.Run(ctx, call)
			if !errors.Is(err, ErrContainmentUnverified) || res != nil {
				t.Fatalf("res %v err %v", res, err)
			}
			if d := time.Since(begin); d > 5*time.Second {
				t.Errorf("waited %v", d)
			}
		})
	}
}
