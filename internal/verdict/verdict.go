package verdict

import (
	"fmt"
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
			verdictCount++
			if openIdx == -1 {
				openIdx = i
			} else if closeIdx != -1 {
				// A second VERDICT v1 outside the envelope is malformed
				return Result{
					Classification: Malformed,
					Commentary:     text,
					Problem:        "multiple VERDICT v1 lines found",
				}
			}
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

	// Check for leading/trailing spaces on VERDICT v1 (opening line must be exactly)
	if strings.HasSuffix(strings.TrimPrefix(lines[openIdx], "VERDICT v1"), " ") {
		// This shouldn't happen since we check for exact match
		return Result{
			Classification: Malformed,
			Commentary:     text,
			Problem:        "opening line has trailing space",
		}
	}

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
	fieldOrder := []string{}
	lastMultilineField := ""
	problem := ""

	for _, line := range envelopeLines {
		if strings.Contains(line, ":") {
			parts := strings.SplitN(line, ":", 2)
			name := strings.TrimSpace(parts[0])
			value := strings.TrimSpace(parts[1])

			// Check if name is valid (lowercase letters and underscores)
			if !isValidFieldName(name) {
				return Result{
					Classification: Malformed,
					Commentary:     text,
					Problem:        fmt.Sprintf("unknown field: %s", name),
				}
			}

			// Check if field already exists
			if _, exists := fields[name]; exists {
				return Result{
					Classification: Malformed,
					Commentary:     text,
					Problem:        fmt.Sprintf("duplicate field: %s", name),
				}
			}

			fields[name] = value
			fieldOrder = append(fieldOrder, name)
			lastMultilineField = name
		} else {
			// Continuation line
			if lastMultilineField == "" {
				return Result{
					Classification: Malformed,
					Commentary:     text,
					Problem:        "continuation line without preceding field",
				}
			}

			// Only evidence, remedy, and reason can span multiple lines
			if lastMultilineField != "evidence" && lastMultilineField != "remedy" && lastMultilineField != "reason" {
				return Result{
					Classification: Malformed,
					Commentary:     text,
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
				Commentary:     text,
				Problem:        fmt.Sprintf("missing or empty required field: %s", field),
			}
		}
	}

	// Parse verdict value
	verdict := fields["verdict"]
	if verdict != "approve" && verdict != "block" && verdict != "approve-with-changes" {
		return Result{
			Classification: Malformed,
			Commentary:     text,
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
					Commentary:     text,
					Problem:        fmt.Sprintf("missing or empty required block field: %s", field),
				}
			}
		}

		// Validate severity
		severity := fields["severity"]
		if severity != "low" && severity != "medium" && severity != "high" {
			return Result{
				Classification: Malformed,
				Commentary:     text,
				Problem:        fmt.Sprintf("invalid severity value: %s", severity),
			}
		}
	} else {
		// For approve and approve-with-changes, none of the block-only fields should be present
		for _, field := range blockOnlyFields {
			if _, exists := fields[field]; exists {
				return Result{
					Classification: Malformed,
					Commentary:     text,
					Problem:        fmt.Sprintf("%s field not allowed for %s verdict", field, verdict),
				}
			}
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

	if problem != "" {
		result.Problem = problem
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
