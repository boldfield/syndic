package engine

import (
	"errors"
	"fmt"
	"log"

	"github.com/boldfield/syndic/internal/hub"
	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/wire"
)

// Names carried in the detail of a busy refusal: the engine runs one turn or
// one apply at a time (v5 delta section 6, "Exclusive access"), and the
// refusal says which is open.
const (
	busyTurn  = "turn"
	busyApply = "apply"
)

// busyRefusal is the busy refusal for an open turn or apply.
func busyRefusal(kind, turnID string) *wire.Error {
	busy := refusal(wire.ErrorCodeBusy)
	busy.TurnID = strPtr(turnID)
	busy.Detail = strPtr(kind)
	return busy
}

func notAppliable(detail string) *wire.Error {
	r := refusal(wire.ErrorCodeNotAppliable)
	r.Detail = strPtr(detail)
	return r
}

// changePaths is the wire form of a change set's per-path outcomes.
func changePaths(paths []journal.ChangeSetPath) []wire.ChangePath {
	out := make([]wire.ChangePath, len(paths))
	for i, p := range paths {
		out[i] = wire.ChangePath{Path: p.Path, Outcome: p.Outcome}
	}
	return out
}

// changeSetOf returns the turn's row and change set, or a refusal. e.mu is
// held.
func (e *Engine) changeSetOf(turnID string) (*journal.TurnRow, journal.ChangeSet, error) {
	row, err := e.store.GetTurn(turnID)
	switch {
	case errors.Is(err, journal.ErrTurnNotFound):
		r := refusal(wire.ErrorCodeNotFound)
		r.TurnID = strPtr(turnID)
		return nil, journal.ChangeSet{}, r
	case err != nil:
		return nil, journal.ChangeSet{}, fmt.Errorf("engine: get turn: %w", err)
	}
	cs, err := e.store.ChangeSetForTurn(turnID)
	switch {
	case errors.Is(err, journal.ErrNoChangeSet):
		r := notAppliable("no_change_set")
		r.TurnID = strPtr(turnID)
		return nil, journal.ChangeSet{}, r
	case err != nil:
		return nil, journal.ChangeSet{}, fmt.Errorf("engine: get change set: %w", err)
	}
	return row, cs, nil
}

// Apply writes an approved or overridden turn's change set to its workspace.
// The engine runs one turn or one apply at a time: Apply is refused busy while
// a turn or another apply is open, and Submit while an apply runs. The marker
// is set and e.mu released before any filesystem work, so a concurrent request
// sees it and is refused rather than waiting. Nothing is retried or written on
// its own; an apply on an applied change set writes nothing.
func (e *Engine) Apply(turnID string) (wire.ApplyReply, error) {
	e.mu.Lock()
	if err := e.exclusiveCheck(); err != nil {
		e.mu.Unlock()
		return wire.ApplyReply{}, err
	}
	row, cs, err := e.changeSetOf(turnID)
	if err != nil {
		e.mu.Unlock()
		return wire.ApplyReply{}, err
	}
	if row.TerminalState != StateApproved && row.TerminalState != StateOverridden {
		e.mu.Unlock()
		r := notAppliable("not_approved")
		r.TurnID = strPtr(turnID)
		return wire.ApplyReply{}, r
	}
	if cs.State == journal.ChangeSetDiscarded {
		e.mu.Unlock()
		r := notAppliable("discarded")
		r.TurnID = strPtr(turnID)
		return wire.ApplyReply{}, r
	}
	workspace, err := e.workspaceOf(row)
	if err != nil {
		e.mu.Unlock()
		return wire.ApplyReply{}, err
	}
	e.applying = turnID
	hook := e.beforeApply
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		e.applying = ""
		e.mu.Unlock()
	}()

	if hook != nil {
		hook(turnID)
	}
	res, err := e.store.ApplyChangeSet(workspace, cs.ID, row.TerminalState == StateOverridden)
	var changed *journal.BaseChangedError
	switch {
	case errors.As(err, &changed):
		r := refusal(wire.ErrorCodeBaseChanged)
		r.TurnID = strPtr(turnID)
		for _, p := range changed.Paths {
			r.Paths = append(r.Paths, p.Path)
		}
		return wire.ApplyReply{}, r
	case errors.Is(err, journal.ErrWorkspaceMissing):
		r := refusal(wire.ErrorCodeWorkspaceMissing)
		r.TurnID = strPtr(turnID)
		return wire.ApplyReply{}, r
	case errors.Is(err, journal.ErrChangeSetState):
		r := notAppliable("state")
		r.TurnID = strPtr(turnID)
		return wire.ApplyReply{}, r
	case err != nil:
		return wire.ApplyReply{}, fmt.Errorf("engine: apply: %w", err)
	}
	return wire.ApplyReply{State: res.State, Paths: changePaths(res.Paths)}, nil
}

// Discard moves an appliable, partial or conflict change set to discarded. It
// changes no path outcome, never rolls back an applied file and deletes
// nothing: the change set, its bases and diffs stay as the evidence the
// reviewers saw.
func (e *Engine) Discard(turnID string) (wire.DiscardReply, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.exclusiveCheck(); err != nil {
		return wire.DiscardReply{}, err
	}
	if e.open != nil && e.open.id == turnID {
		return wire.DiscardReply{}, busyRefusal(busyTurn, e.open.id)
	}
	_, cs, err := e.changeSetOf(turnID)
	if err != nil {
		return wire.DiscardReply{}, err
	}
	switch cs.State {
	case journal.ChangeSetAppliable, journal.ChangeSetPartial, journal.ChangeSetConflict:
	default:
		r := notAppliable(cs.State)
		r.TurnID = strPtr(turnID)
		return wire.DiscardReply{}, r
	}
	err = e.tx(func(tx *journal.Tx, _ hub.Emit) error { return tx.DiscardChangeSet(cs.ID) })
	if errors.Is(err, journal.ErrChangeSetState) {
		return wire.DiscardReply{}, notAppliable("state")
	}
	if err != nil {
		return wire.DiscardReply{}, fmt.Errorf("engine: discard: %w", err)
	}
	after, err := e.store.GetChangeSet(cs.ID)
	if err != nil {
		return wire.DiscardReply{}, fmt.Errorf("engine: discard: %w", err)
	}
	return wire.DiscardReply{State: after.State, Paths: changePaths(after.Paths)}, nil
}

// exclusiveCheck refuses a request that needs the engine to itself while a
// turn or an apply is open. e.mu is held.
func (e *Engine) exclusiveCheck() error {
	switch {
	case !e.started:
		return errors.New("engine: not started")
	case e.stopping:
		return refusal(wire.ErrorCodeStopping)
	case e.applying != "":
		return busyRefusal(busyApply, e.applying)
	case e.open != nil:
		return busyRefusal(busyTurn, e.open.id)
	}
	return nil
}

// workspaceOf returns the workspace of the turn's conversation, or the
// workspace_missing refusal when it has none. e.mu is held.
func (e *Engine) workspaceOf(row *journal.TurnRow) (string, error) {
	conv, err := e.store.GetConversation(row.ConversationID)
	if err != nil {
		return "", fmt.Errorf("engine: get conversation: %w", err)
	}
	if conv.Workspace == nil || *conv.Workspace == "" {
		r := refusal(wire.ErrorCodeWorkspaceMissing)
		r.TurnID = strPtr(row.ID)
		return "", r
	}
	return *conv.Workspace, nil
}

// recoverApplies classifies every apply a crash left unfinished. It records
// each path's outcome and the recovered state in the journal and writes
// nothing to any workspace; the human decides whether to apply again. A
// conversation whose workspace is gone or was never set is left as it is: it
// cannot be classified, and a later apply reports workspace_missing. e.mu is
// held.
func (e *Engine) recoverApplies() error {
	unfinished, err := e.store.UnfinishedApplies()
	if err != nil {
		return fmt.Errorf("list unfinished applies: %w", err)
	}
	for _, u := range unfinished {
		if u.Workspace == "" {
			continue
		}
		res, err := e.store.RecoverApply(u.Workspace, u.ChangeSetID)
		if errors.Is(err, journal.ErrWorkspaceMissing) {
			continue
		}
		if err != nil {
			return fmt.Errorf("recover apply of turn %s: %w", u.TurnID, err)
		}
		log.Printf("engine: recovered unfinished apply of turn %s: change set %s is %s", u.TurnID, u.ChangeSetID, res.State)
	}
	return nil
}
