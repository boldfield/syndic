package verdict

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var (
	fieldNameRegex = regexp.MustCompile(`^[a-z_-]+: `)
	validFieldsV1  = map[string]bool{
		"candidate": true,
		"attempt":   true,
		"verdict":   true,
		"severity":  true,
		"block_id":  true,
		"evidence":  true,
		"remedy":    true,
		"reason":    true,
	}
	validFieldsV2 = map[string]bool{
		"candidate":   true,
		"attempt":     true,
		"verdict":     true,
		"severity":    true,
		"block_id":    true,
		"evidence":    true,
		"remedy":      true,
		"reason":      true,
		"round":       true,
		"block-state": true,
	}
)

type Classification string

const (
	Valid           Classification = "valid"
	RevisionRequest Classification = "revision_request"
	Rejected        Classification = "rejected"
	Malformed       Classification = "malformed"
)

type BlockState struct {
	BlockID string
	State   string // "withdrawn" or "maintained"
}

type Verdict struct {
	Candidate, Attempt string
	Decision           string
	Severity, BlockID  string
	Evidence, Remedy   string
	Reason             string
	Round              int          // see EnvelopeVersion
	BlockStates        []BlockState // only for v2
	EnvelopeVersion    string       // "v1" or "v2"; v1 has Round=0, v2 has Round>=0
}

type Result struct {
	Verdict        Verdict
	Classification Classification
	Commentary     string
	Problem        string
}

type V2Context struct {
	Round             int
	OwnerOpenBlockIDs map[string]bool
}

func Parse(text, candidateHash, attemptID string) Result {
	return ParseWithContext(text, candidateHash, attemptID, nil)
}

func ParseWithContext(text, candidateHash, attemptID string, v2Ctx *V2Context) Result {
	lines := strings.Split(text, "\n")

	// Handle CRLF: trim trailing \r from each line
	for i, line := range lines {
		lines[i] = strings.TrimSuffix(line, "\r")
	}

	// Find the opening and closing lines
	openIdx := -1
	closeIdx := -1
	verdictCount := 0
	envelopeVersion := ""

	for i, line := range lines {
		if line == "VERDICT v1" || line == "VERDICT v2" {
			if openIdx == -1 {
				// First VERDICT found
				openIdx = i
				if line == "VERDICT v1" {
					envelopeVersion = "v1"
				} else {
					envelopeVersion = "v2"
				}
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
	blockStates := []BlockState{}
	lastMultilineField := ""

	for _, line := range envelopeLines {
		if fieldNameRegex.MatchString(line) {
			// This is a field line
			parts := strings.SplitN(line, ": ", 2)
			name := parts[0]
			value := parts[1]

			// Check if name is valid (lowercase letters, underscores, and hyphens)
			if !isValidFieldName(name, envelopeVersion) {
				// Check if this is a v2-only field in a v1 envelope
				if envelopeVersion == "v1" && (name == "round" || name == "block-state") {
					return Result{
						Classification: Malformed,
						Commentary:     commentary,
						Verdict:        buildPartialVerdict(fields),
						Problem:        fmt.Sprintf("field %s is only allowed in VERDICT v2, not v1", name),
					}
				}
				return Result{
					Classification: Malformed,
					Commentary:     commentary,
					Verdict:        buildPartialVerdict(fields),
					Problem:        fmt.Sprintf("unknown field: %s", name),
				}
			}

			// Handle block-state specially (can appear multiple times)
			if name == "block-state" {
				stateParts := strings.SplitN(value, " ", 2)
				if len(stateParts) != 2 {
					return Result{
						Classification: Malformed,
						Commentary:     commentary,
						Verdict:        buildPartialVerdict(fields),
						Problem:        "block-state must have format: block-state: <block_id> <state>",
					}
				}
				blockID := strings.TrimSpace(stateParts[0])
				state := strings.TrimSpace(stateParts[1])
				if blockID == "" {
					return Result{
						Classification: Malformed,
						Commentary:     commentary,
						Verdict:        buildPartialVerdict(fields),
						Problem:        "block-state block_id cannot be empty",
					}
				}
				if state != "withdrawn" && state != "maintained" {
					return Result{
						Classification: Malformed,
						Commentary:     commentary,
						Verdict:        buildPartialVerdict(fields),
						Problem:        fmt.Sprintf("invalid block-state value: %s (must be 'withdrawn' or 'maintained')", state),
					}
				}
				// Check for duplicate block-state lines for the same block
				for _, bs := range blockStates {
					if bs.BlockID == blockID {
						return Result{
							Classification: Malformed,
							Commentary:     commentary,
							Verdict:        buildPartialVerdict(fields),
							Problem:        fmt.Sprintf("duplicate block-state line for block %s", blockID),
						}
					}
				}
				blockStates = append(blockStates, BlockState{BlockID: blockID, State: state})
				lastMultilineField = ""
				continue
			}

			// Check if field already exists (block-state is handled above)
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

	// For v2, round is required
	if envelopeVersion == "v2" {
		roundStr, exists := fields["round"]
		if !exists || strings.TrimSpace(roundStr) == "" {
			return Result{
				Classification: Malformed,
				Commentary:     commentary,
				Verdict:        buildPartialVerdict(fields),
				Problem:        "missing or empty required field for VERDICT v2: round",
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

	// A context that demands v2 (a later round, or open blocks needing a
	// disposition) cannot be satisfied by a v1 envelope, which carries neither.
	if envelopeVersion == "v1" && v2Ctx != nil && (v2Ctx.Round > 0 || len(v2Ctx.OwnerOpenBlockIDs) > 0) {
		return Result{
			Classification: Malformed,
			Commentary:     commentary,
			Verdict:        buildPartialVerdict(fields),
			Problem:        "VERDICT v1 not accepted after the first round or with open blocks; VERDICT v2 required",
		}
	}

	// Parse round value for v2
	var roundValue int
	if envelopeVersion == "v2" {
		// V2 without context is malformed (fail closed)
		if v2Ctx == nil {
			return Result{
				Classification: Malformed,
				Commentary:     commentary,
				Verdict:        buildPartialVerdict(fields),
				Problem:        "VERDICT v2 requires validation context (round and owner open blocks)",
			}
		}

		roundStr := strings.TrimSpace(fields["round"])
		var err error
		roundValue, err = parseRound(roundStr)
		if err != nil {
			return Result{
				Classification: Malformed,
				Commentary:     commentary,
				Verdict:        buildPartialVerdict(fields),
				Problem:        fmt.Sprintf("invalid round value: %s", err.Error()),
			}
		}

		// Validate round matches expected round
		if roundValue != v2Ctx.Round {
			return Result{
				Classification: Malformed,
				Commentary:     commentary,
				Verdict:        buildPartialVerdict(fields),
				Problem:        fmt.Sprintf("wrong round: expected %d, got %d", v2Ctx.Round, roundValue),
			}
		}

		// Validate block-state dispositions against expected open blocks
		seenBlocks := make(map[string]bool)
		for _, bs := range blockStates {
			seenBlocks[bs.BlockID] = true
			// Check if this block is in the expected set
			if !v2Ctx.OwnerOpenBlockIDs[bs.BlockID] {
				return Result{
					Classification: Malformed,
					Commentary:     commentary,
					Verdict:        buildPartialVerdict(fields),
					Problem:        fmt.Sprintf("foreign block-state: %s is not an open block owned by this reviewer", bs.BlockID),
				}
			}
		}
		// Check for missing block-state lines
		for blockID := range v2Ctx.OwnerOpenBlockIDs {
			if !seenBlocks[blockID] {
				return Result{
					Classification: Malformed,
					Commentary:     commentary,
					Verdict:        buildPartialVerdict(fields),
					Problem:        fmt.Sprintf("missing block-state for open block %s", blockID),
				}
			}
		}

		// Validate that approve doesn't have maintained blocks
		if verdict == "approve" && len(blockStates) > 0 {
			for _, bs := range blockStates {
				if bs.State == "maintained" {
					return Result{
						Classification: Malformed,
						Commentary:     commentary,
						Verdict:        buildPartialVerdict(fields),
						Problem:        "approve verdict cannot have maintained blocks",
					}
				}
			}
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

	// A second VERDICT anywhere in the text is malformed
	if verdictCount > 1 {
		return Result{
			Classification: Malformed,
			Commentary:     commentary,
			Verdict:        buildPartialVerdict(fields),
			Problem:        "multiple VERDICT envelopes found",
		}
	}

	// Build the Verdict struct
	result := Result{
		Verdict: Verdict{
			Candidate:       strings.TrimSpace(fields["candidate"]),
			Attempt:         strings.TrimSpace(fields["attempt"]),
			Decision:        verdict,
			Reason:          strings.TrimSpace(fields["reason"]),
			EnvelopeVersion: envelopeVersion,
			Round:           roundValue,
			BlockStates:     blockStates,
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

func isValidFieldName(name, envelopeVersion string) bool {
	if envelopeVersion == "v2" {
		return validFieldsV2[name]
	}
	return validFieldsV1[name]
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

// parseRound parses a round string as a non-negative integer.
// Rejects leading '+', leading zeros, and trailing garbage.
func parseRound(roundStr string) (int, error) {
	roundStr = strings.TrimSpace(roundStr)
	if roundStr == "" {
		return 0, fmt.Errorf("round cannot be empty")
	}
	// Reject leading '+'
	if roundStr[0] == '+' {
		return 0, fmt.Errorf("round must not have leading '+' sign, got %q", roundStr)
	}
	// Reject leading zeros (except for "0" itself)
	if len(roundStr) > 1 && roundStr[0] == '0' {
		return 0, fmt.Errorf("round must not have leading zeros, got %q", roundStr)
	}
	// Use strconv.Atoi which will reject non-numeric characters
	round, err := strconv.Atoi(roundStr)
	if err != nil {
		return 0, fmt.Errorf("round must be a valid integer, got %q", roundStr)
	}
	if round < 0 {
		return 0, fmt.Errorf("round must be non-negative, got %d", round)
	}
	return round, nil
}
