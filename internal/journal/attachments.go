package journal

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Attachment represents a file attached to a turn.
type Attachment struct {
	ID         string
	TurnID     string
	Path       string
	SHA256     string
	ByteLen    int
	BaseSHA    string
	RecordedAt time.Time
}

// StoreAttachmentsWithBases stores a set of attachment records for a turn, their
// snapshot bases as observations, and sets the attachments_hash and file_tainted
// flag. All records are committed atomically in a single transaction within the
// caller's Tx, or none if any fails. The attachments are stored in ascending
// byte order of path. Returns the computed attachments_hash.
//
// This function does NOT open its own transaction; it must be called within
// an existing Tx for atomicity with turn creation.
//
// The hash computation must stay synchronized with candidate.AttachmentManifestHash.
func (tx *Tx) StoreAttachmentsWithBases(turnID string, attachments []Attachment) (string, error) {
	if len(attachments) == 0 {
		return "none", nil
	}

	// Sort attachments by path in ascending order
	sort.Slice(attachments, func(i, j int) bool {
		return attachments[i].Path < attachments[j].Path
	})

	// Get the current snapshot version so we allocate new versions for each attachment
	currentVersion, err := tx.SnapshotVersion(turnID)
	if err != nil {
		return "", fmt.Errorf("get snapshot version: %w", err)
	}

	// Validate and store attachment records
	for i, att := range attachments {
		if att.Path == "" {
			return "", fmt.Errorf("attachment path must be non-empty")
		}
		if strings.ContainsAny(att.Path, "\n\r") {
			return "", fmt.Errorf("attachment path must not contain newline or carriage return")
		}
		if att.SHA256 == "" || len(att.SHA256) != 64 {
			return "", fmt.Errorf("attachment content_sha256 must be 64 lowercase hex characters")
		}
		if att.ByteLen < 0 {
			return "", fmt.Errorf("attachment byte_len must be non-negative")
		}

		if _, err := tx.tx.Exec(`
			INSERT INTO attachments (id, turn_id, path, sha256, byte_len, recorded_at)
			VALUES (?, ?, ?, ?, ?, ?)
		`, att.ID, turnID, att.Path, att.SHA256, att.ByteLen, nowStamp()); err != nil {
			return "", fmt.Errorf("store attachment: %w", err)
		}

		// Store attachment base as an observation with NULL attempt_id.
		// The base is recorded as an observation so read_file and overlay logic
		// can access it correctly without a fake author attempt.
		// Allocate a unique snapshot_version for each attachment.
		obsID, err := generateID()
		if err != nil {
			return "", fmt.Errorf("generate observation id: %w", err)
		}
		basePath := []byte(att.Path)
		journalSeq, err := tx.nextJournalSeq()
		if err != nil {
			return "", fmt.Errorf("allocate journal seq: %w", err)
		}
		// Allocate unique snapshot_version for this attachment: currentVersion + i + 1
		snapshotVersion := currentVersion + i + 1

		if _, err := tx.tx.Exec(`
			INSERT INTO observations (id, turn_id, attempt_id, snapshot_version, path, kind, content_sha256, journal_seq, recorded_at)
			VALUES (?, ?, NULL, ?, ?, 'f', ?, ?, ?)
		`, obsID, turnID, snapshotVersion, basePath, att.BaseSHA, journalSeq, nowStamp()); err != nil {
			return "", fmt.Errorf("store attachment base observation: %w", err)
		}
	}

	// Compute attachments_hash using the canonical manifest format.
	// This must stay synchronized with candidate.AttachmentManifestHash.
	hash := computeAttachmentsHash(attachments)

	// Set attachments_hash on the turn
	if _, err := tx.tx.Exec(`
		UPDATE turns SET attachments_hash = ? WHERE id = ?
	`, hash, turnID); err != nil {
		return "", fmt.Errorf("set turn attachments_hash: %w", err)
	}

	// Set file_tainted flag
	if _, err := tx.tx.Exec(`
		UPDATE turns SET file_tainted = 1 WHERE id = ?
	`, turnID); err != nil {
		return "", fmt.Errorf("set turn file_tainted: %w", err)
	}

	return hash, nil
}

// computeAttachmentsHash computes the SHA-256 of the attachments manifest.
// The manifest begins with "syndic-attachments/v1\n" followed by one line
// per attachment (sorted by path): "path_len:path\nsha256\nbyte_len\n".
//
// IMPORTANT: This implementation must stay synchronized with
// candidate.AttachmentManifestHash. Any changes here must be reflected there.
func computeAttachmentsHash(attachments []Attachment) string {
	if len(attachments) == 0 {
		return "none"
	}

	var buf bytes.Buffer
	buf.WriteString("syndic-attachments/v1\n")

	for _, att := range attachments {
		fmt.Fprintf(&buf, "%d:%s\n", len(att.Path), att.Path)
		fmt.Fprintf(&buf, "%s\n", att.SHA256)
		fmt.Fprintf(&buf, "%d\n", att.ByteLen)
	}

	hash := sha256.Sum256(buf.Bytes())
	return fmt.Sprintf("%x", hash)
}

// GetAttachments retrieves all attachments for a turn, with their base SHAs
// from the observations table, ordered by path.
func (tx *Tx) GetAttachments(turnID string) ([]Attachment, error) {
	rows, err := tx.tx.Query(`
		SELECT a.id, a.turn_id, a.path, a.sha256, a.byte_len,
		       COALESCE(o.content_sha256, '') as base_sha256, a.recorded_at
		FROM attachments a
		LEFT JOIN observations o ON o.turn_id = a.turn_id AND
		                             o.path = CAST(a.path AS BLOB) AND
		                             o.kind = 'f' AND o.attempt_id IS NULL
		WHERE a.turn_id = ?
		ORDER BY a.path ASC
	`, turnID)
	if err != nil {
		return nil, fmt.Errorf("query attachments: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var attachments []Attachment
	for rows.Next() {
		var a Attachment
		var recordedAtStr string
		if err := rows.Scan(&a.ID, &a.TurnID, &a.Path, &a.SHA256, &a.ByteLen, &a.BaseSHA, &recordedAtStr); err != nil {
			return nil, fmt.Errorf("scan attachment: %w", err)
		}
		t, err := parseTime(recordedAtStr)
		if err != nil {
			return nil, fmt.Errorf("parse recorded_at: %w", err)
		}
		a.RecordedAt = t
		attachments = append(attachments, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate attachments: %w", err)
	}

	return attachments, nil
}

// GetAttachmentsWithBases retrieves attachments and their bases from observations.
// This queries both the attachments table and the observations table to get
// the base SHA256 for each attachment.
func (tx *Tx) GetAttachmentsWithBases(turnID string) ([]Attachment, error) {
	rows, err := tx.tx.Query(`
		SELECT a.id, a.turn_id, a.path, a.sha256, a.byte_len, o.content_sha256, a.recorded_at
		FROM attachments a
		LEFT JOIN observations o ON o.turn_id = a.turn_id AND o.path = CAST(a.path AS BLOB) AND o.kind = 'f'
		WHERE a.turn_id = ?
		ORDER BY a.path ASC
	`, turnID)
	if err != nil {
		return nil, fmt.Errorf("query attachments with bases: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var attachments []Attachment
	for rows.Next() {
		var a Attachment
		var recordedAtStr string
		var baseSHA *string
		if err := rows.Scan(&a.ID, &a.TurnID, &a.Path, &a.SHA256, &a.ByteLen, &baseSHA, &recordedAtStr); err != nil {
			return nil, fmt.Errorf("scan attachment: %w", err)
		}
		if baseSHA != nil {
			a.BaseSHA = *baseSHA
		}
		t, err := parseTime(recordedAtStr)
		if err != nil {
			return nil, fmt.Errorf("parse recorded_at: %w", err)
		}
		a.RecordedAt = t
		attachments = append(attachments, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate attachments: %w", err)
	}

	return attachments, nil
}

// StoreAttachments stores attachments and their bases for a turn, opening its own
// transaction. This is primarily for testing; production code should use
// StoreAttachmentsWithBases within the turn creation transaction for atomicity.
func (s *Store) StoreAttachments(turnID string, attachments []Attachment) (string, error) {
	var hash string
	err := s.Tx(func(tx *Tx) error {
		var txErr error
		hash, txErr = tx.StoreAttachmentsWithBases(turnID, attachments)
		return txErr
	})
	return hash, err
}

// GetAttachments retrieves attachments from the database (read-only wrapper for Store).
func (s *Store) GetAttachments(turnID string) ([]Attachment, error) {
	var attachments []Attachment
	err := s.Tx(func(tx *Tx) error {
		var txErr error
		attachments, txErr = tx.GetAttachments(turnID)
		return txErr
	})
	return attachments, err
}
