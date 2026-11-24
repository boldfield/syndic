package tui

import (
	"encoding/json"
	"testing"

	"github.com/boldfield/syndic/internal/wire"
)

func TestShouldNotifyOffByDefault(t *testing.T) {
	a := &App{notify: false}
	frame := terminalEventFrame(1, 10)

	bell, notice, _, _, _ := a.shouldNotify(frame)
	if bell || notice {
		t.Errorf("expected no bell/notice when notify is off")
	}
}

func TestShouldNotifyIgnoresNonTerminalEvents(t *testing.T) {
	a := &App{notify: true, liveFromSeq: 10}
	frame := wire.Frame{
		Kind: wire.EventKindTurnSubmitted,
		Seq:  15,
	}

	bell, notice, _, _, _ := a.shouldNotify(frame)
	if bell || notice {
		t.Errorf("expected no bell/notice for non-terminal events")
	}
}

func TestShouldNotifyBellForLiveTerminalEvent(t *testing.T) {
	a := &App{notify: true, liveFromSeq: 10, lastProcessedSeq: 0}
	frame := terminalEventFrame(1, 15) // seq 15 >= liveFromSeq 10

	bell, notice, turnID, outcome, _ := a.shouldNotify(frame)
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

func TestShouldNotifyNoticeForReplayTerminalEvent(t *testing.T) {
	a := &App{notify: true, liveFromSeq: 20, lastProcessedSeq: 0}
	frame := terminalEventFrame(1, 15) // seq 15 < liveFromSeq 20

	bell, notice, _, _, _ := a.shouldNotify(frame)
	if bell || !notice {
		t.Errorf("expected no bell but notice for replay terminal event")
	}
	if a.lastProcessedSeq != 15 {
		t.Errorf("expected lastProcessedSeq=15, got %d", a.lastProcessedSeq)
	}
}

func TestShouldNotifyDeduplicatesBells(t *testing.T) {
	a := &App{notify: true, liveFromSeq: 10, lastProcessedSeq: 15}
	frame := terminalEventFrame(1, 15) // same seq, already processed

	bell, notice, _, _, _ := a.shouldNotify(frame)
	if bell || notice {
		t.Errorf("expected no bell/notice for already-processed event")
	}
}

func TestShouldNotifyDeduplicatesNotices(t *testing.T) {
	a := &App{notify: true, liveFromSeq: 20, lastProcessedSeq: 15}
	frame := terminalEventFrame(1, 15) // same seq, already processed

	bell, notice, _, _, _ := a.shouldNotify(frame)
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

	bell, notice, _, _, _ := a.shouldNotify(frame)
	if !bell {
		t.Errorf("expected bell for event at live_from_seq boundary")
	}
}

func TestReplayAfterEmptyReplay(t *testing.T) {
	a := &App{notify: true, liveFromSeq: 10, lastProcessedSeq: 0}

	// Empty replay (no events before live_from_seq)
	// Then a live event arrives
	frame := terminalEventFrame(1, 10)

	bell, notice, _, _, _ := a.shouldNotify(frame)
	if !bell {
		t.Errorf("expected bell after empty replay for first live terminal event")
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
