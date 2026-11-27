package candidate

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"sort"
	"strings"
)

// Attachment represents a single attachment with its metadata.
type Attachment struct {
	Path          string // workspace-relative path
	ContentSHA256 string // 64 lowercase hex characters
	ContentLen    int64  // byte length
}

// AttachmentManifest builds the canonical manifest bytes for a set of attachments.
// Attachments are sorted in ascending byte order of path.
// Returns "none" manifest hash when attachments is empty or nil.
func AttachmentManifest(attachments []Attachment) ([]byte, error) {
	if len(attachments) == 0 {
		return nil, nil
	}

	// Validate all attachments first
	for _, att := range attachments {
		if err := validateAttachment(att); err != nil {
			return nil, err
		}
	}

	// Sort attachments in ascending byte order of path
	sorted := make([]Attachment, len(attachments))
	copy(sorted, attachments)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Path < sorted[j].Path
	})

	var buf bytes.Buffer
	buf.WriteString("syndic-attachments/v1\n")

	for _, att := range sorted {
		// Path length in decimal, colon, path
		fmt.Fprintf(&buf, "%d:%s\n", len(att.Path), att.Path)
		// Content SHA-256 in lowercase hex
		fmt.Fprintf(&buf, "%s\n", att.ContentSHA256)
		// Content length in decimal
		fmt.Fprintf(&buf, "%d\n", att.ContentLen)
	}

	return buf.Bytes(), nil
}

// AttachmentManifestHash computes the SHA-256 hash of the attachment manifest.
// Returns "none" when attachments is empty or nil.
func AttachmentManifestHash(attachments []Attachment) (string, error) {
	if len(attachments) == 0 {
		return "none", nil
	}

	manifest, err := AttachmentManifest(attachments)
	if err != nil {
		return "", err
	}

	hash := sha256.Sum256(manifest)
	return fmt.Sprintf("%x", hash), nil
}

func validateAttachment(att Attachment) error {
	if att.Path == "" {
		return fmt.Errorf("attachment path must be non-empty")
	}
	if strings.ContainsAny(att.Path, "\n\r") {
		return fmt.Errorf("attachment path must not contain newline or carriage return")
	}

	if att.ContentSHA256 == "" {
		return fmt.Errorf("attachment content_sha256 must be non-empty")
	}
	if !isSHA256Hex(att.ContentSHA256) {
		return fmt.Errorf("attachment content_sha256 must be 64 lowercase hex characters, got %q", att.ContentSHA256)
	}

	if att.ContentLen < 0 {
		return fmt.Errorf("attachment content_len must be non-negative, got %d", att.ContentLen)
	}

	return nil
}
