package journal

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// Event represents a row from the events table with all required fields.
type Event struct {
	Seq    int64
	TurnID string // empty when the row's turn_id is null
	Kind   string
	At     time.Time
	Data   json.RawMessage
}

// Emit inserts one event row inside the transaction and returns it complete,
// including Seq and the timestamp that was stored.
func (tx *Tx) Emit(turnID, kind string, payload json.RawMessage) (Event, error) {
	// Validate JSON payload
	if !json.Valid(payload) {
		return Event{}, fmt.Errorf("invalid JSON payload")
	}

	// Convert empty turnID to nil for database null
	var turnIDPtr *string
	if turnID != "" {
		turnIDPtr = &turnID
	}

	// Generate RFC 3339 UTC timestamp with nanoseconds
	now := time.Now().UTC()
	nowStr := now.Format(time.RFC3339Nano)

	// Allocate next sequence number. The store's mu protects nextSeq,
	// and nextSeq is incremented before the transaction executes, so if the
	// transaction rolls back, the number is never reused.
	tx.store.nextSeq++
	seq := tx.store.nextSeq

	// Insert event with explicit sequence number
	_, err := tx.tx.Exec(`
		INSERT INTO events (id, turn_id, event_type, timestamp, data)
		VALUES (?, ?, ?, ?, ?)
	`, seq, turnIDPtr, kind, nowStr, string(payload))
	if err != nil {
		return Event{}, fmt.Errorf("insert event: %w", err)
	}

	return Event{
		Seq:    seq,
		TurnID: turnID,
		Kind:   kind,
		At:     now,
		Data:   payload,
	}, nil
}

// EventsSince returns events with Seq > sinceSeq in ascending Seq order,
// at most limit events. It must read rows written by the existing AppendEvent
// without error. Pure read; goes through Tx for consistency but does not modify state.
func (s *Store) EventsSince(sinceSeq int64, limit int) ([]Event, error) {
	if limit < 1 {
		return nil, fmt.Errorf("limit must be at least 1")
	}

	var events []Event
	err := s.Tx(func(tx *Tx) error {
		rows, err := tx.tx.Query(`
			SELECT id, turn_id, event_type, timestamp, data
			FROM events
			WHERE id > ?
			ORDER BY id ASC
			LIMIT ?
		`, sinceSeq, limit)
		if err != nil {
			return fmt.Errorf("query events: %w", err)
		}
		defer func() { _ = rows.Close() }()

		for rows.Next() {
			var event Event
			var turnIDPtr sql.NullString
			var timestampStr string
			var dataStr string

			if err := rows.Scan(&event.Seq, &turnIDPtr, &event.Kind, &timestampStr, &dataStr); err != nil {
				return fmt.Errorf("scan event: %w", err)
			}

			event.Data = json.RawMessage(dataStr)

			if turnIDPtr.Valid {
				event.TurnID = turnIDPtr.String
			}

			// Parse timestamp - handle both RFC3339Nano (from Emit) and SQLite default format
			t, err := parseTime(timestampStr)
			if err != nil {
				return fmt.Errorf("parse timestamp: %w", err)
			}
			event.At = t

			events = append(events, event)
		}

		if err := rows.Err(); err != nil {
			return fmt.Errorf("iterate rows: %w", err)
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	return events, nil
}

// MaxSeq returns the largest Seq, or 0 for an empty table.
// Pure read; goes through Tx for consistency but does not modify state.
func (s *Store) MaxSeq() (int64, error) {
	var maxSeq int64
	err := s.Tx(func(tx *Tx) error {
		if err := tx.tx.QueryRow(`
			SELECT COALESCE(MAX(id), 0) FROM events
		`).Scan(&maxSeq); err != nil {
			return fmt.Errorf("query max seq: %w", err)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}

	return maxSeq, nil
}

// LatestTurnStateEvent returns the most recent turn_state event for a given turn ID.
// Returns nil, nil if no such event exists.
func (s *Store) LatestTurnStateEvent(turnID string) (*Event, error) {
	var event *Event
	err := s.Tx(func(tx *Tx) error {
		var e Event
		var turnIDPtr sql.NullString
		var timestampStr string
		var dataStr string

		if err := tx.tx.QueryRow(`
			SELECT id, turn_id, event_type, timestamp, data
			FROM events
			WHERE turn_id = ? AND event_type = 'turn_state'
			ORDER BY id DESC
			LIMIT 1
		`, turnID).Scan(&e.Seq, &turnIDPtr, &e.Kind, &timestampStr, &dataStr); err != nil {
			if err == sql.ErrNoRows {
				return nil
			}
			return fmt.Errorf("query latest turn_state: %w", err)
		}

		e.Data = json.RawMessage(dataStr)
		if turnIDPtr.Valid {
			e.TurnID = turnIDPtr.String
		}

		t, err := parseTime(timestampStr)
		if err != nil {
			return fmt.Errorf("parse timestamp: %w", err)
		}
		e.At = t

		event = &e
		return nil
	})
	if err != nil {
		return nil, err
	}

	return event, nil
}
