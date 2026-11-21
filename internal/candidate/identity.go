package candidate

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"regexp"
	"strings"
)

// EvidencePair represents a call_id:result_sha256 pair.
type EvidencePair struct {
	CallID       string // non-empty, no :, ,, newline
	ResultSHA256 string // 64 lowercase hex characters
}

// Identity represents the identity of a candidate answer.
type Identity struct {
	ManifestHash    string
	TurnID          string
	AuthorAttemptID string
	Content         []byte
	Evidence        []EvidencePair // v2: ordered call_id:result_sha256 pairs
	ContextHash     string         // v2: empty for none, or 64-char lowercase hex SHA-256
	Round           int            // v2: revision round number
	ChangesHash     string         // v2: empty for none, or 64-char lowercase hex SHA-256
}

// IdentityV1 represents a v1 candidate for reconstructing historical records.
// Used to validate that stored candidates with v1 hashes remain readable without rehashing.
type IdentityV1 struct {
	ManifestHash    string
	TurnID          string
	AuthorAttemptID string
	Content         []byte
	EvidenceIDs     []string // v1: opaque evidence IDs
}

// Canonical computes the canonical form of an identity in v2 format.
// Returns bytes for v2 format including context, round, and changes fields.
// Evidence must be in call_id:result_sha256 format.
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

	// Format evidence as call_id:result_sha256 pairs
	if len(id.Evidence) == 0 {
		buf.WriteString("evidence:none\n")
	} else {
		var evidenceParts []string
		for _, pair := range id.Evidence {
			evidenceParts = append(evidenceParts, fmt.Sprintf("%s:%s", pair.CallID, pair.ResultSHA256))
		}
		fmt.Fprintf(&buf, "evidence:%s\n", strings.Join(evidenceParts, ","))
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
// It validates that v2-only fields (Round, ContextHash, ChangesHash) are not set.
func CanonicalV1(id IdentityV1) ([]byte, error) {
	if err := validateIdentityV1(id); err != nil {
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

// Hash computes the lowercase hex SHA-256 hash of the canonical v2 form.
func Hash(id Identity) (string, error) {
	canonical, err := Canonical(id)
	if err != nil {
		return "", err
	}

	hash := sha256.Sum256(canonical)
	return fmt.Sprintf("%x", hash), nil
}

// HashV1 computes the lowercase hex SHA-256 hash of the canonical v1 form.
// Used for reconstructing historical v1 candidate hashes.
func HashV1(id IdentityV1) (string, error) {
	canonical, err := CanonicalV1(id)
	if err != nil {
		return "", err
	}

	hash := sha256.Sum256(canonical)
	return fmt.Sprintf("%x", hash), nil
}

var sha256HexRegex = regexp.MustCompile(`^[a-f0-9]{64}$`)

// isSHA256Hex checks if a string is a valid 64-character lowercase hex SHA-256 hash.
func isSHA256Hex(s string) bool {
	return sha256HexRegex.MatchString(s)
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

	// Validate evidence pairs: call_id:result_sha256 format
	for _, pair := range id.Evidence {
		if pair.CallID == "" {
			return fmt.Errorf("evidence call_id must be non-empty")
		}
		if strings.ContainsAny(pair.CallID, ":\n\r,") {
			return fmt.Errorf("evidence call_id must not contain ':', ',', newline or carriage return")
		}
		if pair.ResultSHA256 == "" {
			return fmt.Errorf("evidence result_sha256 must be non-empty")
		}
		if !isSHA256Hex(pair.ResultSHA256) {
			return fmt.Errorf("evidence result_sha256 must be 64 lowercase hex characters, got %q", pair.ResultSHA256)
		}
	}

	if id.Round < 0 {
		return fmt.Errorf("round must be non-negative")
	}

	// ContextHash: empty for none, or valid SHA-256 hash. Never literal "none".
	if id.ContextHash != "" {
		if id.ContextHash == "none" {
			return fmt.Errorf("context_hash must not be literal 'none'; use empty string for absent")
		}
		if !isSHA256Hex(id.ContextHash) {
			return fmt.Errorf("context_hash must be 64 lowercase hex characters or empty, got %q", id.ContextHash)
		}
	}

	// ChangesHash: empty for none, or valid SHA-256 hash. Never literal "none".
	if id.ChangesHash != "" {
		if id.ChangesHash == "none" {
			return fmt.Errorf("changes_hash must not be literal 'none'; use empty string for absent")
		}
		if !isSHA256Hex(id.ChangesHash) {
			return fmt.Errorf("changes_hash must be 64 lowercase hex characters or empty, got %q", id.ChangesHash)
		}
	}

	return nil
}

func validateIdentityV1(id IdentityV1) error {
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
