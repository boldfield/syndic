package journal

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Deliberate mode records (v8 delta sections 2, 5 and 7). As for the tool
// records, every method runs inside the caller's transaction. The text of a
// proposal, critique or disposition is stored in tool_content under its
// SHA-256. Nothing here is written for a turn that is not deliberate.

// ErrDeliberationItemRecorded is returned for a second proposal or critique
// from one participant in a turn, or a second disposition for one candidate.
var ErrDeliberationItemRecorded = errors.New("deliberation item already recorded")

// Deliberate mode and its phases.
const (
	ModeDeliberate = "deliberate"

	PhasePropose   = "propose"
	PhaseCritique  = "critique"
	PhaseIntegrate = "integrate"
	PhaseReview    = "review"
)

// Kinds of deliberation item.
const (
	DeliberationProposal    = "proposal"
	DeliberationCritique    = "critique"
	DeliberationDisposition = "disposition"
)

// Events of a deliberate turn, each carrying the turn id.
const (
	EventProposalsDispatched   = "proposals_dispatched"
	EventProposalsComplete     = "proposals_complete"
	EventCritiquesDispatched   = "critiques_dispatched"
	EventCritiquesComplete     = "critiques_complete"
	EventIntegrationDispatched = "integration_dispatched"
)

var deliberationEvents = map[string]bool{
	EventProposalsDispatched:   true,
	EventProposalsComplete:     true,
	EventCritiquesDispatched:   true,
	EventCritiquesComplete:     true,
	EventIntegrationDispatched: true,
}

// DeliberationItemInput is one proposal, critique or disposition to record.
// RosterPosition is the participant's place in the roster. Label is the
// numbered label peers saw and is empty for a disposition.
type DeliberationItemInput struct {
	TurnID         string
	Participant    string
	RosterPosition int
	AttemptID      string
	Label          string
	Content        []byte
}

// DeliberationItem is a recorded proposal, critique or disposition. Label is
// empty for a disposition; Round and CandidateID are set for a disposition
// only.
type DeliberationItem struct {
	ID             int64
	TurnID         string
	Kind           string
	Participant    string
	RosterPosition int
	AttemptID      string
	Label          string
	ContentSHA256  string
	ContentByteLen int64
	Round          int
	CandidateID    string
	RecordedAt     time.Time
}

// Phase returns the phase a kind of item belongs to in listing order.
func (i DeliberationItem) Phase() string {
	switch i.Kind {
	case DeliberationProposal:
		return PhasePropose
	case DeliberationCritique:
		return PhaseCritique
	}
	return PhaseIntegrate
}

// EmitDeliberationEvent journals one of the five deliberate-turn events for
// the turn, in the caller's transaction.
func (tx *Tx) EmitDeliberationEvent(turnID, kind string) error {
	if !deliberationEvents[kind] {
		return fmt.Errorf("%q is not a deliberation event", kind)
	}
	if turnID == "" {
		return fmt.Errorf("deliberation event %s needs a turn id", kind)
	}
	return tx.emitTools(turnID, kind, map[string]any{"turn_id": turnID})
}

// SetDeliberationPhase marks the turn deliberate and sets its current phase.
func (tx *Tx) SetDeliberationPhase(turnID, phase string) error {
	switch phase {
	case PhasePropose, PhaseCritique, PhaseIntegrate, PhaseReview:
	default:
		return fmt.Errorf("unknown deliberation phase %q", phase)
	}
	res, err := tx.tx.Exec(`UPDATE turns SET deliberation_mode = ?, deliberation_phase = ? WHERE id = ?`,
		ModeDeliberate, phase, turnID)
	if err != nil {
		return fmt.Errorf("set deliberation phase: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return fmt.Errorf("turn %q not found", turnID)
	}
	return nil
}

// DeliberationPhase returns the turn's mode and phase, both empty for a turn
// that is not deliberate.
func (tx *Tx) DeliberationPhase(turnID string) (mode, phase string, err error) {
	var m, p sql.NullString
	err = tx.tx.QueryRow(`SELECT deliberation_mode, deliberation_phase FROM turns WHERE id = ?`, turnID).Scan(&m, &p)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", fmt.Errorf("turn %q not found", turnID)
	}
	if err != nil {
		return "", "", fmt.Errorf("query deliberation phase: %w", err)
	}
	return m.String, p.String, nil
}

// SetCandidateDeliberationHash records the hash of the deliberation a
// candidate binds. It may be set once.
func (tx *Tx) SetCandidateDeliberationHash(candidateID, hash string) error {
	if hash == "" {
		return fmt.Errorf("deliberation hash must not be empty")
	}
	res, err := tx.tx.Exec(`UPDATE candidates SET deliberation_hash = ? WHERE id = ? AND deliberation_hash IS NULL`, hash, candidateID)
	if err != nil {
		return fmt.Errorf("set deliberation hash: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return fmt.Errorf("candidate %q not found or its deliberation hash is set", candidateID)
	}
	return nil
}

// CandidateDeliberationHash returns the candidate's deliberation hash, empty
// outside deliberate mode.
func (tx *Tx) CandidateDeliberationHash(candidateID string) (string, error) {
	var h sql.NullString
	err := tx.tx.QueryRow(`SELECT deliberation_hash FROM candidates WHERE id = ?`, candidateID).Scan(&h)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("candidate %q not found", candidateID)
	}
	if err != nil {
		return "", fmt.Errorf("query deliberation hash: %w", err)
	}
	return h.String, nil
}

// RecordProposal records a participant's proposal. A second one from the same
// participant in the turn is refused with ErrDeliberationItemRecorded.
func (tx *Tx) RecordProposal(in DeliberationItemInput) (DeliberationItem, error) {
	return tx.recordDeliberationItem(DeliberationProposal, in, 0, "")
}

// RecordCritique records a participant's critique, as RecordProposal does.
func (tx *Tx) RecordCritique(in DeliberationItemInput) (DeliberationItem, error) {
	return tx.recordDeliberationItem(DeliberationCritique, in, 0, "")
}

// RecordDisposition records the integrator's disposition for one candidate in
// the given round (from 1). Its label is empty. A second disposition for the
// candidate is refused with ErrDeliberationItemRecorded.
func (tx *Tx) RecordDisposition(in DeliberationItemInput, round int, candidateID string) (DeliberationItem, error) {
	if round < 1 {
		return DeliberationItem{}, fmt.Errorf("disposition round must be at least 1, got %d", round)
	}
	if candidateID == "" {
		return DeliberationItem{}, fmt.Errorf("a disposition needs its candidate")
	}
	return tx.recordDeliberationItem(DeliberationDisposition, in, round, candidateID)
}

func (tx *Tx) recordDeliberationItem(kind string, in DeliberationItemInput, round int, candidateID string) (DeliberationItem, error) {
	if in.Participant == "" || in.RosterPosition < 0 {
		return DeliberationItem{}, fmt.Errorf("a deliberation item needs a participant and a roster position")
	}
	if kind == DeliberationDisposition && in.Label != "" {
		return DeliberationItem{}, fmt.Errorf("a disposition has no label")
	}
	if kind != DeliberationDisposition && in.Label == "" {
		return DeliberationItem{}, fmt.Errorf("a %s needs a label", kind)
	}
	var attemptTurn string
	err := tx.tx.QueryRow(`SELECT turn_id FROM attempts WHERE id = ?`, in.AttemptID).Scan(&attemptTurn)
	if errors.Is(err, sql.ErrNoRows) {
		return DeliberationItem{}, fmt.Errorf("attempt %q not found", in.AttemptID)
	}
	if err != nil {
		return DeliberationItem{}, fmt.Errorf("query attempt: %w", err)
	}
	if attemptTurn != in.TurnID {
		return DeliberationItem{}, fmt.Errorf("attempt %q belongs to turn %q, not %q", in.AttemptID, attemptTurn, in.TurnID)
	}
	var exists bool
	if kind == DeliberationDisposition {
		var candTurn string
		err := tx.tx.QueryRow(`SELECT turn_id FROM candidates WHERE id = ?`, candidateID).Scan(&candTurn)
		if errors.Is(err, sql.ErrNoRows) {
			return DeliberationItem{}, fmt.Errorf("candidate %q not found", candidateID)
		}
		if err != nil {
			return DeliberationItem{}, fmt.Errorf("query candidate: %w", err)
		}
		if candTurn != in.TurnID {
			return DeliberationItem{}, fmt.Errorf("candidate %q belongs to turn %q, not %q", candidateID, candTurn, in.TurnID)
		}
		err = tx.tx.QueryRow(`SELECT EXISTS (SELECT 1 FROM deliberation_items WHERE kind = 'disposition' AND candidate_id = ?)`, candidateID).Scan(&exists)
		if err != nil {
			return DeliberationItem{}, fmt.Errorf("query disposition: %w", err)
		}
		if exists {
			return DeliberationItem{}, fmt.Errorf("candidate %q: %w", candidateID, ErrDeliberationItemRecorded)
		}
	} else {
		err := tx.tx.QueryRow(`SELECT EXISTS (SELECT 1 FROM deliberation_items WHERE turn_id = ? AND participant = ? AND kind = ?)`,
			in.TurnID, in.Participant, kind).Scan(&exists)
		if err != nil {
			return DeliberationItem{}, fmt.Errorf("query %s: %w", kind, err)
		}
		if exists {
			return DeliberationItem{}, fmt.Errorf("%s from %q in turn %q: %w", kind, in.Participant, in.TurnID, ErrDeliberationItemRecorded)
		}
	}
	sha, err := tx.StoreContent(in.Content)
	if err != nil {
		return DeliberationItem{}, err
	}
	var label, cand any
	var rnd any
	if kind != DeliberationDisposition {
		label = in.Label
	} else {
		rnd, cand = round, candidateID
	}
	at := nowStamp()
	res, err := tx.tx.Exec(`
		INSERT INTO deliberation_items (turn_id, kind, participant, roster_position, attempt_id, label,
			content_sha256, content_byte_len, round, candidate_id, recorded_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, in.TurnID, kind, in.Participant, in.RosterPosition, in.AttemptID, label, sha, len(in.Content), rnd, cand, at)
	if err != nil {
		return DeliberationItem{}, fmt.Errorf("insert %s: %w", kind, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return DeliberationItem{}, fmt.Errorf("insert %s: %w", kind, err)
	}
	out := DeliberationItem{
		ID: id, TurnID: in.TurnID, Kind: kind, Participant: in.Participant, RosterPosition: in.RosterPosition,
		AttemptID: in.AttemptID, Label: in.Label, ContentSHA256: sha, ContentByteLen: int64(len(in.Content)),
		Round: round, CandidateID: candidateID,
	}
	out.RecordedAt, _ = parseTime(at)
	return out, nil
}

// DeliberationItems lists a turn's items ordered by phase (proposals, then
// critiques, then dispositions), then roster position, with dispositions in
// round order. cursor is empty for the first page, or the next cursor the
// previous page returned; the returned next cursor is empty on the last page.
// A page holds at most limit items.
func (tx *Tx) DeliberationItems(turnID, cursor string, limit int) (items []DeliberationItem, next string, err error) {
	if limit < 1 {
		return nil, "", fmt.Errorf("limit must be at least 1")
	}
	after := [4]int64{-1, -1, -1, -1}
	if cursor != "" {
		parts := strings.Split(cursor, ".")
		if len(parts) != 4 {
			return nil, "", fmt.Errorf("invalid deliberation cursor %q", cursor)
		}
		for i, p := range parts {
			if after[i], err = strconv.ParseInt(p, 10, 64); err != nil {
				return nil, "", fmt.Errorf("invalid deliberation cursor %q", cursor)
			}
		}
	}
	rows, err := tx.tx.Query(`
		SELECT id, turn_id, kind, participant, roster_position, attempt_id, label, content_sha256,
			content_byte_len, round, candidate_id, recorded_at,
			CASE kind WHEN 'proposal' THEN 0 WHEN 'critique' THEN 1 ELSE 2 END AS phase_rank
		FROM deliberation_items
		WHERE turn_id = ?
			AND (CASE kind WHEN 'proposal' THEN 0 WHEN 'critique' THEN 1 ELSE 2 END, roster_position, COALESCE(round, 0), id) > (?, ?, ?, ?)
		ORDER BY phase_rank, roster_position, COALESCE(round, 0), id
		LIMIT ?
	`, turnID, after[0], after[1], after[2], after[3], limit+1)
	if err != nil {
		return nil, "", fmt.Errorf("query deliberation items: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var ranks []int
	for rows.Next() {
		var it DeliberationItem
		var label, cand sql.NullString
		var round sql.NullInt64
		var recorded string
		var rank int
		if err := rows.Scan(&it.ID, &it.TurnID, &it.Kind, &it.Participant, &it.RosterPosition, &it.AttemptID, &label,
			&it.ContentSHA256, &it.ContentByteLen, &round, &cand, &recorded, &rank); err != nil {
			return nil, "", fmt.Errorf("scan deliberation item: %w", err)
		}
		it.Label, it.CandidateID, it.Round = label.String, cand.String, int(round.Int64)
		if it.RecordedAt, err = parseTime(recorded); err != nil {
			return nil, "", fmt.Errorf("parse timestamp: %w", err)
		}
		items = append(items, it)
		ranks = append(ranks, rank)
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("iterate deliberation items: %w", err)
	}
	if len(items) > limit {
		items, ranks = items[:limit], ranks[:limit]
		last := items[limit-1]
		next = fmt.Sprintf("%d.%d.%d.%d", ranks[limit-1], last.RosterPosition, last.Round, last.ID)
	}
	return items, next, nil
}

// DeliberationItems lists a turn's items as Tx.DeliberationItems does.
func (s *Store) DeliberationItems(turnID, cursor string, limit int) (items []DeliberationItem, next string, err error) {
	err = s.Tx(func(tx *Tx) error {
		var txErr error
		items, next, txErr = tx.DeliberationItems(turnID, cursor, limit)
		return txErr
	})
	return items, next, err
}
