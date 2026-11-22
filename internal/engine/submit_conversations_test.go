package engine_test

import (
	"strings"
	"testing"

	"github.com/boldfield/syndic/internal/engine"
	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/transport/fake"
	"github.com/boldfield/syndic/internal/wire"
)

func soloScriptN(answers ...string) map[string][]fake.Step {
	steps := make([]fake.Step, len(answers))
	for i, a := range answers {
		steps[i] = enginetest.Reply(a)
	}
	return map[string][]fake.Step{"author": steps}
}

func soloIn(id, input string, conv *string) wire.SubmitRequest {
	preset := "solo"
	return wire.SubmitRequest{SubmissionID: id, Input: input, Preset: &preset, ConversationID: conv}
}

// submitSolo submits one solo turn and waits for it to end.
func submitSolo(t *testing.T, h *enginetest.Harness, req wire.SubmitRequest) wire.SubmitReply {
	t.Helper()
	reply, err := h.Engine.Submit(req)
	if err != nil {
		t.Fatal(err)
	}
	wantEnd(t, h, reply.TurnID, engine.StateSoloComplete, "solo_ok")
	return reply
}

func turnCount(t *testing.T, h *enginetest.Harness, convID string) int {
	t.Helper()
	turns, err := h.Store.GetConversationTurns(convID)
	if err != nil {
		t.Fatal(err)
	}
	return len(turns)
}

func TestSubmitReplaysSameSubmissionAndConflictsOnAnyChangedMember(t *testing.T) {
	h := enginetest.New(t, enginetest.RosterTOML, soloScriptN("a1"))
	req := soloIn("s1", "q1", nil)
	req.Workspace = "/work/a"
	first := submitSolo(t, h, req)
	if first.ConversationID == "" {
		t.Fatal("reply carries no conversation id")
	}

	again, err := h.Engine.Submit(req)
	if err != nil {
		t.Fatal(err)
	}
	if again.TurnID != first.TurnID || again.ConversationID != first.ConversationID {
		t.Fatalf("replay = %+v, first = %+v", again, first)
	}

	conv, from, rounds, other := first.ConversationID, first.TurnID, 2, "/work/b"
	provisional := true
	changed := map[string]func(r *wire.SubmitRequest){
		"workspace":          func(r *wire.SubmitRequest) { r.Workspace = other },
		"conversation_id":    func(r *wire.SubmitRequest) { r.ConversationID = &conv },
		"context_from_turn":  func(r *wire.SubmitRequest) { r.ContextFromTurn = &from },
		"max_rounds":         func(r *wire.SubmitRequest) { r.MaxRounds = &rounds },
		"input":              func(r *wire.SubmitRequest) { r.Input = "q2" },
		"allow_provisional":  func(r *wire.SubmitRequest) { r.AllowProvisional = &provisional },
		"quick_profile/none": func(r *wire.SubmitRequest) { r.Preset = nil; q := "fast"; r.QuickProfile = &q },
	}
	for name, change := range changed {
		r := req
		change(&r)
		_, err := h.Engine.Submit(r)
		we := wantRefusal(t, err, wire.ErrorCodeSubmissionConflict)
		if we.TurnID == nil || *we.TurnID != first.TurnID {
			t.Fatalf("%s: conflict = %+v", name, we)
		}
	}
	if n := turnCount(t, h, first.ConversationID); n != 1 {
		t.Fatalf("turns = %d after conflicts", n)
	}
}

func TestSubmitReplaysPreConversationTurn(t *testing.T) {
	h := enginetest.New(t, enginetest.RosterTOML, soloScriptN("a1"))
	req := soloIn("s1", "q1", nil)
	first := submitSolo(t, h, req)

	// Rewrite the turn as a pre-conversation build journaled it.
	legacy, err := engine.LegacySubmissionHash(req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.DB.Conn().Exec(`UPDATE turns SET submission_hash = ? WHERE id = ?`, legacy, first.TurnID); err != nil {
		t.Fatal(err)
	}
	again, err := h.Engine.Submit(req)
	if err != nil || again.TurnID != first.TurnID {
		t.Fatalf("legacy replay = %+v, %v", again, err)
	}
	// A retry that sends any added member is a different submission.
	req.Workspace = "/work/a"
	_, err = h.Engine.Submit(req)
	_ = wantRefusal(t, err, wire.ErrorCodeSubmissionConflict)
}

func TestSubmitConversationAdoptionAndWorkspace(t *testing.T) {
	h := enginetest.New(t, enginetest.RosterTOML, soloScriptN("a1", "a2", "a3"))
	req := soloIn("s1", "q1", nil)
	req.Workspace = "/work/a"
	first := submitSolo(t, h, req)

	row, err := h.Store.GetConversation(first.ConversationID)
	if err != nil || row.Workspace == nil || *row.Workspace != "/work/a" {
		t.Fatalf("conversation = %+v, %v", row, err)
	}
	if countEvents(h, wire.EventKindConversationCreated) != 1 {
		t.Fatalf("conversation_created events = %d", countEvents(h, wire.EventKindConversationCreated))
	}

	// A different workspace is refused and writes nothing.
	conv := first.ConversationID
	bad := soloIn("s2", "q2", &conv)
	bad.Workspace = "/work/b"
	_, err = h.Engine.Submit(bad)
	_ = wantRefusal(t, err, wire.ErrorCodeWorkspaceMismatch)

	// An unknown conversation is refused, and creates no orphan.
	missing := "no-such-conversation"
	unknown := soloIn("s3", "q2", &missing)
	_, err = h.Engine.Submit(unknown)
	we := wantRefusal(t, err, wire.ErrorCodeNotFound)
	if we.Detail == nil || *we.Detail != "conversation_unknown" {
		t.Fatalf("detail = %v", we.Detail)
	}
	if n := turnCount(t, h, conv); n != 1 {
		t.Fatalf("turns = %d after refusals", n)
	}
	all, _, err := h.Store.ListConversations(nil, "", 10)
	if err != nil || len(all) != 1 {
		t.Fatalf("conversations = %d, %v", len(all), err)
	}

	// The same workspace, and none at all, continue it.
	ok := soloIn("s4", "q2", &conv)
	ok.Workspace = "/work/a"
	if r := submitSolo(t, h, ok); r.ConversationID != conv {
		t.Fatalf("conversation = %q", r.ConversationID)
	}
	if r := submitSolo(t, h, soloIn("s5", "q3", &conv)); r.ConversationID != conv {
		t.Fatalf("conversation = %q", r.ConversationID)
	}
	if n := turnCount(t, h, conv); n != 3 {
		t.Fatalf("turns = %d", n)
	}
}

func TestSubmitNullWorkspaceConversationAdoptsFirstWorkspace(t *testing.T) {
	h := enginetest.New(t, enginetest.RosterTOML, soloScriptN("a1", "a2"))
	first := submitSolo(t, h, soloIn("s1", "q1", nil))
	row, err := h.Store.GetConversation(first.ConversationID)
	if err != nil || row.Workspace != nil {
		t.Fatalf("workspace = %v, %v", row.Workspace, err)
	}
	conv := first.ConversationID
	next := soloIn("s2", "q2", &conv)
	next.Workspace = "/work/a"
	submitSolo(t, h, next)
	row, err = h.Store.GetConversation(conv)
	if err != nil || row.Workspace == nil || *row.Workspace != "/work/a" {
		t.Fatalf("workspace = %v, %v", row.Workspace, err)
	}
}

func TestSubmitFreezesRealHistory(t *testing.T) {
	h := enginetest.New(t, enginetest.RosterTOML, soloScriptN("first answer", "second answer"))
	first := submitSolo(t, h, soloIn("s1", "first question", nil))
	if first.ContextHash != "" {
		t.Fatalf("first turn context hash = %q, want none", first.ContextHash)
	}
	conv := first.ConversationID
	second := submitSolo(t, h, soloIn("s2", "second question", &conv))
	if len(second.ContextHash) != 64 {
		t.Fatalf("context hash = %q", second.ContextHash)
	}

	row, err := h.Store.GetTurn(second.TurnID)
	if err != nil || row.ContextHash == nil || *row.ContextHash != second.ContextHash {
		t.Fatalf("stored context hash = %v, %v", row.ContextHash, err)
	}
	doc, err := h.Store.HistoryDocument(second.ContextHash)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"syndic-history/v1\n", "first question", "first answer", "unreviewed (solo)", first.TurnID} {
		if !strings.Contains(string(doc), want) {
			t.Fatalf("frozen document lacks %q:\n%s", want, doc)
		}
	}
	if strings.Contains(string(doc), "second") {
		t.Fatalf("frozen document holds the current turn:\n%s", doc)
	}

	// The author saw it: prior input and answer, then the current input.
	calls := h.Adapters["author"].Calls()
	if len(calls) != 2 {
		t.Fatalf("author calls = %d", len(calls))
	}
	msgs := calls[1].Request.Messages
	if len(msgs) != 4 || msgs[0].Role != "system" ||
		msgs[1].Role != "user" || !strings.Contains(msgs[1].Content, "first question") ||
		msgs[2].Role != "assistant" || msgs[2].Content != "first answer" ||
		msgs[3].Role != "user" || msgs[3].Content != "second question" {
		t.Fatalf("author messages = %+v", msgs)
	}

	// The replay returns the frozen hash.
	again, err := h.Engine.Submit(soloIn("s2", "second question", &conv))
	if err != nil || again.ContextHash != second.ContextHash || again.ConversationID != conv {
		t.Fatalf("replay = %+v, %v", again, err)
	}
}

func TestSubmitHistoryReachesReviewers(t *testing.T) {
	h := enginetest.New(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   {enginetest.Reply("answer one"), enginetest.Reply("answer two")},
		"reviewer": {approveStep(), approveStep()},
	})
	first, err := h.Engine.Submit(wire.SubmitRequest{SubmissionID: "r1", Input: "question one"})
	if err != nil {
		t.Fatal(err)
	}
	wantEnd(t, h, first.TurnID, engine.StateApproved, "all_approved")
	conv := first.ConversationID
	second, err := h.Engine.Submit(wire.SubmitRequest{SubmissionID: "r2", Input: "question two", ConversationID: &conv})
	if err != nil {
		t.Fatal(err)
	}
	wantEnd(t, h, second.TurnID, engine.StateApproved, "all_approved")

	doc, err := h.Store.HistoryDocument(second.ContextHash)
	if err != nil || !strings.Contains(string(doc), "answer one") || !strings.Contains(string(doc), "reviewed") {
		t.Fatalf("document = %q, %v", doc, err)
	}
	calls := h.Adapters["reviewer"].Calls()
	if len(calls) != 2 {
		t.Fatalf("reviewer calls = %d", len(calls))
	}
	// Reviewers receive the whole canonical document, verbatim, as one
	// fenced block; the first turn's reviewer had none.
	first0 := calls[0].Request.Messages[1].Content
	if strings.Contains(first0, "BEGIN HISTORY") {
		t.Fatalf("first review carries history:\n%s", first0)
	}
	user := calls[1].Request.Messages[1].Content
	if strings.Count(user, "BEGIN HISTORY") != 1 || !strings.Contains(user, string(doc)) {
		t.Fatalf("reviewer message lacks the frozen document:\n%s", user)
	}
	// The author's request carries it as messages.
	author := h.Adapters["author"].Calls()[1].Request.Messages
	if len(author) != 4 || author[2].Content != "answer one" {
		t.Fatalf("author messages = %+v", author)
	}
	// Reviewer identities are not part of the document.
	if strings.Contains(string(doc), "reviewer") {
		t.Fatalf("document names a participant:\n%s", doc)
	}
}

func TestSubmitContextFromTurnTrimsExactly(t *testing.T) {
	h := enginetest.New(t, enginetest.RosterTOML, soloScriptN("a1", "a2", "a3", "a4"))
	t1 := submitSolo(t, h, soloIn("s1", "q1", nil))
	conv := t1.ConversationID
	t2 := submitSolo(t, h, soloIn("s2", "q2", &conv))
	req := soloIn("s3", "q3", &conv)
	req.ContextFromTurn = &t2.TurnID
	t3 := submitSolo(t, h, req)

	doc, err := h.Store.HistoryDocument(t3.ContextHash)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(doc), t1.TurnID) || !strings.Contains(string(doc), t2.TurnID) {
		t.Fatalf("trimmed document:\n%s", doc)
	}
	msgs := h.Adapters["author"].Calls()[2].Request.Messages
	if len(msgs) != 4 || !strings.Contains(msgs[1].Content, "q2") || strings.Contains(msgs[1].Content, "q1") {
		t.Fatalf("author messages = %+v", msgs)
	}

	// An unknown trim point is refused and writes nothing.
	bogus := "no-such-turn"
	bad := soloIn("s4", "q4", &conv)
	bad.ContextFromTurn = &bogus
	_, err = h.Engine.Submit(bad)
	we := wantRefusal(t, err, wire.ErrorCodeBadRequest)
	if we.Detail == nil || *we.Detail != "unknown_context_from_turn" {
		t.Fatalf("detail = %v", we.Detail)
	}
	if n := turnCount(t, h, conv); n != 3 {
		t.Fatalf("turns = %d", n)
	}
}

func TestSubmitOversizeHistoryRefusedWithoutTruncation(t *testing.T) {
	// A context of 10000 tokens leaves 9000 usable; the author reserves 4096
	// for output. One 2600-byte input fits, two do not, and the history is
	// counted at its real bytes.
	big := strings.Repeat("x", 2600)
	h := enginetest.New(t, enginetest.RosterTOML, soloScriptN("a1", "a2", "a3"),
		enginetest.WithContextBound("author", 10000))
	t1 := submitSolo(t, h, soloIn("s1", big, nil))
	conv := t1.ConversationID
	t2 := submitSolo(t, h, soloIn("s2", "small", &conv))

	over := soloIn("s3", big, &conv)
	_, err := h.Engine.Submit(over)
	we := wantRefusal(t, err, wire.ErrorCodeBudget)
	if we.Budget == nil || *we.Budget != "context" || we.Participant == nil || *we.Participant != "author" ||
		we.Admission == nil || we.Limit == nil || *we.Admission <= *we.Limit {
		t.Fatalf("refusal = %+v", we)
	}
	if n := turnCount(t, h, conv); n != 2 {
		t.Fatalf("turns = %d: a refused submit wrote a turn", n)
	}
	if n := len(h.Adapters["author"].Calls()); n != 2 {
		t.Fatalf("author calls = %d", n)
	}

	// The user trims; the history is then exactly the later turns.
	over.SubmissionID = "s4"
	over.ContextFromTurn = &t2.TurnID
	t3 := submitSolo(t, h, over)
	msgs := h.Adapters["author"].Calls()[2].Request.Messages
	for _, m := range msgs {
		if strings.Contains(m.Content, big[:100]) && m.Content != big {
			t.Fatalf("history was truncated or kept: %d bytes", len(m.Content))
		}
	}
	if t3.ContextHash == "" {
		t.Fatal("trimmed turn has no context hash")
	}
}

func TestSubmitBusyIsGlobalAcrossConversations(t *testing.T) {
	h := enginetest.New(t, enginetest.RosterTOML, soloScriptN("a1", "a2"))
	other := submitSolo(t, h, soloIn("s0", "q0", nil))

	release := h.Hold("author")
	open, err := h.Engine.Submit(soloIn("s1", "q1", nil))
	if err != nil {
		t.Fatal(err)
	}
	// Another conversation, and a new one, are both refused while it runs.
	conv := other.ConversationID
	for _, req := range []wire.SubmitRequest{soloIn("s2", "q2", &conv), soloIn("s3", "q3", nil)} {
		_, err := h.Engine.Submit(req)
		we := wantRefusal(t, err, wire.ErrorCodeBusy)
		if we.TurnID == nil || *we.TurnID != open.TurnID {
			t.Fatalf("busy = %+v", we)
		}
	}
	if n := turnCount(t, h, conv); n != 1 {
		t.Fatalf("turns = %d", n)
	}
	all, _, err := h.Store.ListConversations(nil, "", 10)
	if err != nil || len(all) != 2 {
		t.Fatalf("conversations = %d, %v: a busy refusal created one", len(all), err)
	}
	release()
	wantEnd(t, h, open.TurnID, engine.StateSoloComplete, "solo_ok")
}

func TestSubmitTaintIsConversationWideAndSurvivesTrim(t *testing.T) {
	h := enginetest.New(t, enginetest.RosterTOML, soloScriptN("a1", "a2", "a3", "a4", "a5"))
	t1 := submitSolo(t, h, soloIn("s1", "q1", nil))
	conv := t1.ConversationID
	if tainted(t, h, t1.TurnID) {
		t.Fatal("a tool-free turn is tainted")
	}
	// A turn that ran with read is tainted (milestone E records it).
	if err := h.Store.SetTurnFileTainted(t1.TurnID, true); err != nil {
		t.Fatal(err)
	}
	t2 := submitSolo(t, h, soloIn("s2", "q2", &conv))
	if !tainted(t, h, t2.TurnID) {
		t.Fatal("a turn after a tainted one is not tainted")
	}

	// Trimming the tainted turn out of the history does not clear it.
	trim := soloIn("s3", "q3", &conv)
	trim.ContextFromTurn = &t2.TurnID
	t3 := submitSolo(t, h, trim)
	doc, err := h.Store.HistoryDocument(t3.ContextHash)
	if err != nil || strings.Contains(string(doc), t1.TurnID) {
		t.Fatalf("trim kept the tainted turn: %v\n%s", err, doc)
	}
	if !tainted(t, h, t3.TurnID) {
		t.Fatal("trimming laundered the taint")
	}

	// Taint with nothing in view, and in another conversation.
	if err := h.Store.SetTurnFileTainted(t2.TurnID, false); err != nil {
		t.Fatal(err)
	}
	if err := h.Store.SetTurnFileTainted(t3.TurnID, false); err != nil {
		t.Fatal(err)
	}
	t4 := submitSolo(t, h, soloIn("s4", "q4", &conv))
	if !tainted(t, h, t4.TurnID) {
		t.Fatal("taint of the oldest turn did not carry forward")
	}
	fresh := submitSolo(t, h, soloIn("s5", "q5", nil))
	if tainted(t, h, fresh.TurnID) {
		t.Fatal("taint leaked into another conversation")
	}
}

func tainted(t *testing.T, h *enginetest.Harness, turnID string) bool {
	t.Helper()
	row, err := h.Store.GetTurn(turnID)
	if err != nil {
		t.Fatal(err)
	}
	return row.FileTainted
}
