package engine

import (
	"errors"
	"github.com/boldfield/syndic/internal/config"
	"github.com/boldfield/syndic/internal/hub"
	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/tools"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/wire"
	"os"
	"testing"
	"time"
)

// SetPayloadHook installs a hook that may replace any event payload, so a
// test can make a transaction fail after its row writes. Call it before the
// first request.
func (e *Engine) SetPayloadHook(f func(kind string, payload any) any) {
	e.payloadHook = f
}

// SetCrashHook installs a hook the terminal path consults after its Tx
// committed and before each Adapter.Cancel call. When it returns true the
// engine stops there: no call is made and nothing further is written. Call it
// before the first request.
func (e *Engine) SetCrashHook(f func() bool) {
	e.crashHook = f
}

// SetRetryDelay shortens or lengthens the wait before an unreachable
// attempt's retry for engines built afterwards, and returns a function that
// restores it.
func SetRetryDelay(d time.Duration) (restore func()) {
	old := retryDelay
	retryDelay = d
	return func() { retryDelay = old }
}

// DisableDelivery stops engines built afterwards from sending terminal
// deliveries, for tests that pin the exact calls, events and participant
// states of the turn itself. It returns a function that restores delivery.
func DisableDelivery() (restore func()) {
	old := deliveryOff
	deliveryOff = true
	return func() { deliveryOff = old }
}

// TestMain turns terminal delivery off for every test by default: a delivery
// races the assertions of tests that count calls, attempts or events after a
// turn ends. Tests of delivery turn it back on with EnableDelivery.
func TestMain(m *testing.M) {
	deliveryOff = true
	os.Exit(m.Run())
}

// EnableDelivery makes engines built afterwards send terminal deliveries. It
// returns a function that restores the previous setting.
func EnableDelivery() (restore func()) {
	old := deliveryOff
	deliveryOff = false
	return func() { deliveryOff = old }
}

// RefereeIdentity is the endpoint and model of a referee, for tests of
// start-time probing of referee identities.
type RefereeIdentity = refereeIdentity

// SetRefereeIdentities replaces the roster's referee identities for engines
// started afterwards and returns a function that restores them.
func SetRefereeIdentities(f func(*config.Roster) []RefereeIdentity) (restore func()) {
	old := refereeIdentities
	refereeIdentities = f
	return func() { refereeIdentities = old }
}

// LegacySubmissionHash is the four-member hash turns were journaled under
// before conversations.
func LegacySubmissionHash(req wire.SubmitRequest) (string, error) {
	return legacySubmissionHash(req)
}

// SetAuthorChangeLines replaces how an author result's change-set lines are
// read, so a test can give a revision a patch. It returns a function that
// restores the reader.
func SetAuthorChangeLines(f func(transport.CompleteResult) []string) (restore func()) {
	old := authorChangeLines
	authorChangeLines = f
	return func() { authorChangeLines = old }
}

// ReserveOpen runs the dispatch check of the open turn for participant id
// with the request's messages, tool definitions and output bound, and
// returns the request's estimate and the turn's charge after it.
func (e *Engine) ReserveOpen(id string, msgs []transport.Message, defs []transport.Tool, maxOut int) (est, charged int, err error) {
	e.mu.Lock()
	t := e.open
	e.mu.Unlock()
	if t == nil {
		return 0, 0, errors.New("no open turn")
	}
	err = e.tx(func(tx *journal.Tx, _ hub.Emit) error {
		var err error
		if est, err = e.reserve(tx, t, id, msgs, defs, maxOut); err != nil {
			return err
		}
		turn, err := tx.GetTurn(t.id)
		if err != nil {
			return err
		}
		rec, _, err := readBudget(turn)
		charged = rec.Charged
		return err
	})
	return est, charged, err
}

// RefereeOutcome is the decision of the referee on one web call.
type RefereeOutcome struct {
	Allow     bool
	Reason    string
	Cause     string
	AttemptID string
}

// DispatchReferee asks the referee about a web call of the open turn, as a web
// effect does with the caller's slot held. It returns the decision, or the
// error that left none.
func (e *Engine) DispatchReferee(turnID, caller, callID, name, target string) (RefereeOutcome, error) {
	out, err := e.dispatchReferee(refereeCall{turnID: turnID, caller: caller, callID: callID, name: name, target: target})
	return RefereeOutcome{Allow: out.allow, Reason: out.reason, Cause: out.cause, AttemptID: out.attemptID}, err
}

// ErrToolAbandoned is what an effect returns when its call must not run.
var ErrToolAbandoned = errToolAbandoned

// WebOffReason is why web is not offered to the callers of the endpoint.
func (e *Engine) WebOffReason(endpoint string) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.webOffReason(endpoint)
}

// RefereeAdapterCount is how many referee attempts have an adapter kept for
// cancelling their held work.
func (e *Engine) RefereeAdapterCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.refereeAdapters)
}

// DispatchCommandReferee asks the referee about a bash call of the open turn,
// as a bash effect does with the caller's slot held.
func (e *Engine) DispatchCommandReferee(turnID, caller, callID, command string) (RefereeOutcome, error) {
	out, err := e.dispatchCommandReferee(commandCall{turnID: turnID, caller: caller, callID: callID, command: command})
	return RefereeOutcome{Allow: out.allow, Reason: out.reason, Cause: out.cause, AttemptID: out.attemptID}, err
}

// SetSearchClient replaces the client a search call is built with, so a test
// can answer queries without a network. Call it before the first request.
func (e *Engine) SetSearchClient(f func(scr *tools.URLScreener, ref tools.Referee) (tools.SearchClient, error)) {
	e.searchClientFn = f
}
