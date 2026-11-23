package acceptance

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/candidate"
	"github.com/boldfield/syndic/internal/client"
	"github.com/boldfield/syndic/internal/engine"
	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/history"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/transport/fake"
	"github.com/boldfield/syndic/internal/wire"
)

const (
	conversationsPara = "Conversations"
	contextPara       = "Context"
)

func init() {
	cover(conversationsPara, "a retried `submission_id` with a different `conversation_id`, `context_from_turn`, `max_rounds` or `workspace` is `submission_conflict`, and with all of them identical replays the stored reply", "TestConversations_EightFieldDeduplication")
	cover(conversationsPara, "a submit to an existing conversation with a different workspace is refused `workspace_mismatch`; the `conversations` reply is ordered by latest submit and filtered by workspace when one is given; default selection by workspace never opens a null-workspace conversation", "TestConversations_WorkspaceBoundDefaults")
	cover(conversationsPara, "a turn journaled before migration 005 appears as a conversation of one turn with a null workspace, is not opened by default, opens by prefix from any directory, and takes the workspace of the first submit to it", "TestConversations_LegacyNullAdoption")
	cover(conversationsPara, "`--conversation` with a unique prefix opens it, and with an unknown or ambiguous one, or one from another workspace, nothing is sent", "TestConversations_PrefixSelection")
	cover(conversationsPara, "a conversation whose last turn is weeks old in the journal's clock can be continued", "TestConversations_ContinueAfterWeeks")

	cover(contextPara, "a second turn's author request contains the first turn's input and answer; a reviewer's request contains the history as fenced data", "TestContext_SecondTurnRequests")
	cover(contextPara, "an `incomplete` first turn's input is present in the second turn's request and its answer slot is the missing-answer line", "TestContext_IncompleteTurnMarker")
	cover(contextPara, "each answer in history carries its label, and the input of every earlier turn is retained", "TestContext_LabelsAndRetainedInputs")
	cover(contextPara, "the same input after different histories yields different candidate hashes", "TestContext_DifferentHistoriesDifferentHashes")
	cover(contextPara, "the author's messages and the reviewer's block render from one canonical document and `context_hash` is the hash of that document", "TestContext_CanonicalDocument")
	cover(contextPara, "changing a changed-path annotation changes `context_hash`", "TestContext_ChangedPathAnnotationChangesHash")
	cover(contextPara, "an approval for one `context_hash` cannot approve a candidate with another", "TestContext_ApprovalRequiresMatchingHash")
	cover(contextPara, "a history that does not fit is refused with `budget: context` and nothing is dropped", "TestContext_BudgetRefusalWithoutTruncation")
	cover(contextPara, "`context_from_turn` drops exactly the earlier turns", "TestContext_ContextFromTurnDropsExactly")
	cover(contextPara, "no request attributes a history answer to a participant", "TestContext_NoParticipantAttribution")
}

const convWorkspaceA, convWorkspaceB = "/work/a", "/work/b"

// convStep answers whatever it is asked. A review is approved; the author's
// answer names the last message it was sent, so each answer is distinct and
// says which input it answers whatever order the steps are consumed in.
func convStep() fake.Step {
	return enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
		switch req.Envelope.Phase {
		case "review":
			return fake.VerdictApprove(req.Envelope.CandidateHash, req.Envelope.AttemptID)
		case "author":
			return "answer to " + req.Messages[len(req.Messages)-1].Content
		}
		return authorOK
	})
}

func convSteps(n int) []fake.Step {
	out := make([]fake.Step, n)
	for i := range out {
		out[i] = convStep()
	}
	return out
}

// convSuite is a suite whose author and reviewer both answer with convStep,
// after the reviewer's own first steps.
func convSuite(t *testing.T, reviewerFirst []fake.Step, opts ...enginetest.Option) *suite {
	t.Helper()
	return newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   convSteps(24),
		"reviewer": append(append([]fake.Step(nil), reviewerFirst...), convSteps(24)...),
	}, opts...)
}

// convSubmit sends a submission and waits for its turn to end.
func convSubmit(t *testing.T, s *suite, c *client.Client, req wire.SubmitRequest) (wire.SubmitReply, wire.TurnReply) {
	t.Helper()
	reply, err := c.Submit(s.ctx(), req)
	if err != nil {
		t.Fatalf("submit %s: %v", req.SubmissionID, err)
	}
	s.WaitTerminal(reply.TurnID)
	return reply, s.Turn(t, c, reply.TurnID)
}

func convReq(id, input, workspace string, conv *string) wire.SubmitRequest {
	return wire.SubmitRequest{SubmissionID: id, Input: input, Workspace: workspace, ConversationID: conv}
}

func soloReq(id, input, workspace string, conv *string) wire.SubmitRequest {
	preset := "solo"
	r := convReq(id, input, workspace, conv)
	r.Preset = &preset
	return r
}

func convTurns(t *testing.T, s *suite, c *client.Client, convID string) []wire.ConversationTurnSummary {
	t.Helper()
	conv, err := c.Conversation(s.ctx(), convID)
	if err != nil {
		t.Fatalf("conversation %s: %v", convID, err)
	}
	return conv.Turns
}

func convList(t *testing.T, s *suite, c *client.Client, workspace *string) []wire.ConversationSummary {
	t.Helper()
	reply, err := c.Conversations(s.ctx(), workspace, "", wire.MaxConversationListSize)
	if err != nil {
		t.Fatalf("conversations: %v", err)
	}
	return reply.Items
}

func convIDs(items []wire.ConversationSummary) []string {
	ids := make([]string, len(items))
	for i, it := range items {
		ids[i] = it.ID
	}
	return ids
}

func countTurnSubmitted(s *suite) int {
	n := 0
	for _, ev := range s.Events() {
		if ev.Kind == wire.EventKindTurnSubmitted {
			n++
		}
	}
	return n
}

// frozenDoc fetches the history document stored under hash and checks it is
// the document the hash is the SHA-256 of.
func frozenDoc(t *testing.T, s *suite, hash string) []byte {
	t.Helper()
	if len(hash) != 64 {
		t.Fatalf("context hash = %q, want 64 hex characters", hash)
	}
	doc, err := s.Store.HistoryDocument(hash)
	if err != nil {
		t.Fatalf("history document %s: %v", hash, err)
	}
	sum := sha256.Sum256(doc)
	if hex.EncodeToString(sum[:]) != hash {
		t.Fatalf("document stored under %s hashes to %x", hash, sum)
	}
	return doc
}

func lastCall(t *testing.T, s *suite, participant, phase string) transport.CompleteRequest {
	t.Helper()
	calls := s.calls(participant, phase)
	if len(calls) == 0 {
		t.Fatalf("%s received no %s request", participant, phase)
	}
	return calls[len(calls)-1]
}

func field(name, content string) string {
	return fmt.Sprintf("%s %d\n%s\n", name, len(content), content)
}

func TestConversations_EightFieldDeduplication(t *testing.T) {
	t.Parallel()
	s := convSuite(t, nil)
	c := s.Dial(t)

	base := soloReq("s1", "q1", convWorkspaceA, nil)
	first, _ := convSubmit(t, s, c, base)
	if first.ConversationID == "" || first.TurnID == "" {
		t.Fatalf("reply = %+v", first)
	}

	// Identical retry replays the stored reply and makes nothing.
	again, err := c.Submit(s.ctx(), base)
	if err != nil || !reflect.DeepEqual(again, first) {
		t.Fatalf("replay = %+v, %v; want %+v", again, err, first)
	}

	// Each of the eight members, changed alone, is a conflict.
	conv, from, rounds := first.ConversationID, first.TurnID, 2
	review, fast, provisional := "review", "fast", true
	changes := []struct {
		member string
		change func(r *wire.SubmitRequest)
	}{
		{"input", func(r *wire.SubmitRequest) { r.Input = "q2" }},
		{"preset", func(r *wire.SubmitRequest) { r.Preset = &review }},
		{"quick_profile", func(r *wire.SubmitRequest) { r.Preset = nil; r.QuickProfile = &fast }},
		{"allow_provisional", func(r *wire.SubmitRequest) { r.AllowProvisional = &provisional }},
		{"workspace", func(r *wire.SubmitRequest) { r.Workspace = convWorkspaceB }},
		{"conversation_id", func(r *wire.SubmitRequest) { r.ConversationID = &conv }},
		{"context_from_turn", func(r *wire.SubmitRequest) { r.ContextFromTurn = &from }},
		{"max_rounds", func(r *wire.SubmitRequest) { r.MaxRounds = &rounds }},
	}
	for _, tc := range changes {
		r := base
		tc.change(&r)
		_, err := c.Submit(s.ctx(), r)
		we := wantRefusal(t, err, wire.ErrorCodeSubmissionConflict)
		if we.TurnID == nil || *we.TurnID != first.TurnID {
			t.Fatalf("%s: conflict = %+v, want it to name turn %s", tc.member, we, first.TurnID)
		}
		if n := len(convTurns(t, s, c, first.ConversationID)); n != 1 {
			t.Fatalf("%s: the conversation has %d turns after the conflict, want 1", tc.member, n)
		}
		if n := countTurnSubmitted(s); n != 1 {
			t.Fatalf("%s: %d turn_submitted events, want 1", tc.member, n)
		}
	}
	if n := len(convList(t, s, c, nil)); n != 1 {
		t.Fatalf("%d conversations after the conflicts, want 1", n)
	}

	// quick_profile changed alone: the baseline names no preset and no
	// quick_profile, so every other member stays byte-for-byte identical.
	qbase := convReq("s3", "q3", convWorkspaceA, nil)
	qfirst, _ := convSubmit(t, s, c, qbase)
	qvariant := qbase
	qvariant.QuickProfile = &fast
	_, err = c.Submit(s.ctx(), qvariant)
	qe := wantRefusal(t, err, wire.ErrorCodeSubmissionConflict)
	if qe.TurnID == nil || *qe.TurnID != qfirst.TurnID {
		t.Fatalf("quick_profile alone: conflict = %+v, want it to name turn %s", qe, qfirst.TurnID)
	}
	if n := len(convTurns(t, s, c, qfirst.ConversationID)); n != 1 {
		t.Fatalf("quick_profile alone: %d turns after the conflict, want 1", n)
	}
	if n := countTurnSubmitted(s); n != 2 {
		t.Fatalf("quick_profile alone: %d turn_submitted events, want 2", n)
	}
	if n := len(convList(t, s, c, nil)); n != 2 {
		t.Fatalf("%d conversations after the quick_profile conflict, want 2", n)
	}

	// With all eight members present and identical the stored reply is replayed.
	zero := 0
	full := soloReq("s2", "q2", convWorkspaceA, &conv)
	full.ContextFromTurn = &from
	full.MaxRounds = &zero
	no := false
	full.AllowProvisional = &no
	second, _ := convSubmit(t, s, c, full)
	replay, err := c.Submit(s.ctx(), full)
	if err != nil || !reflect.DeepEqual(replay, second) {
		t.Fatalf("full replay = %+v, %v; want %+v", replay, err, second)
	}
	if n := len(convTurns(t, s, c, conv)); n != 2 {
		t.Fatalf("%d turns, want 2", n)
	}
	// And the same full request with a changed context_from_turn conflicts.
	other := second.TurnID
	full.ContextFromTurn = &other
	_, err = c.Submit(s.ctx(), full)
	_ = wantRefusal(t, err, wire.ErrorCodeSubmissionConflict)
	if n := len(convTurns(t, s, c, conv)); n != 2 {
		t.Fatalf("%d turns after the second conflict, want 2", n)
	}
}

func TestConversations_WorkspaceBoundDefaults(t *testing.T) {
	t.Parallel()
	s := convSuite(t, nil)
	c := s.Dial(t)
	wsA, wsB := convWorkspaceA, convWorkspaceB

	a, _ := convSubmit(t, s, c, soloReq("a1", "in a", wsA, nil))
	b, _ := convSubmit(t, s, c, soloReq("b1", "in b", wsB, nil))
	legacy, _ := convSubmit(t, s, c, soloReq("l1", "legacy", "", nil))
	a2, _ := convSubmit(t, s, c, soloReq("a2", "in a again", wsA, nil))

	// The workspace filter keeps only that workspace, latest submit first, and
	// never lists a null-workspace conversation.
	inA := convList(t, s, c, &wsA)
	if got, want := convIDs(inA), []string{a2.ConversationID, a.ConversationID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("conversations for %s = %v, want %v", wsA, got, want)
	}
	for _, it := range inA {
		if it.Workspace == nil || *it.Workspace != wsA {
			t.Fatalf("listed %+v for workspace %s", it, wsA)
		}
	}
	// The default for a directory is therefore the first of its list.
	if inA[0].ID != a2.ConversationID {
		t.Fatalf("default for %s = %s, want the most recently submitted %s", wsA, inA[0].ID, a2.ConversationID)
	}
	if got := convIDs(convList(t, s, c, &wsB)); !reflect.DeepEqual(got, []string{b.ConversationID}) {
		t.Fatalf("conversations for %s = %v", wsB, got)
	}
	none := "/work/none"
	if got := convList(t, s, c, &none); len(got) != 0 {
		t.Fatalf("a directory with no conversation lists %+v, want none", got)
	}

	// Unfiltered, latest first, with each workspace and the legacy null one.
	all := convList(t, s, c, nil)
	if got, want := convIDs(all), []string{a2.ConversationID, legacy.ConversationID, b.ConversationID, a.ConversationID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("all conversations = %v, want %v", got, want)
	}
	if all[1].Workspace != nil {
		t.Fatalf("legacy conversation has workspace %q, want null", *all[1].Workspace)
	}
	if all[0].TurnCount != 1 && all[0].TurnCount != 2 || all[0].Preview == "" {
		t.Fatalf("summary = %+v", all[0])
	}

	// A new submit moves a conversation to the head of its workspace's list.
	conv := a.ConversationID
	convSubmit(t, s, c, soloReq("a3", "back in the first", wsA, &conv))
	if got := convList(t, s, c, &wsA)[0].ID; got != a.ConversationID {
		t.Fatalf("default for %s after a submit to %s = %s", wsA, a.ConversationID, got)
	}

	// A different workspace is refused, and nothing is written.
	calls := len(s.Adapters["author"].Calls())
	events := countTurnSubmitted(s)
	before := len(convTurns(t, s, c, conv))
	bad := soloReq("a4", "elsewhere", wsB, &conv)
	_, err := c.Submit(s.ctx(), bad)
	_ = wantRefusal(t, err, wire.ErrorCodeWorkspaceMismatch)
	if n := len(convTurns(t, s, c, conv)); n != before {
		t.Fatalf("turns = %d after the refusal, want %d", n, before)
	}
	if n := countTurnSubmitted(s); n != events {
		t.Fatalf("turn_submitted events = %d after the refusal, want %d", n, events)
	}
	if n := len(s.Adapters["author"].Calls()); n != calls {
		t.Fatalf("author calls = %d after the refusal, want %d", n, calls)
	}
	// The stored workspace did not change.
	for _, it := range convList(t, s, c, nil) {
		if it.ID == conv && (it.Workspace == nil || *it.Workspace != wsA) {
			t.Fatalf("workspace of %s is now %v", conv, it.Workspace)
		}
	}
}

func TestConversations_LegacyNullAdoption(t *testing.T) {
	t.Parallel()
	s := convSuite(t, nil)
	c := s.Dial(t)
	wsA := convWorkspaceA

	// A turn submitted with no workspace is what migration 005 leaves for a
	// turn journaled before it: a conversation of one turn, workspace null.
	legacy, _ := convSubmit(t, s, c, soloReq("l1", "old question", "", nil))
	turns := convTurns(t, s, c, legacy.ConversationID)
	if len(turns) != 1 || turns[0].ID != legacy.TurnID {
		t.Fatalf("legacy conversation turns = %+v", turns)
	}
	items := convList(t, s, c, nil)
	if len(items) != 1 || items[0].Workspace != nil || items[0].TurnCount != 1 {
		t.Fatalf("legacy summary = %+v", items)
	}

	// It is not opened by default for any directory.
	if got := convList(t, s, c, &wsA); len(got) != 0 {
		t.Fatalf("the default for %s lists %+v, want none", wsA, got)
	}

	// It opens by prefix from any directory, and the first submit adopts the
	// submitting workspace.
	prefix := legacy.ConversationID[:8]
	reply, err := c.SubmitToConversation(s.ctx(), prefix, soloReq("l2", "new question", wsA, nil))
	if err != nil || reply.ConversationID != legacy.ConversationID {
		t.Fatalf("submit by prefix = %+v, %v", reply, err)
	}
	s.WaitTerminal(reply.TurnID)
	items = convList(t, s, c, nil)
	if len(items) != 1 || items[0].Workspace == nil || *items[0].Workspace != wsA || items[0].TurnCount != 2 {
		t.Fatalf("summary after adoption = %+v", items)
	}
	if got := convIDs(convList(t, s, c, &wsA)); !reflect.DeepEqual(got, []string{legacy.ConversationID}) {
		t.Fatalf("the default for %s = %v, want the adopted conversation", wsA, got)
	}

	// Once adopted it is bound: another workspace is refused.
	conv := legacy.ConversationID
	_, err = c.Submit(s.ctx(), soloReq("l3", "third", convWorkspaceB, &conv))
	_ = wantRefusal(t, err, wire.ErrorCodeWorkspaceMismatch)
	if n := len(convTurns(t, s, c, conv)); n != 2 {
		t.Fatalf("%d turns after the refusal, want 2", n)
	}
}

func TestConversations_PrefixSelection(t *testing.T) {
	t.Parallel()
	s := convSuite(t, nil)
	c := s.Dial(t)

	one, _ := convSubmit(t, s, c, soloReq("p1", "one", convWorkspaceA, nil))
	two, _ := convSubmit(t, s, c, soloReq("p2", "two", convWorkspaceB, nil))
	calls := len(s.Adapters["author"].Calls())
	submitted := countTurnSubmitted(s)
	quiet := func(what string) {
		t.Helper()
		if n := countTurnSubmitted(s); n != submitted {
			t.Fatalf("%s: %d turn_submitted events, want %d: something was sent", what, n, submitted)
		}
		if n := len(s.Adapters["author"].Calls()); n != calls {
			t.Fatalf("%s: author calls = %d, want %d", what, n, calls)
		}
		if n := len(convList(t, s, c, nil)); n != 2 {
			t.Fatalf("%s: %d conversations, want 2", what, n)
		}
	}

	// A unique prefix, and the full id, resolve to the conversation.
	for _, prefix := range []string{one.ConversationID[:8], one.ConversationID} {
		conv, err := c.Conversation(s.ctx(), prefix)
		if err != nil || conv.ID != one.ConversationID || len(conv.Turns) != 1 || conv.Turns[0].ID != one.TurnID {
			t.Fatalf("conversation %q = %+v, %v", prefix, conv, err)
		}
	}

	// Unknown and ambiguous prefixes are refused, and submit nothing.
	for _, tc := range []struct{ name, prefix, code string }{
		{"unknown", "zzzz-no-such", wire.ErrorCodeNotFound},
		{"ambiguous", "", wire.ErrorCodeAmbiguous},
	} {
		_, err := c.Conversation(s.ctx(), tc.prefix)
		_ = wantRefusal(t, err, tc.code)
		_, err = c.SubmitToConversation(s.ctx(), tc.prefix, soloReq("px-"+tc.name, "never", convWorkspaceA, nil))
		_ = wantRefusal(t, err, tc.code)
		quiet(tc.name)
	}

	// A conversation of another workspace is refused by the engine, writing nothing.
	_, err := c.SubmitToConversation(s.ctx(), two.ConversationID[:8], soloReq("px-other", "never", convWorkspaceA, nil))
	_ = wantRefusal(t, err, wire.ErrorCodeWorkspaceMismatch)
	quiet("another workspace")

	// The same prefix with the right workspace sends.
	reply, err := c.SubmitToConversation(s.ctx(), two.ConversationID[:8], soloReq("px-ok", "continued", convWorkspaceB, nil))
	if err != nil || reply.ConversationID != two.ConversationID {
		t.Fatalf("submit by prefix = %+v, %v", reply, err)
	}
	s.WaitTerminal(reply.TurnID)
	if n := len(convTurns(t, s, c, two.ConversationID)); n != 2 {
		t.Fatalf("%d turns, want 2", n)
	}
}

func TestConversations_ContinueAfterWeeks(t *testing.T) {
	t.Parallel()
	s := convSuite(t, nil)
	c := s.Dial(t)
	first, _ := convSubmit(t, s, c, soloReq("w1", "old question", convWorkspaceA, nil))

	s.Clock.Advance(21 * 24 * time.Hour)

	conv := first.ConversationID
	second, turn := convSubmit(t, s, c, soloReq("w2", "weeks later", convWorkspaceA, &conv))
	if second.ConversationID != conv || turn.State != engine.StateSoloComplete {
		t.Fatalf("continued turn = %+v in %s", turn, second.ConversationID)
	}
	doc := string(frozenDoc(t, s, second.ContextHash))
	if !strings.Contains(doc, "old question") {
		t.Fatalf("history of the continued turn lacks the old input:\n%s", doc)
	}
	if n := len(convTurns(t, s, c, conv)); n != 2 {
		t.Fatalf("%d turns, want 2", n)
	}
}

// twoTurns runs a first approved turn and a second in the same conversation,
// returning both replies.
func twoTurns(t *testing.T, s *suite, c *client.Client) (first, second wire.SubmitReply) {
	t.Helper()
	first, turn := convSubmit(t, s, c, convReq("t1", "first question", convWorkspaceA, nil))
	wantTurn(t, turn, engine.StateApproved, "all_approved")
	conv := first.ConversationID
	second, turn = convSubmit(t, s, c, convReq("t2", "second question", convWorkspaceA, &conv))
	wantTurn(t, turn, engine.StateApproved, "all_approved")
	return first, second
}

func TestContext_SecondTurnRequests(t *testing.T) {
	t.Parallel()
	s := convSuite(t, nil)
	c := s.Dial(t)
	first, second := twoTurns(t, s, c)
	if first.ContextHash != "" {
		t.Fatalf("the first turn's context hash = %q, want none", first.ContextHash)
	}
	doc := frozenDoc(t, s, second.ContextHash)

	// The author's second request: system, the first turn as user and
	// assistant messages, then the current input.
	author := s.calls("author", "author")
	if len(author) != 2 {
		t.Fatalf("author received %d requests, want 2", len(author))
	}
	if len(author[0].Messages) != 2 {
		t.Fatalf("the first turn's author messages = %+v, want system and input only", author[0].Messages)
	}
	msgs := author[1].Messages
	if len(msgs) != 4 || msgs[0].Role != "system" || msgs[1].Role != "user" || msgs[2].Role != "assistant" || msgs[3].Role != "user" {
		t.Fatalf("author messages = %+v", msgs)
	}
	if !strings.Contains(msgs[1].Content, "first question") {
		t.Fatalf("the history user message lacks the first input:\n%s", msgs[1].Content)
	}
	if msgs[2].Content != "answer to first question" {
		t.Fatalf("the history assistant message = %q, want the first answer verbatim", msgs[2].Content)
	}
	if msgs[3].Content != "second question" {
		t.Fatalf("the current input = %q", msgs[3].Content)
	}

	// The reviewers' request carries the whole document as one fenced block;
	// the first turn's carries none.
	reviews := s.calls("reviewer", "review")
	if len(reviews) != 2 {
		t.Fatalf("reviewer received %d requests, want 2", len(reviews))
	}
	for _, m := range reviews[0].Messages {
		if strings.Contains(m.Content, "BEGIN HISTORY") {
			t.Fatalf("the first review carries a history block:\n%s", m.Content)
		}
	}
	var withBlock int
	for _, m := range reviews[1].Messages {
		if m.Role != "system" && strings.Contains(m.Content, "BEGIN HISTORY") {
			withBlock++
			if m.Role != "user" {
				t.Fatalf("the history block is in a %s message", m.Role)
			}
			if !fencedBlock(m.Content, doc) {
				t.Fatalf("the reviewer message lacks the document as a fenced block:\n%s\ndocument:\n%s", m.Content, doc)
			}
		}
	}
	if withBlock != 1 {
		t.Fatalf("%d reviewer messages carry history, want 1", withBlock)
	}
}

// fencedBlock reports whether message holds doc verbatim as the body of one
// block whose BEGIN and END lines carry the same delimiter.
func fencedBlock(message string, doc []byte) bool {
	re := regexp.MustCompile(`===== (\S+) BEGIN HISTORY =====\n`)
	m := re.FindStringSubmatchIndex(message)
	if m == nil {
		return false
	}
	delim := message[m[2]:m[3]]
	want := string(doc) + "===== " + delim + " END HISTORY =====\n"
	return strings.HasPrefix(message[m[1]:], want) && strings.Count(message, "BEGIN HISTORY") == 1 && strings.Count(message, "END HISTORY") == 1
}

func TestContext_IncompleteTurnMarker(t *testing.T) {
	t.Parallel()
	// The first review is never sent: the turn ends incomplete with the
	// author's draft unapproved.
	s := convSuite(t, []fake.Step{{Kind: fake.StepKindNotSent, Err: errors.New("no secret")}})
	c := s.Dial(t)
	first, turn := convSubmit(t, s, c, convReq("i1", "an unfinished question", convWorkspaceA, nil))
	if turn.State != engine.StateIncomplete {
		t.Fatalf("first turn is %q, want incomplete", turn.State)
	}
	draft := "answer to an unfinished question"
	conv := first.ConversationID
	second, turn := convSubmit(t, s, c, convReq("i2", "the next question", convWorkspaceA, &conv))
	wantTurn(t, turn, engine.StateApproved, "all_approved")

	missing := "no approved answer (incomplete)"
	msgs := lastCall(t, s, "author", "author").Messages
	if len(msgs) != 4 {
		t.Fatalf("author messages = %+v, want system, input, missing answer and current input", msgs)
	}
	if !strings.Contains(msgs[1].Content, "an unfinished question") {
		t.Fatalf("the incomplete turn's input is not in the history:\n%s", msgs[1].Content)
	}
	if msgs[2].Role != "assistant" || msgs[2].Content != missing {
		t.Fatalf("the answer slot = %q (%s), want exactly %q", msgs[2].Content, msgs[2].Role, missing)
	}
	doc := string(frozenDoc(t, s, second.ContextHash))
	if !strings.Contains(doc, field("answer", missing)) || !strings.Contains(doc, field("state", "incomplete")) {
		t.Fatalf("the document does not record the missing answer:\n%s", doc)
	}
	// The draft nobody approved is in no request of the second turn.
	if strings.Contains(doc, draft) {
		t.Fatalf("the document holds the unapproved draft:\n%s", doc)
	}
	for _, p := range []struct{ who, phase string }{{"author", "author"}, {"reviewer", "review"}} {
		for _, m := range lastCall(t, s, p.who, p.phase).Messages {
			if strings.Contains(m.Content, draft) {
				t.Fatalf("%s was shown the unapproved draft:\n%s", p.who, m.Content)
			}
		}
	}
}

// unapprovedStep approves every review except those of a turn whose input
// carries marker, which it answers in prose twice over, so the turn ends
// incomplete without depending on the order the steps are consumed in.
func unapprovedStep(marker string) fake.Step {
	var mu sync.Mutex
	marked := map[string]bool{}
	return enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
		if req.Envelope.Phase != "review" {
			return convStepText(req)
		}
		mu.Lock()
		defer mu.Unlock()
		for _, m := range req.Messages {
			if strings.Contains(historyBlock.ReplaceAllString(m.Content, ""), marker) {
				marked[req.Envelope.TurnID] = true
			}
		}
		if marked[req.Envelope.TurnID] {
			return fake.VerdictProseApprove()
		}
		return fake.VerdictApprove(req.Envelope.CandidateHash, req.Envelope.AttemptID)
	})
}

// historyBlock matches a fenced history block, delimiters included.
var historyBlock = regexp.MustCompile(`(?s)===== (\S+) BEGIN HISTORY =====\n.*?===== \S+ END HISTORY =====\n`)

func convStepText(req transport.CompleteRequest) string {
	if req.Envelope.Phase == "author" {
		return "answer to " + req.Messages[len(req.Messages)-1].Content
	}
	return authorOK
}

func TestContext_LabelsAndRetainedInputs(t *testing.T) {
	t.Parallel()
	steps := make([]fake.Step, 24)
	for i := range steps {
		steps[i] = unapprovedStep("NOT-APPROVED")
	}
	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{"author": convSteps(24), "reviewer": steps})
	c := s.Dial(t)

	t1, turn := convSubmit(t, s, c, convReq("l1", "reviewed question", convWorkspaceA, nil))
	wantTurn(t, turn, engine.StateApproved, "all_approved")
	conv := t1.ConversationID
	t2, turn := convSubmit(t, s, c, soloReq("l2", "solo question", convWorkspaceA, &conv))
	if turn.State != engine.StateSoloComplete {
		t.Fatalf("solo turn is %q", turn.State)
	}
	t3, turn := convSubmit(t, s, c, convReq("l3", "NOT-APPROVED stays", convWorkspaceA, &conv))
	if turn.State != engine.StateIncomplete {
		t.Fatalf("turn 3 is %q, want incomplete", turn.State)
	}
	t4, turn := convSubmit(t, s, c, convReq("l4", "NOT-APPROVED but accepted", convWorkspaceA, &conv))
	if turn.State != engine.StateIncomplete {
		t.Fatalf("turn 4 is %q, want incomplete", turn.State)
	}
	if err := c.Override(s.ctx(), t4.TurnID, "the operator accepts it"); err != nil {
		t.Fatalf("override: %v", err)
	}
	if got := s.Turn(t, c, t4.TurnID).State; got != engine.StateOverridden {
		t.Fatalf("turn 4 is %q after the override", got)
	}
	final, turn := convSubmit(t, s, c, convReq("l5", "last question", convWorkspaceA, &conv))
	wantTurn(t, turn, engine.StateApproved, "all_approved")

	doc := frozenDoc(t, s, final.ContextHash)
	parsed, err := history.Parse(doc)
	if err != nil {
		t.Fatalf("parse: %v\n%s", err, doc)
	}
	want := []struct{ id, input, answer, label, state string }{
		{t1.TurnID, "reviewed question", "answer to reviewed question", "reviewed", engine.StateApproved},
		{t2.TurnID, "solo question", "answer to solo question", "unreviewed (solo)", engine.StateSoloComplete},
		{t3.TurnID, "NOT-APPROVED stays", "no approved answer (incomplete)", "reviewed", engine.StateIncomplete},
		{t4.TurnID, "NOT-APPROVED but accepted", "answer to NOT-APPROVED but accepted", "human override", engine.StateOverridden},
	}
	included := parsed.Included()
	if len(included) != len(want) {
		t.Fatalf("document includes %d turns, want %d:\n%s", len(included), len(want), doc)
	}
	author := lastCall(t, s, "author", "author").Messages
	if len(author) != 2+2*len(want) {
		t.Fatalf("author messages = %d, want %d", len(author), 2+2*len(want))
	}
	var block string
	for _, m := range lastCall(t, s, "reviewer", "review").Messages {
		if fencedBlock(m.Content, doc) {
			block = m.Content
		}
	}
	if block == "" {
		t.Fatal("the reviewer was shown no fenced history block")
	}
	for i, w := range want {
		got := included[i]
		answer := w.answer
		if got.ID != w.id || got.State != w.state || got.Label != w.label || string(got.Input) != w.input {
			t.Fatalf("turn %d of the document = %+v, want %+v", i, got, w)
		}
		// Every input is retained; an answer is a missing-answer line or the text shown.
		for _, f := range []string{field("turn", w.id), field("input", w.input), field("answer", answer), field("label", w.label), field("state", w.state)} {
			if !strings.Contains(string(doc), f) {
				t.Fatalf("document lacks %q:\n%s", f, doc)
			}
			if f != field("turn", w.id) && f != field("state", w.state) && !strings.Contains(block, f) {
				t.Fatalf("the reviewer's block lacks %q", f)
			}
		}
		// The author sees the input and label in the user message and the answer slot verbatim.
		u, a := author[1+2*i], author[2+2*i]
		if !strings.Contains(u.Content, field("input", w.input)) || !strings.Contains(u.Content, field("label", w.label)) {
			t.Fatalf("author user message %d = %q", i, u.Content)
		}
		if a.Role != "assistant" || a.Content != answer {
			t.Fatalf("author answer slot %d = %+v, want %q", i, a, answer)
		}
	}
}

func TestContext_DifferentHistoriesDifferentHashes(t *testing.T) {
	t.Parallel()
	s := convSuite(t, nil)
	c := s.Dial(t)
	x, _ := convSubmit(t, s, c, convReq("x1", "history alpha", convWorkspaceA, nil))
	y, _ := convSubmit(t, s, c, convReq("y1", "history beta", convWorkspaceA, nil))
	cx, cy := x.ConversationID, y.ConversationID
	sx, tx := convSubmit(t, s, c, convReq("x2", "the same input", convWorkspaceA, &cx))
	sy, ty := convSubmit(t, s, c, convReq("y2", "the same input", convWorkspaceA, &cy))

	if sx.ContextHash == "" || sy.ContextHash == "" || sx.ContextHash == sy.ContextHash {
		t.Fatalf("context hashes %q and %q: the same input after different histories must differ", sx.ContextHash, sy.ContextHash)
	}
	if tx.CandidateHash == nil || ty.CandidateHash == nil || *tx.CandidateHash == *ty.CandidateHash {
		t.Fatalf("candidate hashes %v and %v must differ", tx.CandidateHash, ty.CandidateHash)
	}

	// The candidate hash is bound to the frozen context: recomputed with it,
	// the hash is the turn's; with none, or the other's, it is not.
	manifest := ""
	if mh := s.Engine.Hello().ManifestHash; mh != nil {
		manifest = *mh
	}
	hashFor := func(turnID, answer, ctx string) string {
		h, err := candidate.Hash(candidate.Identity{
			ManifestHash:    manifest,
			TurnID:          turnID,
			AuthorAttemptID: s.attemptRows(t, turnID, "author", "author")[0].ID,
			ContextHash:     ctx,
			Content:         []byte(answer),
		})
		if err != nil {
			t.Fatal(err)
		}
		return h
	}
	if got := hashFor(sx.TurnID, "answer to the same input", sx.ContextHash); got != *tx.CandidateHash {
		t.Fatalf("recomputed candidate hash %s, turn has %s", got, *tx.CandidateHash)
	}
	for _, other := range []string{"", sy.ContextHash} {
		if hashFor(sx.TurnID, "answer to the same input", other) == *tx.CandidateHash {
			t.Fatalf("candidate hash does not depend on the context %q", other)
		}
	}
	// And the histories themselves are the two conversations' own.
	if !strings.Contains(string(frozenDoc(t, s, sx.ContextHash)), "history alpha") || !strings.Contains(string(frozenDoc(t, s, sy.ContextHash)), "history beta") {
		t.Fatal("a conversation's frozen history holds the other's input")
	}
}

func TestContext_CanonicalDocument(t *testing.T) {
	t.Parallel()
	s := convSuite(t, nil)
	c := s.Dial(t)
	first, second := twoTurns(t, s, c)
	doc := frozenDoc(t, s, second.ContextHash)

	// The document's layout: its first line, then length-prefixed fields.
	want := "syndic-history/v1\n" +
		field("turn", first.TurnID) + field("state", engine.StateApproved) +
		field("input", "first question") + field("answer", "answer to first question") + field("label", "reviewed")
	if string(doc) != want {
		t.Fatalf("document:\n%q\nwant:\n%q", doc, want)
	}
	parsed, err := history.Parse(doc)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !reflect.DeepEqual(parsed.Bytes(), doc) || parsed.Hash() != second.ContextHash {
		t.Fatalf("the document does not re-render to itself or hash to the context hash")
	}

	// The author's history messages are the document's rendering, exactly.
	var wantMsgs []transport.Message
	for _, m := range parsed.RenderForAuthor() {
		wantMsgs = append(wantMsgs, transport.Message{Role: m.Role, Content: m.Content})
	}
	msgs := lastCall(t, s, "author", "author").Messages
	if got := msgs[1 : len(msgs)-1]; !reflect.DeepEqual(got, wantMsgs) {
		t.Fatalf("author history messages = %+v, want %+v", got, wantMsgs)
	}

	// The reviewer's block is the same document, byte for byte.
	found := false
	for _, m := range lastCall(t, s, "reviewer", "review").Messages {
		found = found || fencedBlock(m.Content, doc)
	}
	if !found {
		t.Fatal("no reviewer message holds the document as one fenced block")
	}

	// The turn record shows the history it ran against.
	turn := s.Turn(t, c, second.TurnID)
	if turn.ContextFromTurn == nil || *turn.ContextFromTurn != first.TurnID {
		t.Fatalf("turn context_from_turn = %v, want %s", turn.ContextFromTurn, first.TurnID)
	}
}

func TestContext_ChangedPathAnnotationChangesHash(t *testing.T) {
	t.Parallel()
	// Milestone E's change sets are not part of this engine, so the
	// annotation is exercised on the document type the engine renders with.
	build := func(changes []history.Change, state string) string {
		d := history.New()
		if err := d.AddTurn(history.Turn{
			ID: "turn-1", State: engine.StateApproved, Input: []byte("q"), Answer: []byte("a"), Label: "reviewed",
			ChangesState: state, Changes: changes,
		}); err != nil {
			t.Fatal(err)
		}
		return d.Hash()
	}
	applied := []history.Change{{Path: "a.txt", Outcome: history.OutcomeApplied}, {Path: "b.txt", Outcome: history.OutcomeNotApplied}}
	flipped := []history.Change{{Path: "a.txt", Outcome: history.OutcomeApplied}, {Path: "b.txt", Outcome: history.OutcomeConflict}}
	renamed := []history.Change{{Path: "a.txt", Outcome: history.OutcomeApplied}, {Path: "c.txt", Outcome: history.OutcomeNotApplied}}

	base := build(applied, "partial")
	if build(applied, "partial") != base {
		t.Fatal("the same annotation hashed twice differs")
	}
	for name, other := range map[string]string{
		"a path's outcome":       build(flipped, "partial"),
		"a path's name":          build(renamed, "partial"),
		"the change set's state": build(applied, "discarded"),
		"no annotation":          build(nil, ""),
	} {
		if other == base {
			t.Fatalf("changing %s did not change the context hash", name)
		}
	}
}

func TestContext_ApprovalRequiresMatchingHash(t *testing.T) {
	t.Parallel()
	var s *suite
	// A reviewer shown a history approves the candidate as it would be
	// identified under no history, not under the one it was shown.
	stale := enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
		if req.Envelope.Phase != "review" {
			return convStepText(req)
		}
		shown := false
		for _, m := range req.Messages {
			shown = shown || historyBlock.MatchString(m.Content)
		}
		if !shown {
			return fake.VerdictApprove(req.Envelope.CandidateHash, req.Envelope.AttemptID)
		}
		h, err := candidate.Hash(candidate.Identity{
			ManifestHash:    *s.Engine.Hello().ManifestHash,
			TurnID:          req.Envelope.TurnID,
			AuthorAttemptID: s.attemptRows(t, req.Envelope.TurnID, "author", "author")[0].ID,
			ContextHash:     "",
			Content:         []byte("answer to second question"),
		})
		if err != nil {
			t.Error(err)
		}
		return fake.VerdictApprove(h, req.Envelope.AttemptID)
	})
	steps := make([]fake.Step, 12)
	for i := range steps {
		steps[i] = stale
	}
	s = newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{"author": convSteps(12), "reviewer": steps})
	c := s.Dial(t)
	first, turn := convSubmit(t, s, c, convReq("m1", "first question", convWorkspaceA, nil))
	wantTurn(t, turn, engine.StateApproved, "all_approved")

	conv := first.ConversationID
	second, turn := convSubmit(t, s, c, convReq("m2", "second question", convWorkspaceA, &conv))
	if len(second.ContextHash) != 64 {
		t.Fatalf("second turn context hash = %q", second.ContextHash)
	}
	wantTurn(t, turn, engine.StateIncomplete, "reviewer_failed:reviewer:rejected")
	rows := s.verdictRows(t, second.TurnID)
	if len(rows) != 1 || rows[0].ValidationStatus != "rejected" || rows[0].CandidateHash == *turn.CandidateHash {
		t.Fatalf("verdicts = %+v, want one rejected verdict that is not for candidate %s", rows, *turn.CandidateHash)
	}
	if turn.State == engine.StateApproved || len(turn.OpenBlocks) != 0 {
		t.Fatalf("the turn was approved by an approval of another context: %+v", turn)
	}
}

func TestContext_BudgetRefusalWithoutTruncation(t *testing.T) {
	t.Parallel()
	// A context of 10000 tokens leaves 9000 usable; the author reserves 4096
	// for output, so 4904 bytes of prompt fit. One 2600-byte input fits with
	// its history; a second does not, and history counts at its real bytes.
	big := strings.Repeat("x", 2600)
	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   {enginetest.Reply("ok1"), enginetest.Reply("ok2"), enginetest.Reply("ok3"), enginetest.Reply("ok4")},
		"reviewer": convSteps(4),
	}, enginetest.WithContextBound("author", 10000))
	c := s.Dial(t)

	t1, _ := convSubmit(t, s, c, soloReq("b1", big, convWorkspaceA, nil))
	conv := t1.ConversationID
	t2, _ := convSubmit(t, s, c, soloReq("b2", "small", convWorkspaceA, &conv))
	doc2 := frozenDoc(t, s, t2.ContextHash)
	if !strings.Contains(string(doc2), big) {
		t.Fatalf("the second turn's history was cut: %d bytes", len(doc2))
	}

	calls, submitted := len(s.Adapters["author"].Calls()), countTurnSubmitted(s)
	over := soloReq("b3", big, convWorkspaceA, &conv)
	_, err := c.Submit(s.ctx(), over)
	we := wantRefusal(t, err, wire.ErrorCodeBudget)
	if we.Budget == nil || *we.Budget != "context" || we.Participant == nil || *we.Participant != "author" ||
		we.Admission == nil || we.Limit == nil || *we.Admission <= *we.Limit {
		t.Fatalf("refusal = %+v, want budget context for the author with admission over the limit", we)
	}
	if n := len(convTurns(t, s, c, conv)); n != 2 {
		t.Fatalf("%d turns, want the refused submit to have written none", n)
	}
	if n := countTurnSubmitted(s); n != submitted {
		t.Fatalf("%d turn_submitted events after the refusal, want %d", n, submitted)
	}
	if n := len(s.Adapters["author"].Calls()); n != calls {
		t.Fatalf("%d author calls after the refusal, want %d: nothing is dispatched", n, calls)
	}
	// Nothing was dropped to make it fit: the stored history is whole, and
	// the refusal is not a trim.
	if again := frozenDoc(t, s, t2.ContextHash); string(again) != string(doc2) {
		t.Fatal("the stored history changed")
	}

	// The user trims; then exactly the later turns are history, untruncated.
	over.SubmissionID = "b4"
	over.ContextFromTurn = &t2.TurnID
	t3, _ := convSubmit(t, s, c, over)
	doc3 := string(frozenDoc(t, s, t3.ContextHash))
	if strings.Contains(doc3, big) || !strings.Contains(doc3, t2.TurnID) {
		t.Fatalf("the trimmed history holds the wrong turns:\n%s", doc3)
	}
	msgs := lastCall(t, s, "author", "author").Messages
	if len(msgs) != 4 || msgs[3].Content != big {
		t.Fatalf("author messages after the trim = %d, current input cut or changed", len(msgs))
	}
}

func TestContext_ContextFromTurnDropsExactly(t *testing.T) {
	t.Parallel()
	s := convSuite(t, nil)
	c := s.Dial(t)
	t1, _ := convSubmit(t, s, c, convReq("c1", "Q-one", convWorkspaceA, nil))
	conv := t1.ConversationID
	t2, _ := convSubmit(t, s, c, convReq("c2", "Q-two", convWorkspaceA, &conv))
	t3, _ := convSubmit(t, s, c, convReq("c3", "Q-three", convWorkspaceA, &conv))

	ids := func(doc []byte) []string {
		p, err := history.Parse(doc)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, turn := range p.Included() {
			out = append(out, turn.ID)
		}
		return out
	}
	all := frozenDoc(t, s, t3.ContextHash)
	if got := ids(all); !reflect.DeepEqual(got, []string{t1.TurnID, t2.TurnID}) {
		t.Fatalf("untrimmed history = %v", got)
	}

	// Trim from the second turn: exactly the first is left out.
	req := convReq("c4", "Q-four", convWorkspaceA, &conv)
	req.ContextFromTurn = &t2.TurnID
	t4, turn := convSubmit(t, s, c, req)
	if got := ids(frozenDoc(t, s, t4.ContextHash)); !reflect.DeepEqual(got, []string{t2.TurnID, t3.TurnID}) {
		t.Fatalf("history trimmed from %s = %v, want the second and third turns", t2.TurnID, got)
	}
	if turn.ContextFromTurn == nil || *turn.ContextFromTurn != t2.TurnID {
		t.Fatalf("turn context_from_turn = %v, want %s", turn.ContextFromTurn, t2.TurnID)
	}
	author := lastCall(t, s, "author", "author").Messages
	if len(author) != 6 {
		t.Fatalf("author messages = %d, want system, two turns of two messages, and the input", len(author))
	}
	want := []string{"Q-two", "answer to Q-two", "Q-three", "answer to Q-three"}
	for i, w := range want {
		if !strings.Contains(author[1+i].Content, w) {
			t.Fatalf("author message %d = %q, want it to hold %q", 1+i, author[1+i].Content, w)
		}
	}
	for _, p := range []struct{ who, phase string }{{"author", "author"}, {"reviewer", "review"}} {
		for _, m := range lastCall(t, s, p.who, p.phase).Messages {
			if strings.Contains(m.Content, "Q-one") || strings.Contains(m.Content, t1.TurnID) {
				t.Fatalf("%s was shown the trimmed turn:\n%s", p.who, m.Content)
			}
		}
	}

	// Trim from the latest earlier turn: only that one is in.
	req = convReq("c5", "Q-five", convWorkspaceA, &conv)
	req.ContextFromTurn = &t4.TurnID
	t5, _ := convSubmit(t, s, c, req)
	if got := ids(frozenDoc(t, s, t5.ContextHash)); !reflect.DeepEqual(got, []string{t4.TurnID}) {
		t.Fatalf("history trimmed from %s = %v", t4.TurnID, got)
	}

	// An unknown trim point is refused and writes nothing.
	bogus := "no-such-turn"
	bad := convReq("c6", "Q-six", convWorkspaceA, &conv)
	bad.ContextFromTurn = &bogus
	_, err := c.Submit(s.ctx(), bad)
	_ = wantRefusal(t, err, wire.ErrorCodeBadRequest)
	if n := len(convTurns(t, s, c, conv)); n != 5 {
		t.Fatalf("%d turns after the refusal, want 5", n)
	}
}

func TestContext_NoParticipantAttribution(t *testing.T) {
	t.Parallel()
	s := convSuite(t, nil)
	c := s.Dial(t)
	_, second := twoTurns(t, s, c)
	doc := string(frozenDoc(t, s, second.ContextHash))

	// The history, wherever it appears, names no participant, model or endpoint.
	banned := []string{"author", "reviewer", "llama2", "llama3", "local", "participant"}
	for _, b := range banned {
		if strings.Contains(strings.ToLower(doc), b) {
			t.Fatalf("the document names %q:\n%s", b, doc)
		}
	}
	history := lastCall(t, s, "author", "author").Messages
	for _, m := range history[1 : len(history)-1] {
		for _, b := range banned {
			if strings.Contains(strings.ToLower(m.Content), b) {
				t.Fatalf("a history message names %q: %+v", b, m)
			}
		}
	}
	// In the reviewer's request the history block is the document alone, and
	// that has been shown to name no one; the answer itself is an assistant
	// message of the author's request only.
	for _, m := range lastCall(t, s, "reviewer", "review").Messages {
		if m.Role == "assistant" {
			t.Fatalf("the reviewer's request has an assistant message: %+v", m)
		}
	}
}
