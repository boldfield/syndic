package fake

import (
	"fmt"
	"strings"
)

// VerdictApprove returns a well-formed approval envelope with all required fields per delta §6.
func VerdictApprove(candidateHash, attemptID string) string {
	return strings.Join([]string{
		"VERDICT v1",
		"candidate: " + candidateHash,
		"attempt: " + attemptID,
		"verdict: approve",
		"reason: Approved",
		"END VERDICT",
	}, "\n")
}

// VerdictBlock returns a well-formed block envelope with all required fields per delta §6.
func VerdictBlock(candidateHash, attemptID, blockID, severity, evidence, remedy, reason string) string {
	return strings.Join([]string{
		"VERDICT v1",
		"candidate: " + candidateHash,
		"attempt: " + attemptID,
		"verdict: block",
		"severity: " + severity,
		"block_id: " + blockID,
		"evidence: " + evidence,
		"remedy: " + remedy,
		"reason: " + reason,
		"END VERDICT",
	}, "\n")
}

// VerdictProseApprove returns prose containing "approve" with no envelope.
func VerdictProseApprove() string {
	return "I approve of this response."
}

// VerdictWrongHash returns an approve with a different candidate hash than the one given.
func VerdictWrongHash(candidateHash, attemptID string) string {
	wrongHash := candidateHash + "-stale"
	return strings.Join([]string{
		"VERDICT v1",
		"candidate: " + wrongHash,
		"attempt: " + attemptID,
		"verdict: approve",
		"reason: Approved",
		"END VERDICT",
	}, "\n")
}

// VerdictApproveWithChanges returns an approve-with-changes verdict.
func VerdictApproveWithChanges(candidateHash, attemptID string) string {
	return strings.Join([]string{
		"VERDICT v1",
		"candidate: " + candidateHash,
		"attempt: " + attemptID,
		"verdict: approve-with-changes",
		"reason: Changes requested",
		"END VERDICT",
	}, "\n")
}

// VerdictTwoEnvelopes returns output containing two envelopes.
func VerdictTwoEnvelopes(candidateHash, attemptID string) string {
	return strings.Join([]string{
		"VERDICT v1",
		"candidate: " + candidateHash,
		"attempt: " + attemptID,
		"verdict: approve",
		"reason: Approved",
		"END VERDICT",
		"VERDICT v1",
		"candidate: " + candidateHash,
		"attempt: " + attemptID,
		"verdict: approve",
		"reason: Approved",
		"END VERDICT",
	}, "\n")
}

// VerdictBlockWithEndMarker returns a block with "END VERDICT" in the reason.
func VerdictBlockWithEndMarker(candidateHash, attemptID string) string {
	return strings.Join([]string{
		"VERDICT v1",
		"candidate: " + candidateHash,
		"attempt: " + attemptID,
		"verdict: block",
		"severity: high",
		"block_id: b1",
		"evidence: test",
		"remedy: fix it",
		"reason: This is a reason with",
		"END VERDICT",
		"on its own line, plus more text",
		"after the marker",
		"END VERDICT",
	}, "\n")
}

// VerdictWrongAttemptID returns an approve with a different attempt ID than the one given.
func VerdictWrongAttemptID(candidateHash, attemptID string) string {
	wrongAttemptID := attemptID + "-stale"
	return strings.Join([]string{
		"VERDICT v1",
		"candidate: " + candidateHash,
		"attempt: " + wrongAttemptID,
		"verdict: approve",
		"reason: Approved",
		"END VERDICT",
	}, "\n")
}

// VerdictV2Approve returns a well-formed VERDICT v2 approval envelope.
func VerdictV2Approve(candidateHash, attemptID string, round int) string {
	return strings.Join([]string{
		"VERDICT v2",
		"candidate: " + candidateHash,
		"attempt: " + attemptID,
		"round: " + fmt.Sprint(round),
		"verdict: approve",
		"reason: Approved",
		"END VERDICT",
	}, "\n")
}

// VerdictV2Block returns a well-formed VERDICT v2 block envelope.
func VerdictV2Block(candidateHash, attemptID string, round int, blockID, severity, evidence, remedy, reason string) string {
	return strings.Join([]string{
		"VERDICT v2",
		"candidate: " + candidateHash,
		"attempt: " + attemptID,
		"round: " + fmt.Sprint(round),
		"verdict: block",
		"severity: " + severity,
		"block_id: " + blockID,
		"evidence: " + evidence,
		"remedy: " + remedy,
		"reason: " + reason,
		"END VERDICT",
	}, "\n")
}

// VerdictV2WithBlockStates returns a well-formed VERDICT v2 approval with block-state lines.
func VerdictV2WithBlockStates(candidateHash, attemptID string, round int, blockStates []string) string {
	lines := []string{
		"VERDICT v2",
		"candidate: " + candidateHash,
		"attempt: " + attemptID,
		"round: " + fmt.Sprint(round),
	}
	lines = append(lines, blockStates...)
	lines = append(lines, []string{
		"verdict: approve",
		"reason: Approved",
		"END VERDICT",
	}...)
	return strings.Join(lines, "\n")
}

// VerdictV2ApproveMaintainedBlock returns a VERDICT v2 approval with a maintained block (invalid).
func VerdictV2ApproveMaintainedBlock(candidateHash, attemptID string, round int) string {
	return strings.Join([]string{
		"VERDICT v2",
		"candidate: " + candidateHash,
		"attempt: " + attemptID,
		"round: " + fmt.Sprint(round),
		"block-state: b1 maintained",
		"verdict: approve",
		"reason: Approved",
		"END VERDICT",
	}, "\n")
}

// VerdictV2WithdrawnBlock returns a VERDICT v2 block with block-state lines showing withdrawal.
func VerdictV2WithdrawnBlock(candidateHash, attemptID string, round int) string {
	return strings.Join([]string{
		"VERDICT v2",
		"candidate: " + candidateHash,
		"attempt: " + attemptID,
		"round: " + fmt.Sprint(round),
		"block-state: b1 withdrawn",
		"verdict: approve",
		"reason: Approved",
		"END VERDICT",
	}, "\n")
}
