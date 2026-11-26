package engine_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

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

// A reviewer's verdict journals the results of its own calls, and a block
// citing one of them transfers exactly that result to the author. A result the
// block does not cite, and any result of the other reviewer, never reaches the
// author, even when the other reviewer's block names it.
func TestCitedReviewerResultTransfersToAuthor(t *testing.T) {
	var h *enginetest.Harness
	var r1Uncited string
	r1Done := make(chan struct{})
	var mu sync.Mutex
	own := map[string][]string{} // reviewer -> its result ids
	record := func(participant, attempt string, bodies ...string) []string {
		var ids []string
		err := h.Store.Tx(func(tx *journal.Tx) error {
			for i, b := range bodies {
				call, err := tx.RecordToolCall(attempt, i, fmt.Sprintf("prov_%d", i), "lookup", "read", []byte(`{"q":"`+b+`"}`))
				if err != nil {
					return err
				}
				res, err := tx.RecordToolResult(call.ID, journal.ToolCallExecuted, []byte(b), "")
				if err != nil {
					return err
				}
				ids = append(ids, res.ID)
			}
			return nil
		})
		if err != nil {
			panic(err)
		}
		mu.Lock()
		own[participant] = append(own[participant], ids...)
		mu.Unlock()
		return ids
	}
	r1 := enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
		ids := record("r1", req.Envelope.AttemptID, "cited-result-body", "uncited-result-body")
		r1Uncited = ids[1]
		close(r1Done)
		return fake.VerdictBlock(req.Envelope.CandidateHash, req.Envelope.AttemptID, "b1", "high", "see "+ids[0], "r", "r")
	})
	r2 := enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
		record("r2", req.Envelope.AttemptID, "other-reviewer-body")
		<-r1Done
		// Names the other reviewer's uncited result, which this reviewer never received.
		return fake.VerdictBlock(req.Envelope.CandidateHash, req.Envelope.AttemptID, "b2", "high", "see "+r1Uncited, "r", "r")
	})
	scripts := map[string][]fake.Step{
		"author": {enginetest.Reply(authorText), enginetest.Reply(revisedText)},
		"r1":     {r1, v2Verdict("1", "approve", "", "b1 withdrawn")},
		"r2":     {r2, v2Verdict("1", "approve", "", "b2 withdrawn")},
	}
	h = enginetest.New(t, revisionRoster("2", "r1", "r2"), scripts)
	turnID := submitReview(t, h, "s1")
	wantEnd(t, h, turnID, engine.StateApproved, "all_approved")

	calls := h.Adapters["author"].Calls()
	if len(calls) != 2 {
		t.Fatalf("author calls = %d, want 2", len(calls))
	}
	var feedback string
	for _, m := range calls[1].Request.Messages {
		feedback += m.Content + "\n"
	}
	if strings.Count(feedback, "cited-result-body") != 1 {
		t.Fatalf("the cited result should reach the author once:\n%s", feedback)
	}
	for _, leak := range []string{"uncited-result-body", "other-reviewer-body"} {
		if strings.Contains(feedback, leak) {
			t.Fatalf("author received %q:\n%s", leak, feedback)
		}
	}

	verdicts, err := h.Store.ListVerdicts(turnID)
	if err != nil {
		t.Fatal(err)
	}
	withEvidence := 0
	for _, v := range verdicts {
		ev, err := h.Store.GetVerdictEvidence(v.ID)
		if err != nil {
			continue
		}
		withEvidence++
		// Every verdict holds exactly its own reviewer's results, the
		// round-1 verdict included.
		a, err := h.Store.GetAttempt(v.AttemptID)
		if err != nil {
			t.Fatal(err)
		}
		if got, want := strings.Join(ev.ResultIDs, ","), strings.Join(own[a.Participant], ","); got != want {
			t.Fatalf("%s verdict %s results = %s, want %s", a.Participant, v.ID, got, want)
		}
	}
	if withEvidence != 4 {
		t.Fatalf("verdicts with evidence = %d, want 4", withEvidence)
	}
}

// Each accepted verdict binds only the observations its own reviewer added
// since its last verdict: never the author's, never the other reviewer's and
// never one an earlier verdict took. Its snapshot version is the candidate's
// frozen version raised to the newest observation it binds.
func TestVerdictEvidenceBindsOnlyOwnNewObservations(t *testing.T) {
	var h *enginetest.Harness
	var mu sync.Mutex
	// who maps a verdict attempt to its reviewer and round; want maps it to
	// the observation ids its verdict should bind; frozen holds the author's
	// observation each round, the newest before that round's candidate froze.
	type key struct {
		participant string
		round       int
	}
	who := map[string]key{}
	want := map[string][]string{}
	r1Observed := make(chan struct{})
	frozen := map[int]string{}
	observe := func(req transport.CompleteRequest, paths ...string) []string {
		var ids []string
		err := h.Store.Tx(func(tx *journal.Tx) error {
			for _, p := range paths {
				o, err := tx.RecordObservation(req.Envelope.TurnID, req.Envelope.AttemptID, p, journal.ObservationFile, []byte(p))
				if err != nil {
					return err
				}
				ids = append(ids, o.ID)
			}
			return nil
		})
		if err != nil {
			panic(err)
		}
		return ids
	}
	author := func(round int, path, text string) fake.Step {
		return enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
			ids := observe(req, path)
			mu.Lock()
			frozen[round] = ids[0]
			mu.Unlock()
			return text
		})
	}
	reviewer := func(participant string, round int, reply func(hash, attempt string) string, paths ...string) fake.Step {
		return enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
			ids := observe(req, paths...)
			mu.Lock()
			who[req.Envelope.AttemptID] = key{participant, round}
			want[req.Envelope.AttemptID] = ids
			mu.Unlock()
			return reply(req.Envelope.CandidateHash, req.Envelope.AttemptID)
		})
	}
	block := func(id string) func(string, string) string {
		return func(hash, attempt string) string {
			return fake.VerdictBlock(hash, attempt, id, "high", "e", "r", "r")
		}
	}
	withdraw := func(id string) func(string, string) string {
		return func(hash, attempt string) string {
			return "VERDICT v2\ncandidate: " + hash + "\nattempt: " + attempt +
				"\nround: 1\nblock-state: " + id + " withdrawn\nverdict: approve\nreason: r\nEND VERDICT"
		}
	}
	scripts := map[string][]fake.Step{
		"author": {author(0, "author-0.txt", authorText), author(1, "author-1.txt", revisedText)},
		"r1": {
			reviewer("r1", 0, block("b1"), "r1-0a.txt", "r1-0b.txt"),
			reviewer("r1", 1, func(hash, attempt string) string {
				close(r1Observed)
				return withdraw("b1")(hash, attempt)
			}, "r1-1.txt"),
		},
		"r2": {
			reviewer("r2", 0, block("b2"), "r2-0.txt"),
			// A lookup with no observation, accepted after r1's later first
			// touch: the verdict keeps the frozen version, not the turn's.
			reviewer("r2", 1, func(hash, attempt string) string {
				err := h.Store.Tx(func(tx *journal.Tx) error {
					call, err := tx.RecordToolCall(attempt, 0, "prov_r2", "lookup", "read", []byte(`{}`))
					if err != nil {
						return err
					}
					_, err = tx.RecordToolResult(call.ID, journal.ToolCallExecuted, []byte("r2 result"), "")
					return err
				})
				if err != nil {
					panic(err)
				}
				select {
				case <-r1Observed:
				case <-time.After(5 * time.Second):
					panic("r1 never observed in round 1")
				}
				return withdraw("b2")(hash, attempt)
			}),
		},
	}
	h = enginetest.New(t, revisionRoster("2", "r1", "r2"), scripts)
	turnID := submitReview(t, h, "s1")
	wantEnd(t, h, turnID, engine.StateApproved, "all_approved")

	versions := map[string]int{}
	err := h.Store.Tx(func(tx *journal.Tx) error {
		all, err := tx.Observations(turnID)
		for _, o := range all {
			versions[o.ID] = o.SnapshotVersion
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 6 {
		t.Fatalf("observations = %d, want 6", len(versions))
	}
	verdicts, err := h.Store.ListVerdicts(turnID)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[key]bool{}
	bound := map[string]bool{}
	for _, v := range verdicts {
		k, ok := who[v.AttemptID]
		if !ok {
			t.Fatalf("verdict %s from an unknown attempt", v.ID)
		}
		seen[k] = true
		ev, err := h.Store.GetVerdictEvidence(v.ID)
		if len(want[v.AttemptID]) == 0 && k != (key{"r2", 1}) {
			// Nothing looked up: nothing is journaled under the verdict.
			if !errors.Is(err, journal.ErrNoSuchContent) {
				t.Fatalf("%v verdict evidence = %+v, %v; want none", k, ev, err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%v verdict evidence: %v", k, err)
		}
		got, exp := append([]string{}, ev.ObservationIDs...), append([]string{}, want[v.AttemptID]...)
		sort.Strings(got)
		sort.Strings(exp)
		if strings.Join(got, ",") != strings.Join(exp, ",") {
			t.Fatalf("%v verdict binds observations %v, want its own new %v", k, got, exp)
		}
		version := versions[frozen[k.round]]
		if k == (key{"r2", 1}) && (len(ev.ResultIDs) != 1 || len(got) != 0) {
			t.Fatalf("r2's round-1 verdict evidence = %+v, want its one result", ev)
		}
		for _, id := range got {
			if bound[id] {
				t.Fatalf("observation %s bound twice", id)
			}
			bound[id] = true
			version = max(version, versions[id])
		}
		if ev.SnapshotVersion != version {
			t.Fatalf("%v verdict snapshot version = %d, want %d", k, ev.SnapshotVersion, version)
		}
	}
	for _, k := range []key{{"r1", 0}, {"r2", 0}, {"r1", 1}, {"r2", 1}} {
		if !seen[k] {
			t.Fatalf("no verdict from %v", k)
		}
	}
}

// A block that cites a reviewer's result and is maintained into later rounds
// transfers that result on every revision while it stays open, though the
// maintaining verdict carries no evidence field of its own. Once the block is
// withdrawn the result no longer reaches the author, and the other
// reviewer's result never does.
func TestMaintainedBlockCitationTransfersOnEveryRevision(t *testing.T) {
	var h *enginetest.Harness
	record := func(attempt, body string) string {
		var id string
		err := h.Store.Tx(func(tx *journal.Tx) error {
			call, err := tx.RecordToolCall(attempt, 0, "prov_0", "lookup", "read", []byte(`{"q":"`+body+`"}`))
			if err != nil {
				return err
			}
			res, err := tx.RecordToolResult(call.ID, journal.ToolCallExecuted, []byte(body), "")
			if err != nil {
				return err
			}
			id = res.ID
			return nil
		})
		if err != nil {
			panic(err)
		}
		return id
	}
	r1 := enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
		id := record(req.Envelope.AttemptID, "maintained-cited-body")
		return fake.VerdictBlock(req.Envelope.CandidateHash, req.Envelope.AttemptID, "b1", "high", "see "+id, "r", "r")
	})
	r2 := enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
		record(req.Envelope.AttemptID, "other-reviewer-body")
		return fake.VerdictApprove(req.Envelope.CandidateHash, req.Envelope.AttemptID)
	})
	scripts := map[string][]fake.Step{
		"author": {enginetest.Reply(authorText), enginetest.Reply(revisedText), enginetest.Reply(revisedText + " twice"), enginetest.Reply(revisedText + " thrice")},
		"r1": {
			r1,
			v2Verdict("1", "approve-with-changes", "", "b1 maintained"),
			v2Verdict("2", "approve-with-changes", "", "b1 withdrawn"),
			v2Verdict("3", "approve", ""),
		},
		"r2": {r2, v2Verdict("1", "approve", ""), v2Verdict("2", "approve", ""), v2Verdict("3", "approve", "")},
	}
	h = enginetest.New(t, revisionRoster("3", "r1", "r2"), scripts)
	turnID := submitReview(t, h, "s1")
	wantEnd(t, h, turnID, engine.StateApproved, "all_approved")

	calls := h.Adapters["author"].Calls()
	if len(calls) != 4 {
		t.Fatalf("author calls = %d, want 4", len(calls))
	}
	for i, wantCited := range []bool{true, true, false} {
		var feedback string
		for _, m := range calls[i+1].Request.Messages {
			feedback += m.Content + "\n"
		}
		got := strings.Count(feedback, "maintained-cited-body")
		if wantCited && got != 1 {
			t.Fatalf("revision %d: cited result of open block b1 appears %d times, want 1:\n%s", i+1, got, feedback)
		}
		if !wantCited && got != 0 {
			t.Fatalf("revision %d: result of withdrawn block b1 reached the author:\n%s", i+1, feedback)
		}
		if strings.Contains(feedback, "other-reviewer-body") {
			t.Fatalf("revision %d: author received the other reviewer's result:\n%s", i+1, feedback)
		}
	}
}
