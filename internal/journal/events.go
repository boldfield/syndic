package journal

import (
	"database/sql"
	"encoding/json"
	"errors"
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

	// Insert event and get back the auto-generated sequence number
	result, err := tx.tx.Exec(`
		INSERT INTO events (turn_id, event_type, timestamp, data)
		VALUES (?, ?, ?, ?)
	`, turnIDPtr, kind, nowStr, string(payload))
	if err != nil {
		return Event{}, fmt.Errorf("insert event: %w", err)
	}

	seq, err := result.LastInsertId()
	if err != nil {
		return Event{}, fmt.Errorf("get last insert id: %w", err)
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
// without error.
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
func (s *Store) MaxSeq() (int64, error) {
	var maxSeq int64
	err := s.Tx(func(tx *Tx) error {
		if err := tx.tx.QueryRow(`
			SELECT COALESCE(MAX(id), 0) FROM events
		`).Scan(&maxSeq); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			return fmt.Errorf("query max seq: %w", err)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}

	return maxSeq, nil
}
