package journal

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"
)

// Store provides the durable-intent and recovery API over the journal schema.
// All methods run in a single transaction.
type Store struct {
	db      *DB
	mu      sync.Mutex
	nextSeq int64
}

// NewStore creates a Store from a database handle.
func NewStore(db *DB) *Store {
	var maxSeq int64
	_ = db.conn.QueryRow(`SELECT COALESCE(MAX(id), 0) FROM events`).Scan(&maxSeq)
	return &Store{db: db, nextSeq: maxSeq}
}

func generateID() (string, error) {
	const idLen = 16
	b := make([]byte, idLen)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return "", fmt.Errorf("generate random bytes: %w", err)
	}
	return hex.EncodeToString(b), nil
}

func computeSHA256(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func parseTime(s string) (time.Time, error) {
	formats := []string{
		time.RFC3339,
		time.RFC3339Nano,
		"2006-01-02 15:04:05",
	}
	var lastErr error
	for _, format := range formats {
		t, err := time.Parse(format, s)
		if err == nil {
			return t, nil
		}
		lastErr = err
	}
	return time.Time{}, fmt.Errorf("unable to parse time %q: %w", s, lastErr)
}

// CreateRun creates a new run with the given manifest, returning the run ID and manifest hash.
func (s *Store) CreateRun(manifest json.RawMessage) (runID, manifestHash string, err error) {
	err = s.Tx(func(tx *Tx) error {
		var txErr error
		runID, manifestHash, txErr = tx.CreateRun(manifest)
		return txErr
	})
	return runID, manifestHash, err
}

// CreateTurn creates a new turn for the given run, returning the turn ID.
func (s *Store) CreateTurn(runID, mode string, deadline *time.Time) (turnID string, err error) {
	err = s.Tx(func(tx *Tx) error {
		var txErr error
		turnID, txErr = tx.CreateTurn(runID, mode, deadline)
		return txErr
	})
	return turnID, err
}

// SetTurnTerminal sets the terminal state of a turn. It rejects if the turn already has a terminal state.
func (s *Store) SetTurnTerminal(turnID, state string) error {
	return s.Tx(func(tx *Tx) error {
		return tx.SetTurnTerminal(turnID, state)
	})
}

// AppendMessage appends a message to a turn, computing the hash and sequence number.
func (s *Store) AppendMessage(turnID, phase, sender string, recipients []string, content []byte, parents []string) (msgID string, sha256 string, byteLen int, err error) {
	err = s.Tx(func(tx *Tx) error {
		var txErr error
		msgID, sha256, byteLen, txErr = tx.AppendMessage(turnID, phase, sender, recipients, content, parents)
		return txErr
	})
	return msgID, sha256, byteLen, err
}

// BeginAttempt creates a pending attempt. The row is committed before the function returns.
func (s *Store) BeginAttempt(turnID, participant, phase string, epoch int, deadline *time.Time, repairOf string) (attemptID string, err error) {
	err = s.Tx(func(tx *Tx) error {
		var txErr error
		attemptID, txErr = tx.BeginAttempt(turnID, participant, phase, epoch, deadline, repairOf)
		return txErr
	})
	return attemptID, err
}

// CompleteAttempt sets an attempt's output and state to completed.
func (s *Store) CompleteAttempt(attemptID string, rawScrubbed string, scrubCount int, identity, status, nativeFinish, transportStatus string) error {
	return s.Tx(func(tx *Tx) error {
		return tx.CompleteAttempt(attemptID, rawScrubbed, scrubCount, identity, status, nativeFinish, transportStatus)
	})
}

// QuarantineLateOutput sets an attempt to completed with the same output as CompleteAttempt, but marks it quarantined.
func (s *Store) QuarantineLateOutput(attemptID string, rawScrubbed string, scrubCount int, identity, status, nativeFinish, transportStatus string) error {
	return s.Tx(func(tx *Tx) error {
		return tx.QuarantineLateOutput(attemptID, rawScrubbed, scrubCount, identity, status, nativeFinish, transportStatus)
	})
}

// SetCancellation sets the cancellation state of an attempt.
func (s *Store) SetCancellation(attemptID, cancellationState string) error {
	return s.Tx(func(tx *Tx) error {
		return tx.SetCancellation(attemptID, cancellationState)
	})
}

// MarkUnknown sets an attempt's state to unknown.
func (s *Store) MarkUnknown(attemptID string) error {
	return s.Tx(func(tx *Tx) error {
		return tx.MarkUnknown(attemptID)
	})
}

// RecoverPending returns all attempts currently in pending state.
func (s *Store) RecoverPending() ([]string, error) {
	var attemptIDs []string
	err := s.Tx(func(tx *Tx) error {
		var txErr error
		attemptIDs, txErr = tx.RecoverPending()
		return txErr
	})
	return attemptIDs, err
}

// AppendEvent appends an event not tied to any turn (events.turn_id is NULL).
func (s *Store) AppendEvent(kind string, payload json.RawMessage) error {
	return s.Tx(func(tx *Tx) error {
		return tx.AppendEvent(kind, payload)
	})
}

// AttemptRow represents a row from the attempts table.
type AttemptRow struct {
	ID                string
	TurnID            string
	Participant       string
	Phase             string
	Epoch             int
	DispatchedAt      time.Time
	Deadline          *time.Time
	State             string
	Status            *string
	CancellationState string
	Quarantined       bool
	RawOutput         *string
	ScrubCount        *int
	ObservedIdentity  *string
	NativeFinish      *string
	TransportStatus   *string
	RepairOf          *string
}

// GetAttempt retrieves an attempt by ID.
func (s *Store) GetAttempt(attemptID string) (*AttemptRow, error) {
	var row *AttemptRow
	err := s.Tx(func(tx *Tx) error {
		var txErr error
		row, txErr = tx.GetAttempt(attemptID)
		return txErr
	})
	return row, err
}

// RunRow represents a row from the runs table.
type RunRow struct {
	ID           string
	CreatedAt    time.Time
	Manifest     json.RawMessage
	ManifestHash string
}

// GetRun retrieves a run by ID.
func (s *Store) GetRun(runID string) (*RunRow, error) {
	var row *RunRow
	err := s.Tx(func(tx *Tx) error {
		var txErr error
		row, txErr = tx.GetRun(runID)
		return txErr
	})
	return row, err
}

// CreateCandidate stores a new candidate with a caller-provided hash.
func (s *Store) CreateCandidate(turnID, content, hash string) (candidateID string, err error) {
	err = s.Tx(func(tx *Tx) error {
		var txErr error
		candidateID, txErr = tx.CreateCandidate(turnID, content, hash)
		return txErr
	})
	return candidateID, err
}

// RecordVerdict stores a verdict from an attempt.
func (s *Store) RecordVerdict(attemptID, echoedCandidateHash, parsed, reason, validationStatus string) (verdictID string, err error) {
	err = s.Tx(func(tx *Tx) error {
		var txErr error
		verdictID, txErr = tx.RecordVerdict(attemptID, echoedCandidateHash, parsed, reason, validationStatus)
		return txErr
	})
	return verdictID, err
}

// OpenBlock opens a block from a verdict. Rejects if the verdict's parsed value is not block.
func (s *Store) OpenBlock(verdictID, blockID, candidateHash, severity, evidence, remedy string) error {
	return s.Tx(func(tx *Tx) error {
		return tx.OpenBlock(verdictID, blockID, candidateHash, severity, evidence, remedy)
	})
}

// WithdrawBlock withdraws a block against a new candidate hash.
func (s *Store) WithdrawBlock(blockID, againstCandidateHash string) error {
	return s.Tx(func(tx *Tx) error {
		return tx.WithdrawBlock(blockID, againstCandidateHash)
	})
}

// OverrideBlock overrides a block. Rejects unless the block is open.
func (s *Store) OverrideBlock(blockID string) error {
	return s.Tx(func(tx *Tx) error {
		return tx.OverrideBlock(blockID)
	})
}

// Block represents a row from the blocks table.
type Block struct {
	ID            string
	VerdictID     string
	CandidateHash string
	Severity      string
	Evidence      string
	Remedy        string
	State         string
}

// OpenBlocks returns blocks in open state for a turn, across all its candidates.
func (s *Store) OpenBlocks(turnID string) ([]Block, error) {
	var blocks []Block
	err := s.Tx(func(tx *Tx) error {
		var txErr error
		blocks, txErr = tx.OpenBlocks(turnID)
		return txErr
	})
	return blocks, err
}
