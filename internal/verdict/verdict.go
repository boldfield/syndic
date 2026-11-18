package verdict

import (
	"fmt"
	"regexp"
	"strings"
)

type Classification string

const (
	Valid           Classification = "valid"
	RevisionRequest Classification = "revision_request"
	Rejected        Classification = "rejected"
	Malformed       Classification = "malformed"
)

type Verdict struct {
	Candidate, Attempt string
	Decision           string
	Severity, BlockID  string
	Evidence, Remedy   string
	Reason             string
}

type Result struct {
	Verdict        Verdict
	Classification Classification
	Commentary     string
	Problem        string
}

func Parse(text, candidateHash, attemptID string) Result {
	lines := strings.Split(text, "\n")

	// Handle CRLF: trim trailing \r from each line
	for i, line := range lines {
		lines[i] = strings.TrimSuffix(line, "\r")
	}

	// Find the opening and closing lines
	openIdx := -1
	closeIdx := -1
	verdictCount := 0

	for i, line := range lines {
		if line == "VERDICT v1" {
			if openIdx == -1 {
				// First VERDICT v1 found
				openIdx = i
			}
			verdictCount++
		}
		if line == "END VERDICT" && openIdx != -1 && closeIdx == -1 {
			closeIdx = i
		}
	}

	// Check if we found both opening and closing lines
	if openIdx == -1 || closeIdx == -1 {
		return Result{
			Classification: Malformed,
			Commentary:     text,
			Problem:        "no valid VERDICT v1 envelope found",
		}
	}

	// If a second VERDICT v1 was found, we'll mark the result as malformed
	// but only after parsing the first envelope

	// Extract commentary (before opening and after closing)
	commentBefore := strings.Join(lines[:openIdx], "\n")
	commentAfter := strings.Join(lines[closeIdx+1:], "\n")
	commentary := ""
	if commentBefore != "" && commentAfter != "" {
		commentary = commentBefore + "\n" + commentAfter
	} else if commentBefore != "" {
		commentary = commentBefore
	} else if commentAfter != "" {
		commentary = commentAfter
	}

	// Extract envelope lines
	envelopeLines := lines[openIdx+1 : closeIdx]

	// Parse the fields
	fields := make(map[string]string)
	lastMultilineField := ""
	fieldNameRegex := regexp.MustCompile(`^[a-z_]+: `)

	for _, line := range envelopeLines {
		if fieldNameRegex.MatchString(line) {
			// This is a field line
			parts := strings.SplitN(line, ": ", 2)
			name := parts[0]
			value := parts[1]

			// Check if name is valid (lowercase letters and underscores)
			if !isValidFieldName(name) {
				return Result{
					Classification: Malformed,
					Commentary:     commentary,
					Verdict:        buildPartialVerdict(fields),
					Problem:        fmt.Sprintf("unknown field: %s", name),
				}
			}

			// Check if field already exists
			if _, exists := fields[name]; exists {
				return Result{
					Classification: Malformed,
					Commentary:     commentary,
					Verdict:        buildPartialVerdict(fields),
					Problem:        fmt.Sprintf("duplicate field: %s", name),
				}
			}

			fields[name] = value
			lastMultilineField = name
		} else {
			// Continuation line
			if lastMultilineField == "" {
				return Result{
					Classification: Malformed,
					Commentary:     commentary,
					Problem:        "continuation line without preceding field",
				}
			}

			// Only evidence, remedy, and reason can span multiple lines
			if lastMultilineField != "evidence" && lastMultilineField != "remedy" && lastMultilineField != "reason" {
				return Result{
					Classification: Malformed,
					Commentary:     commentary,
					Verdict:        buildPartialVerdict(fields),
					Problem:        fmt.Sprintf("continuation line after single-line field: %s", lastMultilineField),
				}
			}

			fields[lastMultilineField] += "\n" + line
		}
	}

	// Check for required fields
	requiredFields := []string{"candidate", "attempt", "verdict", "reason"}
	for _, field := range requiredFields {
		val, exists := fields[field]
		if !exists || strings.TrimSpace(val) == "" {
			return Result{
				Classification: Malformed,
				Commentary:     commentary,
				Verdict:        buildPartialVerdict(fields),
				Problem:        fmt.Sprintf("missing or empty required field: %s", field),
			}
		}
	}

	// Parse verdict value
	verdict := fields["verdict"]
	if verdict != "approve" && verdict != "block" && verdict != "approve-with-changes" {
		return Result{
			Classification: Malformed,
			Commentary:     commentary,
			Verdict:        buildPartialVerdict(fields),
			Problem:        fmt.Sprintf("invalid verdict value: %s", verdict),
		}
	}

	// Check block-only fields
	blockOnlyFields := []string{"severity", "block_id", "evidence", "remedy"}
	if verdict == "block" {
		// All block-only fields must be present and non-empty
		for _, field := range blockOnlyFields {
			val, exists := fields[field]
			if !exists || strings.TrimSpace(val) == "" {
				return Result{
					Classification: Malformed,
					Commentary:     commentary,
					Verdict:        buildPartialVerdict(fields),
					Problem:        fmt.Sprintf("missing or empty required block field: %s", field),
				}
			}
		}

		// Validate severity
		severity := fields["severity"]
		if severity != "low" && severity != "medium" && severity != "high" {
			return Result{
				Classification: Malformed,
				Commentary:     commentary,
				Verdict:        buildPartialVerdict(fields),
				Problem:        fmt.Sprintf("invalid severity value: %s", severity),
			}
		}
	} else {
		// For approve and approve-with-changes, none of the block-only fields should be present
		for _, field := range blockOnlyFields {
			if _, exists := fields[field]; exists {
				return Result{
					Classification: Malformed,
					Commentary:     commentary,
					Verdict:        buildPartialVerdict(fields),
					Problem:        fmt.Sprintf("%s field not allowed for %s verdict", field, verdict),
				}
			}
		}
	}

	// A second VERDICT v1 anywhere in the text is malformed
	if verdictCount > 1 {
		return Result{
			Classification: Malformed,
			Commentary:     commentary,
			Verdict:        buildPartialVerdict(fields),
			Problem:        "multiple VERDICT v1 lines found",
		}
	}

	// Build the Verdict struct
	result := Result{
		Verdict: Verdict{
			Candidate: strings.TrimSpace(fields["candidate"]),
			Attempt:   strings.TrimSpace(fields["attempt"]),
			Decision:  verdict,
			Reason:    strings.TrimSpace(fields["reason"]),
		},
		Commentary: commentary,
	}

	if verdict == "block" {
		result.Verdict.Severity = strings.TrimSpace(fields["severity"])
		result.Verdict.BlockID = strings.TrimSpace(fields["block_id"])
		result.Verdict.Evidence = strings.TrimSpace(fields["evidence"])
		result.Verdict.Remedy = strings.TrimSpace(fields["remedy"])
	}

	// Check if candidate and attempt match
	if result.Verdict.Candidate != candidateHash {
		result.Classification = Rejected
		result.Problem = "candidate hash mismatch"
		return result
	}

	if result.Verdict.Attempt != attemptID {
		result.Classification = Rejected
		result.Problem = "attempt ID mismatch"
		return result
	}

	// Classify based on verdict type
	if verdict == "approve-with-changes" {
		result.Classification = RevisionRequest
	} else {
		result.Classification = Valid
	}

	return result
}

func isValidFieldName(name string) bool {
	validFields := map[string]bool{
		"candidate": true,
		"attempt":   true,
		"verdict":   true,
		"severity":  true,
		"block_id":  true,
		"evidence":  true,
		"remedy":    true,
		"reason":    true,
	}
	return validFields[name]
}

// buildPartialVerdict constructs a Verdict from parsed fields, trimming values appropriately.
func buildPartialVerdict(fields map[string]string) Verdict {
	return Verdict{
		Candidate: strings.TrimSpace(fields["candidate"]),
		Attempt:   strings.TrimSpace(fields["attempt"]),
		Decision:  fields["verdict"],
		Severity:  strings.TrimSpace(fields["severity"]),
		BlockID:   strings.TrimSpace(fields["block_id"]),
		Evidence:  strings.TrimSpace(fields["evidence"]),
		Remedy:    strings.TrimSpace(fields["remedy"]),
		Reason:    strings.TrimSpace(fields["reason"]),
	}
}
