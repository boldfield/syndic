package journal

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Effect records (v9 delta section 5). Every method runs inside the caller's
// transaction and emits its journal event in that same transaction. The
// set's state is never stored: DeriveEffectSetState computes it from the
// effect rows and the discard record.

// Effect states.
const (
	EffectPending     = "pending"
	EffectSkipped     = "skipped"
	EffectDispatching = "dispatching"
	EffectDone        = "done"
	EffectFailed      = "failed"
	EffectUncertain   = "uncertain"
)

// Effect set states, in the order the design's rules are tried.
const (
	EffectSetInFlight  = "in flight"
	EffectSetDiscarded = "discarded"
	EffectSetApplied   = "applied"
	EffectSetStopped   = "stopped"
	EffectSetClosed    = "closed"
	EffectSetAppliable = "appliable"
	EffectSetResumable = "resumable"
)

// ErrEffectNotEligible is returned when an effect cannot be moved to
// dispatching: it is not the one eligible effect, or the set is final.
var ErrEffectNotEligible = errors.New("effect is not eligible for dispatch")

// EffectInput is one effect as staged: the server and its identity, the tool
// and its pin, and the canonical arguments.
type EffectInput struct {
	Server         string
	ServerIdentity string
	Tool           string
	PinSHA256      string
	Args           []byte
}

// Effect is one effect row. SkippedRule is set in the skipped state only.
// ReplySHA256 is set for done and failed, and may be set for uncertain.
// AckState is empty until the human acknowledges the effect, then equals the
// state acknowledged.
type Effect struct {
	ID             string
	TurnID         string
	Ordinal        int
	Server         string
	ServerIdentity string
	Tool           string
	PinSHA256      string
	ArgsSHA256     string
	State          string
	SkippedRule    string
	ReplySHA256    string
	ReplyByteLen   int64
	AckState       string
	AckedAt        time.Time
}

// Acknowledged reports whether the human has acknowledged the effect.
func (e Effect) Acknowledged() bool { return e.AckState != "" }

// EffectSet is a turn's effects in order, with the canary set stored with
// them and the discard record.
type EffectSet struct {
	TurnID    string
	Canary    []byte
	Effects   []Effect
	Discarded bool
}

// State is the set's derived state.
func (s EffectSet) State() string {
	st, _ := DeriveEffectSetState(s.Effects, s.Discarded)
	return st
}

// Eligible returns the one effect eligible for dispatch, if any.
func (s EffectSet) Eligible() (Effect, bool) {
	_, i := DeriveEffectSetState(s.Effects, s.Discarded)
	if i < 0 {
		return Effect{}, false
	}
	return s.Effects[i], true
}

// EffectSetFinal reports whether a set state is final. A set in flight is
// never final.
func EffectSetFinal(state string) bool {
	return state == EffectSetApplied || state == EffectSetClosed || state == EffectSetDiscarded
}

// DeriveEffectSetState derives a set's state by the first applicable of the
// design's seven rules, from its effects in order and whether it was
// discarded. It also returns the index of the one eligible effect, or -1.
// An effect is eligible when it is pending, or skipped and not acknowledged,
// every effect before it is done or acknowledged, and the set is neither in
// flight nor final.
func DeriveEffectSetState(effects []Effect, discarded bool) (state string, eligible int) {
	var dispatching, allDone, stopping, pending, anyDoneOrAck bool
	allDone = true
	for _, e := range effects {
		if e.State != EffectDone {
			allDone = false
		}
		switch e.State {
		case EffectDispatching:
			dispatching = true
		case EffectPending:
			pending = true
		case EffectSkipped, EffectFailed, EffectUncertain:
			if !e.Acknowledged() {
				stopping = true
			}
		}
		if e.State == EffectDone || e.Acknowledged() {
			anyDoneOrAck = true
		}
	}
	switch {
	case dispatching:
		return EffectSetInFlight, -1
	case discarded:
		return EffectSetDiscarded, -1
	case allDone:
		return EffectSetApplied, -1
	}
	eligible = eligibleEffect(effects)
	switch {
	case stopping:
		return EffectSetStopped, eligible
	case !pending:
		return EffectSetClosed, -1
	case !anyDoneOrAck:
		return EffectSetAppliable, eligible
	default:
		return EffectSetResumable, eligible
	}
}

// eligibleEffect returns the index of the first effect that is not done or
// acknowledged, if it is pending or an unacknowledged skipped one; else -1.
func eligibleEffect(effects []Effect) int {
	for i, e := range effects {
		if e.State == EffectDone || e.Acknowledged() {
			continue
		}
		if e.State == EffectPending || e.State == EffectSkipped {
			return i
		}
		return -1
	}
	return -1
}

// CreateEffectSet stages a turn's effects in order, with the canary set
// stored with them. A turn has at most one set and a set at least one
// effect. Arguments are stored by hash. It emits effect_set_created.
func (tx *Tx) CreateEffectSet(turnID string, canary []byte, in []EffectInput) (EffectSet, error) {
	if len(in) == 0 {
		return EffectSet{}, fmt.Errorf("an effect set needs at least one effect")
	}
	var n int
	if err := tx.tx.QueryRow(`SELECT COUNT(*) FROM turns WHERE id = ?`, turnID).Scan(&n); err != nil {
		return EffectSet{}, fmt.Errorf("query turn: %w", err)
	}
	if n == 0 {
		return EffectSet{}, fmt.Errorf("turn %q not found", turnID)
	}
	if err := tx.tx.QueryRow(`SELECT COUNT(*) FROM effect_sets WHERE turn_id = ?`, turnID).Scan(&n); err != nil {
		return EffectSet{}, fmt.Errorf("query effect set: %w", err)
	}
	if n > 0 {
		return EffectSet{}, fmt.Errorf("turn %q already has an effect set", turnID)
	}
	at := nowStamp()
	if _, err := tx.tx.Exec(`INSERT INTO effect_sets (turn_id, canary_set, recorded_at) VALUES (?, ?, ?)`,
		turnID, string(canary), at); err != nil {
		return EffectSet{}, fmt.Errorf("insert effect set: %w", err)
	}
	ids := make([]string, 0, len(in))
	for i, e := range in {
		if e.Server == "" || e.Tool == "" || e.ServerIdentity == "" || e.PinSHA256 == "" {
			return EffectSet{}, fmt.Errorf("effect %d needs a server, a tool, a server identity and a pin", i)
		}
		argsSHA, err := tx.StoreContent(e.Args)
		if err != nil {
			return EffectSet{}, err
		}
		id, err := generateID()
		if err != nil {
			return EffectSet{}, err
		}
		if _, err := tx.tx.Exec(`
			INSERT INTO effects (id, turn_id, ordinal, server, server_identity, tool, pin_sha256, args_sha256, state, recorded_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, id, turnID, i, e.Server, e.ServerIdentity, e.Tool, e.PinSHA256, argsSHA, EffectPending, at); err != nil {
			return EffectSet{}, fmt.Errorf("insert effect: %w", err)
		}
		ids = append(ids, id)
	}
	if err := tx.emitTools(turnID, "effect_set_created", map[string]any{"effect_ids": ids}); err != nil {
		return EffectSet{}, err
	}
	return tx.EffectSet(turnID)
}

const effectSelect = `
	SELECT id, turn_id, ordinal, server, server_identity, tool, pin_sha256, args_sha256, state,
		skipped_rule, reply_sha256, reply_byte_len, ack_state, acked_at
	FROM effects`

func scanEffect(sc interface{ Scan(...any) error }) (Effect, error) {
	var e Effect
	var rule, reply, ack, ackAt sql.NullString
	var n sql.NullInt64
	if err := sc.Scan(&e.ID, &e.TurnID, &e.Ordinal, &e.Server, &e.ServerIdentity, &e.Tool, &e.PinSHA256,
		&e.ArgsSHA256, &e.State, &rule, &reply, &n, &ack, &ackAt); err != nil {
		return Effect{}, err
	}
	e.SkippedRule, e.ReplySHA256, e.ReplyByteLen, e.AckState = rule.String, reply.String, n.Int64, ack.String
	if ackAt.Valid {
		e.AckedAt, _ = parseTime(ackAt.String)
	}
	return e, nil
}

func (tx *Tx) getEffect(effectID string) (Effect, error) {
	e, err := scanEffect(tx.tx.QueryRow(effectSelect+` WHERE id = ?`, effectID))
	if errors.Is(err, sql.ErrNoRows) {
		return Effect{}, fmt.Errorf("effect %q not found", effectID)
	}
	if err != nil {
		return Effect{}, fmt.Errorf("query effect: %w", err)
	}
	return e, nil
}

// GetEffect returns one effect.
func (tx *Tx) GetEffect(effectID string) (Effect, error) { return tx.getEffect(effectID) }

// EffectSet returns a turn's effect set. It is an error if the turn has none.
func (tx *Tx) EffectSet(turnID string) (EffectSet, error) {
	set := EffectSet{TurnID: turnID}
	var canary string
	err := tx.tx.QueryRow(`SELECT canary_set FROM effect_sets WHERE turn_id = ?`, turnID).Scan(&canary)
	if errors.Is(err, sql.ErrNoRows) {
		return EffectSet{}, fmt.Errorf("turn %q has no effect set", turnID)
	}
	if err != nil {
		return EffectSet{}, fmt.Errorf("query effect set: %w", err)
	}
	set.Canary = []byte(canary)
	rows, err := tx.tx.Query(effectSelect+` WHERE turn_id = ? ORDER BY ordinal`, turnID)
	if err != nil {
		return EffectSet{}, fmt.Errorf("query effects: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		e, err := scanEffect(rows)
		if err != nil {
			return EffectSet{}, fmt.Errorf("scan effect: %w", err)
		}
		set.Effects = append(set.Effects, e)
	}
	if err := rows.Err(); err != nil {
		return EffectSet{}, err
	}
	var n int
	if err := tx.tx.QueryRow(`SELECT COUNT(*) FROM effect_set_discards WHERE turn_id = ?`, turnID).Scan(&n); err != nil {
		return EffectSet{}, fmt.Errorf("query discard: %w", err)
	}
	set.Discarded = n > 0
	return set, nil
}

// MarkEffectSkipped records that a check failed immediately before dispatch,
// with the failed rule. Only the effect that is eligible can be skipped; a
// skipped one that is checked again and fails again takes the new rule. It
// emits effect_skipped.
func (tx *Tx) MarkEffectSkipped(effectID, rule string) error {
	if rule == "" {
		return fmt.Errorf("a skipped effect needs the failed rule")
	}
	e, err := tx.requireEligible(effectID)
	if err != nil {
		return err
	}
	if _, err := tx.tx.Exec(`UPDATE effects SET state = ?, skipped_rule = ? WHERE id = ?`,
		EffectSkipped, rule, effectID); err != nil {
		return fmt.Errorf("mark effect skipped: %w", err)
	}
	return tx.emitTools(e.TurnID, "effect_skipped", map[string]any{"effect_id": effectID, "rule": rule})
}

// requireEligible loads the effect and its set and returns the effect only if
// it is the one eligible effect and the set is not final.
func (tx *Tx) requireEligible(effectID string) (Effect, error) {
	e, err := tx.getEffect(effectID)
	if err != nil {
		return Effect{}, err
	}
	set, err := tx.EffectSet(e.TurnID)
	if err != nil {
		return Effect{}, err
	}
	state, idx := DeriveEffectSetState(set.Effects, set.Discarded)
	if EffectSetFinal(state) || idx < 0 || set.Effects[idx].ID != effectID {
		return Effect{}, fmt.Errorf("effect %q in a set that is %s: %w", effectID, state, ErrEffectNotEligible)
	}
	return e, nil
}

// DispatchEffect journals the engine's intent to send: it moves the effect to
// dispatching, only if it is eligible and its set is not final, checked and
// written in this one transaction. Otherwise it returns
// ErrEffectNotEligible and changes nothing. It emits effect_dispatch.
func (tx *Tx) DispatchEffect(effectID string) error {
	e, err := tx.requireEligible(effectID)
	if err != nil {
		return err
	}
	res, err := tx.tx.Exec(`
		UPDATE effects SET state = ?, skipped_rule = NULL
		WHERE id = ? AND state IN ('pending', 'skipped') AND ack_state IS NULL
	`, EffectDispatching, effectID)
	if err != nil {
		return fmt.Errorf("dispatch effect: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return fmt.Errorf("effect %q: %w", effectID, ErrEffectNotEligible)
	}
	return tx.emitTools(e.TurnID, "effect_dispatch", map[string]any{"effect_id": effectID})
}

// RecordEffectOutcome journals the one outcome of a dispatching effect: done
// or failed with the stored reply, or uncertain with a reply or none. The
// reply is stored by content hash. It emits effect_outcome.
func (tx *Tx) RecordEffectOutcome(effectID, state string, reply []byte) (Effect, error) {
	switch state {
	case EffectDone, EffectFailed:
		if reply == nil {
			return Effect{}, fmt.Errorf("a %s effect stores its reply", state)
		}
	case EffectUncertain:
	default:
		return Effect{}, fmt.Errorf("unknown effect outcome state %q", state)
	}
	e, err := tx.getEffect(effectID)
	if err != nil {
		return Effect{}, err
	}
	if e.State != EffectDispatching {
		return Effect{}, fmt.Errorf("effect %q is %s, not dispatching", effectID, e.State)
	}
	var shaArg *string
	var lenArg *int64
	payload := map[string]any{"effect_id": effectID, "state": state}
	if reply != nil {
		sum, err := tx.StoreContent(reply)
		if err != nil {
			return Effect{}, err
		}
		n := int64(len(reply))
		shaArg, lenArg = &sum, &n
		payload["reply_sha256"], payload["reply_byte_len"] = sum, n
	}
	if _, err := tx.tx.Exec(`UPDATE effects SET state = ?, reply_sha256 = ?, reply_byte_len = ? WHERE id = ?`,
		state, shaArg, lenArg, effectID); err != nil {
		return Effect{}, fmt.Errorf("record effect outcome: %w", err)
	}
	if err := tx.emitTools(e.TurnID, "effect_outcome", payload); err != nil {
		return Effect{}, err
	}
	return tx.getEffect(effectID)
}

// AcknowledgeEffect records the human's acknowledgement of an effect that is
// skipped, failed or uncertain. state must be the effect's present state. It
// is set once. It emits effect_acknowledged.
func (tx *Tx) AcknowledgeEffect(effectID, state string) error {
	e, err := tx.getEffect(effectID)
	if err != nil {
		return err
	}
	switch e.State {
	case EffectSkipped, EffectFailed, EffectUncertain:
	default:
		return fmt.Errorf("effect %q is %s and cannot be acknowledged", effectID, e.State)
	}
	if state != e.State {
		return fmt.Errorf("effect %q is %s, not %s", effectID, e.State, state)
	}
	if e.Acknowledged() {
		return fmt.Errorf("effect %q is already acknowledged", effectID)
	}
	if _, err := tx.tx.Exec(`UPDATE effects SET ack_state = ?, acked_at = ? WHERE id = ?`,
		state, nowStamp(), effectID); err != nil {
		return fmt.Errorf("acknowledge effect: %w", err)
	}
	return tx.emitTools(e.TurnID, "effect_acknowledged", map[string]any{"effect_id": effectID, "state": state})
}

// DiscardEffectSet records the human's discard of an appliable, resumable or
// stopped set. It changes no effect. It emits effect_set_discarded.
func (tx *Tx) DiscardEffectSet(turnID string) error {
	set, err := tx.EffectSet(turnID)
	if err != nil {
		return err
	}
	switch st := set.State(); st {
	case EffectSetAppliable, EffectSetResumable, EffectSetStopped:
	default:
		return fmt.Errorf("effect set of turn %q is %s and cannot be discarded", turnID, st)
	}
	if _, err := tx.tx.Exec(`INSERT INTO effect_set_discards (turn_id, discarded_at) VALUES (?, ?)`,
		turnID, nowStamp()); err != nil {
		return fmt.Errorf("insert discard: %w", err)
	}
	return tx.emitTools(turnID, "effect_set_discarded", map[string]any{})
}
