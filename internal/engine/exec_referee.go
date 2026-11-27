package engine

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/boldfield/syndic/internal/hub"
	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/manifest"
	"github.com/boldfield/syndic/internal/referee"
	"github.com/boldfield/syndic/internal/tools"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/workspace"
)

// The referee of a bash call (v7 delta section 6). One decision is a loop of
// referee attempts with read steps between them. Each attempt is an ordinary
// accounted dispatch of the calling participant, as a web decision's is; the
// reads between attempts are executed by the engine, charged nothing, and
// journaled as referee_read under the decision.

const (
	defaultRefereeExecMaxOutputTokens = 256
	defaultRefereeMaxReads            = 6

	// Codes of the tool errors a referee's call is answered with when it fails
	// a check before it runs.
	refereeErrNotOffered = "not_offered"
	refereeErrReadLimit  = "read_limit"

	// refereeMustAnswer is sent, with no tool definitions, once the referee has
	// no reads left.
	refereeMustAnswer = "No further reads are available for this decision: the bound on reads has been reached. You must now answer with exactly one line, allow or deny followed by a reason."
)

// refereeExecMaxOutput is tools.referee.exec_max_output_tokens, 256 when unset.
func (e *Engine) refereeExecMaxOutput() int {
	if e.roster != nil && e.roster.Tools != nil && e.roster.Tools.Referee != nil && e.roster.Tools.Referee.ExecMaxOutputTokens != nil {
		return *e.roster.Tools.Referee.ExecMaxOutputTokens
	}
	return defaultRefereeExecMaxOutputTokens
}

// refereeMaxReads is tools.referee.max_reads, 6 when unset.
func (e *Engine) refereeMaxReads() int {
	if e.roster != nil && e.roster.Tools != nil && e.roster.Tools.Referee != nil && e.roster.Tools.Referee.MaxReads != nil {
		return *e.roster.Tools.Referee.MaxReads
	}
	return defaultRefereeMaxReads
}

// commandCall is one bash call to be refereed. callID is the journal's id of
// the accepted call.
type commandCall struct {
	turnID  string
	caller  string
	callID  string
	command string
}

// refereeRead is one read of a decision, kept until the decision is recorded.
type refereeRead struct {
	attemptID string
	name      string
	args      []byte
	result    []byte
	failed    bool
}

// commandRefereeCause maps the causes a web decision names for itself onto the
// causes the journal records for a command decision: a late result is a
// timeout, a result that failed the identity or completion checks is
// malformed, and a held pool, where nothing was dispatched, is an adapter
// error.
func commandRefereeCause(cause string) string {
	switch cause {
	case refereeCauseLate:
		return journal.RefereeCauseTimeout
	case refereeCauseGate:
		return journal.RefereeCauseMalformed
	case refereeCausePoolHeld:
		return journal.RefereeCauseAdapterError
	}
	return cause
}

// recordCommandDecision journals the decision of a bash call with the reads
// the referee made, in the caller's transaction.
func recordCommandDecision(tx *journal.Tx, c commandCall, out refereeOutcome, reads []refereeRead) error {
	verdict, cause := journal.RefereeDeny, commandRefereeCause(out.cause)
	if out.allow {
		verdict, cause = journal.RefereeAllow, ""
	}
	d, err := tx.RecordRefereeDecision(c.callID, verdict, out.reason, cause)
	if err != nil {
		return err
	}
	for _, r := range reads {
		if _, err := tx.RecordRefereeRead(d.ID, r.attemptID, r.name, r.args, r.result, r.failed); err != nil {
			return err
		}
	}
	return nil
}

// dispatchCommandReferee decides one bash call that passed the deterministic
// checks and has its scratch built, and returns the decision. It runs with the
// caller's pool slot held and e.mu not held, as dispatchReferee does, and the
// decision is recorded with its reads. Only an allow from an accepted,
// identity- and completion-checked, call-free result of the current epoch
// authorizes the command. A reservation that does not fit ends the turn
// budget and returns an error that is errToolAbandoned, with no decision. Any
// other error leaves no decision and the command must not run.
func (e *Engine) dispatchCommandReferee(c commandCall) (refereeOutcome, error) {
	e.mu.Lock()
	t := e.open
	if t == nil || t.id != c.turnID || e.turnExpired(t) {
		e.mu.Unlock()
		return refereeOutcome{}, errToolAbandoned
	}
	endpoint := e.participantConfig(c.caller).Endpoint
	ref, run, ok := e.refereeOf(endpoint)
	if !ok || e.webOffReason(endpoint) != "" {
		e.mu.Unlock()
		return refereeOutcome{}, errRefereeUnavailable
	}
	if e.poolHeld(e.poolOf(c.caller)) {
		out := refereeOutcome{reason: "capacity is held", cause: refereeCausePoolHeld}
		err := e.tx(func(tx *journal.Tx, _ hub.Emit) error { return recordCommandDecision(tx, c, out, nil) })
		e.mu.Unlock()
		return out, err
	}
	adapter := e.adapters[manifest.RefereeLabel(ref.RunEndpoint, ref.Model)]
	prior, err := e.priorCommands(t.id, c)
	if err != nil {
		e.mu.Unlock()
		return refereeOutcome{}, err
	}
	var patterns []string
	if e.roster != nil && e.roster.Tools != nil {
		patterns = e.roster.Tools.ProtectedPaths
	}
	maxReads := e.refereeMaxReads()
	readDefs, err := tools.ForGroups([]string{tools.GroupRead}, false)
	if err != nil {
		e.mu.Unlock()
		return refereeOutcome{}, err
	}
	workspacePath := t.workspace
	resultBytes := e.maxResultBytes()
	e.mu.Unlock()
	if workspacePath == "" {
		return refereeOutcome{}, errors.New("engine: a command decision needs the conversation's workspace")
	}
	fs, err := workspace.OpenConfined(workspacePath, patterns)
	if err != nil {
		return refereeOutcome{}, err
	}
	defer func() { _ = fs.Close() }()
	reader, err := tools.NewRefereeReader(e.store, fs, t.id, resultBytes)
	if err != nil {
		return refereeOutcome{}, err
	}

	msgs := referee.CommandPromptMessages(referee.CommandPayload{
		UserInput:  t.input,
		CallName:   tools.NameBash,
		Command:    c.command,
		PriorCalls: prior,
	})
	defs := readDefs
	var reads []refereeRead
	record := func(tx *journal.Tx, _ hub.Emit, out refereeOutcome) error {
		return recordCommandDecision(tx, c, out, reads)
	}
	for {
		e.mu.Lock()
		if e.open != t || e.turnExpired(t) {
			e.mu.Unlock()
			return refereeOutcome{}, errToolAbandoned
		}
		req, attemptID, err := e.beginRefereeWith(t, c.caller, ref, run, msgs, defs, e.refereeExecMaxOutput())
		if err != nil {
			defer e.mu.Unlock()
			if r, ok := asRefusal(err); ok {
				e.finishBudget(t, r)
				return refereeOutcome{}, &refereeBudgetEnded{refusal: r}
			}
			return refereeOutcome{}, err
		}
		e.refereeAdapters[attemptID] = adapter
		e.mu.Unlock()

		ours := func() bool {
			e.mu.Lock()
			defer e.mu.Unlock()
			return e.cancelled[attemptID]
		}
		res, fail := callOnce(adapter, req, ours)
		e.returned(attemptID)

		e.mu.Lock()
		out, err := e.completeRefereeWith(t, c.caller, ref, req, attemptID, res, fail, record)
		if err == nil {
			delete(e.refereeAdapters, attemptID)
		}
		e.mu.Unlock()
		if err != nil || !out.step {
			return out, err
		}

		results, newReads, err := e.runRefereeReads(reader, readDefs, attemptID, out.calls, len(reads), maxReads)
		if err != nil {
			return refereeOutcome{}, err
		}
		reads = append(reads, newReads...)
		e.mu.Lock()
		next, _, err := e.toolStepMessages(req, transport.CompleteResult{Text: out.text, ToolCalls: out.calls}, toolStepOutcome{results: results}, false)
		e.mu.Unlock()
		if err != nil {
			return refereeOutcome{}, err
		}
		msgs, defs = next, readDefs
		if len(reads) >= maxReads {
			msgs = append(msgs, transport.Message{Role: "user", Content: refereeMustAnswer})
			defs = nil
		}
	}
}

// runRefereeReads answers the calls of a read step in order. A call is checked
// for the tool being one of the three read tools, its arguments validating
// against the tool's schema, and a read remaining under maxReads, having
// already made used; one that fails is answered with a tool error and does not
// count (intended: the decision as a whole has the turn deadline), and the others still run. A read that returns a tool error counts. It
// returns an outcome for every call and the reads that ran.
func (e *Engine) runRefereeReads(reader *tools.RefereeReader, defs []transport.Tool, attemptID string, calls []transport.ToolCall, used, maxReads int) ([]toolCallOutcome, []refereeRead, error) {
	schemas := make(map[string]map[string]interface{}, len(defs))
	for _, d := range defs {
		schemas[d.Function.Name] = d.Function.Parameters
	}
	var results []toolCallOutcome
	var reads []refereeRead
	for _, call := range calls {
		out := toolCallOutcome{call: journal.ToolCall{Name: call.Function.Name, ProviderCallID: call.ID}, status: journal.ToolCallError}
		schema, offered := schemas[call.Function.Name]
		var args json.RawMessage
		var argsErr error
		if offered {
			args, argsErr = validateToolArgs(schema, call.Function.Arguments)
		}
		switch {
		case call.Type != "function" || !offered:
			out.errorCode, out.content = refereeErrNotOffered, []byte(fmt.Sprintf("%q is not a tool available here", call.Function.Name))
		case argsErr != nil:
			out.errorCode, out.content = tools.ErrCodeBadArguments, []byte("invalid arguments: "+argsErr.Error())
		case used+len(reads) >= maxReads:
			out.errorCode, out.content = refereeErrReadLimit, []byte("no reads remain for this decision")
		default:
			res, err := reader.Read(call.Function.Name, args)
			if err != nil {
				return nil, nil, err
			}
			out.content, out.errorCode = res.Content, res.ErrorCode
			if res.ErrorCode == "" {
				out.status = journal.ToolCallExecuted
			}
			reads = append(reads, refereeRead{attemptID: attemptID, name: call.Function.Name, args: args, result: res.Content, failed: res.ErrorCode != ""})
		}
		results = append(results, out)
	}
	return results, reads, nil
}

// priorCommands are the caller's own bash calls of the turn that precede the
// call, as command strings only. e.mu is held.
func (e *Engine) priorCommands(turnID string, c commandCall) ([]string, error) {
	var prior []string
	err := e.tx(func(tx *journal.Tx, _ hub.Emit) error {
		recorded, err := tx.ToolCallsByTurn(turnID, c.caller)
		if err != nil {
			return err
		}
		for _, call := range recorded {
			if call.ID == c.callID {
				return nil
			}
			if call.Class != journal.ToolClassExec || call.Name != tools.NameBash {
				continue
			}
			args, err := tx.Content(call.ArgsSHA256)
			if err != nil {
				return err
			}
			var parsed struct {
				Command string `json:"command"`
			}
			if json.Unmarshal(args, &parsed) == nil {
				prior = append(prior, parsed.Command)
			}
		}
		return nil
	})
	return prior, err
}
