// Package mcp holds the engine's side of the Model Context Protocol.
package mcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"github.com/boldfield/syndic/internal/mcp/jcs"
)

// ErrMalformedReply is wrapped by every error Render returns for a reply it
// cannot render.
var ErrMalformedReply = errors.New("mcp: malformed tools/call reply")

// ErrDuplicateMember is returned, wrapping ErrMalformedReply, for a reply
// with a duplicate object member name at any depth.
var ErrDuplicateMember = fmt.Errorf("%w: duplicate member name", ErrMalformedReply)

// ErrorLine is the first line of the rendering of a reply the server marked
// as an error.
const ErrorLine = "[the server reported an error]"

// Rendered is the outcome of rendering one tools/call result.
type Rendered struct {
	// Text is the rendering a model and the journal receive.
	Text string
	// IsError reports whether the server marked the reply as an error.
	IsError bool
	// RawText holds the text of each text block that entered the rendering,
	// in order, before any joining. It is empty when structuredContent was
	// rendered instead.
	RawText [][]byte
}

// Render turns the result object of a tools/call reply into its rendering.
// It depends on result alone, and reads no part of it beyond measuring a
// non-text block.
func Render(result []byte) (Rendered, error) {
	if _, err := jcs.Canonicalize(result); err != nil {
		if errors.Is(err, jcs.ErrDuplicateMember) {
			return Rendered{}, fmt.Errorf("%w (%v)", ErrDuplicateMember, err)
		}
		return Rendered{}, fmt.Errorf("%w: %v", ErrMalformedReply, err)
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(result, &obj); err != nil || obj == nil {
		return Rendered{}, fmt.Errorf("%w: result is not an object", ErrMalformedReply)
	}

	var out Rendered
	if raw, ok := obj["isError"]; ok {
		if isNull(raw) {
			return Rendered{}, fmt.Errorf("%w: isError is not a boolean", ErrMalformedReply)
		}
		if err := json.Unmarshal(raw, &out.IsError); err != nil {
			return Rendered{}, fmt.Errorf("%w: isError is not a boolean", ErrMalformedReply)
		}
	}

	var blocks []json.RawMessage
	if raw, ok := obj["content"]; ok {
		if isNull(raw) {
			return Rendered{}, fmt.Errorf("%w: content is not an array", ErrMalformedReply)
		}
		if err := json.Unmarshal(raw, &blocks); err != nil {
			return Rendered{}, fmt.Errorf("%w: content is not an array", ErrMalformedReply)
		}
	}

	var lines []string
	if out.IsError {
		lines = append(lines, ErrorLine)
	}

	if raw, ok := obj["structuredContent"]; ok {
		if isNull(raw) {
			return Rendered{}, fmt.Errorf("%w: structuredContent is null", ErrMalformedReply)
		}
		canon, err := jcs.Canonicalize(raw)
		if err != nil {
			return Rendered{}, fmt.Errorf("%w: structuredContent: %v", ErrMalformedReply, err)
		}
		lines = append(lines, string(canon), "["+strconv.Itoa(len(blocks))+" content blocks not shown]")
		out.Text = strings.Join(lines, "\n")
		return out, nil
	}

	for i, b := range blocks {
		var blk map[string]json.RawMessage
		if err := json.Unmarshal(b, &blk); err != nil || blk == nil {
			return Rendered{}, fmt.Errorf("%w: content block %d is not an object", ErrMalformedReply, i)
		}
		typ, ok := stringMember(blk["type"])
		if !ok {
			return Rendered{}, fmt.Errorf("%w: content block %d has no string type", ErrMalformedReply, i)
		}
		if strings.ContainsFunc(typ, unicode.IsControl) {
			return Rendered{}, fmt.Errorf("%w: content block %d has a type with a control character", ErrMalformedReply, i)
		}
		if typ != "text" {
			lines = append(lines, "["+typ+", "+strconv.Itoa(len(bytes.TrimSpace(b)))+" bytes]")
			continue
		}
		text, ok := stringMember(blk["text"])
		if !ok {
			return Rendered{}, fmt.Errorf("%w: content block %d has no string text", ErrMalformedReply, i)
		}
		out.RawText = append(out.RawText, []byte(text))
		lines = append(lines, text)
	}
	out.Text = strings.Join(lines, "\n")
	return out, nil
}

func isNull(raw json.RawMessage) bool { return string(bytes.TrimSpace(raw)) == "null" }

// stringMember decodes raw as a JSON string; absent and null are not strings.
func stringMember(raw json.RawMessage) (string, bool) {
	var s *string
	if err := json.Unmarshal(raw, &s); err != nil || s == nil {
		return "", false
	}
	return *s, true
}
