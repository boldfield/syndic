package history

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// Turn represents the minimal data needed to build one turn's history entry.
type Turn struct {
	ID           string   // turn id
	State        string   // terminal_state (approved, incomplete, cancelled, etc.)
	Input        []byte   // user's input
	Answer       []byte   // approved answer or empty if incomplete/cancelled
	Label        string   // reviewed, unreviewed (solo), human override, etc.
	ChangesState string   // change set state (e.g., "applied", "pending", empty)
	Changes      []string // per-path changes: "path applied|not applied|conflict"
}

// Document represents a canonical syndic-history/v1 document.
type Document struct {
	buf *bytes.Buffer
}

// New creates a new Document builder.
func New() *Document {
	return &Document{buf: &bytes.Buffer{}}
}

// ValidateState checks if a state value is valid.
func ValidateState(state string) error {
	validStates := map[string]bool{
		"approved":   true,
		"incomplete": true,
		"cancelled":  true,
	}
	if !validStates[state] {
		return fmt.Errorf("invalid state: %q", state)
	}
	return nil
}

// ValidateLabel checks if a label value is valid.
func ValidateLabel(label string) error {
	validLabels := map[string]bool{
		"reviewed":          true,
		"unreviewed (solo)": true,
		"human override":    true,
	}
	if !validLabels[label] {
		return fmt.Errorf("invalid label: %q", label)
	}
	return nil
}

// AddTurn adds one turn to the document with byte-length framed fields.
// Each field is written as: <name> <byte-length>\n<exactly byte-length bytes>\n
func (d *Document) AddTurn(turn Turn) error {
	// Validate state and label
	if err := ValidateState(turn.State); err != nil {
		return err
	}
	if err := ValidateLabel(turn.Label); err != nil {
		return err
	}

	// Write each field with byte-length framing
	if err := d.writeField("turn", []byte(turn.ID)); err != nil {
		return err
	}
	if err := d.writeField("state", []byte(turn.State)); err != nil {
		return err
	}
	if err := d.writeField("input", turn.Input); err != nil {
		return err
	}

	// For answer, use "no approved answer" line if the turn is incomplete/cancelled
	var answerBytes []byte
	if turn.State == "incomplete" || turn.State == "cancelled" {
		answerBytes = []byte(fmt.Sprintf("no approved answer (%s)", turn.State))
	} else {
		answerBytes = turn.Answer
	}
	if err := d.writeField("answer", answerBytes); err != nil {
		return err
	}

	if err := d.writeField("label", []byte(turn.Label)); err != nil {
		return err
	}

	// Write changes fields if present
	if turn.ChangesState != "" {
		if err := d.writeField("changes", []byte(turn.ChangesState)); err != nil {
			return err
		}
	}
	for _, change := range turn.Changes {
		if err := d.writeField("change", []byte(change)); err != nil {
			return err
		}
	}

	return nil
}

// writeField writes one field in the format: <name> <byte-length>\n<bytes>\n
func (d *Document) writeField(name string, content []byte) error {
	// First line: <name> <byte-length>\n
	line := fmt.Sprintf("%s %d\n", name, len(content))
	if _, err := d.buf.WriteString(line); err != nil {
		return err
	}

	// Content bytes
	if _, err := d.buf.Write(content); err != nil {
		return err
	}

	// Trailing newline
	if _, err := d.buf.WriteString("\n"); err != nil {
		return err
	}

	return nil
}

// Bytes returns the complete document as bytes, with the preamble.
// The document begins with "syndic-history/v1" on a line by itself.
func (d *Document) Bytes() []byte {
	var result bytes.Buffer
	result.WriteString("syndic-history/v1\n")
	result.Write(d.buf.Bytes())
	return result.Bytes()
}

// Hash returns the SHA-256 hash of the complete document as a hex string.
func (d *Document) Hash() string {
	h := sha256.Sum256(d.Bytes())
	return hex.EncodeToString(h[:])
}

// String returns the document as a string (for debugging/inspection).
func (d *Document) String() string {
	return string(d.Bytes())
}

// parseField parses a strict field header and reads the field content.
// Ensures exact format: "<name> <length>\n" with no extra whitespace.
// Returns (fieldName, content, newOffset, error)
func (d *Document) parseField(docBytes []byte, offset int) (string, []byte, int, error) {
	lineStart := offset
	for offset < len(docBytes) && docBytes[offset] != '\n' {
		offset++
	}
	if offset >= len(docBytes) {
		return "", nil, offset, fmt.Errorf("unexpected EOF while parsing field header")
	}

	line := string(docBytes[lineStart:offset])
	offset++ // skip newline

	// Parse strictly: "name length" with single space
	var fieldName string
	var byteLen int
	n, err := fmt.Sscanf(line, "%s %d", &fieldName, &byteLen)
	if err != nil {
		return "", nil, offset, fmt.Errorf("parse field header %q: %w", line, err)
	}
	if n != 2 {
		return "", nil, offset, fmt.Errorf("parse field header %q: expected 2 fields, got %d", line, n)
	}

	// Validate no negative lengths
	if byteLen < 0 {
		return "", nil, offset, fmt.Errorf("invalid byte length: %d", byteLen)
	}

	// Read exactly byteLen bytes
	if offset+byteLen > len(docBytes) {
		return "", nil, offset, fmt.Errorf("unexpected EOF reading field %s content (expected %d bytes)", fieldName, byteLen)
	}
	fieldContent := docBytes[offset : offset+byteLen]
	offset += byteLen

	// Skip trailing newline
	if offset >= len(docBytes) || docBytes[offset] != '\n' {
		return "", nil, offset, fmt.Errorf("expected newline after field %s content", fieldName)
	}
	offset++

	return fieldName, fieldContent, offset, nil
}

// Message represents a rendered message with role and content.
type Message struct {
	Role    string // "user" or "assistant"
	Content string
}

// RenderForAuthor renders the document as alternating user/assistant messages.
// Returns a list of Message objects with explicit role tagging.
func (d *Document) RenderForAuthor() ([]Message, error) {
	var messages []Message

	docBytes := d.Bytes()
	offset := 0

	// Skip preamble
	preamble := "syndic-history/v1\n"
	if !bytes.HasPrefix(docBytes, []byte(preamble)) {
		return nil, fmt.Errorf("invalid document: missing preamble")
	}
	offset += len(preamble)

	// Parse each turn
	for offset < len(docBytes) {
		// Parse required fields in order: turn, state, input, answer, label
		fields := make(map[string][]byte)
		fieldNames := []string{"turn", "state", "input", "answer", "label"}

		for _, fieldName := range fieldNames {
			name, content, newOffset, err := d.parseField(docBytes, offset)
			if err != nil {
				return nil, err
			}

			if name != fieldName {
				return nil, fmt.Errorf("expected field %s, got %s", fieldName, name)
			}

			fields[fieldName] = content
			offset = newOffset
		}

		// Skip optional changes and change fields
		for offset < len(docBytes) {
			// Peek at the next field name
			lineStart := offset
			for offset < len(docBytes) && docBytes[offset] != '\n' && docBytes[offset] != ' ' {
				offset++
			}

			if offset >= len(docBytes) || docBytes[offset] == '\n' {
				// No field marker found
				offset = lineStart
				break
			}

			nextFieldName := string(docBytes[lineStart:offset])
			if nextFieldName == "changes" || nextFieldName == "change" {
				// Parse and skip this field
				offset = lineStart
				_, _, newOffset, err := d.parseField(docBytes, offset)
				if err != nil {
					return nil, err
				}
				offset = newOffset
			} else {
				// Not a changes field, stop
				offset = lineStart
				break
			}
		}

		// Build messages for this turn with role tagging
		inputStr := string(fields["input"])
		answerStr := string(fields["answer"])

		// Add user message with input
		messages = append(messages, Message{
			Role:    "user",
			Content: inputStr,
		})

		// Add assistant message with answer or missing-answer marker
		messages = append(messages, Message{
			Role:    "assistant",
			Content: answerStr,
		})
	}

	return messages, nil
}

// RenderForReviewer renders the document as a fenced data block for reviewers.
// Returns the full canonical document formatted for inclusion in a reviewer's message.
func (d *Document) RenderForReviewer(fenceDelim string) string {
	if fenceDelim == "" {
		fenceDelim = "```data"
	}
	return fmt.Sprintf("%s\n%s%s\n", fenceDelim, string(d.Bytes()), fenceDelim)
}
