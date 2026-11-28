package journal

import (
	"encoding/json"
	"fmt"
	"strings"
)

// TurnEventsOfKinds returns the turn's journal events whose kind is one of
// kinds, with Seq > afterSeq, in ascending Seq order, at most limit of them.
// It is the ordered record of a turn's tool, referee, evidence and change-set
// state: every event is journaled in the transaction of the state it names,
// so reading them in order reconstructs that state.
func (s *Store) TurnEventsOfKinds(turnID string, kinds []string, afterSeq int64, limit int) ([]Event, error) {
	if limit < 1 {
		return nil, fmt.Errorf("limit must be at least 1")
	}
	if len(kinds) == 0 {
		return nil, nil
	}
	args := []any{turnID, afterSeq}
	for _, k := range kinds {
		args = append(args, k)
	}
	args = append(args, limit)
	query := `SELECT id, event_type, timestamp, data FROM events
		WHERE turn_id = ? AND id > ? AND event_type IN (?` + strings.Repeat(",?", len(kinds)-1) + `)
		ORDER BY id ASC LIMIT ?`
	var out []Event
	err := s.Tx(func(tx *Tx) error {
		rows, err := tx.tx.Query(query, args...)
		if err != nil {
			return fmt.Errorf("query turn events: %w", err)
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			ev := Event{TurnID: turnID}
			var ts, data string
			if err := rows.Scan(&ev.Seq, &ev.Kind, &ts, &data); err != nil {
				return fmt.Errorf("scan turn event: %w", err)
			}
			if ev.At, err = parseTime(ts); err != nil {
				return fmt.Errorf("parse timestamp: %w", err)
			}
			ev.Data = json.RawMessage(data)
			out = append(out, ev)
		}
		return rows.Err()
	})
	return out, err
}
