// Package deliberate holds the pure parts of deliberate mode. It imports
// nothing from the engine, the journal or a transport.
package deliberate

import (
	"bytes"
	"errors"
	"fmt"
)

// The two marker lines of the integration envelope, exactly these bytes.
const (
	AnswerMarker      = "=== SYNDIC ANSWER ==="
	DispositionMarker = "=== SYNDIC DISPOSITION ==="
)

// Reason is the code a malformed envelope carries.
type Reason string

const (
	ReasonTextBeforeAnswer    Reason = "text_before_answer"
	ReasonAnswerMarkerMissing Reason = "answer_marker_missing"
	ReasonDispositionMissing  Reason = "disposition_marker_missing"
	ReasonMarkerRepeated      Reason = "marker_repeated"
	ReasonMarkersOutOfOrder   Reason = "markers_out_of_order"
	ReasonEmptyAnswer         Reason = "empty_answer"
	ReasonEmptyDisposition    Reason = "empty_disposition"
)

// MalformedError reports an integration result that is not a valid envelope.
type MalformedError struct {
	Reason Reason
}

func (e *MalformedError) Error() string {
	return fmt.Sprintf("malformed integration envelope: %s", e.Reason)
}

// ReasonOf returns the reason code of a malformed-envelope error.
func ReasonOf(err error) (Reason, bool) {
	var m *MalformedError
	if errors.As(err, &m) {
		return m.Reason, true
	}
	return "", false
}

func malformed(r Reason) error { return &MalformedError{Reason: r} }

// ParseEnvelope splits an integration result into the answer and the
// disposition.
//
// A marker is a whole line: the marker's exact bytes followed by a line feed,
// or by the end of the result when it is the last line. A line ending of CR LF
// therefore does not make a marker (the CR is part of the line), and neither
// do trailing spaces; such a near-marker is ordinary text. "Empty" for the
// lines before the answer marker means zero bytes, so a line holding only a CR
// or spaces is text. The answer and the disposition are checked for being
// empty or white space only after the structure is.
//
// The answer is the bytes between the two marker lines and the disposition the
// bytes after the second, each without the line break that adjoins a marker.
// Any other bytes, including a trailing line break at the end of the result,
// are kept.
func ParseEnvelope(result []byte) (answer, disposition []byte, err error) {
	type marker struct{ start, end int } // end is past the marker's bytes
	var answers, dispositions []marker

	for pos := 0; pos <= len(result); {
		nl := bytes.IndexByte(result[pos:], '\n')
		lineEnd := len(result)
		if nl >= 0 {
			lineEnd = pos + nl
		}
		line := result[pos:lineEnd]
		switch string(line) {
		case AnswerMarker:
			answers = append(answers, marker{pos, lineEnd})
		case DispositionMarker:
			dispositions = append(dispositions, marker{pos, lineEnd})
		}
		if nl < 0 {
			break
		}
		pos = lineEnd + 1
	}

	switch {
	case len(answers) == 0:
		return nil, nil, malformed(ReasonAnswerMarkerMissing)
	case len(dispositions) == 0:
		return nil, nil, malformed(ReasonDispositionMissing)
	case len(answers) > 1 || len(dispositions) > 1:
		return nil, nil, malformed(ReasonMarkerRepeated)
	}
	a, d := answers[0], dispositions[0]
	if d.start < a.start {
		return nil, nil, malformed(ReasonMarkersOutOfOrder)
	}

	// The first line that is not empty must be the answer marker.
	for _, line := range bytes.Split(result[:a.start], []byte{'\n'}) {
		if len(line) != 0 {
			return nil, nil, malformed(ReasonTextBeforeAnswer)
		}
	}

	// Answer: after the answer marker's line break, before the line break
	// that precedes the disposition marker.
	if a.end < len(result) { // always true here, a disposition marker follows
		ansStart := a.end + 1
		ansEnd := d.start - 1
		if ansEnd >= ansStart {
			answer = result[ansStart:ansEnd]
		}
	}
	if d.end < len(result) {
		disposition = result[d.end+1:]
	}

	if len(bytes.TrimSpace(answer)) == 0 {
		return nil, nil, malformed(ReasonEmptyAnswer)
	}
	if len(bytes.TrimSpace(disposition)) == 0 {
		return nil, nil, malformed(ReasonEmptyDisposition)
	}
	return answer, disposition, nil
}

// RenderEnvelope writes the envelope form. Parsing the output returns the same
// two values for any answer and disposition that contain no marker line and
// are not empty or white space only.
func RenderEnvelope(answer, disposition []byte) []byte {
	var b bytes.Buffer
	b.WriteString(AnswerMarker)
	b.WriteByte('\n')
	b.Write(answer)
	b.WriteByte('\n')
	b.WriteString(DispositionMarker)
	b.WriteByte('\n')
	b.Write(disposition)
	return b.Bytes()
}
