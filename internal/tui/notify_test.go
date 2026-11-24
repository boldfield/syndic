package tui

import (
	"encoding/json"
	"testing"

	"github.com/boldfield/syndic/internal/wire"
)

func TestShouldNotifyOffByDefault(t *testing.T) {
	a := &App{notify: false, liveFromSeq: 10, lastProcessedSeq: 0}
	frame := terminalEventFrame(1, 15)

	bell, notice, _, _, _ := a.shouldNotify(frame, true)
	if bell || notice {
		t.Errorf("expected no bell/notice when notify is off")
	}
}

func TestShouldNotifyIgnoresNonTerminalEvents(t *testing.T) {
	a := &App{notify: true, liveFromSeq: 10, lastProcessedSeq: 0}
	frame := wire.Frame{
		Kind: wire.EventKindTurnSubmitted,
		Seq:  15,
	}

	bell, notice, _, _, _ := a.shouldNotify(frame, true)
	if bell || notice {
		t.Errorf("expected no bell/notice for non-terminal events")
	}
}

func TestShouldNotifyBellForLiveTerminalEvent(t *testing.T) {
	a := &App{notify: true, liveFromSeq: 10, lastProcessedSeq: 0}
	frame := terminalEventFrame(1, 15) // seq 15 >= liveFromSeq 10

	bell, notice, turnID, outcome, _ := a.shouldNotify(frame, true)
	if !bell || notice {
		t.Errorf("expected bell but no notice for live terminal event")
	}
	if turnID != "turn-1" {
		t.Errorf("expected turnID=turn-1, got %q", turnID)
	}
	if outcome != "approved" {
		t.Errorf("expected outcome=approved, got %q", outcome)
	}
	if a.lastProcessedSeq != 15 {
		t.Errorf("expected lastProcessedSeq=15, got %d", a.lastProcessedSeq)
	}
}

func TestShouldNotifyNoticeOnlyOnReconnect(t *testing.T) {
	a := &App{notify: false, liveFromSeq: 20, lastProcessedSeq: 0}
	frame := terminalEventFrame(1, 15) // seq 15 < liveFromSeq 20

	// On first connect, no notice
	bell, notice, _, _, _ := a.shouldNotify(frame, true)
	if bell || notice {
		t.Errorf("expected no bell/notice on first connect for replay event")
	}

	// Reset for reconnect test
	a.lastProcessedSeq = 0
	bell, notice, _, _, _ = a.shouldNotify(frame, false)
	if bell || !notice {
		t.Errorf("expected no bell but notice on reconnect for replay event")
	}
}

func TestShouldNotifyDeduplicatesBells(t *testing.T) {
	a := &App{notify: true, liveFromSeq: 10, lastProcessedSeq: 15}
	frame := terminalEventFrame(1, 15) // same seq, already processed

	bell, notice, _, _, _ := a.shouldNotify(frame, true)
	if bell || notice {
		t.Errorf("expected no bell/notice for already-processed event")
	}
}

func TestShouldNotifyDeduplicatesNotices(t *testing.T) {
	a := &App{notify: false, liveFromSeq: 20, lastProcessedSeq: 15}
	frame := terminalEventFrame(1, 15) // same seq, already processed

	bell, notice, _, _, _ := a.shouldNotify(frame, false)
	if bell || notice {
		t.Errorf("expected no bell/notice for already-processed event")
	}
}

func TestRecordMissedTerminalEvent(t *testing.T) {
	a := &App{missedTerminalEvents: make(map[string]string), missedTerminalEventsTm: make(map[string]string)}
	a.recordMissedTerminalEvent("turn-1", "approved", "2024-01-01T00:00:00Z")

	if a.missedTerminalEvents["turn-1"] != "approved" {
		t.Errorf("expected outcome=approved, got %q", a.missedTerminalEvents["turn-1"])
	}
	if a.missedTerminalEventsTm["turn-1"] != "2024-01-01T00:00:00Z" {
		t.Errorf("expected timestamp, got %q", a.missedTerminalEventsTm["turn-1"])
	}
}

func TestBoundaryEventBell(t *testing.T) {
	a := &App{notify: true, liveFromSeq: 10, lastProcessedSeq: 0}
	frame := terminalEventFrame(1, 10) // seq exactly at liveFromSeq boundary

	bell, _, _, _, _ := a.shouldNotify(frame, true)
	if !bell {
		t.Errorf("expected bell for event at live_from_seq boundary")
	}
}

func TestShouldNotifyTracksEveryFrame(t *testing.T) {
	a := &App{notify: false, liveFromSeq: 10, lastProcessedSeq: 0}

	// A non-terminal event should still update lastProcessedSeq
	frame := wire.Frame{
		Kind: wire.EventKindTurnSubmitted,
		Seq:  5,
	}
	a.shouldNotify(frame, true)
	if a.lastProcessedSeq != 5 {
		t.Errorf("expected lastProcessedSeq=5 after non-terminal event, got %d", a.lastProcessedSeq)
	}
}

func TestBellEmitterInjection(t *testing.T) {
	bellCount := 0
	emit := func() { bellCount++ }
	a := &App{
		notify:      true,
		liveFromSeq: 10,
		lastProcessedSeq: 0,
		emitBell:    emit,
	}

	frame := terminalEventFrame(1, 15) // live terminal event
	bell, _, _, _, _ := a.shouldNotify(frame, true)
	if !bell {
		t.Errorf("expected bell")
	}

	cmd := a.bellCmd()
	if cmd == nil {
		t.Errorf("expected bellCmd to return a command")
	}

	// Execute the command
	msg := cmd()
	if msg == nil {
		t.Errorf("expected bellCmd to execute and return a message")
	}
	if bellCount != 1 {
		t.Errorf("expected bell emitter to be called once, got %d", bellCount)
	}
}

func TestNilBellEmitterReturnsNilCmd(t *testing.T) {
	a := &App{
		notify:           true,
		liveFromSeq:      10,
		lastProcessedSeq: 0,
		emitBell:         nil,
	}

	cmd := a.bellCmd()
	if cmd != nil {
		t.Errorf("expected bellCmd to return nil when emitBell is nil")
	}
}

func terminalEventFrame(turnNum, seq int64) wire.Frame {
	body := wire.EvTurnState{
		TurnID: "turn-1",
		State:  "approved",
	}
	bodyBytes, _ := json.Marshal(body)
	return wire.Frame{
		Kind: wire.EventKindTurnState,
		Seq:  seq,
		At:   "2024-01-01T00:00:00Z",
		Body: bodyBytes,
	}
}
