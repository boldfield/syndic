package engine

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"sort"

	"github.com/boldfield/syndic/internal/candidate"
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
	case "delete":
		baseContent, err := getContent(p.BaseSHA256)
		if err != nil {
			return diff, fmt.Errorf("fetch base content: %w", err)
		}
		diff.Diff = unifiedDiffDelete(p.Path, baseContent)
	}

	return diff, nil
}

// unifiedDiffDelete returns a unified diff for a deleted file.
func unifiedDiffDelete(path string, content []byte) string {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "--- a/%s\n", path)
	fmt.Fprintf(&buf, "+++ /dev/null\n")

	lines := bytes.Split(content, []byte("\n"))
	// bytes.Split on content ending in \n produces an extra empty element
	if len(content) > 0 && bytes.HasSuffix(content, []byte("\n")) {
		lines = lines[:len(lines)-1]
	}

	lineCount := len(lines)
	if lineCount == 0 && len(content) > 0 {
		lineCount = 1
	}

	fmt.Fprintf(&buf, "@@ -1,%d +0,0 @@\n", lineCount)
	for _, line := range lines {
		buf.WriteByte('-')
		buf.Write(line)
		buf.WriteByte('\n')
	}

	// Add "\ No newline at end of file" marker if content doesn't end with newline
	if len(content) > 0 && !bytes.HasSuffix(content, []byte("\n")) {
		buf.WriteString("\\ No newline at end of file\n")
	}

	return buf.String()
}

// unifiedDiffCreate returns a unified diff for a created file.
func unifiedDiffCreate(path string, content []byte) string {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "--- /dev/null\n")
	fmt.Fprintf(&buf, "+++ b/%s\n", path)

	if len(content) == 0 {
		// Empty file has no lines
		fmt.Fprintf(&buf, "@@ -0,0 +1,0 @@\n")
		return buf.String()
	}

	lines := bytes.Split(content, []byte("\n"))
	// bytes.Split on content ending in \n produces an extra empty element
	if bytes.HasSuffix(content, []byte("\n")) {
		lines = lines[:len(lines)-1]
	}

	lineCount := len(lines)
	fmt.Fprintf(&buf, "@@ -0,0 +1,%d @@\n", lineCount)
	for _, line := range lines {
		buf.WriteByte('+')
		buf.Write(line)
		buf.WriteByte('\n')
	}

	// Add "\ No newline at end of file" marker if content doesn't end with newline
	if !bytes.HasSuffix(content, []byte("\n")) {
		buf.WriteString("\\ No newline at end of file\n")
	}

	return buf.String()
}

// unifiedDiffModify returns a unified diff between base and new content.
func unifiedDiffModify(path string, base, new []byte) string {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "--- a/%s\n", path)
	fmt.Fprintf(&buf, "+++ b/%s\n", path)

	baseLineBytes := bytes.Split(base, []byte("\n"))
	newLineBytes := bytes.Split(new, []byte("\n"))

	newNoNewline := len(new) > 0 && !bytes.HasSuffix(new, []byte("\n"))

	// bytes.Split on content ending in \n produces an extra empty element
	if len(base) > 0 && bytes.HasSuffix(base, []byte("\n")) {
		baseLineBytes = baseLineBytes[:len(baseLineBytes)-1]
	}
	if len(new) > 0 && bytes.HasSuffix(new, []byte("\n")) {
		newLineBytes = newLineBytes[:len(newLineBytes)-1]
	}

	if len(baseLineBytes) == 0 && len(newLineBytes) == 0 {
		return buf.String()
	}

	fmt.Fprintf(&buf, "@@ -1,%d +1,%d @@\n", len(baseLineBytes), len(newLineBytes))
	for _, line := range baseLineBytes {
		buf.WriteByte('-')
		buf.Write(line)
		buf.WriteByte('\n')
	}
	for _, line := range newLineBytes {
		buf.WriteByte('+')
		buf.Write(line)
		buf.WriteByte('\n')
	}

	// Add "\ No newline at end of file" marker if new content doesn't end with newline
	if newNoNewline {
		buf.WriteString("\\ No newline at end of file\n")
	}

	return buf.String()
}

// AuthorEvidence collects tool calls and results for the author's attempts
// in a turn, excluding results cited only by reviewers.
func AuthorEvidence(tx *journal.Tx, turnID, authorParticipant string) ([]journal.ToolCall, error) {
	// Query only author calls, not all participant calls
	calls, err := tx.ToolCallsByTurn(turnID, authorParticipant)
	if err != nil {
		return nil, err
	}
	return calls, nil
}

// ResultContent fetches content by SHA-256 from the journal.
func ResultContent(tx *journal.Tx, sha256 string) ([]byte, error) {
	return tx.Content(sha256)
}

// FormatDiff formats a unified diff for inclusion in a prompt.
// It includes metadata headers and the unified diff itself.
func FormatDiff(d UnifiedDiff) string {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "Path: %s\nOp: %s\n", d.Path, d.Op)
	if d.BaseHash != "" {
		fmt.Fprintf(&buf, "Base: %s\n", d.BaseHash)
	}
	fmt.Fprintf(&buf, "New: %s\n", d.NewHash)
	buf.WriteString(d.Diff)
	return buf.String()
}

// FormatToolCall formats a tool call and result for inclusion in a prompt.
func FormatToolCall(call journal.ToolCall, result *journal.ToolResult, getContent func(string) ([]byte, error)) (string, error) {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "Tool Call ID: %s\n", call.ID)
	fmt.Fprintf(&buf, "Tool: %s\n", call.Name)

	// Include call arguments verbatim, not just the hash
	args, err := getContent(call.ArgsSHA256)
	if err != nil {
		return "", fmt.Errorf("fetch tool call arguments: %w", err)
	}
	fmt.Fprintf(&buf, "Arguments (%d bytes):\n%s\n", len(args), args)

	if result != nil {
		fmt.Fprintf(&buf, "Result Status: %s\n", result.Status)
		if result.Status == journal.ToolCallExecuted || result.Status == journal.ToolCallError {
			content, err := getContent(result.ResultSHA256)
			if err != nil {
				return "", fmt.Errorf("fetch tool result: %w", err)
			}
			fmt.Fprintf(&buf, "Result (%d bytes):\n%s\n", len(content), content)
		}
	}

	return buf.String(), nil
}

// FormatAuthorEvidence builds the evidence block for reviewer delivery,
// containing all author tool calls and results verbatim. Fencing is handled
// by prompt.Reviewer, not here.
func FormatAuthorEvidence(calls []journal.ToolCall, getContent func(string) ([]byte, error)) (string, error) {
	if len(calls) == 0 {
		return "", nil
	}

	var buf bytes.Buffer

	for _, call := range calls {
		formatted, err := FormatToolCall(call, call.Result, getContent)
		if err != nil {
			return "", err
		}
		buf.WriteString(formatted)
		buf.WriteString("\n")
	}

	return buf.String(), nil
}

// BuildEvidencePairs builds evidence pairs from tool calls for the candidate hash.
// Each pair is {call_id:result_sha256} for calls with recorded results.
func BuildEvidencePairs(calls []journal.ToolCall) []candidate.EvidencePair {
	var pairs []candidate.EvidencePair
	for _, call := range calls {
		if call.Result != nil && call.Result.ResultSHA256 != "" {
			pairs = append(pairs, candidate.EvidencePair{
				CallID:       call.ID,
				ResultSHA256: call.Result.ResultSHA256,
			})
		}
	}
	return pairs
}

// CalculateChangesHash calculates the SHA-256 hash over canonical change set
// lines followed by the snapshot hash. For now, this returns a placeholder
// as the full change set freezing implementation is in progress.
func CalculateChangesHash(snapshotHash string) string {
	// Placeholder implementation: SHA-256 of "snapshot:<snapshot_hash>"
	// The full implementation will include the change set paths canonical form.
	line := fmt.Sprintf("snapshot:%s", snapshotHash)
	hash := sha256.Sum256([]byte(line))
	return fmt.Sprintf("%x", hash)
}
