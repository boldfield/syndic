package engine

import (
	"github.com/boldfield/syndic/internal/config"
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
