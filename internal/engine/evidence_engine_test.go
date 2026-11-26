package engine_test

import (
	"os"
	"path/filepath"
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

// revisionEvidence runs a two-round turn. The author's first candidate is
// built from a file read in ws, and the file is edited live while the first
// reviewer runs. That reviewer also records a call of its own under its
// attempt and then blocks, so a second dispatch follows.
func revisionEvidence(t *testing.T, ws string) (*enginetest.Harness, string, *[]string) {
	t.Helper()
	var h *enginetest.Harness
	var seen []string
	author := enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
		base, err := os.ReadFile(filepath.Join(ws, "a.txt"))
		if err != nil {
			panic(err)
		}
		turn, attempt := req.Envelope.TurnID, req.Envelope.AttemptID
		err = h.Store.Tx(func(tx *journal.Tx) error {
			if _, err := tx.RecordObservation(turn, attempt, "a.txt", journal.ObservationFile, base); err != nil {
				return err
			}
			if _, err := tx.StageWrite(turn, "a.txt", []byte(evStaged)); err != nil {
				return err
			}
			call, err := tx.RecordToolCall(attempt, 0, "prov_1", "read_file", "read", []byte(`{"path":"a.txt"}`))
			if err != nil {
				return err
			}
			_, err = tx.RecordToolResult(call.ID, journal.ToolCallExecuted, []byte("author result"), "")
			return err
		})
		if err != nil {
			panic(err)
		}
		return authorText
	})
	round := 0
	reviewer := enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
		seen = append(seen, req.Messages[len(req.Messages)-1].Content)
		if round++; round > 1 {
			return "VERDICT v2\ncandidate: " + req.Envelope.CandidateHash + "\nattempt: " + req.Envelope.AttemptID +
				"\nround: 1\nblock-state: b1 withdrawn\nverdict: approve\nreason: r\nEND VERDICT"
		}
		// The live workspace moves on, and the reviewer looks something up.
		if err := os.WriteFile(filepath.Join(ws, "a.txt"), []byte("live edit\nafter freeze\n"), 0o600); err != nil {
			panic(err)
		}
		err := h.Store.Tx(func(tx *journal.Tx) error {
			call, err := tx.RecordToolCall(req.Envelope.AttemptID, 0, "prov_r", "reviewer_lookup", "read", []byte(`{"q":"x"}`))
			if err != nil {
				return err
			}
			_, err = tx.RecordToolResult(call.ID, journal.ToolCallExecuted, []byte("reviewer result"), "")
			return err
		})
		if err != nil {
			panic(err)
		}
		return fake.VerdictBlock(req.Envelope.CandidateHash, req.Envelope.AttemptID, "b1", "high", "e", "r", "r")
	})
	scripts := map[string][]fake.Step{
		"author":   {author, enginetest.Reply(revisedText)},
		"reviewer": {reviewer, reviewer},
	}
	h = enginetest.New(t, revisionRoster("2"), scripts)
	return h, submitReview(t, h, "s1"), &seen
}

// fenced returns the text between a fence's BEGIN and END lines.
func fenced(t *testing.T, msg, name string) string {
	t.Helper()
	b, e := " BEGIN "+name+" =====\n", "\n===== "
	i := strings.Index(msg, b)
	if i < 0 {
		t.Fatalf("no %s in:\n%s", name, msg)
	}
	rest := msg[i+len(b):]
	j := strings.Index(rest, e)
	if j < 0 {
		t.Fatalf("unterminated %s in:\n%s", name, msg)
	}
	return rest[:j]
}

func liveWorkspace(t *testing.T) string {
	t.Helper()
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "a.txt"), []byte(evBase), 0o600); err != nil {
		t.Fatal(err)
	}
	return ws
}

// Acceptance (a): the diff is regenerated from the journal, so an edit to
// the live workspace after the freeze cannot change it. The second dispatch,
// after the live edit, carries the same change-set bytes as the first.
func TestDiffRegeneratedIndependentOfLiveWorkspace(t *testing.T) {
	ws := liveWorkspace(t)
	h, turnID, seen := revisionEvidence(t, ws)
	wantEnd(t, h, turnID, engine.StateApproved, "all_approved")
	if len(*seen) != 2 {
		t.Fatalf("reviewer requests = %d, want 2", len(*seen))
	}
	live, err := os.ReadFile(filepath.Join(ws, "a.txt"))
	if err != nil || !strings.Contains(string(live), "live edit") {
		t.Fatalf("live workspace was not edited: %q, %v", live, err)
	}
	const want = "--- a/a.txt\n+++ b/a.txt\n@@ -1,3 +1,3 @@\n one\n-two\n+2\n three"
	for i, msg := range *seen {
		if got := fenced(t, msg, "CHANGE SET"); got != want {
			t.Fatalf("dispatch %d diff = %q, want %q", i, got, want)
		}
		if strings.Contains(msg, "live edit") {
			t.Fatalf("dispatch %d carries live workspace content", i)
		}
	}
}

// Only the author's calls reach the packet or the candidate hash. A reviewer
// records a call of its own in round 0; the round-1 dispatch and the round-1
// candidate hash must not include it.
func TestPacketExcludesOtherParticipantsCalls(t *testing.T) {
	h, turnID, seen := revisionEvidence(t, liveWorkspace(t))
	wantEnd(t, h, turnID, engine.StateApproved, "all_approved")
	for i, msg := range *seen {
		got := fenced(t, msg, "AUTHOR EVIDENCE")
		if strings.Count(got, "call ") != 1 || !strings.Contains(got, "author result") ||
			strings.Contains(got, "reviewer_lookup") || strings.Contains(got, "reviewer result") {
			t.Fatalf("dispatch %d evidence is not the author's alone:\n%s", i, got)
		}
	}
	all, err := h.Store.ToolCallsByTurn(turnID, "")
	if err != nil || len(all) != 2 {
		t.Fatalf("the turn should hold the author's and the reviewer's call: %+v, %v", all, err)
	}
	authorCalls, err := h.Store.ToolCallsByTurn(turnID, "author")
	if err != nil || len(authorCalls) != 1 || authorCalls[0].Result == nil {
		t.Fatalf("author calls = %+v, %v", authorCalls, err)
	}
	cs, err := h.Store.ChangeSetForTurn(turnID)
	if err != nil {
		t.Fatal(err)
	}
	turn, err := h.Store.GetTurn(turnID)
	if err != nil {
		t.Fatal(err)
	}
	id := candidate.Identity{
		ManifestHash:    *h.Engine.Hello().ManifestHash,
		TurnID:          turnID,
		AuthorAttemptID: h.Adapters["author"].Calls()[1].Request.Envelope.AttemptID,
		Round:           1,
		Content:         []byte(revisedText),
		Evidence:        []candidate.EvidencePair{{CallID: authorCalls[0].ID, ResultSHA256: authorCalls[0].Result.ResultSHA256}},
		ChangesHash:     cs.ChangesHash,
	}
	if turn.ContextHash != nil {
		id.ContextHash = *turn.ContextHash
	}
	got, err := candidate.Hash(id)
	if err != nil {
		t.Fatal(err)
	}
	var last string
	for _, ev := range h.Events() {
		if p, ok := ev.Payload.(*wire.EvCandidateFrozen); ok && ev.TurnID == turnID {
			last = p.CandidateHash
		}
	}
	if got != last {
		t.Fatalf("round-1 candidate hash %s is not the hash over the author's calls alone %s", last, got)
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
