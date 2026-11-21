package history

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/boldfield/syndic/internal/prompt"
)

const preamble = "syndic-history/v1\n"

// Change outcomes carried by one `change` field (design section 6).
const (
	OutcomeApplied    = "applied"
	OutcomeNotApplied = "not applied"
	OutcomeConflict   = "conflict"
)

// Turn is the data one history entry is built from.
type Turn struct {
	ID    string
	State string // approved, incomplete, cancelled, solo_complete or overridden
	Input []byte
	// Answer is the approved answer. It is ignored for incomplete and
	// cancelled turns, whose answer field is always the missing-answer line.
	Answer []byte
	Label  string // reviewed, unreviewed (solo) or human override
	// ChangesState is the change set's state; empty when the turn has none.
	ChangesState string
	Changes      []Change
}

// Change is one path's own outcome within a change set.
type Change struct {
	Path    string
	Outcome string
}

// Document is a canonical syndic-history/v1 document under construction.
type Document struct {
	turns           []Turn
	contextFromTurn string
}

// New returns an empty Document.
func New() *Document { return &Document{} }

func isMissing(state string) bool { return state == "incomplete" || state == "cancelled" }

func missingLine(state string) string { return "no approved answer (" + state + ")" }

// ValidateState checks a terminal state.
func ValidateState(state string) error {
	switch state {
	case "approved", "incomplete", "cancelled", "solo_complete", "overridden":
		return nil
	}
	return fmt.Errorf("invalid state: %q", state)
}

// ValidateLabel checks a label.
func ValidateLabel(label string) error {
	switch label {
	case "reviewed", "unreviewed (solo)", "human override":
		return nil
	}
	return fmt.Errorf("invalid label: %q", label)
}

// ValidateChangesState checks a change-set state.
func ValidateChangesState(state string) error {
	switch state {
	case "appliable", "applied", "partial", "conflict", "discarded":
		return nil
	}
	return fmt.Errorf("invalid changes state: %q", state)
}

// ValidateChange checks one per-path change.
func ValidateChange(c Change) error {
	switch c.Outcome {
	case OutcomeApplied, OutcomeNotApplied, OutcomeConflict:
	default:
		return fmt.Errorf("invalid change outcome: %q", c.Outcome)
	}
	if c.Path == "" {
		return errors.New("invalid change: empty path")
	}
	if strings.ContainsAny(c.Path, "\r\n") {
		return errors.New("invalid change: path contains a line break")
	}
	// "p not" + "applied" would read back as "p" + "not applied".
	if c.Outcome == OutcomeApplied && strings.HasSuffix(c.Path, " not") {
		return fmt.Errorf("invalid change: path %q is ambiguous with outcome %q", c.Path, c.Outcome)
	}
	return nil
}

func validateTurn(t Turn) error {
	if t.ID == "" {
		return errors.New("invalid turn: empty id")
	}
	if err := ValidateState(t.State); err != nil {
		return err
	}
	if err := ValidateLabel(t.Label); err != nil {
		return err
	}
	if t.ChangesState != "" {
		if err := ValidateChangesState(t.ChangesState); err != nil {
			return err
		}
	} else if len(t.Changes) > 0 {
		return errors.New("invalid turn: change fields require a changes state")
	}
	for _, c := range t.Changes {
		if err := ValidateChange(c); err != nil {
			return err
		}
	}
	return nil
}

// AddTurn appends one turn.
func (d *Document) AddTurn(t Turn) error {
	if err := validateTurn(t); err != nil {
		return err
	}
	for _, e := range d.turns {
		if e.ID == t.ID {
			return fmt.Errorf("duplicate turn id: %q", t.ID)
		}
	}
	d.turns = append(d.turns, t)
	return nil
}

// SetContextFromTurn records the trim point: every turn from id onward is
// included. An unknown id is an error; without a trim point all turns are.
func (d *Document) SetContextFromTurn(id string) error {
	for _, t := range d.turns {
		if t.ID == id {
			d.contextFromTurn = id
			return nil
		}
	}
	return fmt.Errorf("unknown turn ID: %q", id)
}

// Included returns the turns the document carries, in order.
func (d *Document) Included() []Turn {
	if d.contextFromTurn == "" {
		return d.turns
	}
	for i, t := range d.turns {
		if t.ID == d.contextFromTurn {
			return d.turns[i:]
		}
	}
	return nil
}

func writeField(buf *bytes.Buffer, name string, content []byte) {
	buf.WriteString(name)
	buf.WriteByte(' ')
	buf.WriteString(strconv.Itoa(len(content)))
	buf.WriteByte('\n')
	buf.Write(content)
	buf.WriteByte('\n')
}

func answerBytes(t Turn) []byte {
	if isMissing(t.State) {
		return []byte(missingLine(t.State))
	}
	return t.Answer
}

// writeTurn is the one serializer.
func writeTurn(buf *bytes.Buffer, t Turn) {
	writeField(buf, "turn", []byte(t.ID))
	writeField(buf, "state", []byte(t.State))
	writeField(buf, "input", t.Input)
	writeField(buf, "answer", answerBytes(t))
	writeField(buf, "label", []byte(t.Label))
	if t.ChangesState != "" {
		writeField(buf, "changes", []byte(t.ChangesState))
	}
	for _, c := range t.Changes {
		writeField(buf, "change", []byte(c.Path+" "+c.Outcome))
	}
}

// Bytes returns the canonical document.
func (d *Document) Bytes() []byte {
	var buf bytes.Buffer
	buf.WriteString(preamble)
	for _, t := range d.Included() {
		writeTurn(&buf, t)
	}
	return buf.Bytes()
}

// Hash returns the hex SHA-256 of Bytes.
func (d *Document) Hash() string {
	h := sha256.Sum256(d.Bytes())
	return hex.EncodeToString(h[:])
}

// String returns the document as a string.
func (d *Document) String() string { return string(d.Bytes()) }

var fieldNames = map[string]bool{
	"turn": true, "state": true, "input": true, "answer": true,
	"label": true, "changes": true, "change": true,
}

// readField parses one `<name> <length>\n<bytes>\n` field strictly: a known
// name, one space, a canonical decimal length (no sign, no leading zero).
func readField(b []byte, off int) (name string, content []byte, next int, err error) {
	nl := bytes.IndexByte(b[off:], '\n')
	if nl < 0 {
		return "", nil, 0, errors.New("unexpected EOF in field header")
	}
	name, lenStr, ok := strings.Cut(string(b[off:off+nl]), " ")
	if !ok || !fieldNames[name] {
		return "", nil, 0, fmt.Errorf("invalid field header %q", b[off:off+nl])
	}
	if lenStr == "" || (len(lenStr) > 1 && lenStr[0] == '0') {
		return "", nil, 0, fmt.Errorf("non-canonical length %q", lenStr)
	}
	for _, c := range lenStr {
		if c < '0' || c > '9' {
			return "", nil, 0, fmt.Errorf("non-canonical length %q", lenStr)
		}
	}
	n, err := strconv.Atoi(lenStr)
	if err != nil {
		return "", nil, 0, fmt.Errorf("invalid length %q: %w", lenStr, err)
	}
	start := off + nl + 1
	if n > len(b)-start-1 {
		return "", nil, 0, fmt.Errorf("field %s: unexpected EOF", name)
	}
	if b[start+n] != '\n' {
		return "", nil, 0, fmt.Errorf("field %s: missing trailing newline", name)
	}
	return name, b[start : start+n], start + n + 1, nil
}

func peekName(b []byte, off int) string {
	if i := bytes.IndexByte(b[off:], ' '); i >= 0 {
		return string(b[off : off+i])
	}
	return ""
}

func parseChange(s string) (Change, error) {
	for _, o := range []string{OutcomeNotApplied, OutcomeApplied, OutcomeConflict} {
		if p, ok := strings.CutSuffix(s, " "+o); ok {
			c := Change{Path: p, Outcome: o}
			return c, ValidateChange(c)
		}
	}
	return Change{}, fmt.Errorf("invalid change %q", s)
}

// Parse reads a canonical document strictly. Anything but exactly what
// Bytes produces is rejected.
func Parse(b []byte) (*Document, error) {
	if !bytes.HasPrefix(b, []byte(preamble)) {
		return nil, errors.New("missing syndic-history/v1 preamble")
	}
	d := New()
	off := len(preamble)
	for off < len(b) {
		var t Turn
		for _, want := range []string{"turn", "state", "input", "answer", "label"} {
			if off >= len(b) {
				return nil, fmt.Errorf("unexpected EOF, expected field %s", want)
			}
			name, content, next, err := readField(b, off)
			if err != nil {
				return nil, err
			}
			if name != want {
				return nil, fmt.Errorf("expected field %s, got %s", want, name)
			}
			off = next
			switch name {
			case "turn":
				t.ID = string(content)
			case "state":
				t.State = string(content)
			case "input":
				t.Input = content
			case "answer":
				t.Answer = content
			case "label":
				t.Label = string(content)
			}
		}
		if isMissing(t.State) {
			if string(t.Answer) != missingLine(t.State) {
				return nil, fmt.Errorf("turn %q: %s turn must carry the missing-answer line", t.ID, t.State)
			}
			t.Answer = nil
		}
		if off < len(b) && peekName(b, off) == "changes" {
			_, content, next, err := readField(b, off)
			if err != nil {
				return nil, err
			}
			t.ChangesState = string(content)
			off = next
		}
		for off < len(b) && peekName(b, off) == "change" {
			_, content, next, err := readField(b, off)
			if err != nil {
				return nil, err
			}
			c, err := parseChange(string(content))
			if err != nil {
				return nil, err
			}
			t.Changes = append(t.Changes, c)
			off = next
		}
		if err := d.AddTurn(t); err != nil {
			return nil, err
		}
	}
	return d, nil
}

// Message is one rendered author message.
type Message struct {
	Role    string // "user" or "assistant"
	Content string
}

// RenderForAuthor renders each included turn as a user message and an
// assistant message. The user message carries every field except the answer,
// framed as in the document (turn, state, input, label, changes, change), so
// the id, state, label and annotations all reach the author unambiguously. The
// assistant message is the approved answer verbatim, or for a missing answer
// only the missing-answer line. Distinct documents render distinctly.
func (d *Document) RenderForAuthor() []Message {
	var msgs []Message
	for _, t := range d.Included() {
		var buf bytes.Buffer
		writeField(&buf, "turn", []byte(t.ID))
		writeField(&buf, "state", []byte(t.State))
		writeField(&buf, "input", t.Input)
		writeField(&buf, "label", []byte(t.Label))
		if t.ChangesState != "" {
			writeField(&buf, "changes", []byte(t.ChangesState))
		}
		for _, c := range t.Changes {
			writeField(&buf, "change", []byte(c.Path+" "+c.Outcome))
		}
		msgs = append(msgs,
			Message{Role: "user", Content: buf.String()},
			Message{Role: "assistant", Content: string(answerBytes(t))})
	}
	return msgs
}

// RenderForReviewer renders the whole canonical document as one block
// delimited as the v4 candidate is: a random delimiter from rand, with
// prompt.ErrDelimiterCollision if it appears in the document.
func (d *Document) RenderForReviewer(rand io.Reader) (string, error) {
	delim, err := prompt.NewDelimiter(rand)
	if err != nil {
		return "", err
	}
	doc := d.Bytes()
	if bytes.Contains(doc, []byte(delim)) {
		return "", prompt.ErrDelimiterCollision
	}
	return fmt.Sprintf("===== %s BEGIN HISTORY =====\n%s===== %s END HISTORY =====\n", delim, doc, delim), nil
}
