// Package deliberate holds deliberate mode's pure pieces.
package deliberate

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/boldfield/syndic/internal/history"
)

const header = "syndic-deliberation/v1\n"

// Document returns the deliberation document: the header, one proposal field
// for each proposer in roster order, one critique field for each, then the
// disposition field. Each field is framed as v5 frames a history field.
func Document(proposals, critiques [][]byte, disposition []byte) ([]byte, error) {
	if len(proposals) < 2 {
		return nil, fmt.Errorf("deliberation needs at least two proposals, got %d", len(proposals))
	}
	if len(proposals) != len(critiques) {
		return nil, fmt.Errorf("%d proposals but %d critiques", len(proposals), len(critiques))
	}
	var buf bytes.Buffer
	buf.WriteString(header)
	for _, p := range proposals {
		history.WriteField(&buf, "proposal", p)
	}
	for _, c := range critiques {
		history.WriteField(&buf, "critique", c)
	}
	history.WriteField(&buf, "disposition", disposition)
	return buf.Bytes(), nil
}

// Hash returns the SHA-256 of the document in lower-case hex.
func Hash(document []byte) string {
	h := sha256.Sum256(document)
	return hex.EncodeToString(h[:])
}

// readField reads one `<name> <length>\n<bytes>\n` field at off.
func readField(b []byte, off int) (name string, content []byte, next int, err error) {
	nl := bytes.IndexByte(b[off:], '\n')
	if nl < 0 {
		return "", nil, 0, errors.New("unexpected EOF in field header")
	}
	name, lenStr, ok := strings.Cut(string(b[off:off+nl]), " ")
	if !ok {
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
		return "", nil, 0, fmt.Errorf("field %s: length runs past the end", name)
	}
	if b[start+n] != '\n' {
		return "", nil, 0, fmt.Errorf("field %s: missing trailing newline", name)
	}
	return name, b[start : start+n], start + n + 1, nil
}

// Parse reads a deliberation document strictly, refusing anything but exactly
// what Document produces.
func Parse(document []byte) (proposals, critiques [][]byte, disposition []byte, err error) {
	if !bytes.HasPrefix(document, []byte(header)) {
		return nil, nil, nil, errors.New("missing syndic-deliberation/v1 header")
	}
	off := len(header)
	var disp []byte
	haveDisp := false
	for off < len(document) {
		name, content, next, err := readField(document, off)
		if err != nil {
			return nil, nil, nil, err
		}
		switch {
		case haveDisp:
			return nil, nil, nil, errors.New("bytes after the disposition field")
		case name == "proposal" && len(critiques) == 0:
			proposals = append(proposals, content)
		case name == "critique":
			critiques = append(critiques, content)
		case name == "disposition":
			disp, haveDisp = content, true
		default:
			return nil, nil, nil, fmt.Errorf("field %s out of order", name)
		}
		off = next
	}
	if !haveDisp {
		return nil, nil, nil, errors.New("missing disposition field")
	}
	if len(proposals) < 2 {
		return nil, nil, nil, fmt.Errorf("deliberation needs at least two proposals, got %d", len(proposals))
	}
	if len(proposals) != len(critiques) {
		return nil, nil, nil, fmt.Errorf("%d proposals but %d critiques", len(proposals), len(critiques))
	}
	return proposals, critiques, disp, nil
}
