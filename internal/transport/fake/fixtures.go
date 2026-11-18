package fake

import "strings"

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
