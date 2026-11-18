package engine_test

import (
	"strings"
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

const authorText = "the answer is 42"

// reviewRoster is RosterTOML with the author renamed and one reviewer per
// id, all of them required by the review preset.
func reviewRoster(authorID string, reviewers ...string) string {
	toml := enginetest.RosterTOML
	toml = strings.ReplaceAll(toml, `id = "author"`, `id = "`+authorID+`"`)
	toml = strings.ReplaceAll(toml, `author = "author"`, `author = "`+authorID+`"`)
	start := strings.Index(toml, "[[participants]]\nid = \"reviewer\"")
	toml = toml[:start] + toml[strings.Index(toml, "[[endpoints]]"):]
	var extra, quoted []string
	for _, id := range reviewers {
		extra = append(extra, "[[participants]]\nid = \""+id+"\"\nmodel = \"llama3\"\nroute = \"api\"\nendpoint = \"local\"\neligible_roles = [\"reviewer\"]\npool = \"default\"\n")
		quoted = append(quoted, `"`+id+`"`)
	}
	toml = strings.Replace(toml, "[[endpoints]]", strings.Join(extra, "\n")+"\n[[endpoints]]", 1)
	toml = strings.Replace(toml, `required_reviewers = ["reviewer"]`, "required_reviewers = ["+strings.Join(quoted, ", ")+"]", 1)
	return toml
}

func approveStep() fake.Step {
	return enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
		return fake.VerdictApprove(req.Envelope.CandidateHash, req.Envelope.AttemptID)
	})
}

func blockStep(blockID string) fake.Step {
	return enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
		return fake.VerdictBlock(req.Envelope.CandidateHash, req.Envelope.AttemptID, blockID, "high", "evidence", "remedy", "reason")
	})
}

func verdictStep(f func(hash, attempt string) string) fake.Step {
	return enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
		return f(req.Envelope.CandidateHash, req.Envelope.AttemptID)
	})
}

func reviewScripts(reviewer fake.Step) map[string][]fake.Step {
	return map[string][]fake.Step{
		"author":   {enginetest.Reply(authorText)},
		"reviewer": {reviewer},
	}
}

func reviewScriptsMultiple(steps ...fake.Step) map[string][]fake.Step {
	return map[string][]fake.Step{
		"author":   {enginetest.Reply(authorText)},
		"reviewer": steps,
	}
}

func submitReview(t *testing.T, h *enginetest.Harness, id string) string {
	t.Helper()
	reply, err := h.Engine.Submit(wire.SubmitRequest{SubmissionID: id, Input: "what is the answer?"})
	if err != nil {
		t.Fatal(err)
	}
	return reply.TurnID
}

// waitEnded blocks until the turn is terminal and returns its row.
func waitEnded(t *testing.T, h *enginetest.Harness, turnID string) *journal.TurnRow {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		row, err := h.Store.GetTurn(turnID)
		if err != nil {
			t.Fatal(err)
		}
		if row.TerminalState != "" {
			return row
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("turn %s never ended; states %v", turnID, h.TurnStates(turnID))
	return nil
}

func wantEnd(t *testing.T, h *enginetest.Harness, turnID, state, reason string) {
	t.Helper()
	row := waitEnded(t, h, turnID)
	if row.TerminalState != state || row.TerminalReason != reason {
		t.Fatalf("turn ended %q %q, want %q %q", row.TerminalState, row.TerminalReason, state, reason)
	}
}

func verdictRows(t *testing.T, h *enginetest.Harness, turnID string) []journal.VerdictRow {
	t.Helper()
	rows, err := h.Store.ListVerdicts(turnID)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func getAttempt(t *testing.T, h *enginetest.Harness, id string) *journal.AttemptRow {
	t.Helper()
	var row *journal.AttemptRow
	if err := h.Store.Tx(func(tx *journal.Tx) error {
		var err error
		row, err = tx.GetAttempt(id)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return row
}

// attemptsOf returns the ids of the turn's attempts for a participant, in
// the order they began.
func attemptsOf(h *enginetest.Harness, turnID, participant string) []string {
	var ids []string
	for _, ev := range h.Events() {
		if p, ok := ev.Payload.(*wire.EvAttemptBegun); ok && ev.TurnID == turnID && p.Participant == participant {
			ids = append(ids, p.AttemptID)
		}
	}
	return ids
}

func waitCount(t *testing.T, h *enginetest.Harness, kind string, want int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if countEvents(h, kind) >= want {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("saw %d %s events, want %d", countEvents(h, kind), kind, want)
}

func TestReviewApprove(t *testing.T) {
	h := enginetest.New(t, enginetest.RosterTOML, reviewScripts(approveStep()))
	turnID := submitReview(t, h, "s1")
	wantEnd(t, h, turnID, engine.StateApproved, "all_approved")

	want := []string{
		engine.StateSubmitted, engine.StateAuthorDispatched, engine.StateCandidateFrozen,
		engine.StateReviewersDispatched, engine.StateApproved,
	}
	if got := h.TurnStates(turnID); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("turn states = %v, want %v", got, want)
	}

	authorAttempt := attemptsOf(h, turnID, "author")[0]
	wantHash, err := candidate.Hash(candidate.Identity{
		ManifestHash:    h.Engine.Hello().ManifestHash,
		TurnID:          turnID,
		AuthorAttemptID: authorAttempt,
		Content:         []byte(authorText),
	})
	if err != nil {
		t.Fatal(err)
	}
	var frozen *wire.EvCandidateFrozen
	for _, ev := range h.Events() {
		if p, ok := ev.Payload.(*wire.EvCandidateFrozen); ok {
			frozen = p
		}
	}
	if frozen == nil || frozen.CandidateHash != wantHash || frozen.TurnID != turnID {
		t.Fatalf("candidate_frozen = %+v, want hash %s", frozen, wantHash)
	}
	calls := h.Adapters["reviewer"].Calls()
	if len(calls) != 1 || calls[0].Request.Envelope.CandidateHash != wantHash {
		t.Fatalf("reviewer calls = %+v", calls)
	}
	env := calls[0].Request.Envelope
	if env.Phase != "review" || env.Participant != "reviewer" || env.TurnID != turnID || env.Epoch != 0 {
		t.Fatalf("envelope = %+v", env)
	}

	row := getAttempt(t, h, env.AttemptID)
	if row.Phase != "review" || row.Delimiter == nil || *row.Delimiter == "" || row.MaxOutputTokens == nil || row.Epoch != env.Epoch {
		t.Fatalf("attempt row = %+v", row)
	}
	if !strings.Contains(calls[0].Request.Messages[1].Content, *row.Delimiter) {
		t.Fatal("request is not fenced by the recorded delimiter")
	}
	if !strings.Contains(calls[0].Request.Messages[0].Content, env.AttemptID) {
		t.Fatal("system message lacks the attempt id")
	}
	if n := countEvents(h, wire.EventKindMessageAppended); n != 2 {
		t.Fatalf("message_appended events = %d, want author and reviewer request", n)
	}

	rows := verdictRows(t, h, turnID)
	if len(rows) != 1 || rows[0].ValidationStatus != "valid" || rows[0].Parsed != "approve" || rows[0].CandidateHash != wantHash {
		t.Fatalf("verdicts = %+v", rows)
	}
	if _, err := h.Engine.Submit(soloReq("s2")); err != nil {
		t.Fatalf("engine stayed busy: %v", err)
	}
}

func TestReviewVerdictOutcomes(t *testing.T) {
	cases := []struct {
		name       string
		scripts    map[string][]fake.Step
		reason     string
		verdictLen int
		calls      int
		status     string
	}{
		{"prose approve", reviewScriptsMultiple(
			enginetest.Reply(fake.VerdictProseApprove()),
			enginetest.Reply(fake.VerdictProseApprove()),
		), "malformed_twice:reviewer", 2, 2, "malformed"},
		{"wrong hash", reviewScripts(verdictStep(fake.VerdictWrongHash)), "reviewer_failed:reviewer:rejected", 1, 1, "rejected"},
		{"approve with changes", reviewScripts(verdictStep(fake.VerdictApproveWithChanges)), "revision_request:reviewer", 1, 1, "revision_request"},
		{"two envelopes", reviewScriptsMultiple(
			verdictStep(fake.VerdictTwoEnvelopes),
			verdictStep(fake.VerdictTwoEnvelopes),
		), "malformed_twice:reviewer", 2, 2, "malformed"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := enginetest.New(t, enginetest.RosterTOML, c.scripts)
			turnID := submitReview(t, h, "s1")
			wantEnd(t, h, turnID, engine.StateIncomplete, c.reason)
			rows := verdictRows(t, h, turnID)
			if len(rows) != c.verdictLen || (c.verdictLen > 0 && rows[0].ValidationStatus != c.status) {
				t.Fatalf("verdicts = %+v, want %d %s", rows, c.verdictLen, c.status)
			}
			if n := len(h.Adapters["reviewer"].Calls()); n != c.calls {
				t.Fatalf("reviewer called %d times, want %d", n, c.calls)
			}
			if _, err := h.Engine.Submit(soloReq("s2")); err != nil {
				t.Fatalf("engine stayed busy: %v", err)
			}
		})
	}
}

func TestReviewBlockWithEndMarker(t *testing.T) {
	h := enginetest.New(t, enginetest.RosterTOML, reviewScripts(verdictStep(fake.VerdictBlockWithEndMarker)))
	turnID := submitReview(t, h, "s1")
	row := waitEnded(t, h, turnID)
	rows := verdictRows(t, h, turnID)
	if len(rows) != 1 || rows[0].ValidationStatus != "valid" || rows[0].Parsed != "block" {
		t.Fatalf("verdicts = %+v", rows)
	}
	wantID := rows[0].ID + ":b1"
	if row.TerminalState != engine.StateIncomplete || row.TerminalReason != "block:"+wantID {
		t.Fatalf("turn ended %q %q, want block:%s", row.TerminalState, row.TerminalReason, wantID)
	}
	blocks, err := h.Store.OpenBlocks(turnID)
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 1 || blocks[0].ID != wantID || blocks[0].State != "open" || blocks[0].ReviewerBlockID != "b1" || blocks[0].Severity != "high" {
		t.Fatalf("open blocks = %+v", blocks)
	}
	var opened *wire.EvBlockOpened
	for _, ev := range h.Events() {
		if p, ok := ev.Payload.(*wire.EvBlockOpened); ok {
			opened = p
		}
	}
	if opened == nil || opened.BlockID != wantID {
		t.Fatalf("block_opened = %+v", opened)
	}
}

func TestReviewAuthorFailureDispatchesNothing(t *testing.T) {
	scripts := reviewScripts(approveStep())
	scripts["author"] = []fake.Step{{Kind: fake.StepKindReply, Text: "cut off", Status: transport.StatusTruncated, TransportStatus: transport.TransportStatusOK}}
	h := enginetest.New(t, enginetest.RosterTOML, scripts)
	turnID := submitReview(t, h, "s1")
	wantEnd(t, h, turnID, engine.StateIncomplete, "author_failed:truncated")
	if n := len(h.Adapters["reviewer"].Calls()); n != 0 {
		t.Fatalf("reviewer called %d times", n)
	}
	if n := len(attemptsOf(h, turnID, "reviewer")); n != 0 {
		t.Fatalf("%d reviewer attempts begun", n)
	}
	if n := countEvents(h, wire.EventKindCandidateFrozen); n != 0 {
		t.Fatalf("%d candidates frozen", n)
	}
	if status, err := h.Engine.Status(); err != nil || len(status.TurnsOpen) != 0 {
		t.Fatalf("status = %+v, err %v", status, err)
	}
	if _, err := h.Engine.Submit(soloReq("s2")); err != nil {
		t.Fatalf("engine stayed busy: %v", err)
	}
}

func TestReviewReviewerGateFailures(t *testing.T) {
	cases := []struct {
		name   string
		step   fake.Step
		reason string
	}{
		{"identity mismatch", fake.Step{Kind: fake.StepKindReply, Text: "x", Status: transport.StatusComplete, TransportStatus: transport.TransportStatusOK, ReportedModel: "other-model"},
			"reviewer_failed:reviewer:gate:identity mismatch"},
		{"truncated", fake.Step{Kind: fake.StepKindReply, Text: "x", Status: transport.StatusTruncated, TransportStatus: transport.TransportStatusOK},
			"reviewer_failed:reviewer:truncated"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := enginetest.New(t, enginetest.RosterTOML, reviewScripts(c.step))
			turnID := submitReview(t, h, "s1")
			wantEnd(t, h, turnID, engine.StateIncomplete, c.reason)
			if rows := verdictRows(t, h, turnID); len(rows) != 0 {
				t.Fatalf("verdicts = %+v", rows)
			}
			row := getAttempt(t, h, attemptsOf(h, turnID, "reviewer")[0])
			if row.GateError == nil || row.Commentary != nil && *row.Commentary != "" {
				t.Fatalf("attempt row = %+v", row)
			}
			if _, err := h.Engine.Submit(soloReq("s2")); err != nil {
				t.Fatalf("engine stayed busy: %v", err)
			}
		})
	}
}

func TestReviewTwoReviewersBothApprove(t *testing.T) {
	scripts := map[string][]fake.Step{
		"author": {enginetest.Reply(authorText)},
		"r1":     {approveStep()},
		"r2":     {approveStep()},
	}
	h := enginetest.New(t, reviewRoster("author", "r1", "r2"), scripts)
	turnID := submitReview(t, h, "s1")
	wantEnd(t, h, turnID, engine.StateApproved, "all_approved")
	if rows := verdictRows(t, h, turnID); len(rows) != 2 {
		t.Fatalf("verdicts = %+v", rows)
	}
	states := h.TurnStates(turnID)
	if states[len(states)-1] != engine.StateApproved || states[len(states)-2] != engine.StateReviewersDispatched {
		t.Fatalf("turn states = %v", states)
	}
}

func TestReviewApproveAndBlockEitherOrder(t *testing.T) {
	for _, blockFirst := range []bool{true, false} {
		name := "approve first"
		if blockFirst {
			name = "block first"
		}
		t.Run(name, func(t *testing.T) {
			scripts := map[string][]fake.Step{
				"author": {enginetest.Reply(authorText)},
				"r1":     {approveStep()},
				"r2":     {blockStep("b1")},
			}
			h := enginetest.New(t, reviewRoster("author", "r1", "r2"), scripts)
			// Hold the reviewer that must answer second.
			second := "r1"
			if !blockFirst {
				second = "r2"
			}
			release := h.Hold(second)
			turnID := submitReview(t, h, "s1")
			waitCount(t, h, wire.EventKindVerdictRecorded, 1)
			if blockFirst {
				row := waitEnded(t, h, turnID)
				release()
				waitCount(t, h, wire.EventKindAttemptCompleted, 3)
				after, err := h.Store.GetTurn(turnID)
				if err != nil || after.TerminalState != row.TerminalState || after.TerminalReason != row.TerminalReason {
					t.Fatalf("turn changed from %+v to %+v, err %v", row, after, err)
				}
			} else {
				// The approve alone does not end the turn.
				if row, err := h.Store.GetTurn(turnID); err != nil || row.TerminalState != "" {
					t.Fatalf("turn ended early: %+v, err %v", row, err)
				}
				if status, err := h.Engine.Status(); err != nil || len(status.TurnsOpen) != 1 {
					t.Fatalf("status = %+v, err %v", status, err)
				}
				release()
			}
			row := waitEnded(t, h, turnID)
			rows := verdictRows(t, h, turnID)
			var blockVerdict string
			for _, v := range rows {
				if v.Parsed == "block" {
					blockVerdict = v.ID
				}
			}
			if row.TerminalState != engine.StateIncomplete || blockVerdict == "" || row.TerminalReason != "block:"+blockVerdict+":b1" {
				t.Fatalf("turn ended %q %q; verdicts %+v", row.TerminalState, row.TerminalReason, rows)
			}
			blocks, err := h.Store.OpenBlocks(turnID)
			if err != nil || len(blocks) != 1 || blocks[0].ID != row.TerminalReason[len("block:"):] {
				t.Fatalf("open blocks = %+v, err %v", blocks, err)
			}
		})
	}
}

func TestReviewLateResultIsQuarantined(t *testing.T) {
	scripts := map[string][]fake.Step{
		"author": {enginetest.Reply(authorText)},
		"r1":     {approveStep()},
		"r2":     {blockStep("b1")},
	}
	h := enginetest.New(t, reviewRoster("author", "r1", "r2"), scripts)
	release := h.Hold("r1")
	turnID := submitReview(t, h, "s1")
	before := waitEnded(t, h, turnID)
	if status, err := h.Engine.Status(); err != nil {
		t.Fatal(err)
	} else {
		for _, p := range status.Participants {
			if p.ID == "r1" && p.State != engine.ParticipantRunning {
				t.Fatalf("r1 state = %s while in flight", p.State)
			}
		}
	}
	release()
	waitCount(t, h, wire.EventKindAttemptCompleted, 3)

	after, err := h.Store.GetTurn(turnID)
	if err != nil || after.TerminalState != before.TerminalState || after.TerminalReason != before.TerminalReason || after.Epoch != before.Epoch {
		t.Fatalf("turn changed from %+v to %+v, err %v", before, after, err)
	}
	if row := getAttempt(t, h, attemptsOf(h, turnID, "r1")[0]); !row.Quarantined {
		t.Fatalf("late attempt not quarantined: %+v", row)
	}
	if rows := verdictRows(t, h, turnID); len(rows) != 1 {
		t.Fatalf("verdicts = %+v", rows)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		status, err := h.Engine.Status()
		if err != nil {
			t.Fatal(err)
		}
		running := false
		for _, p := range status.Participants {
			running = running || p.State == engine.ParticipantRunning
		}
		if !running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("a quarantined reviewer is still reported running")
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestReviewRosterSizes(t *testing.T) {
	for _, n := range []int{3, 5} {
		t.Run(string(rune('0'+n))+" participants", func(t *testing.T) {
			scripts := map[string][]fake.Step{"author": {enginetest.Reply(authorText)}}
			var reviewers []string
			for i := 1; i < n; i++ {
				id := "r" + string(rune('0'+i))
				reviewers = append(reviewers, id)
				scripts[id] = []fake.Step{approveStep()}
			}
			h := enginetest.New(t, reviewRoster("author", reviewers...), scripts)
			turnID := submitReview(t, h, "s1")
			wantEnd(t, h, turnID, engine.StateApproved, "all_approved")
			if rows := verdictRows(t, h, turnID); len(rows) != n-1 {
				t.Fatalf("verdicts = %d, want %d", len(rows), n-1)
			}
		})
	}
}

func TestReviewBlockIDsDoNotCollide(t *testing.T) {
	scripts := map[string][]fake.Step{
		"author": {enginetest.Reply(authorText)},
		"r1":     {blockStep("b1")},
		"r2":     {blockStep("b1")},
	}
	h := enginetest.New(t, reviewRoster("author", "r1", "r2"), scripts)
	release := h.Hold("r2")
	turnID := submitReview(t, h, "s1")
	row := waitEnded(t, h, turnID)
	first := strings.TrimPrefix(row.TerminalReason, "block:")

	// The turn has ended, so r2's own block cannot arrive through the
	// engine; record it through the store.
	r2Attempt := attemptsOf(h, turnID, "r2")[0]
	var second string
	err := h.Store.Tx(func(tx *journal.Tx) error {
		verdictID, err := tx.RecordVerdict(r2Attempt, "hash", "block", "reason", "valid")
		if err != nil {
			return err
		}
		second, err = tx.OpenBlockFor(verdictID, "b1", "hash", "high", "e", "r")
		return err
	})
	release()
	if err != nil {
		t.Fatalf("second block with the same reviewer id: %v", err)
	}
	if first == second || !strings.HasSuffix(first, ":b1") || !strings.HasSuffix(second, ":b1") {
		t.Fatalf("block ids %q and %q", first, second)
	}
	blocks, err := h.Store.OpenBlocks(turnID)
	if err != nil || len(blocks) != 2 {
		t.Fatalf("open blocks = %+v, err %v", blocks, err)
	}
}

func TestReviewRequestContent(t *testing.T) {
	const authorID = "zq9-writer"
	scripts := map[string][]fake.Step{
		authorID:   {enginetest.Reply(authorText)},
		"reviewer": {approveStep()},
	}
	h := enginetest.New(t, reviewRoster(authorID, "reviewer"), scripts)
	turnID := submitReview(t, h, "s1")
	wantEnd(t, h, turnID, engine.StateApproved, "all_approved")

	calls := h.Adapters["reviewer"].Calls()
	if len(calls) != 1 {
		t.Fatalf("reviewer calls = %d", len(calls))
	}
	msgs := calls[0].Request.Messages
	if len(msgs) != 2 || msgs[0].Role != "system" || msgs[1].Role != "user" {
		t.Fatalf("messages = %+v", msgs)
	}
	if !strings.Contains(msgs[1].Content, "\n"+authorText+"\n") || !strings.Contains(msgs[1].Content, "what is the answer?") {
		t.Fatalf("user message lacks the candidate or request:\n%s", msgs[1].Content)
	}
	for _, m := range msgs {
		if strings.Contains(m.Content, authorID) {
			t.Fatalf("author id leaked into the %s message", m.Role)
		}
	}
	env := calls[0].Request.Envelope
	if env.Participant == authorID {
		t.Fatalf("envelope = %+v", env)
	}
}

func TestReviewCommentaryStored(t *testing.T) {
	step := enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
		return "before the envelope\n" + fake.VerdictApprove(req.Envelope.CandidateHash, req.Envelope.AttemptID) + "\nafter the envelope"
	})
	h := enginetest.New(t, enginetest.RosterTOML, reviewScripts(step))
	turnID := submitReview(t, h, "s1")
	wantEnd(t, h, turnID, engine.StateApproved, "all_approved")
	row := getAttempt(t, h, attemptsOf(h, turnID, "reviewer")[0])
	if row.Commentary == nil || !strings.Contains(*row.Commentary, "before the envelope") || !strings.Contains(*row.Commentary, "after the envelope") ||
		strings.Contains(*row.Commentary, "VERDICT v1") {
		t.Fatalf("commentary = %v", row.Commentary)
	}
}

func TestReviewDispatchTxIsAtomic(t *testing.T) {
	h := enginetest.New(t, enginetest.RosterTOML, reviewScripts(approveStep()))
	h.Engine.SetPayloadHook(func(kind string, payload any) any {
		if p, ok := payload.(wire.EvTurnState); ok && p.State == engine.StateReviewersDispatched {
			return unmarshalable{C: make(chan int)}
		}
		return payload
	})
	turnID := submitReview(t, h, "s1")
	wantEnd(t, h, turnID, engine.StateIncomplete, "reviewer_failed:reviewer:error")
	if n := len(attemptsOf(h, turnID, "reviewer")); n != 0 {
		t.Fatalf("%d reviewer attempts leaked", n)
	}
	if n := len(h.Adapters["reviewer"].Calls()); n != 0 {
		t.Fatalf("reviewer called %d times", n)
	}
	h.Engine.SetPayloadHook(nil)
	if _, err := h.Engine.Submit(soloReq("s2")); err != nil {
		t.Fatalf("engine stayed busy: %v", err)
	}
}

func TestReviewRepairSuccessful(t *testing.T) {
	h := enginetest.New(t, enginetest.RosterTOML, reviewScriptsMultiple(
		enginetest.Reply(fake.VerdictProseApprove()),
		approveStep(),
	))
	turnID := submitReview(t, h, "s1")
	wantEnd(t, h, turnID, engine.StateApproved, "all_approved")
	attempts := attemptsOf(h, turnID, "reviewer")
	if len(attempts) != 2 {
		t.Fatalf("%d reviewer attempts, want 2", len(attempts))
	}
	first := getAttempt(t, h, attempts[0])
	second := getAttempt(t, h, attempts[1])
	if first.RepairOf != nil {
		t.Fatalf("first attempt has RepairOf set: %v", first.RepairOf)
	}
	if second.RepairOf == nil || *second.RepairOf != first.ID {
		t.Fatalf("second attempt RepairOf = %v, want %s", second.RepairOf, first.ID)
	}
	rows := verdictRows(t, h, turnID)
	if len(rows) != 2 {
		t.Fatalf("%d verdicts, want 2", len(rows))
	}
	firstVerdict := rows[0]
	secondVerdict := rows[1]
	if firstVerdict.AttemptID == attempts[1] {
		firstVerdict, secondVerdict = secondVerdict, firstVerdict
	}
	if firstVerdict.ValidationStatus != "malformed" || secondVerdict.ValidationStatus != "valid" {
		t.Fatalf("verdict statuses = [%s, %s], want [malformed, valid]", firstVerdict.ValidationStatus, secondVerdict.ValidationStatus)
	}
	if n := len(h.Adapters["reviewer"].Calls()); n != 2 {
		t.Fatalf("reviewer called %d times, want 2", n)
	}
	if _, err := h.Engine.Submit(soloReq("s2")); err != nil {
		t.Fatalf("engine stayed busy: %v", err)
	}
}

func TestReviewRepairMessages(t *testing.T) {
	h := enginetest.New(t, enginetest.RosterTOML, reviewScriptsMultiple(
		enginetest.Reply(fake.VerdictProseApprove()),
		approveStep(),
	))
	turnID := submitReview(t, h, "s1")
	waitEnded(t, h, turnID)
	calls := h.Adapters["reviewer"].Calls()
	if len(calls) != 2 {
		t.Fatalf("%d calls, want 2", len(calls))
	}
	originalOutput := fake.VerdictProseApprove()
	originalMsgs := calls[0].Request.Messages
	repairMsgs := calls[1].Request.Messages
	if len(repairMsgs) != 4 {
		t.Fatalf("repair has %d messages, want 4 (system, user, assistant, user)", len(repairMsgs))
	}
	if repairMsgs[0].Role != "system" || repairMsgs[1].Role != "user" || repairMsgs[2].Role != "assistant" || repairMsgs[3].Role != "user" {
		t.Fatalf("repair message roles = %v, want [system, user, assistant, user]", []string{
			repairMsgs[0].Role, repairMsgs[1].Role, repairMsgs[2].Role, repairMsgs[3].Role,
		})
	}
	if repairMsgs[2].Content != originalOutput {
		t.Fatalf("repair assistant message != original output:\nrepair=%q\noriginal=%q", repairMsgs[2].Content, originalOutput)
	}
	if repairMsgs[1].Content != originalMsgs[len(originalMsgs)-1].Content {
		t.Fatalf("repair user message != original user message")
	}
	if _, err := h.Engine.Submit(soloReq("s2")); err != nil {
		t.Fatalf("engine stayed busy: %v", err)
	}
}
