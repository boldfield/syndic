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
}

// Canonical computes the canonical form of an identity, producing seven lines each terminated by newline.
func Canonical(id Identity) ([]byte, error) {
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

	return nil
}
