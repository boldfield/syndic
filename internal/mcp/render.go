// Package mcp holds the engine's side of the Model Context Protocol.
package mcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

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
	if raw, ok := obj["isError"]; ok && !isNull(raw) {
		if err := json.Unmarshal(raw, &out.IsError); err != nil {
			return Rendered{}, fmt.Errorf("%w: isError is not a boolean", ErrMalformedReply)
		}
	}

	var blocks []json.RawMessage
	if raw, ok := obj["content"]; ok && !isNull(raw) {
		if err := json.Unmarshal(raw, &blocks); err != nil {
			return Rendered{}, fmt.Errorf("%w: content is not an array", ErrMalformedReply)
		}
	}

	var lines []string
	if out.IsError {
		lines = append(lines, ErrorLine)
	}

	if raw, ok := obj["structuredContent"]; ok && !isNull(raw) {
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
		var typ string
		if err := json.Unmarshal(blk["type"], &typ); err != nil {
			return Rendered{}, fmt.Errorf("%w: content block %d has no string type", ErrMalformedReply, i)
		}
		if typ != "text" {
			lines = append(lines, "["+typ+", "+strconv.Itoa(len(bytes.TrimSpace(b)))+" bytes]")
			continue
		}
		var text string
		if err := json.Unmarshal(blk["text"], &text); err != nil {
			return Rendered{}, fmt.Errorf("%w: content block %d has no string text", ErrMalformedReply, i)
		}
		out.RawText = append(out.RawText, []byte(text))
		lines = append(lines, text)
	}
	out.Text = strings.Join(lines, "\n")
	return out, nil
}

func isNull(raw json.RawMessage) bool { return string(bytes.TrimSpace(raw)) == "null" }
