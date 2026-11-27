package journal

import (
	"crypto/sha256"
	"fmt"
	"sort"
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

// StoreAttachments stores a set of attachment records for a turn along with
// their snapshot bases. All records are committed in a single transaction,
// or none if any fails. The attachments are stored in ascending byte order
// of path. Returns the computed attachments_hash.
func (tx *Tx) StoreAttachments(turnID string, attachments []Attachment) (string, error) {
	if len(attachments) == 0 {
		return "none", nil
	}

	sort.Slice(attachments, func(i, j int) bool {
		return attachments[i].Path < attachments[j].Path
	})

	for _, att := range attachments {
		if _, err := tx.tx.Exec(`
			INSERT INTO attachments (id, turn_id, path, sha256, byte_len, recorded_at)
			VALUES (?, ?, ?, ?, ?, ?)
		`, att.ID, turnID, att.Path, att.SHA256, att.ByteLen, nowStamp()); err != nil {
			return "", fmt.Errorf("store attachment: %w", err)
		}

		if _, err := tx.tx.Exec(`
			INSERT INTO attachment_bases (attachment_id, base_sha256, recorded_at)
			VALUES (?, ?, ?)
		`, att.ID, att.BaseSHA, nowStamp()); err != nil {
			return "", fmt.Errorf("store attachment base: %w", err)
		}
	}

	hash := computeAttachmentsHash(attachments)
	return hash, nil
}

// GetAttachments retrieves all attachments for a turn, ordered by path.
func (tx *Tx) GetAttachments(turnID string) ([]Attachment, error) {
	rows, err := tx.tx.Query(`
		SELECT a.id, a.turn_id, a.path, a.sha256, a.byte_len, ab.base_sha256, a.recorded_at
		FROM attachments a
		JOIN attachment_bases ab ON ab.attachment_id = a.id
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

// SetTurnAttachmentsHash sets the attachments_hash for a turn.
func (tx *Tx) SetTurnAttachmentsHash(turnID, hash string) error {
	_, err := tx.tx.Exec(`
		UPDATE turns SET attachments_hash = ? WHERE id = ?
	`, hash, turnID)
	if err != nil {
		return fmt.Errorf("update turn attachments_hash: %w", err)
	}
	return nil
}

// computeAttachmentsHash computes the SHA-256 of the attachments manifest.
// The manifest begins with "syndic-attachments/v1\n" followed by one line
// per attachment (sorted by path): "path_len:path\nsha256\nbyte_len\n".
func computeAttachmentsHash(attachments []Attachment) string {
	h := sha256.New()
	h.Write([]byte("syndic-attachments/v1\n"))

	for _, att := range attachments {
		_, _ = fmt.Fprintf(h, "%d:%s\n", len(att.Path), att.Path)
		_, _ = fmt.Fprintf(h, "%s\n", att.SHA256)
		_, _ = fmt.Fprintf(h, "%d\n", att.ByteLen)
	}

	return fmt.Sprintf("%x", h.Sum(nil))
}

// StoreAttachments stores attachments in the database (read-only wrapper for Store).
func (s *Store) StoreAttachments(turnID string, attachments []Attachment) (string, error) {
	var hash string
	err := s.Tx(func(tx *Tx) error {
		var txErr error
		hash, txErr = tx.StoreAttachments(turnID, attachments)
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

// SetTurnAttachmentsHash sets the attachments_hash on a turn (wrapper for Store).
func (s *Store) SetTurnAttachmentsHash(turnID, hash string) error {
	return s.Tx(func(tx *Tx) error {
		return tx.SetTurnAttachmentsHash(turnID, hash)
	})
}
