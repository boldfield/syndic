package verdict

import (
	"strings"
	"testing"

	"github.com/boldfield/syndic/internal/transport/fake"
)

func TestParseVerdicts(t *testing.T) {
	tests := []struct {
		name             string
		text             string
		candidateHash    string
		attemptID        string
		expectedClass    Classification
		expectedDecision string
		expectedProblem  bool
		checks           func(t *testing.T, r Result)
	}{
		{
			name:             "VerdictApprove",
			text:             fake.VerdictApprove("hash123", "attempt1"),
			candidateHash:    "hash123",
			attemptID:        "attempt1",
			expectedClass:    Valid,
			expectedDecision: "approve",
		},
		{
			name:             "VerdictBlock",
			text:             fake.VerdictBlock("hash123", "attempt1", "b1", "high", "evidence text", "fix it", "reason text"),
			candidateHash:    "hash123",
			attemptID:        "attempt1",
			expectedClass:    Valid,
			expectedDecision: "block",
			checks: func(t *testing.T, r Result) {
				if r.Verdict.Severity != "high" {
					t.Errorf("expected severity high, got %s", r.Verdict.Severity)
				}
				if r.Verdict.BlockID != "b1" {
					t.Errorf("expected block_id b1, got %s", r.Verdict.BlockID)
				}
				if r.Verdict.Evidence != "evidence text" {
					t.Errorf("expected evidence 'evidence text', got %s", r.Verdict.Evidence)
				}
				if r.Verdict.Remedy != "fix it" {
					t.Errorf("expected remedy 'fix it', got %s", r.Verdict.Remedy)
				}
			},
		},
		{
			name:            "VerdictProseApprove",
			text:            fake.VerdictProseApprove(),
			candidateHash:   "hash123",
			attemptID:       "attempt1",
			expectedClass:   Malformed,
			expectedProblem: true,
			checks: func(t *testing.T, r Result) {
				if r.Commentary != "I approve of this response." {
					t.Errorf("expected commentary to be whole text")
				}
			},
		},
		{
			name:            "VerdictWrongHash",
			text:            fake.VerdictWrongHash("hash123", "attempt1"),
			candidateHash:   "hash123",
			attemptID:       "attempt1",
			expectedClass:   Rejected,
			expectedProblem: true,
		},
		{
			name:          "VerdictApproveWithChanges",
			text:          fake.VerdictApproveWithChanges("hash123", "attempt1"),
			candidateHash: "hash123",
			attemptID:     "attempt1",
			expectedClass: RevisionRequest,
		},
		{
			name:            "VerdictTwoEnvelopes",
			text:            fake.VerdictTwoEnvelopes("hash123", "attempt1"),
			candidateHash:   "hash123",
			attemptID:       "attempt1",
			expectedClass:   Malformed,
			expectedProblem: true,
		},
		{
			name:             "VerdictBlockWithEndMarker",
			text:             fake.VerdictBlockWithEndMarker("hash123", "attempt1"),
			candidateHash:    "hash123",
			attemptID:        "attempt1",
			expectedClass:    Valid,
			expectedDecision: "block",
			checks: func(t *testing.T, r Result) {
				if !strings.Contains(r.Verdict.Reason, "This is a reason with") {
					t.Errorf("expected reason to contain 'This is a reason with'")
				}
				if !strings.Contains(r.Commentary, "on its own line") {
					t.Errorf("expected commentary to contain 'on its own line'")
				}
			},
		},
		{
			name:            "WrongAttemptID",
			text:            fake.VerdictApprove("hash123", "attempt1"),
			candidateHash:   "hash123",
			attemptID:       "attempt2",
			expectedClass:   Rejected,
			expectedProblem: true,
		},
		{
			name:            "MissingReason",
			text:            strings.Join([]string{"VERDICT v1", "candidate: hash123", "attempt: attempt1", "verdict: approve", "END VERDICT"}, "\n"),
			candidateHash:   "hash123",
			attemptID:       "attempt1",
			expectedClass:   Malformed,
			expectedProblem: true,
		},
		{
			name:            "DuplicateCandidate",
			text:            strings.Join([]string{"VERDICT v1", "candidate: hash123", "attempt: attempt1", "verdict: approve", "candidate: hash123", "reason: test", "END VERDICT"}, "\n"),
			candidateHash:   "hash123",
			attemptID:       "attempt1",
			expectedClass:   Malformed,
			expectedProblem: true,
		},
		{
			name:            "UnknownFieldConfidence",
			text:            strings.Join([]string{"VERDICT v1", "candidate: hash123", "attempt: attempt1", "verdict: approve", "confidence: high", "reason: test", "END VERDICT"}, "\n"),
			candidateHash:   "hash123",
			attemptID:       "attempt1",
			expectedClass:   Malformed,
			expectedProblem: true,
		},
		{
			name:            "InvalidVerdictMaybe",
			text:            strings.Join([]string{"VERDICT v1", "candidate: hash123", "attempt: attempt1", "verdict: maybe", "reason: test", "END VERDICT"}, "\n"),
			candidateHash:   "hash123",
			attemptID:       "attempt1",
			expectedClass:   Malformed,
			expectedProblem: true,
		},
		{
			name:            "ApproveWithSeverity",
			text:            strings.Join([]string{"VERDICT v1", "candidate: hash123", "attempt: attempt1", "verdict: approve", "severity: low", "reason: test", "END VERDICT"}, "\n"),
			candidateHash:   "hash123",
			attemptID:       "attempt1",
			expectedClass:   Malformed,
			expectedProblem: true,
		},
		{
			name:            "BlockMissingRemedy",
			text:            strings.Join([]string{"VERDICT v1", "candidate: hash123", "attempt: attempt1", "verdict: block", "severity: low", "block_id: b1", "evidence: test", "reason: test", "END VERDICT"}, "\n"),
			candidateHash:   "hash123",
			attemptID:       "attempt1",
			expectedClass:   Malformed,
			expectedProblem: true,
		},
		{
			name:            "InvalidSeverityCritical",
			text:            strings.Join([]string{"VERDICT v1", "candidate: hash123", "attempt: attempt1", "verdict: block", "severity: critical", "block_id: b1", "evidence: test", "remedy: fix", "reason: test", "END VERDICT"}, "\n"),
			candidateHash:   "hash123",
			attemptID:       "attempt1",
			expectedClass:   Malformed,
			expectedProblem: true,
		},
		{
			name:             "MultilineEvidence",
			text:             strings.Join([]string{"VERDICT v1", "candidate: hash123", "attempt: attempt1", "verdict: block", "severity: low", "block_id: b1", "evidence: line1", "line2", "line3", "remedy: fix", "reason: test", "END VERDICT"}, "\n"),
			candidateHash:    "hash123",
			attemptID:        "attempt1",
			expectedClass:    Valid,
			expectedDecision: "block",
			checks: func(t *testing.T, r Result) {
				expected := "line1\nline2\nline3"
				if r.Verdict.Evidence != expected {
					t.Errorf("expected evidence with newlines, got %q", r.Verdict.Evidence)
				}
			},
		},
		{
			name:             "ProseBeforeAndAfterEnvelope",
			text:             strings.Join([]string{"Some prose before", "VERDICT v1", "candidate: hash123", "attempt: attempt1", "verdict: approve", "reason: test", "END VERDICT", "Some prose after"}, "\n"),
			candidateHash:    "hash123",
			attemptID:        "attempt1",
			expectedClass:    Valid,
			expectedDecision: "approve",
			checks: func(t *testing.T, r Result) {
				if !strings.Contains(r.Commentary, "Some prose before") {
					t.Errorf("expected commentary to contain prose before")
				}
				if !strings.Contains(r.Commentary, "Some prose after") {
					t.Errorf("expected commentary to contain prose after")
				}
			},
		},
		{
			name:             "ProseApproveBeforeBlockEnvelope",
			text:             strings.Join([]string{"I approve of this", "VERDICT v1", "candidate: hash123", "attempt: attempt1", "verdict: block", "severity: high", "block_id: b1", "evidence: bad", "remedy: fix", "reason: test", "END VERDICT"}, "\n"),
			candidateHash:    "hash123",
			attemptID:        "attempt1",
			expectedClass:    Valid,
			expectedDecision: "block",
		},
		{
			name:             "CRLFLineEndings",
			text:             "VERDICT v1\r\ncandidate: hash123\r\nattempt: attempt1\r\nverdict: approve\r\nreason: test\r\nEND VERDICT\r\n",
			candidateHash:    "hash123",
			attemptID:        "attempt1",
			expectedClass:    Valid,
			expectedDecision: "approve",
		},
		{
			name:            "OpeningLineWithTrailingSpace",
			text:            strings.Join([]string{"VERDICT v1 ", "candidate: hash123", "attempt: attempt1", "verdict: approve", "reason: test", "END VERDICT"}, "\n"),
			candidateHash:   "hash123",
			attemptID:       "attempt1",
			expectedClass:   Malformed,
			expectedProblem: true,
		},
		{
			name:            "NoEndVerdictLine",
			text:            strings.Join([]string{"VERDICT v1", "candidate: hash123", "attempt: attempt1", "verdict: approve", "reason: test"}, "\n"),
			candidateHash:   "hash123",
			attemptID:       "attempt1",
			expectedClass:   Malformed,
			expectedProblem: true,
		},
		{
			name:            "EmptyInput",
			text:            "",
			candidateHash:   "hash123",
			attemptID:       "attempt1",
			expectedClass:   Malformed,
			expectedProblem: true,
		},
		{
			name:            "NoOpeningVerdictLine",
			text:            strings.Join([]string{"candidate: hash123", "attempt: attempt1", "verdict: approve", "reason: test", "END VERDICT"}, "\n"),
			candidateHash:   "hash123",
			attemptID:       "attempt1",
			expectedClass:   Malformed,
			expectedProblem: true,
		},
		{
			name:             "ContinuationLineWithColon",
			text:             strings.Join([]string{"VERDICT v1", "candidate: hash123", "attempt: attempt1", "verdict: block", "severity: low", "block_id: b1", "evidence: see main.go:42 for details", "remedy: fix it", "reason: test", "END VERDICT"}, "\n"),
			candidateHash:    "hash123",
			attemptID:        "attempt1",
			expectedClass:    Valid,
			expectedDecision: "block",
			checks: func(t *testing.T, r Result) {
				if r.Verdict.Evidence != "see main.go:42 for details" {
					t.Errorf("expected evidence with colon, got %q", r.Verdict.Evidence)
				}
			},
		},
		{
			name:            "SecondVerdictInsideEnvelope",
			text:            strings.Join([]string{"VERDICT v1", "candidate: hash123", "attempt: attempt1", "verdict: approve", "reason: r", "VERDICT v1", "END VERDICT"}, "\n"),
			candidateHash:   "hash123",
			attemptID:       "attempt1",
			expectedClass:   Malformed,
			expectedProblem: true,
		},
		{
			name:            "MalformedWithProseAndPartialVerdict",
			text:            strings.Join([]string{"Some prose before", "VERDICT v1", "candidate: hash123", "attempt: attempt1", "verdict: maybe", "reason: test", "END VERDICT", "Some prose after"}, "\n"),
			candidateHash:   "hash123",
			attemptID:       "attempt1",
			expectedClass:   Malformed,
			expectedProblem: true,
			checks: func(t *testing.T, r Result) {
				if !strings.Contains(r.Commentary, "Some prose before") {
					t.Errorf("expected commentary before in malformed result")
				}
				if !strings.Contains(r.Commentary, "Some prose after") {
					t.Errorf("expected commentary after in malformed result")
				}
				if r.Verdict.Candidate != "hash123" {
					t.Errorf("expected partial verdict filled, got candidate %q", r.Verdict.Candidate)
				}
				if r.Verdict.Attempt != "attempt1" {
					t.Errorf("expected partial verdict filled, got attempt %q", r.Verdict.Attempt)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := Parse(tt.text, tt.candidateHash, tt.attemptID)

			if result.Classification != tt.expectedClass {
				t.Errorf("expected classification %s, got %s", tt.expectedClass, result.Classification)
			}

			if tt.expectedDecision != "" && result.Verdict.Decision != tt.expectedDecision {
				t.Errorf("expected decision %s, got %s", tt.expectedDecision, result.Verdict.Decision)
			}

			if tt.expectedProblem && result.Problem == "" {
				t.Errorf("expected non-empty problem")
			}

			if !tt.expectedProblem && result.Classification != Valid && result.Classification != RevisionRequest && result.Problem == "" {
				t.Errorf("expected problem for classification %s", result.Classification)
			}

			if tt.checks != nil {
				tt.checks(t, result)
			}
		})
	}
}

func TestParseMultilineFields(t *testing.T) {
	tests := []struct {
		name          string
		fieldName     string
		fieldValue    string
		expectedValue string
	}{
		{"MultilineReason", "reason", "This is line 1\nline 2\nline 3", "This is line 1\nline 2\nline 3"},
		{"MultilineEvidence", "evidence", "Bad code on line 1\nand also line 2", "Bad code on line 1\nand also line 2"},
		{"MultilineRemedy", "remedy", "Change 1\nChange 2", "Change 1\nChange 2"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lines := strings.Split(tt.fieldValue, "\n")

			// Build the envelope with multiline field
			var envelope []string
			switch tt.fieldName {
			case "reason":
				envelope = []string{"VERDICT v1", "candidate: hash123", "attempt: attempt1", "verdict: block", "severity: low", "block_id: b1", "evidence: test", "remedy: fix", tt.fieldName + ": " + lines[0]}
				envelope = append(envelope, lines[1:]...)
			case "evidence":
				envelope = []string{"VERDICT v1", "candidate: hash123", "attempt: attempt1", "verdict: block", "severity: low", "block_id: b1", tt.fieldName + ": " + lines[0]}
				envelope = append(envelope, lines[1:]...)
				envelope = append(envelope, "remedy: fix", "reason: test")
			case "remedy":
				envelope = []string{"VERDICT v1", "candidate: hash123", "attempt: attempt1", "verdict: block", "severity: low", "block_id: b1", "evidence: test", tt.fieldName + ": " + lines[0]}
				envelope = append(envelope, lines[1:]...)
				envelope = append(envelope, "reason: test")
			}
			envelope = append(envelope, "END VERDICT")

			text := strings.Join(envelope, "\n")
			result := Parse(text, "hash123", "attempt1")

			if result.Classification != Valid {
				t.Errorf("expected valid, got %s: %s", result.Classification, result.Problem)
			}

			var actual string
			switch tt.fieldName {
			case "reason":
				actual = result.Verdict.Reason
			case "evidence":
				actual = result.Verdict.Evidence
			case "remedy":
				actual = result.Verdict.Remedy
			}

			if actual != tt.expectedValue {
				t.Errorf("expected %q, got %q", tt.expectedValue, actual)
			}
		})
	}
}

func TestParseTrimming(t *testing.T) {
	text := strings.Join([]string{
		"VERDICT v1",
		"candidate:   hash123   ",
		"attempt:   attempt1   ",
		"verdict: approve",
		"reason:   This is trimmed   ",
		"END VERDICT",
	}, "\n")

	result := Parse(text, "hash123", "attempt1")

	if result.Classification != Valid {
		t.Errorf("expected valid, got %s", result.Classification)
	}

	if result.Verdict.Candidate != "hash123" {
		t.Errorf("expected candidate 'hash123', got %q", result.Verdict.Candidate)
	}

	if result.Verdict.Reason != "This is trimmed" {
		t.Errorf("expected reason 'This is trimmed', got %q", result.Verdict.Reason)
	}
}
