// Package command runs one exec command through a sandbox backend and
// produces its result (v7 delta, section 2, "Invocation", "Bounds", "Output"
// and "Result"): the shell and its arguments, the effective timeout, the
// termination sequence at the bound or on cancellation, and the output
// captured by the design's rules. It is given a backend, a prepared
// directory and a command; it knows nothing of the engine, the referee, the
// scratch build or the journal.
package command

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/boldfield/syndic/internal/sandbox"
)

// Bounds that are design constants, and the defaults of those the roster may
// override under [tools.exec] (v7 delta, section 2, "Bounds").
const (
	// MaxCommandBytes is max_command_bytes: a longer command is refused
	// before anything is started.
	MaxCommandBytes = 4096
	// DefaultTimeoutS is the default of timeout_s.
	DefaultTimeoutS = 120
	// DefaultMaxTimeoutS is the default maximum of timeout_s.
	DefaultMaxTimeoutS = 600
	// DefaultMaxOutputBytes is the default of max_output_bytes.
	DefaultMaxOutputBytes = 61440
)

// Distinct outcomes of Run that carry no result.
var (
	// ErrNoShell: neither /bin/bash nor /bin/sh exists, so exec is unoffered
	// for the run.
	ErrNoShell = errors.New("command: neither /bin/bash nor /bin/sh exists")
	// ErrCommandTooLong: the command is longer than MaxCommandBytes.
	ErrCommandTooLong = fmt.Errorf("command: command longer than %d bytes", MaxCommandBytes)
	// ErrInvalidTimeout: the requested timeout_s is not positive.
	ErrInvalidTimeout = errors.New("command: timeout_s must be positive")
	// ErrTerminatedByCaller: the caller's context ended; the command was
	// terminated and its containment confirmed empty.
	ErrTerminatedByCaller = errors.New("command: terminated by caller")
	// ErrContainmentUnverified: the termination sequence did not confirm the
	// containment empty within its cleanup deadline (v7 delta, section 3,
	// "Containment unverified"). It takes precedence over every other
	// outcome, ErrTerminatedByCaller included.
	ErrContainmentUnverified = errors.New("command: containment unverified")
)

// Shell is the shell a run uses, named by its path.
type Shell string

// The shells, in order of preference (v7 delta, section 2, "Invocation").
const (
	Bash Shell = "/bin/bash"
	Sh   Shell = "/bin/sh"
)

// Argv returns the argument vector that runs command: bash with no profile
// and no rc file, or sh.
func (s Shell) Argv(command string) []string {
	if s == Bash {
		return []string{string(Bash), "--noprofile", "--norc", "-c", command}
	}
	return []string{string(s), "-c", command}
}

// ShellExists reports whether path is an executable regular file, following
// symbolic links. It is SelectShell's default check.
func ShellExists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0
}

// SelectShell chooses the run's shell once: Bash where exists reports it,
// else Sh, else ErrNoShell. A nil exists is ShellExists.
func SelectShell(exists func(path string) bool) (Shell, error) {
	if exists == nil {
		exists = ShellExists
	}
	for _, s := range []Shell{Bash, Sh} {
		if exists(string(s)) {
			return s, nil
		}
	}
	return "", ErrNoShell
}

// Bounds are the bounds of [tools.exec] that Run applies. A zero field is its
// design default.
type Bounds struct {
	// DefaultTimeoutS is the timeout_s of a call that asks for none.
	DefaultTimeoutS int
	// MaxTimeoutS is the most any call runs for.
	MaxTimeoutS int
	// MaxOutputBytes bounds the rendered length of the kept output.
	MaxOutputBytes int
}

func (b Bounds) withDefaults() Bounds {
	if b.DefaultTimeoutS <= 0 {
		b.DefaultTimeoutS = DefaultTimeoutS
	}
	if b.MaxTimeoutS <= 0 {
		b.MaxTimeoutS = DefaultMaxTimeoutS
	}
	if b.MaxOutputBytes <= 0 {
		b.MaxOutputBytes = DefaultMaxOutputBytes
	}
	return b
}

// EffectiveTimeoutS returns the timeout a call runs with: the requested value,
// or the default when requested is nil, and never more than the maximum.
func (b Bounds) EffectiveTimeoutS(requested *int) (int, error) {
	b = b.withDefaults()
	t := b.DefaultTimeoutS
	if requested != nil {
		if *requested <= 0 {
			return 0, ErrInvalidTimeout
		}
		t = *requested
	}
	return min(t, b.MaxTimeoutS), nil
}

// Runner runs commands for one run.
type Runner struct {
	// Backend starts each command.
	Backend sandbox.Backend
	// Shell is the run's shell, from SelectShell.
	Shell Shell
	// Sequence is the termination sequence. It must be configured: with a
	// zero CleanupDeadline no containment is confirmed and every call is
	// ErrContainmentUnverified.
	Sequence sandbox.Sequence
	// Bounds are the bounds applied to each call.
	Bounds Bounds
}

// Call is one bash call.
type Call struct {
	// Command is the command string.
	Command string
	// TimeoutS is the requested timeout_s, or nil for the default.
	TimeoutS *int
	// Spec is the prepared directories, environment, system locations and
	// proxy. Run sets Argv and Timeout, and removes BASH_ENV and ENV from
	// Env.
	Spec sandbox.Spec
	// Revoke, when not nil, is called when termination begins, before any
	// wait; see sandbox.Sequence.Run.
	Revoke func()
	// Gate, when not nil, wraps the process start: Run passes it the start
	// and it calls it, or does not, holding the run's gate open throughout
	// (sandbox.Gate.Start). An error it returns without starting ends the
	// call, with nothing started.
	Gate func(start func() error) error
}

// Result is the design's result less view, which is the caller's (v7 delta,
// section 2, "Result").
type Result struct {
	ExitCode int `json:"exit_code"`
	// Signal is the number of the signal that ended the shell, or 0; when
	// set, ExitCode is 128 plus it.
	Signal            int    `json:"signal,omitempty"`
	TimedOut          bool   `json:"timed_out"`
	Truncated         bool   `json:"truncated"`
	CaptureComplete   bool   `json:"capture_complete"`
	Output            string `json:"output"`
	OutputSHA256      string `json:"output_sha256"`
	OutputBytesKept   int    `json:"output_bytes_kept"`
	OutputBytesTotal  int64  `json:"output_bytes_total"`
	DurationMS        int64  `json:"duration_ms"`
	TimeoutSEffective int    `json:"timeout_s_effective"`
	Shell             string `json:"shell"`
	// Kept is the kept raw output, for the caller to store under
	// OutputSHA256.
	Kept []byte `json:"-"`
}

type waited struct {
	status sandbox.ExitStatus
	err    error
	at     time.Time
}

// Run runs one call and returns its result, delivered only after the
// termination sequence has confirmed the containment empty. A non-zero exit
// is a result. Without a result it returns ErrNoShell, ErrCommandTooLong or
// ErrInvalidTimeout before starting anything; ErrTerminatedByCaller when ctx
// ended first; ErrContainmentUnverified when confirmation did not come; or
// the backend's error.
func (r *Runner) Run(ctx context.Context, call Call) (*Result, error) {
	if len(call.Command) > MaxCommandBytes {
		return nil, ErrCommandTooLong
	}
	if r.Shell == "" {
		return nil, ErrNoShell
	}
	bounds := r.Bounds.withDefaults()
	timeoutS, err := bounds.EffectiveTimeoutS(call.TimeoutS)
	if err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, ErrTerminatedByCaller
	}
	timeout := time.Duration(timeoutS) * time.Second

	spec := call.Spec
	spec.Argv = r.Shell.Argv(call.Command)
	spec.Timeout = timeout
	spec.Env = withoutStartupFiles(spec.Env)

	// Cancellation is handled by the termination sequence once the command
	// has started, so that nothing is left uncontained by an aborted start.
	var proc sandbox.Process
	startFn := func() error {
		var err error
		proc, err = r.Backend.Start(context.WithoutCancel(ctx), spec)
		return err
	}
	if call.Gate != nil {
		var gateErr *sandbox.GateClosedError
		err = call.Gate(startFn)
		if errors.As(err, &gateErr) {
			return nil, fmt.Errorf("command: enter: %w", err)
		}
	} else {
		err = startFn()
	}
	if err != nil {
		return nil, fmt.Errorf("command: start: %w", err)
	}
	start := time.Now()
	out := startCapture(proc.Output(), bounds.MaxOutputBytes)
	// The capture cutoff runs from the shell's end, concurrently with the
	// termination sequence, so that confirming the containment empty does
	// not extend the time the pipe is read.
	waitc := make(chan waited, 1)
	capc := make(chan Output, 1)
	go func() {
		st, err := proc.Wait()
		waitc <- waited{status: st, err: err, at: time.Now()}
		capc <- out.finish(captureCutoff)
	}()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	var (
		w         waited
		exited    bool
		timedOut  bool
		cancelled bool
	)
	select {
	case w = <-waitc:
		exited = true
	case <-timer.C:
		select {
		case w = <-waitc:
			exited = true
		default:
			timedOut = true
		}
	case <-ctx.Done():
		cancelled = true
	}

	if !r.Sequence.Run(proc, exited && w.err == nil, call.Revoke) {
		out.stop()
		return nil, ErrContainmentUnverified
	}
	if cancelled {
		out.stop()
		return nil, ErrTerminatedByCaller
	}
	if !exited {
		// Containment is confirmed empty, so the shell has ended.
		w = <-waitc
	}
	if w.err != nil {
		out.stop()
		return nil, fmt.Errorf("command: wait: %w", w.err)
	}

	captured := <-capc
	if ctx.Err() != nil {
		return nil, ErrTerminatedByCaller
	}
	res := &Result{
		ExitCode:          w.status.Code,
		Signal:            w.status.Signal,
		TimedOut:          timedOut,
		Truncated:         captured.Truncated,
		CaptureComplete:   captured.Complete,
		Output:            captured.Text,
		OutputSHA256:      captured.SHA256,
		OutputBytesKept:   len(captured.Kept),
		OutputBytesTotal:  captured.BytesTotal,
		DurationMS:        w.at.Sub(start).Milliseconds(),
		TimeoutSEffective: timeoutS,
		Shell:             string(r.Shell),
		Kept:              captured.Kept,
	}
	if res.Signal != 0 {
		res.ExitCode = 128 + res.Signal
	}
	return res, nil
}

// withoutStartupFiles returns env less BASH_ENV and ENV, which name files a
// shell would read at startup.
func withoutStartupFiles(env []string) []string {
	kept := make([]string, 0, len(env))
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if name == "BASH_ENV" || name == "ENV" {
			continue
		}
		kept = append(kept, kv)
	}
	return kept
}
