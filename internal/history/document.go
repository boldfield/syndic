package history

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// Turn represents the minimal data needed to build one turn's history entry.
type Turn struct {
	ID           string // turn id
	State        string // terminal_state (approved, incomplete, cancelled, etc.)
	Input        []byte // user's input
	Answer       []byte // approved answer or empty if incomplete/cancelled
	Label        string // reviewed, unreviewed (solo), human override, etc.
	IsIncomplete bool   // true if state is incomplete or cancelled
}

// Document represents a canonical syndic-history/v1 document.
type Document struct {
	buf *bytes.Buffer
}

// New creates a new Document builder.
func New() *Document {
	return &Document{buf: &bytes.Buffer{}}
}

// AddTurn adds one turn to the document with byte-length framed fields.
// Each field is written as: <name> <byte-length>\n<exactly byte-length bytes>\n
func (d *Document) AddTurn(turn Turn) error {
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
	if turn.IsIncomplete {
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

// RenderForAuthor renders the document as alternating user/assistant messages,
// excluding the preamble. Used to construct the author's message sequence.
// Returns a list of message strings in order.
func (d *Document) RenderForAuthor() ([]string, error) {
	var messages []string

	// Parse the document to extract turns
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
		// Parse fields: turn, state, input, answer, label
		fields := make(map[string][]byte)
		fieldNames := []string{"turn", "state", "input", "answer", "label"}

		for _, fieldName := range fieldNames {
			// Parse "<name> <byte-length>\n"
			lineStart := offset
			for offset < len(docBytes) && docBytes[offset] != '\n' {
				offset++
			}
			if offset >= len(docBytes) {
				return nil, fmt.Errorf("unexpected EOF while parsing field header")
			}

			line := string(docBytes[lineStart:offset])
			offset++ // skip newline

			var expectedName string
			var byteLen int
			if _, err := fmt.Sscanf(line, "%s %d", &expectedName, &byteLen); err != nil {
				return nil, fmt.Errorf("parse field header %q: %w", line, err)
			}

			if expectedName != fieldName {
				return nil, fmt.Errorf("expected field %s, got %s", fieldName, expectedName)
			}

			// Read exactly byteLen bytes
			if offset+byteLen > len(docBytes) {
				return nil, fmt.Errorf("unexpected EOF reading field content")
			}
			fieldContent := docBytes[offset : offset+byteLen]
			fields[fieldName] = append([]byte{}, fieldContent...)
			offset += byteLen

			// Skip trailing newline
			if offset >= len(docBytes) || docBytes[offset] != '\n' {
				return nil, fmt.Errorf("expected newline after field content")
			}
			offset++
		}

		// Build message for this turn
		inputStr := string(fields["input"])
		answerStr := string(fields["answer"])

		// Add user message with input
		messages = append(messages, inputStr)

		// Add assistant message with answer or missing-answer marker
		messages = append(messages, answerStr)
	}

	return messages, nil
}
