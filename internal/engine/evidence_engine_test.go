package engine_test

import (
	"os"
	"strings"
	"testing"

	"github.com/boldfield/syndic/internal/candidate"
	"github.com/boldfield/syndic/internal/engine"
	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/transport/fake"
	"github.com/boldfield/syndic/internal/wire"
)

const (
	evBase   = "one\ntwo\nthree\n"
	evStaged = "one\n2\nthree\n"
)

// seededAuthor is an author step that, while it runs, records what a tool
// loop would have: an observation of a file, a staged edit of it, and one
// call with its result.
func seededAuthor(h **enginetest.Harness, staged, result string) fake.Step {
	return enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
		turn, attempt := req.Envelope.TurnID, req.Envelope.AttemptID
		err := (*h).Store.Tx(func(tx *journal.Tx) error {
			if _, err := tx.RecordObservation(turn, attempt, "a.txt", journal.ObservationFile, []byte(evBase)); err != nil {
				return err
			}
			if _, err := tx.StageWrite(turn, "a.txt", []byte(staged)); err != nil {
				return err
			}
			call, err := tx.RecordToolCall(attempt, 0, "prov_1", "read_file", "read", []byte(`{"path":"a.txt"}`))
			if err != nil {
				return err
			}
			_, err = tx.RecordToolResult(call.ID, journal.ToolCallExecuted, []byte(result), "")
			return err
		})
		if err != nil {
			panic(err)
		}
		return authorText
	})
}

func evHarness(t *testing.T, staged, result string, opts ...enginetest.Option) (*enginetest.Harness, string, *[]string) {
	t.Helper()
	var h *enginetest.Harness
	var seen []string
	reviewer := enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
		seen = append(seen, req.Messages[len(req.Messages)-1].Content)
		return fake.VerdictApprove(req.Envelope.CandidateHash, req.Envelope.AttemptID)
	})
	scripts := map[string][]fake.Step{
		"author":   {seededAuthor(&h, staged, result)},
		"reviewer": {reviewer},
	}
	h = enginetest.New(t, enginetest.RosterTOML, scripts, opts...)
	return h, submitReview(t, h, "s1"), &seen
}

func frozenHash(t *testing.T, h *enginetest.Harness, turnID string) string {
	t.Helper()
	for _, ev := range h.Events() {
		if p, ok := ev.Payload.(*wire.EvCandidateFrozen); ok && ev.TurnID == turnID {
			return p.CandidateHash
		}
	}
	t.Fatal("no candidate frozen")
	return ""
}

// The reviewer receives the change set as a unified diff generated from the
// stored base, and the author's call and result verbatim, both fenced.
func TestReviewerReceivesDiffAndAuthorEvidence(t *testing.T) {
	h, turnID, seen := evHarness(t, evStaged, "file says: one two three")
	wantEnd(t, h, turnID, engine.StateApproved, "all_approved")
	if len(*seen) != 1 {
		t.Fatalf("reviewer requests = %d", len(*seen))
	}
	got := (*seen)[0]
	for _, want := range []string{
		" BEGIN CHANGE SET =====\n--- a/a.txt\n+++ b/a.txt\n@@ -1,3 +1,3 @@\n one\n-two\n+2\n three\n===== ",
		" END CHANGE SET =====",
		" BEGIN AUTHOR EVIDENCE =====\ncall 1: read_file (id ",
		"arguments:\n{\"path\":\"a.txt\"}\nresult (executed):\nfile says: one two three\n===== ",
		" END AUTHOR EVIDENCE =====",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("request lacks %q:\n%s", want, got)
		}
	}
	// A change set was frozen and bound to the candidate.
	cs, err := h.Store.ChangeSetForTurn(turnID)
	if err != nil || len(cs.Paths) != 1 || cs.Paths[0].Op != "modify" || cs.ChangesHash == "" || cs.SnapshotHash == "" {
		t.Fatalf("change set = %+v, %v", cs, err)
	}
}

// Acceptance (a): the diff is regenerated from the journal, so a live
// workspace edit after the freeze cannot change it, and a later dispatch
// yields the same bytes.
func TestDiffRegeneratedIndependentOfLiveWorkspace(t *testing.T) {
	ws := t.TempDir()
	h, turnID, seen := evHarness(t, evStaged, "r")
	wantEnd(t, h, turnID, engine.StateApproved, "all_approved")
	if err := os.WriteFile(ws+"/a.txt", []byte("live edit\nafter freeze\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var again string
	err := h.Store.Tx(func(tx *journal.Tx) error {
		cs, err := tx.ChangeSetForTurn(turnID)
		if err != nil {
			return err
		}
		base, err := tx.Content(cs.Paths[0].BaseSHA256)
		if err != nil {
			return err
		}
		body, err := tx.Content(cs.Paths[0].NewSHA256)
		if err != nil {
			return err
		}
		again = engine.UnifiedDiffForTest(cs.Paths[0].Path, false, base, body)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains((*seen)[0], again) {
		t.Fatalf("regenerated diff %q is not what the reviewer received:\n%s", again, (*seen)[0])
	}
	if strings.Contains((*seen)[0], "live edit") {
		t.Fatal("live workspace content reached the reviewer")
	}
}

// Acceptance (b): a packet that does not fit the reviewer's context ends the
// turn incomplete, and nothing is summarized: the reviewer is never called.
func TestOversizePacketEndsIncomplete(t *testing.T) {
	staged := strings.Repeat("line of staged content\n", 3000)
	h, turnID, seen := evHarness(t, staged, "r", enginetest.WithContextBound("reviewer", 30000))
	wantEnd(t, h, turnID, engine.StateIncomplete, "budget:context:reviewer")
	if len(*seen) != 0 || len(reviewerCalls(h, "reviewer")) != 0 {
		t.Fatalf("reviewer was called with an oversize packet")
	}
}

// The same journal content with a small file fits the same bound, so the
// refusal above comes from the packet and not from the bound.
func TestSmallPacketFitsSameBound(t *testing.T) {
	h, turnID, _ := evHarness(t, evStaged, "r", enginetest.WithContextBound("reviewer", 30000))
	wantEnd(t, h, turnID, engine.StateApproved, "all_approved")
}

// Acceptance (c): the candidate hash binds the author's tool results and the
// change set. The recorded hash equals the hash of the identity rebuilt from
// the journal, and dropping either input gives another hash.
func TestAuthorOnlyEvidenceChangesCandidateHash(t *testing.T) {
	rebuild := func(t *testing.T, h *enginetest.Harness, turnID string) (identity candidate.Identity, recorded string) {
		t.Helper()
		recorded = frozenHash(t, h, turnID)
		calls, err := h.Store.ToolCallsByTurn(turnID, "author")
		if err != nil || len(calls) != 1 || calls[0].Result == nil {
			t.Fatalf("calls = %+v, %v", calls, err)
		}
		cs, err := h.Store.ChangeSetForTurn(turnID)
		if err != nil {
			t.Fatal(err)
		}
		turn, err := h.Store.GetTurn(turnID)
		if err != nil {
			t.Fatal(err)
		}
		hello := h.Engine.Hello()
		identity = candidate.Identity{
			ManifestHash:    *hello.ManifestHash,
			TurnID:          turnID,
			AuthorAttemptID: calls[0].AttemptID,
			Content:         []byte(authorText),
			Evidence:        []candidate.EvidencePair{{CallID: calls[0].ID, ResultSHA256: calls[0].Result.ResultSHA256}},
			ChangesHash:     cs.ChangesHash,
		}
		if turn.ContextHash != nil {
			identity.ContextHash = *turn.ContextHash
		}
		return identity, recorded
	}
	hashOf := func(t *testing.T, id candidate.Identity) string {
		t.Helper()
		got, err := candidate.Hash(id)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}

	h1, turn1, _ := evHarness(t, evStaged, "result A")
	wantEnd(t, h1, turn1, engine.StateApproved, "all_approved")
	id1, recorded1 := rebuild(t, h1, turn1)
	if got := hashOf(t, id1); got != recorded1 {
		t.Fatalf("recorded hash %s is not the hash of the journal's identity %s", recorded1, got)
	}
	noEvidence := id1
	noEvidence.Evidence = nil
	if hashOf(t, noEvidence) == recorded1 {
		t.Fatal("hash ignores the author's tool results")
	}
	noChanges := id1
	noChanges.ChangesHash = ""
	if hashOf(t, noChanges) == recorded1 {
		t.Fatal("hash ignores the change set")
	}

	// A second turn that differs only in an author tool result's bytes binds a
	// different result hash into its identity, hence another candidate hash.
	h2, turn2, _ := evHarness(t, evStaged, "result B")
	wantEnd(t, h2, turn2, engine.StateApproved, "all_approved")
	id2, recorded2 := rebuild(t, h2, turn2)
	if got := hashOf(t, id2); got != recorded2 {
		t.Fatalf("recorded hash %s is not the hash of the journal's identity %s", recorded2, got)
	}
	if id1.Evidence[0].ResultSHA256 == id2.Evidence[0].ResultSHA256 {
		t.Fatal("results did not differ")
	}
	swapped := id1
	swapped.Evidence = []candidate.EvidencePair{{CallID: id1.Evidence[0].CallID, ResultSHA256: id2.Evidence[0].ResultSHA256}}
	if hashOf(t, swapped) == recorded1 {
		t.Fatal("changing only the result bytes does not change the hash")
	}
}

// Only the author's calls reach the packet; another participant's calls in the
// same turn stay under that participant.
func TestPacketExcludesOtherParticipantsCalls(t *testing.T) {
	h, turnID, seen := evHarness(t, evStaged, "author result")
	wantEnd(t, h, turnID, engine.StateApproved, "all_approved")
	got := (*seen)[0]
	if strings.Count(got, "call ") != 1 {
		t.Fatalf("packet has other calls:\n%s", got)
	}
}
