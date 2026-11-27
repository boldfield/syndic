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
)

const (
	preambleV1 = "syndic-history/v1\n"
	preambleV2 = "syndic-history/v2\n"
)

// MaxHistoryResultBytes is the default mcp.max_history_result_bytes: a
// rendered effect reply over it is carried as its size and hash.
const MaxHistoryResultBytes = 4096

// ErrDelimiterCollision is returned when a delimiter appears in a document.
var ErrDelimiterCollision = errors.New("delimiter appears in content")

// NewDelimiter reads 16 bytes from rand and returns them as 32 lowercase hex characters.
func NewDelimiter(rand io.Reader) (string, error) {
	b := make([]byte, 16)
	_, err := io.ReadFull(rand, b)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

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
	// EffectsState is the effect set's state; empty when the turn has none.
	// Only a v2 document carries it.
	EffectsState string
	Effects      []Effect
}

// Effect states as written in history. A sent effect keeps its own state
// whatever later happened to its set; only an effect that was never sent is
// NotRun.
const (
	EffectDone      = "done"
	EffectFailed    = "failed"
	EffectUncertain = "uncertain"
	EffectNotRun    = "not run"
)

// Effect is one effect of a turn's effect set as history carries it.
type Effect struct {
	ID        string
	Server    string
	Tool      string
	Arguments []byte // canonical JSON
	State     string // done, failed, uncertain or not run
	// Result is the rendered reply of a done or failed effect. ResultRef is
	// set instead, as "<size> <sha256 hex>", when the reply is over
	// MaxHistoryResultBytes; Parse fills it from a result-ref sub-field and the
	// writer derives it from Result, so callers set Result only.
	Result    []byte
	ResultRef string
}

// HistoryEffectState maps a journaled effect state to the state history
// writes: pending and skipped are "not run" and nothing else is.
// dispatching never appears in a history document and is refused.
func HistoryEffectState(journaled string) (string, error) {
	switch journaled {
	case "pending", "skipped":
		return EffectNotRun, nil
	case "done", "failed", "uncertain":
		return journaled, nil
	}
	return "", fmt.Errorf("effect state %q has no history form", journaled)
}

// ResultRef returns the result-ref value for a reply: its size in decimal, a
// space, and its SHA-256 in hex.
func ResultRef(result []byte) string {
	h := sha256.Sum256(result)
	return strconv.Itoa(len(result)) + " " + hex.EncodeToString(h[:])
}

// Change is one path's own outcome within a change set.
type Change struct {
	Path    string
	Outcome string
}

// Document is a canonical syndic-history document under construction. New
// builds v1 and NewV2 builds v2, the only version that carries effects; Parse
// keeps the version of the document it reads, so a v1 document and its stored
// hash still verify.
type Document struct {
	v2              bool
	turns           []Turn
	contextFromTurn string
}

// New returns an empty syndic-history/v1 Document, which carries no effects.
func New() *Document { return &Document{} }

// NewV2 returns an empty syndic-history/v2 Document.
func NewV2() *Document { return &Document{v2: true} }

func (d *Document) preamble() string {
	if d.v2 {
		return preambleV2
	}
	return preambleV1
}

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

// ValidateChange checks one per-path change. A path may hold any non-empty
// byte sequence, a space or line break included: the field is byte-framed and
// its payload is the outcome, one space, then the path (see changePayload), so
// no path is refused or read back differently.
func ValidateChange(c Change) error {
	switch c.Outcome {
	case OutcomeApplied, OutcomeNotApplied, OutcomeConflict:
	default:
		return fmt.Errorf("invalid change outcome: %q", c.Outcome)
	}
	if c.Path == "" {
		return errors.New("invalid change: empty path")
	}
	return nil
}

// ValidateEffectsState checks an effect-set state.
func ValidateEffectsState(state string) error {
	switch state {
	case "in flight", "discarded", "applied", "stopped", "closed", "appliable", "resumable":
		return nil
	}
	return fmt.Errorf("invalid effects state: %q", state)
}

// ValidateEffect checks one effect.
func ValidateEffect(e Effect) error {
	if e.ID == "" || e.Server == "" || e.Tool == "" {
		return errors.New("invalid effect: empty id, server or tool")
	}
	switch e.State {
	case EffectDone, EffectFailed:
		if e.ResultRef != "" {
			if len(e.Result) > 0 {
				return errors.New("invalid effect: result and result-ref")
			}
			return validateResultRef(e.ResultRef)
		}
	case EffectUncertain, EffectNotRun:
		if len(e.Result) > 0 || e.ResultRef != "" {
			return fmt.Errorf("invalid effect: %s carries no result", e.State)
		}
	default:
		return fmt.Errorf("invalid effect state: %q", e.State)
	}
	return nil
}

func validateResultRef(ref string) error {
	size, sum, ok := strings.Cut(ref, " ")
	if !ok || size == "" || (len(size) > 1 && size[0] == '0') || len(sum) != 64 {
		return fmt.Errorf("invalid result-ref %q", ref)
	}
	for _, c := range size {
		if c < '0' || c > '9' {
			return fmt.Errorf("invalid result-ref %q", ref)
		}
	}
	for _, c := range sum {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return fmt.Errorf("invalid result-ref %q", ref)
		}
	}
	n, err := strconv.Atoi(size)
	if err != nil || n <= MaxHistoryResultBytes {
		return fmt.Errorf("invalid result-ref %q: size not over the bound", ref)
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
	if t.EffectsState != "" {
		if err := ValidateEffectsState(t.EffectsState); err != nil {
			return err
		}
	} else if len(t.Effects) > 0 {
		return errors.New("invalid turn: effect fields require an effects state")
	}
	for _, e := range t.Effects {
		if err := ValidateEffect(e); err != nil {
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
	if !d.v2 && t.EffectsState != "" {
		return errors.New("invalid turn: a v1 document carries no effects")
	}
	t.Effects = normalizeEffects(t.Effects)
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

// normalizeEffects derives the result-ref of an over-bound result, so the
// stored form is the one the writer emits and Parse returns.
func normalizeEffects(in []Effect) []Effect {
	if in == nil {
		return nil
	}
	out := make([]Effect, len(in))
	for i, e := range in {
		if len(e.Result) > MaxHistoryResultBytes {
			e.ResultRef = ResultRef(e.Result)
			e.Result = nil
		}
		out[i] = e
	}
	return out
}

func effectPayload(e Effect) []byte {
	var buf bytes.Buffer
	writeField(&buf, "id", []byte(e.ID))
	writeField(&buf, "server", []byte(e.Server))
	writeField(&buf, "tool", []byte(e.Tool))
	writeField(&buf, "arguments", e.Arguments)
	writeField(&buf, "state", []byte(e.State))
	if e.State == EffectDone || e.State == EffectFailed {
		if e.ResultRef != "" {
			writeField(&buf, "result-ref", []byte(e.ResultRef))
		} else {
			writeField(&buf, "result", e.Result)
		}
	}
	return buf.Bytes()
}

func writeEffects(buf *bytes.Buffer, t Turn) {
	if t.EffectsState == "" {
		return
	}
	writeField(buf, "effects", []byte(t.EffectsState))
	for _, e := range t.Effects {
		writeField(buf, "effect", effectPayload(e))
	}
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
		writeField(buf, "change", changePayload(c))
	}
	writeEffects(buf, t)
}

// Bytes returns the canonical document.
func (d *Document) Bytes() []byte {
	var buf bytes.Buffer
	buf.WriteString(d.preamble())
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
	"effects": true, "effect": true,
}

var effectFieldNames = map[string]bool{
	"id": true, "server": true, "tool": true, "arguments": true,
	"state": true, "result": true, "result-ref": true,
}

// readField parses one `<name> <length>\n<bytes>\n` field strictly: a known
// name, one space, a canonical decimal length (no sign, no leading zero).
func readField(b []byte, off int) (name string, content []byte, next int, err error) {
	return readFieldIn(b, off, fieldNames)
}

func readFieldIn(b []byte, off int, names map[string]bool) (name string, content []byte, next int, err error) {
	nl := bytes.IndexByte(b[off:], '\n')
	if nl < 0 {
		return "", nil, 0, errors.New("unexpected EOF in field header")
	}
	name, lenStr, ok := strings.Cut(string(b[off:off+nl]), " ")
	if !ok || !names[name] {
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

// changePayload is the payload of a `change` field: the outcome, one space,
// then the path's bytes unchanged to the end of the field.
func changePayload(c Change) []byte {
	return []byte(c.Outcome + " " + c.Path)
}

// parseChange reverses changePayload. No outcome followed by a space is a
// prefix of another, so at most one matches and the rest is the path.
func parseChange(s string) (Change, error) {
	for _, o := range []string{OutcomeApplied, OutcomeNotApplied, OutcomeConflict} {
		if p, ok := strings.CutPrefix(s, o+" "); ok {
			c := Change{Path: p, Outcome: o}
			return c, ValidateChange(c)
		}
	}
	return Change{}, fmt.Errorf("invalid change %q", s)
}

// parseEffect reverses effectPayload, strictly: id, server, tool, arguments
// and state in order, then for done and failed exactly one of result and
// result-ref, which must be the form the writer would choose.
func parseEffect(b []byte) (Effect, error) {
	var e Effect
	off := 0
	next := func(want string) ([]byte, error) {
		if off >= len(b) {
			return nil, fmt.Errorf("effect: unexpected end, expected %s", want)
		}
		name, content, n, err := readFieldIn(b, off, effectFieldNames)
		if err != nil {
			return nil, fmt.Errorf("effect: %w", err)
		}
		if name != want {
			return nil, fmt.Errorf("effect: expected field %s, got %s", want, name)
		}
		off = n
		return content, nil
	}
	for _, want := range []string{"id", "server", "tool", "arguments", "state"} {
		c, err := next(want)
		if err != nil {
			return Effect{}, err
		}
		switch want {
		case "id":
			e.ID = string(c)
		case "server":
			e.Server = string(c)
		case "tool":
			e.Tool = string(c)
		case "arguments":
			e.Arguments = c
		case "state":
			e.State = string(c)
		}
	}
	if e.State == EffectDone || e.State == EffectFailed {
		if off >= len(b) {
			return Effect{}, errors.New("effect: missing result")
		}
		name, c, n, err := readFieldIn(b, off, effectFieldNames)
		if err != nil {
			return Effect{}, fmt.Errorf("effect: %w", err)
		}
		switch name {
		case "result":
			if len(c) > MaxHistoryResultBytes {
				return Effect{}, errors.New("effect: result over the history bound")
			}
			e.Result = c
		case "result-ref":
			e.ResultRef = string(c)
		default:
			return Effect{}, fmt.Errorf("effect: expected result or result-ref, got %s", name)
		}
		off = n
	}
	if off != len(b) {
		return Effect{}, errors.New("effect: trailing bytes")
	}
	return e, ValidateEffect(e)
}

// Parse reads a canonical document strictly. Anything but exactly what
// Bytes produces is rejected.
func Parse(b []byte) (*Document, error) {
	d := New()
	switch {
	case bytes.HasPrefix(b, []byte(preambleV2)):
		d.v2 = true
	case bytes.HasPrefix(b, []byte(preambleV1)):
	default:
		return nil, errors.New("missing syndic-history/v1 or v2 preamble")
	}
	off := len(d.preamble())
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
		if off < len(b) && peekName(b, off) == "effects" {
			_, content, next, err := readField(b, off)
			if err != nil {
				return nil, err
			}
			t.EffectsState = string(content)
			off = next
		}
		for off < len(b) && peekName(b, off) == "effect" {
			_, content, next, err := readField(b, off)
			if err != nil {
				return nil, err
			}
			e, err := parseEffect(content)
			if err != nil {
				return nil, err
			}
			t.Effects = append(t.Effects, e)
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
			writeField(&buf, "change", changePayload(c))
		}
		writeEffects(&buf, t)
		msgs = append(msgs,
			Message{Role: "user", Content: buf.String()},
			Message{Role: "assistant", Content: string(answerBytes(t))})
	}
	return msgs
}

// RenderForReviewer renders the whole canonical document as one block
// delimited by a random delimiter from rand, returning ErrDelimiterCollision if
// it appears in the document.
func (d *Document) RenderForReviewer(rand io.Reader) (string, error) {
	delim, err := NewDelimiter(rand)
	if err != nil {
		return "", err
	}
	doc := d.Bytes()
	if bytes.Contains(doc, []byte(delim)) {
		return "", ErrDelimiterCollision
	}
	return fmt.Sprintf("===== %s BEGIN HISTORY =====\n%s===== %s END HISTORY =====\n", delim, doc, delim), nil
}

// WriteField appends one `<name> <byte-length>\n<bytes>\n` field to buf, the
// framing other canonical documents reuse.
func WriteField(buf *bytes.Buffer, name string, content []byte) {
	writeField(buf, name, content)
}
