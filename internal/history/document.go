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
	buf             *bytes.Buffer
	turns           []Turn // Track turns for trim selection support
	contextFromTurn string // If set, only include turns from this turn ID onward
}

// New creates a new Document builder.
func New() *Document {
	return &Document{
		buf:   &bytes.Buffer{},
		turns: make([]Turn, 0),
	}
}

// SetContextFromTurn sets the trim point: only turns from this ID onward will be included.
// If the turn ID is not found, all turns are included.
func (d *Document) SetContextFromTurn(turnID string) {
	d.contextFromTurn = turnID
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

// ValidateChange checks if a change line is in the required format: "<path> applied|not applied|conflict"
func ValidateChange(change string) error {
	// Split on the last space to separate path and outcome
	lastSpace := -1
	for i := len(change) - 1; i >= 0; i-- {
		if change[i] == ' ' {
			lastSpace = i
			break
		}
	}

	if lastSpace < 0 {
		return fmt.Errorf("invalid change format: missing space separator")
	}

	path := change[:lastSpace]
	outcome := change[lastSpace+1:]

	if path == "" {
		return fmt.Errorf("invalid change format: empty path")
	}

	validOutcomes := map[string]bool{
		"applied":     true,
		"not applied": true,
		"conflict":    true,
	}

	if !validOutcomes[outcome] {
		return fmt.Errorf("invalid change outcome %q: must be one of 'applied', 'not applied', or 'conflict'", outcome)
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

	// Validate change annotations format
	for _, change := range turn.Changes {
		if err := ValidateChange(change); err != nil {
			return err
		}
	}

	// If changes are present, ChangesState must also be set
	if len(turn.Changes) > 0 && turn.ChangesState == "" {
		return fmt.Errorf("invalid turn: changes present but ChangesState is empty")
	}

	// Store turn for later filtering
	d.turns = append(d.turns, turn)

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
// Applies contextFromTurn filtering if set.
func (d *Document) Bytes() []byte {
	var result bytes.Buffer
	result.WriteString("syndic-history/v1\n")

	// If no trim point is set, return all turns from the buffer
	if d.contextFromTurn == "" {
		result.Write(d.buf.Bytes())
		return result.Bytes()
	}

	// Find the starting index for the context_from_turn
	startIdx := -1
	for i, turn := range d.turns {
		if turn.ID == d.contextFromTurn {
			startIdx = i
			break
		}
	}

	// If turn ID not found, include all turns
	if startIdx < 0 {
		result.Write(d.buf.Bytes())
		return result.Bytes()
	}

	// Rebuild the document with only the turns from startIdx onward
	for _, turn := range d.turns[startIdx:] {
		d.writeTurnToBuffer(&result, turn)
	}

	return result.Bytes()
}

// writeTurnToBuffer writes a single turn to the buffer in canonical format.
func (d *Document) writeTurnToBuffer(buf *bytes.Buffer, turn Turn) {
	d.writeFieldToBuffer(buf, "turn", []byte(turn.ID))
	d.writeFieldToBuffer(buf, "state", []byte(turn.State))
	d.writeFieldToBuffer(buf, "input", turn.Input)

	// For answer, use "no approved answer" line if the turn is incomplete/cancelled
	var answerBytes []byte
	if turn.State == "incomplete" || turn.State == "cancelled" {
		answerBytes = []byte(fmt.Sprintf("no approved answer (%s)", turn.State))
	} else {
		answerBytes = turn.Answer
	}
	d.writeFieldToBuffer(buf, "answer", answerBytes)
	d.writeFieldToBuffer(buf, "label", []byte(turn.Label))

	// Write changes fields if present
	if turn.ChangesState != "" {
		d.writeFieldToBuffer(buf, "changes", []byte(turn.ChangesState))
	}
	for _, change := range turn.Changes {
		d.writeFieldToBuffer(buf, "change", []byte(change))
	}
}

// writeFieldToBuffer writes one field to a buffer in the format: <name> <byte-length>\n<bytes>\n
func (d *Document) writeFieldToBuffer(buf *bytes.Buffer, name string, content []byte) {
	fmt.Fprintf(buf, "%s %d\n", name, len(content))
	buf.Write(content)
	buf.WriteString("\n")
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
// Enforces exact format: "<name> <decimal-length>\n" with single space and no leading zeros.
// Returns (fieldName, content, newOffset, error)
func (d *Document) parseField(docBytes []byte, offset int) (string, []byte, int, error) {
	lineStart := offset
	for offset < len(docBytes) && docBytes[offset] != '\n' {
		offset++
	}
	if offset >= len(docBytes) {
		return "", nil, offset, fmt.Errorf("unexpected EOF while parsing field header")
	}

	headerBytes := docBytes[lineStart:offset]
	offset++ // skip newline

	// Parse strictly: "name SP length" where SP is a single space
	// Find the space separator
	spaceIdx := -1
	for i := 0; i < len(headerBytes); i++ {
		if headerBytes[i] == ' ' {
			if spaceIdx >= 0 {
				// Multiple spaces not allowed
				return "", nil, offset, fmt.Errorf("parse field header: multiple spaces not allowed")
			}
			spaceIdx = i
		}
	}

	if spaceIdx < 0 {
		return "", nil, offset, fmt.Errorf("parse field header: missing space separator")
	}

	fieldName := string(headerBytes[:spaceIdx])
	lengthStr := string(headerBytes[spaceIdx+1:])

	// Validate length string: must be decimal digits only, no leading zeros
	if len(lengthStr) == 0 {
		return "", nil, offset, fmt.Errorf("parse field header: missing length value")
	}

	// Check for leading zeros or sign
	if (len(lengthStr) > 1 && lengthStr[0] == '0') || lengthStr[0] == '-' || lengthStr[0] == '+' {
		return "", nil, offset, fmt.Errorf("parse field header: invalid length format %q (no leading zeros or signs)", lengthStr)
	}

	// Parse decimal length
	var byteLen int
	_, err := fmt.Sscanf(lengthStr, "%d", &byteLen)
	if err != nil {
		return "", nil, offset, fmt.Errorf("parse field header: invalid length %q: %w", lengthStr, err)
	}

	if byteLen < 0 {
		return "", nil, offset, fmt.Errorf("parse field header: invalid byte length: %d", byteLen)
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
// Returns a list of Message objects with explicit role tagging and lossless annotations.
// Includes state, label, and changes so the hash binds exactly what the author saw.
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

		// Collect optional changes and change fields
		var changes [][]byte
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
				// Parse and collect this field
				offset = lineStart
				_, content, newOffset, err := d.parseField(docBytes, offset)
				if err != nil {
					return nil, err
				}
				changes = append(changes, content)
				offset = newOffset
			} else {
				// Not a changes field, stop
				offset = lineStart
				break
			}
		}

		// Build lossless rendering: include state, label, and changes
		var userContent bytes.Buffer
		userContent.WriteString("state: ")
		userContent.Write(fields["state"])
		userContent.WriteString("\n")
		userContent.WriteString("input: ")
		userContent.Write(fields["input"])
		userContent.WriteString("\n")

		var answerContent bytes.Buffer
		answerContent.WriteString("label: ")
		answerContent.Write(fields["label"])
		answerContent.WriteString("\n")
		answerContent.WriteString("answer: ")
		answerContent.Write(fields["answer"])
		answerContent.WriteString("\n")

		// Add changes if present
		if len(changes) > 0 {
			answerContent.WriteString("changes:\n")
			for _, change := range changes {
				answerContent.Write(change)
				answerContent.WriteString("\n")
			}
		}

		// Add user message with state and input
		messages = append(messages, Message{
			Role:    "user",
			Content: userContent.String(),
		})

		// Add assistant message with label, answer, and changes
		messages = append(messages, Message{
			Role:    "assistant",
			Content: answerContent.String(),
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
