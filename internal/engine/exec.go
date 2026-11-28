package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/boldfield/syndic/internal/command"
	"github.com/boldfield/syndic/internal/execproxy"
	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/observation"
	"github.com/boldfield/syndic/internal/sandbox"
	"github.com/boldfield/syndic/internal/scratch"
	"github.com/boldfield/syndic/internal/tools"
	"github.com/boldfield/syndic/internal/workspace"
)

// Execution of one bash call (v7 delta, section 6, "Order for one bash call"):
// the deterministic checks, the scratch build, the referee's decision, the
// re-check of the step and the run's execution gate, then the command, which
// is journaled with its decision before it starts. What a command writes
// stays in its call's directories, which are removed when the call ends
// (section 5, "Writes are discarded").

// Tool error codes of a bash call that fails a check before it runs.
const (
	execErrTooLong    = "command_too_long"
	execErrCallLimit  = "exec_call_limit"
	execErrNotOffered = toolErrNotOffered
	execErrBound      = "workspace_bound"
	execErrDenied     = "referee_denied"
	execErrUnverified = "containment_unverified"
)

// bashArgs are the validated arguments of a bash call.
type bashArgs struct {
	Command  string `json:"command"`
	TimeoutS *int   `json:"timeout_s"`
}

// execCallsRemaining is how many bash calls caller has left under
// tools.exec.max_calls (the author, per turn) or tools.exec.max_reviewer_calls
// (a reviewer, per round). e.mu is held.
func (e *Engine) execCallsRemaining(t *openTurn, caller string) int {
	ex := e.roster.ExecSettings()
	limit := ex.MaxReviewerCalls
	if caller == t.author {
		limit = ex.MaxCalls
	}
	return limit - t.execCalls[caller]
}

// execRunner is the runner of one call, over the run's backend and shell.
func (e *Engine) execRunner(st *execState) *command.Runner {
	ex := e.roster.ExecSettings()
	return &command.Runner{
		Backend:  st.backend,
		Shell:    command.Shell(st.shell),
		Sequence: st.selfTest.Sequence,
		Bounds:   command.Bounds{DefaultTimeoutS: ex.TimeoutS, MaxTimeoutS: ex.MaxTimeoutS, MaxOutputBytes: ex.MaxOutputBytes},
	}
}

// journalToolError journals a tool error against the accepted call and
// returns it as the call's result.
func (e *Engine) journalToolError(callID, code, format string, args ...any) (tools.Result, error) {
	res := tools.Result{Content: []byte(fmt.Sprintf(format, args...)), ErrorCode: code}
	err := e.store.Tx(func(tx *journal.Tx) error {
		_, err := tx.RecordToolResult(callID, journal.ToolCallError, res.Content, res.ErrorCode)
		return err
	})
	if err != nil {
		return tools.Result{}, fmt.Errorf("journal result of call %s: %w", callID, err)
	}
	return res, nil
}

// bashEffect is the effect of bash for the attempt's step of caller. The hook
// stands in for a wait inside the effect, as for the file tools.
func (e *Engine) bashEffect(fs *workspace.Confined, t *openTurn, caller, attemptID string, hook func(turnID, attemptID, name string)) toolEffect {
	return func(call journal.ToolCall, raw json.RawMessage, recheck func() error) (tools.Result, error) {
		if hook != nil {
			hook(t.id, attemptID, call.Name)
			if err := recheck(); err != nil {
				return tools.Result{}, err
			}
		}
		var args bashArgs
		if err := json.Unmarshal(raw, &args); err != nil {
			return tools.Result{}, fmt.Errorf("engine: bash arguments: %w", err)
		}
		return e.runBash(fs, t, caller, attemptID, call, args, recheck)
	}
}

func (e *Engine) runBash(fs *workspace.Confined, t *openTurn, caller, attemptID string, call journal.ToolCall, args bashArgs, recheck func() error) (tools.Result, error) {
	// The deterministic checks: command length, call bounds, exec offered.
	if len(args.Command) > command.MaxCommandBytes {
		return e.journalToolError(call.ID, execErrTooLong, "the command is %d bytes; the limit is %d", len(args.Command), command.MaxCommandBytes)
	}
	e.mu.Lock()
	if e.execCallsRemaining(t, caller) <= 0 {
		e.mu.Unlock()
		return e.journalToolError(call.ID, execErrCallLimit, "no bash calls remain for you in this turn")
	}
	if t.execCalls == nil {
		t.execCalls = map[string]int{}
	}
	// A call refused below as not offered still counts against the bound.
	t.execCalls[caller]++
	reason := e.execOffReason(t, caller)
	if reason == "" && (caller != t.author || fs == nil) {
		reason = "not_granted:" + caller
	}
	st := e.execState
	submitTime := t.submitTime
	ex := e.roster.ExecSettings()
	allow := e.roster.EffectiveNetworkAllow(t.eff.Preset)
	e.mu.Unlock()
	if reason != "" {
		return e.journalToolError(call.ID, execErrNotOffered, "bash is not available: %s", reason)
	}

	ctx, cancel := e.turnContext(t)
	defer cancel()
	if reason := e.ExecBeforeCommand(ctx, t.id); reason != "" {
		if ctx.Err() != nil {
			return tools.Result{}, errToolAbandoned
		}
		return e.journalToolError(call.ID, execErrNotOffered, "bash is not available: %s", reason)
	}
	if err := recheck(); err != nil {
		return tools.Result{}, err
	}
	runner := e.execRunner(st)
	timeoutS, err := runner.Bounds.EffectiveTimeoutS(args.TimeoutS)
	if err != nil {
		return e.journalToolError(call.ID, tools.ErrCodeBadArguments, "invalid arguments: %v", err)
	}

	// A fresh directory for this call, holding the scratch directory and the
	// temporary directory; all of it is removed when the call ends.
	area := e.exec.TempDir
	if area == "" {
		area = os.TempDir()
	}
	root, err := os.MkdirTemp(area, "syndic-exec-")
	if err != nil {
		return tools.Result{}, fmt.Errorf("engine: exec directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(root) }()
	scratchDir, tmpDir := filepath.Join(root, "scratch"), filepath.Join(root, "tmp")
	if err := os.Mkdir(tmpDir, 0o700); err != nil {
		return tools.Result{}, fmt.Errorf("engine: exec temporary directory: %w", err)
	}

	// The scratch build, with its bound.
	var staged []journal.StagingEntry
	if err := e.store.Tx(func(tx *journal.Tx) (err error) {
		staged, err = tx.Staging(t.id)
		return err
	}); err != nil {
		return tools.Result{}, err
	}
	view, err := scratch.Build(scratchDir,
		scratch.Record{Journal: e.store, Store: observation.NewStore(e.store), FS: fs, TurnID: t.id, AttemptID: attemptID},
		scratch.Overlay(staged),
		scratch.Config{MaxBytes: ex.MaxWorkspaceBytes, MaxFiles: int64(ex.MaxWorkspaceFiles), Exclude: ex.Exclude, SubmitTime: submitTime})
	if errors.Is(err, scratch.ErrBound) {
		return e.journalToolError(call.ID, execErrBound, "%v", err)
	}
	if err != nil {
		return tools.Result{}, fmt.Errorf("engine: scratch build: %w", err)
	}
	if err := recheck(); err != nil {
		return tools.Result{}, err
	}

	// The referee's decision.
	out, err := e.dispatchCommandReferee(commandCall{turnID: t.id, caller: caller, callID: call.ID, command: args.Command})
	switch {
	case errors.Is(err, errRefereeUnavailable):
		return e.journalToolError(call.ID, execErrNotOffered, "bash is not available: no usable referee")
	case err != nil:
		return tools.Result{}, err
	case !out.allow:
		return e.journalToolError(call.ID, execErrDenied, "the command was refused: %s", out.reason)
	}

	// The re-check of the step, then the run's gate.
	if err := recheck(); err != nil {
		return tools.Result{}, err
	}
	if reason, closed := e.execGate.Closed(); closed {
		return e.journalToolError(call.ID, execErrNotOffered, "bash is not available: %s:%s", execOffGateClosed, reason)
	}

	spec := sandbox.Spec{ScratchDir: scratchDir, TempDir: tmpDir, System: st.selfTest.System}
	envCfg := sandbox.EnvConfig{Path: ex.Path, TempDir: tmpDir, Passthrough: ex.EnvPassthrough, Source: e.environ()}
	var revoke func()
	if len(allow) > 0 && !st.none {
		proxy, err := e.newCallProxy(allow, call.ID, attemptID, root)
		if err != nil {
			return tools.Result{}, err
		}
		var once sync.Once
		revoke = func() { once.Do(proxy.Revoke) }
		defer revoke()
		spec.Proxy = &sandbox.Listener{Network: proxy.Network(), Address: proxy.Addr()}
		addr := e.exec.ProxyAddr
		if addr == "" {
			addr = proxy.Addr()
		}
		envCfg.ProxyURL = proxy.ProxyURL(addr)
	}
	spec.Env = sandbox.Env(envCfg)
	policyHash, err := e.execPolicyHash(st, ex.Path, ex.ReadOnlyPaths, allow, spec)
	if err != nil {
		return tools.Result{}, err
	}

	// The command is journaled with its decision before it starts.
	err = e.store.Tx(func(tx *journal.Tx) error {
		d, err := tx.RefereeDecisionForCall(call.ID)
		if err != nil {
			return err
		}
		_, err = tx.RecordExecCall(journal.ExecCallInput{
			ToolCallID:        call.ID,
			Command:           []byte(args.Command),
			TimeoutSEffective: timeoutS,
			Shell:             st.shell,
			View:              journal.ExecView{SnapshotVersion: view.SnapshotVersion, OverlaySHA256: view.OverlaySHA256, ModesSHA256: view.ModesSHA256},
			RefereeDecisionID: d.ID,
			SandboxBackend:    st.backend.Name(),
			PolicyHash:        policyHash,
		})
		return err
	})
	if err != nil {
		return tools.Result{}, err
	}

	res, err := runner.Run(ctx, command.Call{Command: args.Command, TimeoutS: &timeoutS, Spec: spec, Revoke: revoke, Enter: e.execGate.Enter})
	switch {
	case errors.Is(err, sandbox.ErrGateClosed):
		// Nothing started: the gate closed after the pre-check.
		if err := e.recordExecOutcome(call.ID, journal.ExecAbandoned, journal.ExecCauseCancelled); err != nil {
			return tools.Result{}, err
		}
		reason, _ := e.execGate.Closed()
		return e.journalToolError(call.ID, execErrNotOffered, "bash is not available: %s:%s", execOffGateClosed, reason)
	case errors.Is(err, command.ErrContainmentUnverified):
		e.execGate.Close(fmt.Sprintf("call %s: containment not confirmed empty", call.ID))
		if err := e.recordExecOutcome(call.ID, journal.ExecContainmentUnverified, journal.ExecCauseCleanupDeadline); err != nil {
			return tools.Result{}, err
		}
		return e.journalToolError(call.ID, execErrUnverified, "the command's containment could not be confirmed; bash is unavailable for the rest of the run")
	case errors.Is(err, command.ErrTerminatedByCaller):
		if err := e.recordExecOutcome(call.ID, journal.ExecAbandoned, journal.ExecCauseTurnDeadline); err != nil {
			return tools.Result{}, err
		}
		return tools.Result{}, errToolAbandoned
	case err != nil:
		return tools.Result{}, fmt.Errorf("engine: bash: %w", err)
	}

	content := []byte(command.RenderResult(res, view))
	in := journal.ExecResultInput{
		ExitCode:         res.ExitCode,
		TimedOut:         res.TimedOut,
		Truncated:        res.Truncated,
		CaptureComplete:  res.CaptureComplete,
		Output:           res.Kept,
		OutputBytesTotal: res.OutputBytesTotal,
		DurationMS:       res.DurationMS,
	}
	if res.Signal != 0 {
		sig := res.Signal
		in.Signal = &sig
	}
	err = e.store.Tx(func(tx *journal.Tx) error {
		if _, err := tx.RecordExecResult(call.ID, in); err != nil {
			return err
		}
		_, err := tx.RecordToolResult(call.ID, journal.ToolCallExecuted, content, "")
		return err
	})
	if err != nil {
		return tools.Result{}, fmt.Errorf("journal result of call %s: %w", call.ID, err)
	}
	return tools.Result{Content: content}, nil
}

func (e *Engine) recordExecOutcome(callID, kind, cause string) error {
	return e.store.Tx(func(tx *journal.Tx) error {
		_, err := tx.RecordExecOutcome(callID, kind, cause)
		return err
	})
}

// newCallProxy opens the call's proxy listener and credential, which refuse
// a connection once the step is no longer live. A Unix socket lies in the
// call's own directory.
func (e *Engine) newCallProxy(patterns []string, callID, attemptID, root string) (*execproxy.Proxy, error) {
	cfg := execproxy.Config{
		Patterns: patterns,
		Network:  e.exec.ProxyNetwork,
		Journal:  execproxy.StoreJournal{Store: e.store, CallID: callID},
		Check: func() error {
			var live bool
			if err := e.store.Tx(func(tx *journal.Tx) (err error) {
				live, _, err = tx.ToolStepLive(attemptID)
				return err
			}); err != nil {
				return err
			}
			if !live {
				return errToolAbandoned
			}
			return nil
		},
	}
	if cfg.Network == "" {
		cfg.Network = "tcp"
	}
	if cfg.Network == "unix" {
		cfg.Addr = filepath.Join(root, "proxy.sock")
	}
	p, err := execproxy.New(cfg)
	if err != nil {
		return nil, fmt.Errorf("engine: call proxy: %w", err)
	}
	return p, nil
}

// execPolicyHash is the hash of the policy the call ran under, as the
// manifest records it.
func (e *Engine) execPolicyHash(st *execState, path string, readOnly, allow []string, spec sandbox.Spec) (string, error) {
	enforced, _ := st.backend.Capabilities()
	names := make([]string, 0, len(spec.Env))
	for _, kv := range spec.Env {
		name, _, _ := strings.Cut(kv, "=")
		names = append(names, name)
	}
	return sandbox.Policy{
		Path:          path,
		ReadOnlyPaths: readOnly,
		Devices:       spec.System.Devices,
		EnvNames:      names,
		Capabilities:  enforced,
		NetworkAllow:  allow,
	}.Hash()
}

// environ is the environment env_passthrough names are taken from.
func (e *Engine) environ() map[string]string {
	src := e.exec.Environ
	if src == nil {
		src = os.Environ()
	}
	m := make(map[string]string, len(src))
	for _, kv := range src {
		if name, value, ok := strings.Cut(kv, "="); ok {
			m[name] = value
		}
	}
	return m
}
