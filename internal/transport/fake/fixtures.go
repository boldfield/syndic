package fake

import "fmt"

// VerdictApprove returns a well-formed approval envelope with all required fields per delta §6.
func VerdictApprove(candidateHash, attemptID string) string {
	return fmt.Sprintf(`VERDICT v1
candidate: %s
attempt: %s
verdict: approve
reason: Approved
END VERDICT
`, candidateHash, attemptID)
}

// VerdictBlock returns a well-formed block envelope with all required fields per delta §6.
func VerdictBlock(candidateHash, attemptID, blockID, severity, evidence, remedy, reason string) string {
	return fmt.Sprintf(`VERDICT v1
candidate: %s
attempt: %s
verdict: block
severity: %s
block_id: %s
evidence: %s
remedy: %s
reason: %s
END VERDICT
`, candidateHash, attemptID, severity, blockID, evidence, remedy, reason)
}

// VerdictProseApprove returns prose containing "approve" with no envelope.
func VerdictProseApprove() string {
	return "I approve of this response."
}

// VerdictWrongHash returns an approve with a different candidate hash than the one given.
func VerdictWrongHash(candidateHash, attemptID string) string {
	return fmt.Sprintf(`VERDICT v1
candidate: wrong-hash
attempt: %s
verdict: approve
reason: Approved
END VERDICT
`, attemptID)
}

// VerdictApproveWithChanges returns an approve-with-changes verdict.
func VerdictApproveWithChanges(candidateHash, attemptID string) string {
	return fmt.Sprintf(`VERDICT v1
candidate: %s
attempt: %s
verdict: approve-with-changes
reason: Changes requested
END VERDICT
`, candidateHash, attemptID)
}

// VerdictTwoEnvelopes returns output containing two envelopes.
func VerdictTwoEnvelopes(candidateHash, attemptID string) string {
	return fmt.Sprintf(`VERDICT v1
candidate: %s
attempt: %s
verdict: approve
reason: Approved
END VERDICT
VERDICT v1
candidate: %s
attempt: %s
verdict: approve
reason: Approved
END VERDICT
`, candidateHash, attemptID, candidateHash, attemptID)
}

// VerdictBlockWithEndMarker returns a block with "END VERDICT" in the reason.
func VerdictBlockWithEndMarker(candidateHash, attemptID string) string {
	return fmt.Sprintf(`VERDICT v1
candidate: %s
attempt: %s
verdict: block
severity: high
block_id: b1
evidence: test
remedy: fix it
reason: This is a reason with
END VERDICT
on its own line, plus more text
after the marker
END VERDICT
`, candidateHash, attemptID)
}
