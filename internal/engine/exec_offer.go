package engine

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/boldfield/syndic/internal/config"
	"github.com/boldfield/syndic/internal/manifest"
	"github.com/boldfield/syndic/internal/sandbox"
	"github.com/boldfield/syndic/internal/transport"
)

// Whether bash is offered (v7 delta, sections 2, 3 and 6). At engine start the
// engine chooses the backend, runs its support detection and the self-test and
// chooses the shell; a reason for each way exec may end up unoffered is kept
// for the participants reply. Per participant it needs tool_calls: yes and a
// referee model with tool_calls: yes on its endpoint. The run's one execution
// gate is created with the engine; once it is closed exec is offered to no one.

// Reasons exec is not offered. Each is the start of the string the engine
// keeps; a detail may follow after a colon.
const (
	execOffNoBackend     = "no_sandbox_backend"
	execOffUnsupported   = "sandbox_unsupported"
	execOffNoShell       = "no_shell"
	execOffSelfTest      = "self_test_failed"
	execOffNoToolCalls   = "no_tool_calls"
	execOffRefereeCalls  = "referee_no_tool_calls"
	execOffGateClosed    = "containment_unverified"
	execOffNotConfigured = "not_granted"
)

// defaultExecShells are the shells the design names, in order of preference.
var defaultExecShells = []string{"/bin/bash", "/bin/sh"}

// ExecOptions is the engine's seam to command execution. The zero value
// offers nothing under sandbox = "required": no backend is available.
type ExecOptions struct {
	// Backend is the OS sandbox backend used under tools.exec.sandbox =
	// "required". Nil means none is available, and exec is unoffered.
	Backend sandbox.Backend
	// NewBackend builds the backend for the system locations the roster
	// configures, when Backend is nil. The daemon sets it to the platform's
	// backend; a nil result means the platform has none.
	NewBackend func(ctx context.Context, sup sandbox.Supervisor, sys sandbox.SystemLocations) sandbox.Backend
	// Supervisor starts commands under tools.exec.sandbox = "none".
	Supervisor sandbox.Supervisor
	// Shells are the candidate shell paths, in order; empty means /bin/bash
	// then /bin/sh. A bash is run with --noprofile --norc, any other shell
	// plainly, each with -c.
	Shells []string
	// SelfTest holds the fields of the self-test the engine does not know:
	// StateDir, Dir, Helper and Network. The engine sets the shell, the
	// system locations, the network list flag, the sequence and the gate.
	SelfTest sandbox.SelfTestConfig
	// TempDir is the engine's temporary area, where each call's scratch and
	// temporary directories are made; empty means os.TempDir().
	TempDir string
	// ProxyNetwork is the network of a call's proxy listener, "unix" or
	// "tcp"; empty means "tcp".
	ProxyNetwork string
	// ProxyAddr is the address a command reaches its proxy at, when that is
	// not the listener's own (a forwarder in its namespace); empty means the
	// listener's address.
	ProxyAddr string
	// Environ is the environment env_passthrough names are read from, as
	// os.Environ returns it; nil means the process environment.
	Environ []string
}

// execState is what Start decided for exec. It is set once, under e.mu.
type execState struct {
	backend     sandbox.Backend
	none        bool
	shell       string   // path of the chosen shell, empty when none exists
	argv        []string // the shell's arguments before the command
	selfTest    sandbox.SelfTestConfig
	networkList bool
	// off is why exec is unoffered for the run, empty when it is offered.
	off string
	// start is the result of the self-test of engine start.
	start sandbox.SelfTestResult
}

// execTurn is the self-test of one turn, run before its first command.
type execTurn struct {
	done   chan struct{}
	reason string // set before done is closed; empty when it passed
}

// ExecGate returns the run's one execution gate, to be passed to whatever
// starts commands.
func (e *Engine) ExecGate() *sandbox.Gate { return &e.execGate }

// execGranted reports whether any preset gives exec to a participant.
func execGranted(roster *config.Roster) bool {
	for _, p := range roster.Presets {
		if len(p.ExecParticipants()) > 0 {
			return true
		}
	}
	return false
}

// execNetworkList reports whether a network list is in force for a preset
// that grants exec.
func execNetworkList(roster *config.Roster) bool {
	for _, p := range roster.Presets {
		if len(p.ExecParticipants()) > 0 && len(roster.EffectiveNetworkAllow(p)) > 0 {
			return true
		}
	}
	return false
}

// chooseShell returns the first candidate that is an executable file, with
// the arguments that precede the command.
func chooseShell(candidates []string) (path string, args []string) {
	if len(candidates) == 0 {
		candidates = defaultExecShells
	}
	for _, c := range candidates {
		fi, err := os.Stat(c)
		if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm()&0o111 == 0 {
			continue
		}
		if filepath.Base(c) == "bash" {
			return c, []string{"--noprofile", "--norc", "-c"}
		}
		return c, []string{"-c"}
	}
	return "", nil
}

// execArgv is the argument vector of a command (v7 delta, section 2).
func (s *execState) execArgv(command string) []string {
	return append(append([]string{s.shell}, s.argv...), command)
}

// prepareExec chooses the backend and the shell and refuses a preset whose
// network list the backend cannot enforce. It runs at roster load, before any
// command; the self-test is run by runExecSelfTest. e.mu need not be held.
func (e *Engine) prepareExec(roster *config.Roster) (*execState, error) {
	st := &execState{}
	if !execGranted(roster) {
		st.off = execOffNotConfigured
		return st, nil
	}
	ex := roster.ExecSettings()
	st.networkList = execNetworkList(roster)
	st.none = ex.Sandbox == config.ExecSandboxNone
	system := sandbox.SystemLocations{
		Path:          filepath.SplitList(ex.Path),
		ReadOnlyPaths: slices.Clone(ex.ReadOnlyPaths),
		Devices:       slices.Clone(sandbox.DeviceNodes),
	}
	switch {
	case st.none:
		st.backend = sandbox.NewNone(e.exec.Supervisor)
	case e.exec.Backend != nil:
		st.backend = e.exec.Backend
	case e.exec.NewBackend != nil:
		if b := e.exec.NewBackend(context.Background(), e.exec.Supervisor, system); b != nil {
			st.backend = b
		}
	}

	if st.backend != nil && !st.none {
		enforced, _ := st.backend.Capabilities()
		if st.networkList && !enforced.Has(sandbox.N2) {
			for _, name := range slices.Sorted(maps.Keys(roster.Presets)) {
				p := roster.Presets[name]
				if len(p.ExecParticipants()) > 0 && len(roster.EffectiveNetworkAllow(p)) > 0 {
					return nil, &RosterError{Err: fmt.Errorf("preset %q: network_allow is non-empty but sandbox backend %q does not enforce N2 (proxy only)", name, st.backend.Name())}
				}
			}
		}
	}

	st.shell, st.argv = chooseShell(e.exec.Shells)
	st.selfTest = e.exec.SelfTest
	st.selfTest.Argv = st.execArgv
	st.selfTest.Path = ex.Path
	st.selfTest.System = system
	st.selfTest.NetworkList = st.networkList
	st.selfTest.Sequence = sandbox.Sequence{
		KillGrace:       time.Duration(ex.KillGraceS) * time.Second,
		CleanupDeadline: time.Duration(ex.CleanupDeadlineS) * time.Second,
	}
	st.selfTest.Gate = &e.execGate

	switch {
	case st.backend == nil:
		st.off = execOffNoBackend
	case st.shell == "":
		st.off = execOffNoShell
	case !st.none:
		enforced, reason := st.backend.Capabilities()
		if ok, missing := sandbox.Supported(enforced, st.networkList); !ok {
			detail := "missing " + missing.String()
			if reason != "" {
				detail = reason + "; " + detail
			}
			st.off = fmt.Sprintf("%s:%s:%s", execOffUnsupported, st.backend.Name(), detail)
		}
	}
	return st, nil
}

// runExecSelfTest runs the self-test of engine start when exec is still
// offered, and records a failure as the run's reason. Under sandbox = "none"
// there is nothing to test: nothing is enforced.
func (e *Engine) runExecSelfTest(ctx context.Context, st *execState) {
	if st.off != "" || st.none {
		return
	}
	st.start = sandbox.RunAtStart(ctx, st.backend, st.selfTest)
	if !st.start.Passed() {
		st.off = selfTestOffReason(st.start)
	}
}

// selfTestOffReason names the tests that failed.
func selfTestOffReason(res sandbox.SelfTestResult) string {
	var names []string
	for _, c := range res.Failed() {
		names = append(names, string(c.Check))
	}
	return execOffSelfTest + ":" + strings.Join(names, ",")
}

// ExecOffReason is why exec is offered to nobody for the rest of the run, or
// empty when it may be offered: the reason kept at start, or the one the
// closing of the gate gave. The participants reply carries it.
func (e *Engine) ExecOffReason() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.execRunOff()
}

// ExecSandboxNotice is the line the start log carries under
// tools.exec.sandbox = "none", or empty under any other setting (v7 delta,
// section 3, "The opt-out").
func (e *Engine) ExecSandboxNotice() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.execState == nil || !e.execState.none {
		return ""
	}
	return manifest.SandboxNoneNotice
}

// execRunOff is the run-wide reason exec is unoffered. e.mu is held.
func (e *Engine) execRunOff() string {
	if reason, closed := e.execGate.Closed(); closed {
		return execOffGateClosed + ":" + reason
	}
	if e.execState == nil {
		return execOffNotConfigured
	}
	return e.execState.off
}

// ExecOffered reports why exec is not offered to caller in the open turn, or
// returns the empty string when it is. Whatever builds a caller's tool
// definitions asks it, and offers the exec group only on an empty answer; the
// definitions do not offer bash yet, since nothing here executes a command.
func (e *Engine) ExecOffered(turnID, caller string) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	t := e.open
	if t == nil || t.id != turnID {
		return "turn_not_open"
	}
	return e.execOffReason(t, caller)
}

// execOffReason is why exec is not offered to caller in turn t, or empty when
// it is: the run's reason, then the caller's tool_calls, then its referee, then
// the turn's self-test. e.mu is held.
func (e *Engine) execOffReason(t *openTurn, caller string) string {
	if r := e.execRunOff(); r != "" {
		return r
	}
	if e.built.Participants[caller].ToolCalls != transport.TriYes {
		return execOffNoToolCalls + ":" + caller
	}
	endpoint := e.participantConfig(caller).Endpoint
	if r := e.webOffReason(endpoint); r != "" {
		return r
	}
	if ref, _, _ := e.refereeOf(endpoint); ref.ToolCalls != transport.TriYes {
		return execOffRefereeCalls + ":" + endpoint
	}
	if t != nil && t.execTurn != nil {
		select {
		case <-t.execTurn.done:
			return t.execTurn.reason
		default:
		}
	}
	return ""
}

// ExecBeforeCommand runs the turn's self-test before its first command and
// returns why exec is unoffered for the turn, or empty. The test runs once per
// turn however many callers ask; the others wait for it. A failure leaves exec
// unoffered for the turn only. The caller enters the gate immediately before
// the process starts.
func (e *Engine) ExecBeforeCommand(ctx context.Context, turnID string) string {
	e.mu.Lock()
	t := e.open
	if t == nil || t.id != turnID {
		e.mu.Unlock()
		return "turn_not_open"
	}
	if r := e.execRunOff(); r != "" {
		e.mu.Unlock()
		return r
	}
	st := e.execState
	run := t.execTurn == nil && !st.none
	if t.execTurn == nil {
		t.execTurn = &execTurn{done: make(chan struct{})}
		if !run {
			close(t.execTurn.done)
		}
	}
	turn := t.execTurn
	e.mu.Unlock()

	if run {
		res := sandbox.RunBeforeTurn(ctx, st.backend, st.selfTest)
		if !res.Passed() {
			turn.reason = selfTestOffReason(res)
		}
		close(turn.done)
	} else {
		select {
		case <-turn.done:
		case <-ctx.Done():
			return "turn_self_test_interrupted"
		}
	}
	if reason, closed := e.execGate.Closed(); closed {
		return execOffGateClosed + ":" + reason
	}
	return turn.reason
}
