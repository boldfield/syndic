package candidate

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"strings"
)

// Identity represents the identity of a candidate answer.
type Identity struct {
	ManifestHash    string
	TurnID          string
	AuthorAttemptID string
	Content         []byte
	EvidenceIDs     []string
	ContextHash     string // v2: context_hash or "none"
	Round           int    // v2: revision round number
	ChangesHash     string // v2: changes_hash or "none"
}

// Canonical computes the canonical form of an identity in v2 format.
// Returns bytes for v2 format including context, round, and changes fields.
func Canonical(id Identity) ([]byte, error) {
	if err := validateIdentity(id); err != nil {
		return nil, err
	}

	var buf bytes.Buffer

	buf.WriteString("syndic-candidate/v2\n")
	fmt.Fprintf(&buf, "manifest:%s\n", id.ManifestHash)
	fmt.Fprintf(&buf, "turn:%s\n", id.TurnID)
	fmt.Fprintf(&buf, "author_attempt:%s\n", id.AuthorAttemptID)

	contentHash := sha256.Sum256(id.Content)
	fmt.Fprintf(&buf, "content_sha256:%x\n", contentHash)
	fmt.Fprintf(&buf, "content_len:%d\n", len(id.Content))

	if len(id.EvidenceIDs) == 0 {
		buf.WriteString("evidence:none\n")
	} else {
		fmt.Fprintf(&buf, "evidence:%s\n", strings.Join(id.EvidenceIDs, ","))
	}

	// v2 fields: context, round, changes
	if id.ContextHash == "" {
		buf.WriteString("context:none\n")
	} else {
		fmt.Fprintf(&buf, "context:%s\n", id.ContextHash)
	}

	fmt.Fprintf(&buf, "round:%d\n", id.Round)

	if id.ChangesHash == "" {
		buf.WriteString("changes:none\n")
	} else {
		fmt.Fprintf(&buf, "changes:%s\n", id.ChangesHash)
	}

	return buf.Bytes(), nil
}

// CanonicalV1 computes the canonical form of an identity in v1 format.
// This is provided for backward compatibility with historical records.
func CanonicalV1(id Identity) ([]byte, error) {
	if err := validateIdentity(id); err != nil {
		return nil, err
	}

	var buf bytes.Buffer

	buf.WriteString("syndic-candidate/v1\n")
	fmt.Fprintf(&buf, "manifest:%s\n", id.ManifestHash)
	fmt.Fprintf(&buf, "turn:%s\n", id.TurnID)
	fmt.Fprintf(&buf, "author_attempt:%s\n", id.AuthorAttemptID)

	contentHash := sha256.Sum256(id.Content)
	fmt.Fprintf(&buf, "content_sha256:%x\n", contentHash)
	fmt.Fprintf(&buf, "content_len:%d\n", len(id.Content))

	if len(id.EvidenceIDs) == 0 {
		buf.WriteString("evidence:none\n")
	} else {
		fmt.Fprintf(&buf, "evidence:%s\n", strings.Join(id.EvidenceIDs, ","))
	}

	return buf.Bytes(), nil
}

// Hash computes the lowercase hex SHA-256 hash of the canonical form.
func Hash(id Identity) (string, error) {
	canonical, err := Canonical(id)
	if err != nil {
		return "", err
	}

	hash := sha256.Sum256(canonical)
	return fmt.Sprintf("%x", hash), nil
}

func validateIdentity(id Identity) error {
	if id.ManifestHash == "" {
		return fmt.Errorf("manifest hash must be non-empty")
	}
	if strings.ContainsAny(id.ManifestHash, "\n\r") {
		return fmt.Errorf("manifest hash must not contain newline or carriage return")
	}

	if id.TurnID == "" {
		return fmt.Errorf("turn id must be non-empty")
	}
	if strings.ContainsAny(id.TurnID, "\n\r") {
		return fmt.Errorf("turn id must not contain newline or carriage return")
	}

	if id.AuthorAttemptID == "" {
		return fmt.Errorf("author attempt id must be non-empty")
	}
	if strings.ContainsAny(id.AuthorAttemptID, "\n\r") {
		return fmt.Errorf("author attempt id must not contain newline or carriage return")
	}

	for _, eid := range id.EvidenceIDs {
		if eid == "" {
			return fmt.Errorf("evidence id must be non-empty")
		}
		if strings.ContainsAny(eid, ",\n\r") {
			return fmt.Errorf("evidence id must not contain comma, newline or carriage return")
		}
	}

	if id.Round < 0 {
		return fmt.Errorf("round must be non-negative")
	}

	if id.ContextHash != "" && strings.ContainsAny(id.ContextHash, "\n\r") {
		return fmt.Errorf("context hash must not contain newline or carriage return")
	}

	if id.ChangesHash != "" && strings.ContainsAny(id.ChangesHash, "\n\r") {
		return fmt.Errorf("changes hash must not contain newline or carriage return")
	}

	return nil
}
