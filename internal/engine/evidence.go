package engine

import (
	"bytes"
	"fmt"
	"sort"

	"github.com/boldfield/syndic/internal/journal"
)

// UnifiedDiff represents a unified diff for one changed file.
type UnifiedDiff struct {
	Path     string
	Op       string
	BaseHash string
	NewHash  string
	Diff     string
}

// Diffs generates unified diffs for all paths in a change set, using the
// content provider to fetch base and new content by SHA-256 hash. The diffs
// are sorted by path.
func Diffs(cs journal.ChangeSet, getContent func(string) ([]byte, error)) ([]UnifiedDiff, error) {
	if len(cs.Paths) == 0 {
		return nil, nil
	}

	diffs := make([]UnifiedDiff, len(cs.Paths))
	for i, p := range cs.Paths {
		diff, err := diffPath(p, getContent)
		if err != nil {
			return nil, fmt.Errorf("diff for %q: %w", p.Path, err)
		}
		diffs[i] = diff
	}
	sort.Slice(diffs, func(i, j int) bool { return diffs[i].Path < diffs[j].Path })
	return diffs, nil
}

func diffPath(p journal.ChangeSetPath, getContent func(string) ([]byte, error)) (UnifiedDiff, error) {
	diff := UnifiedDiff{
		Path:     p.Path,
		Op:       p.Op,
		BaseHash: p.BaseSHA256,
		NewHash:  p.NewSHA256,
	}

	switch p.Op {
	case "create":
		newContent, err := getContent(p.NewSHA256)
		if err != nil {
			return diff, fmt.Errorf("fetch new content: %w", err)
		}
		diff.Diff = unifiedDiffCreate(p.Path, newContent)
	case "modify":
		baseContent, err := getContent(p.BaseSHA256)
		if err != nil {
			return diff, fmt.Errorf("fetch base content: %w", err)
		}
		newContent, err := getContent(p.NewSHA256)
		if err != nil {
			return diff, fmt.Errorf("fetch new content: %w", err)
		}
		diff.Diff = unifiedDiffModify(p.Path, baseContent, newContent)
	}

	return diff, nil
}

// unifiedDiffCreate returns a unified diff for a created file.
func unifiedDiffCreate(path string, content []byte) string {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "--- /dev/null\n")
	fmt.Fprintf(&buf, "+++ %s\n", path)
	fmt.Fprintf(&buf, "@@ -0,0 +1,%d @@\n", countLines(content))
	for _, line := range bytes.Split(content, []byte("\n")) {
		if len(line) > 0 || !bytes.Equal(line, []byte("")) {
			buf.WriteByte('+')
			buf.Write(line)
			buf.WriteByte('\n')
		} else if bytes.Equal(content, []byte("")) {
			// empty file
		} else {
			buf.WriteString("+\n")
		}
	}
	return buf.String()
}

// unifiedDiffModify returns a unified diff between base and new content.
func unifiedDiffModify(path string, base, new []byte) string {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "--- %s\n", path)
	fmt.Fprintf(&buf, "+++ %s\n", path)

	baseLines := bytes.Split(base, []byte("\n"))
	newLines := bytes.Split(new, []byte("\n"))

	// Simple diff: show all base lines as removed, then all new lines as added
	// This is a minimal implementation; a real diff would use Myers' algorithm
	if len(baseLines) > 0 || len(newLines) > 0 {
		fmt.Fprintf(&buf, "@@ -1,%d +1,%d @@\n", len(baseLines), len(newLines))
		for _, line := range baseLines {
			buf.WriteByte('-')
			buf.Write(line)
			buf.WriteByte('\n')
		}
		for _, line := range newLines {
			buf.WriteByte('+')
			buf.Write(line)
			buf.WriteByte('\n')
		}
	}

	return buf.String()
}

// countLines returns the number of lines in content (treating a final newline as not adding a line).
func countLines(content []byte) int {
	if len(content) == 0 {
		return 0
	}
	count := bytes.Count(content, []byte("\n"))
	if !bytes.HasSuffix(content, []byte("\n")) {
		count++
	}
	return count
}

// AuthorEvidence collects tool calls and results for the author's attempts
// in a turn, excluding results cited only by reviewers.
func AuthorEvidence(tx *journal.Tx, turnID string) ([]journal.ToolCall, error) {
	calls, err := tx.ToolCallsByTurn(turnID, "") // empty participant means all
	if err != nil {
		return nil, err
	}

	// Filter to only author calls; reviewer calls are not included in
	// the author evidence (they are included in revision round feedback separately).
	// This is determined by checking if the participant is the turn's author
	// (but we don't have direct access to the turn here, so this filtering
	// happens at the call site in the engine).
	return calls, nil
}

// ResultContent fetches content by SHA-256 from the journal.
func ResultContent(tx *journal.Tx, sha256 string) ([]byte, error) {
	return tx.Content(sha256)
}

// FormatDiff formats a unified diff for inclusion in a prompt.
func FormatDiff(d UnifiedDiff) string {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "--- Path: %s\n", d.Path)
	fmt.Fprintf(&buf, "--- Op: %s\n", d.Op)
	if d.BaseHash != "" {
		fmt.Fprintf(&buf, "--- Base: %s\n", d.BaseHash)
	}
	fmt.Fprintf(&buf, "--- New: %s\n", d.NewHash)
	buf.WriteString(d.Diff)
	return buf.String()
}

// FormatToolCall formats a tool call and result for inclusion in a prompt.
func FormatToolCall(call journal.ToolCall, result *journal.ToolResult, getContent func(string) ([]byte, error)) (string, error) {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "Tool Call ID: %s\n", call.ID)
	fmt.Fprintf(&buf, "Tool: %s\n", call.Name)
	fmt.Fprintf(&buf, "Arguments hash: %s (%d bytes)\n", call.ArgsSHA256, call.ArgsByteLen)

	if result != nil {
		fmt.Fprintf(&buf, "Result Status: %s\n", result.Status)
		if result.Status == journal.ToolCallExecuted || result.Status == journal.ToolCallError {
			content, err := getContent(result.ResultSHA256)
			if err != nil {
				return "", fmt.Errorf("fetch tool result: %w", err)
			}
			fmt.Fprintf(&buf, "Result (%d bytes):\n%s\n", result.ResultByteLen, content)
		}
	}

	return buf.String(), nil
}

// FormatAuthorEvidence builds the fenced evidence block for reviewer delivery,
// containing all author tool calls and results.
func FormatAuthorEvidence(calls []journal.ToolCall, getContent func(string) ([]byte, error), delimiter string) (string, error) {
	if len(calls) == 0 {
		return "", nil
	}

	var buf bytes.Buffer
	fmt.Fprintf(&buf, "===== %s BEGIN AUTHOR EVIDENCE =====\n", delimiter)

	for _, call := range calls {
		formatted, err := FormatToolCall(call, call.Result, getContent)
		if err != nil {
			return "", err
		}
		buf.WriteString(formatted)
		buf.WriteString("\n")
	}

	fmt.Fprintf(&buf, "===== %s END AUTHOR EVIDENCE =====\n", delimiter)
	return buf.String(), nil
}
