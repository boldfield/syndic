package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/boldfield/syndic/internal/history"
	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/wire"
)

// historyLabel is the label an earlier turn's answer carries. A turn that
// ended without an answer has none to label; its state is in the document, and
// the label records only whether its preset reviewed.
func historyLabel(t journal.HistoryTurn) string {
	switch {
	case t.State == StateOverridden:
		return "human override"
	case t.State == StateSoloComplete || t.Mode == modeSolo:
		return "unreviewed (solo)"
	}
	return "reviewed"
}

// renderHistory builds the canonical history document of a conversation from
// the journal, from contextFromTurn onward when given. It runs in the submit
// Tx, so the bytes it returns are the ones admission, the stored document and
// the context hash are all taken from. An unknown trim point is a refusal.
func renderHistory(tx *journal.Tx, conversationID string, contextFromTurn *string) (*history.Document, error) {
	turns, err := tx.ConversationHistory(conversationID)
	if err != nil {
		return nil, err
	}
	doc := history.New()
	for _, t := range turns {
		if err := doc.AddTurn(history.Turn{
			ID:     t.ID,
			State:  t.State,
			Input:  []byte(t.Input),
			Answer: []byte(t.Answer),
			Label:  historyLabel(t),
		}); err != nil {
			return nil, fmt.Errorf("engine: history turn %s: %w", t.ID, err)
		}
	}
	if contextFromTurn != nil {
		if err := doc.SetContextFromTurn(*contextFromTurn); err != nil {
			bad := refusal(wire.ErrorCodeBadRequest)
			bad.Detail = strPtr("unknown_context_from_turn")
			return nil, bad
		}
	}
	return doc, nil
}

// frozenHistory returns the document's hash and bytes to store, or "" and nil
// when no turn is included: a turn with no history has no context hash.
func frozenHistory(doc *history.Document) (string, []byte) {
	if len(doc.Included()) == 0 {
		return "", nil
	}
	return doc.Hash(), doc.Bytes()
}

// legacySubmissionHash is the hash of the four members a submission was
// defined by before conversations. A turn journaled then carries it, so a
// retry of that submission still replays.
func legacySubmissionHash(req wire.SubmitRequest) (string, error) {
	body, err := json.Marshal(struct {
		AllowProvisional *bool   `json:"allow_provisional"`
		Input            string  `json:"input"`
		Preset           *string `json:"preset"`
		QuickProfile     *string `json:"quick_profile"`
	}{req.AllowProvisional, req.Input, req.Preset, req.QuickProfile})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

// replaysLegacy reports whether prior, journaled under the four-member hash,
// is the same submission as req: the four members match and none of the added
// ones was sent.
func replaysLegacy(req wire.SubmitRequest, priorHash string) (bool, error) {
	if req.Workspace != "" || req.ConversationID != nil || req.ContextFromTurn != nil || req.MaxRounds != nil {
		return false, nil
	}
	legacy, err := legacySubmissionHash(req)
	if err != nil {
		return false, err
	}
	return legacy == priorHash, nil
}

// asWireError unwraps a *wire.Error returned through a transaction.
func asWireError(err error) (*wire.Error, bool) {
	var we *wire.Error
	if errors.As(err, &we) {
		return we, true
	}
	return nil, false
}
