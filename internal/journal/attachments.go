package journal

import (
	"database/sql"
	"fmt"
	"sort"

	"github.com/boldfield/syndic/internal/candidate"
)

// AttachmentContent is one attachment as admitted at submit: its
// workspace-relative path and the bytes the engine read. Everything else the
// journal records about it (hash, length) is derived from the bytes.
type AttachmentContent struct {
	Path    string
	Content []byte
}

// Attachment is a stored attachment record.
type Attachment struct {
	Path          string
	SHA256        string
	ByteLen       int64
	ObservationID string
	Content       []byte
}

// preparedAttachments sorts a copy of atts in ascending byte order of path
// and returns it with the manifest hash ("none" for an empty set). Path and
// duplicate validation happen here, before anything is written.
func preparedAttachments(atts []AttachmentContent) ([]AttachmentContent, string, error) {
	sorted := make([]AttachmentContent, len(atts))
	copy(sorted, atts)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })
	manifest := make([]candidate.Attachment, len(sorted))
	for i, a := range sorted {
		if i > 0 && sorted[i-1].Path == a.Path {
			return nil, "", fmt.Errorf("duplicate attachment path %q", a.Path)
		}
		manifest[i] = candidate.Attachment{Path: a.Path, ContentSHA256: computeSHA256(a.Content), ContentLen: int64(len(a.Content))}
	}
	hash, err := candidate.AttachmentManifestHash(manifest)
	if err != nil {
		return nil, "", err
	}
	return sorted, hash, nil
}

// recordAttachments stores each attachment's bytes, its base observation
// (no attempt, no author tool call) and its attachment record, in path order,
// for a turn row already inserted in this transaction.
func (tx *Tx) recordAttachments(turnID string, sorted []AttachmentContent) error {
	for _, a := range sorted {
		sum, err := tx.StoreContent(a.Content)
		if err != nil {
			return err
		}
		version, err := tx.SnapshotVersion(turnID)
		if err != nil {
			return err
		}
		version++
		id, err := generateID()
		if err != nil {
			return err
		}
		seq, err := tx.nextJournalSeq()
		if err != nil {
			return err
		}
		if _, err := tx.tx.Exec(`
			INSERT INTO observations (id, turn_id, attempt_id, snapshot_version, path, kind, content_sha256, journal_seq, recorded_at)
			VALUES (?, ?, NULL, ?, ?, ?, ?, ?, ?)
		`, id, turnID, version, []byte(a.Path), ObservationFile, sum, seq, nowStamp()); err != nil {
			return fmt.Errorf("insert attachment base: %w", err)
		}
		if _, err := tx.tx.Exec(`
			INSERT INTO turn_attachments (turn_id, path, sha256, byte_len, observation_id) VALUES (?, ?, ?, ?, ?)
		`, turnID, []byte(a.Path), sum, len(a.Content), id); err != nil {
			return fmt.Errorf("insert attachment record: %w", err)
		}
		payload := map[string]any{"observation_id": id, "source": "attachment",
			"snapshot_version": version, "kind": ObservationFile, "journal_seq": seq, "sha256": sum}
		pathJSON(payload, a.Path)
		if err := tx.emitTools(turnID, "observation", payload); err != nil {
			return err
		}
	}
	return nil
}

// TurnAttachments returns the turn's attachment records with their stored
// bytes in ascending byte order of path, after checking that the stored rows
// reproduce the turn's attachments_hash. A turn with none returns nil.
func (tx *Tx) TurnAttachments(turnID string) ([]Attachment, error) {
	var stored sql.NullString
	if err := tx.tx.QueryRow(`SELECT attachments_hash FROM turns WHERE id = ?`, turnID).Scan(&stored); err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrTurnNotFound
		}
		return nil, fmt.Errorf("query attachments_hash: %w", err)
	}
	rows, err := tx.tx.Query(`
		SELECT a.path, a.sha256, a.byte_len, a.observation_id, c.body
		FROM turn_attachments a JOIN tool_content c ON c.sha256 = a.sha256
		WHERE a.turn_id = ? ORDER BY a.path
	`, turnID)
	if err != nil {
		return nil, fmt.Errorf("query attachments: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Attachment
	var manifest []candidate.Attachment
	for rows.Next() {
		var a Attachment
		var path []byte
		if err := rows.Scan(&path, &a.SHA256, &a.ByteLen, &a.ObservationID, &a.Content); err != nil {
			return nil, fmt.Errorf("scan attachment: %w", err)
		}
		a.Path = string(path)
		if got := computeSHA256(a.Content); got != a.SHA256 || int64(len(a.Content)) != a.ByteLen {
			return nil, fmt.Errorf("attachment %q: stored bytes do not match their record", a.Path)
		}
		out = append(out, a)
		manifest = append(manifest, candidate.Attachment{Path: a.Path, ContentSHA256: a.SHA256, ContentLen: a.ByteLen})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate attachments: %w", err)
	}
	hash, err := candidate.AttachmentManifestHash(manifest)
	if err != nil {
		return nil, err
	}
	if stored.Valid && stored.String != hash {
		return nil, fmt.Errorf("turn %q: attachments hash %s does not match stored manifest %s", turnID, stored.String, hash)
	}
	return out, nil
}

// TurnAttachments returns the turn's attachment records with their bytes.
func (s *Store) TurnAttachments(turnID string) (out []Attachment, err error) {
	err = s.Tx(func(tx *Tx) error {
		var txErr error
		out, txErr = tx.TurnAttachments(turnID)
		return txErr
	})
	return out, err
}
