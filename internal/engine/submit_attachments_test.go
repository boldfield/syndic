package engine_test

import (
	"strings"
	"testing"

	"github.com/boldfield/syndic/internal/config"
	"github.com/boldfield/syndic/internal/engine"
	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/tools"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/transport/fake"
	"github.com/boldfield/syndic/internal/wire"
)

// attachRoster is egressRoster plus a solo preset whose only tool is web with
// web and file content off, and protected paths; no [tools] table may precede
// it, so no destinations are named.
func attachRoster(t *testing.T) string {
	t.Helper()
	r := strings.Replace(egressRoster(t), `models = ["llama2", "llama3"]`, `models = ["llama2", "llama3", "refmodel"]
referee_model = "refmodel"`, 1)
	return r + `
[presets.noweb]
mode = "solo"
author = "author"
required_reviewers = []
tools = ["web"]
allow_web_with_read = false

[tools]
protected_paths = [".env*"]
`
}

func newAttachHarness(t *testing.T, scripts map[string][]fake.Step) *enginetest.Harness {
	t.Helper()
	return enginetest.New(t, attachRoster(t), scripts,
		enginetest.WithToolCalls("author", transport.TriYes),
		enginetest.WithToolCalls("reviewer", transport.TriYes),
		enginetest.WithModelToolCalls("local", "refmodel", transport.TriYes),
		enginetest.WithModelToolCalls("lan", "refmodel", transport.TriYes))
}

func attachReq(id, preset, dir string, conv *string, paths ...string) wire.SubmitRequest {
	req := presetIn(id, preset, conv)
	req.Workspace = dir
	req.Attachments = paths
	return req
}

func wantNoSubmission(t *testing.T, h *enginetest.Harness, id string) {
	t.Helper()
	if _, found, err := h.Store.FindSubmission(id); err != nil || found {
		t.Fatalf("refused submission %s recorded: %v, %v", id, found, err)
	}
}

func attachmentsOf(t *testing.T, h *enginetest.Harness, turnID string) []journal.Attachment {
	t.Helper()
	atts, err := h.Store.TurnAttachments(turnID)
	if err != nil {
		t.Fatal(err)
	}
	return atts
}

// An attachment to a participant outside the destinations is refused with
// route attachment before any file is opened: the path does not exist, and
// the egress refusal still wins over not_found.
func TestAttachmentUnauthorizedParticipantRefusedBeforeOpen(t *testing.T) {
	h := newAttachHarness(t, map[string][]fake.Step{"author": {enginetest.Reply("a")}})
	dir := t.TempDir()
	_, err := h.Engine.Submit(attachReq("s1", "review", dir, nil, "missing.txt"))
	wantEgress(t, err, "reviewer", config.EgressRouteAttachment)
	wantNoSubmission(t, h, "s1")
}

// A preset with web and web with file content off refuses an attachment
// though it has no read or write tool and the conversation is untainted.
func TestAttachmentWebWithFileOffRefusedWithoutFileTools(t *testing.T) {
	h := newAttachHarness(t, map[string][]fake.Step{"author": {enginetest.Reply("a")}})
	dir := workspaceWith(t, map[string]string{"data.txt": "hello"})
	_, err := h.Engine.Submit(attachReq("s1", "noweb", dir, nil, "data.txt"))
	wantEgress(t, err, "author", config.EgressRouteAttachment)
	wantNoSubmission(t, h, "s1")
}

// An allowed attachment is recorded, taints the turn, and a later submit to
// the conversation meets the history rules for destinations and web, whole
// or trimmed.
func TestAttachmentTaintsConversationForLaterHistoryRules(t *testing.T) {
	h := newAttachHarness(t, map[string][]fake.Step{
		"author":   {enginetest.Reply("a1"), enginetest.Reply("a2")},
		"reviewer": {approveStep()},
	})
	dir := workspaceWith(t, map[string]string{"data.txt": "attached text"})
	first := submitSolo(t, h, attachReq("s1", "solo", dir, nil, "data.txt"))
	if !tainted(t, h, first.TurnID) {
		t.Fatal("a turn admitted with an attachment is not file-tainted")
	}
	if atts := attachmentsOf(t, h, first.TurnID); len(atts) != 1 || atts[0].Path != "data.txt" || string(atts[0].Content) != "attached text" {
		t.Fatalf("attachment records = %+v", atts)
	}
	conv := first.ConversationID
	later := submitSolo(t, h, presetIn("s2", "solo", &conv))
	if !tainted(t, h, later.TurnID) {
		t.Fatal("a later turn of the conversation is not tainted")
	}

	_, err := h.Engine.Submit(presetIn("s3", "review", &conv))
	wantEgress(t, err, "reviewer", config.EgressRouteHistory)
	trim := presetIn("s4", "review", &conv)
	trim.ContextFromTurn = &later.TurnID
	_, err = h.Engine.Submit(trim)
	wantEgress(t, err, "reviewer", config.EgressRouteHistory)
	_, err = h.Engine.Submit(presetIn("s5", "noweb", &conv))
	wantEgress(t, err, "author", config.EgressRouteHistory)
	if n := turnCount(t, h, conv); n != 2 {
		t.Fatalf("turns = %d after refusals, want 2", n)
	}
}

// A submit to a tainted conversation that the policy refuses opens no file,
// though the attachment would be refused on reading.
func TestAttachmentPolicyRefusalInTaintedConversationOpensNoFile(t *testing.T) {
	h := newAttachHarness(t, map[string][]fake.Step{
		"author": {enginetest.Reply("a1")},
	})
	conv, _ := func() (string, string) {
		r := submitSolo(t, h, attachReq("s1", "solo", workspaceWith(t, map[string]string{"d.txt": "x"}), nil, "d.txt"))
		return r.ConversationID, r.TurnID
	}()
	_, err := h.Engine.Submit(attachReq("s2", "review", "", &conv, "missing.txt"))
	wantEgress(t, err, "reviewer", config.EgressRouteAttachment)
	_, err = h.Engine.Submit(presetIn("s3", "review", &conv))
	wantEgress(t, err, "reviewer", config.EgressRouteHistory)
}

// A submit refused while reading adds no taint: the conversation stays clean
// and a tool-free preset with a participant outside the destinations is still
// accepted into it. The protected pattern of the roster applies to the read.
func TestAttachmentFailedSubmitAddsNoTaint(t *testing.T) {
	h := newAttachHarness(t, map[string][]fake.Step{
		"author":   {enginetest.Reply("a1"), enginetest.Reply("a2")},
		"reviewer": {approveStep()},
	})
	dir := workspaceWith(t, map[string]string{".env": "SECRET=1", "ok.txt": "fine"})
	clean := submitSolo(t, h, attachReq("s1", "solo", dir, nil))
	if tainted(t, h, clean.TurnID) {
		t.Fatal("a turn with no attachment and no tools is tainted")
	}
	conv := clean.ConversationID

	_, err := h.Engine.Submit(attachReq("s2", "solo", dir, &conv, "ok.txt", ".env"))
	we := wantRefusal(t, err, wire.ErrorCodeAttachment)
	if we.Path == nil || *we.Path != ".env" || we.Reason == nil || *we.Reason != "protected" {
		t.Fatalf("refusal = %+v, want protected .env", we)
	}
	wantNoSubmission(t, h, "s2")
	if tainted(t, h, clean.TurnID) {
		t.Fatal("a refused submit tainted the conversation")
	}

	later, err := h.Engine.Submit(presetIn("s3", "review", &conv))
	if err != nil {
		t.Fatalf("tool-free review preset after a failed attachment submit: %v", err)
	}
	wantEnd(t, h, later.TurnID, engine.StateApproved, "all_approved")
	if tainted(t, h, later.TurnID) {
		t.Fatal("the later turn is tainted though the attachment submit failed")
	}
}

// A conversation with no workspace has nothing to attach from.
func TestAttachmentWithoutWorkspaceNotFound(t *testing.T) {
	h := newAttachHarness(t, map[string][]fake.Step{"author": {enginetest.Reply("a")}})
	_, err := h.Engine.Submit(attachReq("s1", "solo", "", nil, "data.txt"))
	we := wantRefusal(t, err, wire.ErrorCodeAttachment)
	if we.Reason == nil || *we.Reason != "not_found" {
		t.Fatalf("refusal = %+v, want not_found", we)
	}
}

// The attachment's bytes are canary material from the commit: the first web
// call of the turn carrying sixteen of them is refused by the canary, before
// any referee question, lookup, connection or credential read.
func TestAttachmentSeedsCanaryBeforeFirstWebCall(t *testing.T) {
	dir := workspaceWith(t, map[string]string{"notes.txt": longContent})
	sh := newSearchHarness(t, searchRoster(searchTable), map[string][]fake.Step{
		"author": {
			toolStepWith(searchCall("c1", "very long file line")),
			enginetest.Reply("final answer"),
		},
	})
	req := toolReq("s1", "webonly", dir)
	req.Attachments = []string{"notes.txt"}
	reply, err := sh.Engine.Submit(req)
	if err != nil {
		t.Fatal(err)
	}
	wantEnd(t, sh.Harness, reply.TurnID, engine.StateSoloComplete, "solo_ok")
	calls := toolCalls(t, sh.Harness, reply.TurnID)
	wantCalls(t, calls, [2]string{journal.ToolCallError, tools.ErrCodeRefused})
	text, err := sh.Store.Content(calls[0].Result.ResultSHA256)
	if err != nil || !strings.Contains(string(text), tools.RuleCanary) {
		t.Fatalf("refusal text %q err %v, want the canary rule named", text, err)
	}
	sh.wantNothingSent(t, reply.TurnID)
}
