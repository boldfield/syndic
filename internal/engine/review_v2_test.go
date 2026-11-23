package engine_test

import (
	"strings"
	"testing"

	"github.com/boldfield/syndic/internal/engine"
	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/transport/fake"
)

// v2Envelope builds a VERDICT v2 approve envelope with extra lines (such as
// block-state) placed before the verdict line.
func v2Envelope(hash, attempt, round string, extra ...string) string {
	lines := []string{"VERDICT v2", "candidate: " + hash, "attempt: " + attempt, "round: " + round}
	lines = append(lines, extra...)
	lines = append(lines, "verdict: approve", "reason: ok", "END VERDICT")
	return strings.Join(lines, "\n")
}

// A malformed v2 envelope takes the existing single repair path: a valid
// repair is accepted exactly as for v1.
func TestReviewV2MalformedThenValidRepair(t *testing.T) {
	cases := []struct {
		name  string
		first func(hash, attempt string) string
	}{
		{"wrong round", func(h, a string) string { return v2Envelope(h, a, "9") }},
		{"foreign block-state", func(h, a string) string { return v2Envelope(h, a, "0", "block-state: other withdrawn") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := enginetest.New(t, enginetest.RosterTOML, reviewScriptsMultiple(
				enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
					return tc.first(req.Envelope.CandidateHash, req.Envelope.AttemptID)
				}),
				enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
					return v2Envelope(req.Envelope.CandidateHash, req.Envelope.AttemptID, "0")
				}),
			))
			turnID := submitReview(t, h, "s1")
			wantEnd(t, h, turnID, engine.StateApproved, "all_approved")
			attempts := attemptsOf(h, turnID, "reviewer")
			if len(attempts) != 2 {
				t.Fatalf("%d reviewer attempts, want 2", len(attempts))
			}
			first, second := getAttempt(t, h, attempts[0]), getAttempt(t, h, attempts[1])
			if second.RepairOf == nil || *second.RepairOf != first.ID {
				t.Fatalf("repair attempt RepairOf = %v, want %s", second.RepairOf, first.ID)
			}
			if first.Participant != second.Participant {
				t.Fatalf("repair participant %q, want %q", second.Participant, first.Participant)
			}
			statuses := map[string]string{}
			for _, v := range verdictRows(t, h, turnID) {
				statuses[v.AttemptID] = v.ValidationStatus
			}
			if statuses[first.ID] != "malformed" || statuses[second.ID] != "valid" {
				t.Fatalf("statuses = %v, want first malformed and repair valid", statuses)
			}
			// The acceptance wrote the record: the repair is listed once, under
			// the repair attempt, and the malformed original is not.
			items, next, err := h.Store.ReviewsPage(turnID, []string{"reviewer"}, "", 50, 1<<20)
			if err != nil || next != "" || len(items) != 1 {
				t.Fatalf("reviews = %d items, next %q, %v; want 1", len(items), next, err)
			}
			if items[0].AttemptID != second.ID || items[0].Kind != "approve" || items[0].Participant != first.Participant || items[0].Round != 0 {
				t.Fatalf("review item = %+v, want approve by %s on repair attempt %s", items[0], first.Participant, second.ID)
			}
			var written int
			if err := h.DB.Conn().QueryRow(`SELECT COUNT(*) FROM review_records WHERE turn_id = ?`, turnID).Scan(&written); err != nil || written != 1 {
				t.Fatalf("records written at acceptance = %d, %v; want 1", written, err)
			}
		})
	}
}

// A second malformed v2 response ends the turn as before.
func TestReviewV2MalformedTwice(t *testing.T) {
	bad := verdictStep(func(hash, attempt string) string { return v2Envelope(hash, attempt, "9") })
	h := enginetest.New(t, enginetest.RosterTOML, reviewScriptsMultiple(bad, bad))
	turnID := submitReview(t, h, "s1")
	wantEnd(t, h, turnID, engine.StateIncomplete, "malformed_twice:reviewer")
	if n := len(h.Adapters["reviewer"].Calls()); n != 2 {
		t.Fatalf("reviewer called %d times, want 2", n)
	}
}

// A well-formed v2 envelope in the right round, with no open blocks, approves.
func TestReviewV2Approve(t *testing.T) {
	h := enginetest.New(t, enginetest.RosterTOML, reviewScripts(
		verdictStep(func(hash, attempt string) string { return fake.VerdictV2Approve(hash, attempt, 0) }),
	))
	turnID := submitReview(t, h, "s1")
	wantEnd(t, h, turnID, engine.StateApproved, "all_approved")
}

// A v2 approve-with-changes is a revision request.
func TestReviewV2ApproveWithChanges(t *testing.T) {
	h := enginetest.New(t, enginetest.RosterTOML, reviewScripts(
		verdictStep(func(hash, attempt string) string {
			return strings.Replace(v2Envelope(hash, attempt, "0"), "verdict: approve", "verdict: approve-with-changes", 1)
		}),
	))
	turnID := submitReview(t, h, "s1")
	wantEnd(t, h, turnID, engine.StateIncomplete, "revision_request:reviewer")
}
