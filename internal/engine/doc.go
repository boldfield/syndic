// Package engine is the review engine: it starts a run, answers status,
// accepts submissions and carries a turn through the state machine of
// docs/design/v4-delta-engine-tui.md section 3.
//
// Every journal write goes through hub.Hub.Tx, with each state change and the
// events announcing it in one transaction. One mutex owns the in-memory turn
// state; adapter calls happen outside it, on their own goroutines.
package engine
