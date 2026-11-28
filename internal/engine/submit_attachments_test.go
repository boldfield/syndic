package engine_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/boldfield/syndic/internal/engine"
	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/transport/fake"
	"github.com/boldfield/syndic/internal/wire"
)

func TestAttachmentProtectedPathEnforcement(t *testing.T) {
	rosterTOML := enginetest.RosterTOML + `
[tools]
protected_paths = [".env*", "secret/"]
`
	h := enginetest.New(t, rosterTOML, map[string][]fake.Step{
		"author": {enginetest.Reply("answer one")},
	})

	// Create workspace with protected files.
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, ".env"), []byte("SECRET"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "data.txt"), []byte("public"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(workspace, "secret"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "secret", "key"), []byte("KEY"), 0o644); err != nil {
		t.Fatal(err)
	}

	preset := "solo"
	req := wire.SubmitRequest{
		SubmissionID: "s1",
		Input:        "question",
		Preset:       &preset,
		Workspace:    workspace,
		Attachments:  []string{".env"},
	}
	_, err := h.Engine.Submit(req)
	we := wantRefusal(t, err, wire.ErrorCodeAttachment)
	if we.Path == nil || *we.Path != ".env" || we.Reason == nil || *we.Reason != "protected" {
		t.Fatalf("protected .env: got %+v", we)
	}

	req.SubmissionID = "s2"
	req.Attachments = []string{"secret/key"}
	_, err = h.Engine.Submit(req)
	we = wantRefusal(t, err, wire.ErrorCodeAttachment)
	if we.Path == nil || *we.Path != "secret/key" || we.Reason == nil || *we.Reason != "protected" {
		t.Fatalf("protected secret/key: got %+v", we)
	}

	// Public file should succeed.
	req.SubmissionID = "s3"
	req.Attachments = []string{"data.txt"}
	reply, err := h.Engine.Submit(req)
	if err != nil {
		t.Fatalf("public file: %v", err)
	}
	wantEnd(t, h, reply.TurnID, engine.StateSoloComplete, "solo_ok")
}

func TestAttachmentUnauthorizedParticipantRefusal(t *testing.T) {
	rosterTOML := createUnauthorizedRoster()
	h := enginetest.New(t, rosterTOML, map[string][]fake.Step{
		"author":                {enginetest.Reply("answer one")},
		"reviewer-unauthorized": {approveStep()},
	})

	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "data.txt"), []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}

	preset := "review"
	req := wire.SubmitRequest{
		SubmissionID: "s1",
		Input:        "question",
		Preset:       &preset,
		Workspace:    workspace,
		Attachments:  []string{"data.txt"},
	}
	_, err := h.Engine.Submit(req)
	we := wantRefusal(t, err, wire.ErrorCodeEgress)
	if we.Route == nil || *we.Route != "attachment" {
		t.Fatalf("want egress/route attachment, got %+v", we)
	}
}

func TestAttachmentWebOnlyRefusal(t *testing.T) {
	rosterTOML := createWebOnlyRoster()
	h := enginetest.New(t, rosterTOML, map[string][]fake.Step{
		"author": {enginetest.Reply("answer one")},
	})

	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "data.txt"), []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}

	preset := "webreview"
	req := wire.SubmitRequest{
		SubmissionID: "s1",
		Input:        "question",
		Preset:       &preset,
		Workspace:    workspace,
		Attachments:  []string{"data.txt"},
	}
	_, err := h.Engine.Submit(req)
	we := wantRefusal(t, err, wire.ErrorCodeEgress)
	if we.Route == nil || *we.Route != "attachment" {
		t.Fatalf("want egress/route attachment, got %+v", we)
	}
}

func TestAttachmentNoTaintAfterFailedSubmit(t *testing.T) {
	rosterTOML := enginetest.RosterTOML + `
[tools]
protected_paths = [".env*"]
`
	h := enginetest.New(t, rosterTOML, map[string][]fake.Step{
		"author": {enginetest.Reply("answer one"), enginetest.Reply("answer two")},
	})

	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "data.txt"), []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, ".env"), []byte("SECRET"), 0o644); err != nil {
		t.Fatal(err)
	}

	preset := "solo"
	// First, try to submit with a protected file (should fail).
	req := wire.SubmitRequest{
		SubmissionID: "s1",
		Input:        "first question",
		Preset:       &preset,
		Workspace:    workspace,
		Attachments:  []string{".env"},
	}
	_, err := h.Engine.Submit(req)
	we := wantRefusal(t, err, wire.ErrorCodeAttachment)
	if we.Reason == nil || *we.Reason != "protected" {
		t.Fatalf("protected file should refuse: %+v", we)
	}

	// Now submit without attachments to create first turn.
	req.SubmissionID = "s2"
	req.Attachments = nil
	reply, err := h.Engine.Submit(req)
	if err != nil {
		t.Fatalf("second submit: %v", err)
	}
	wantEnd(t, h, reply.TurnID, engine.StateSoloComplete, "solo_ok")
	conv := reply.ConversationID

	// Now submit another turn in the same conversation - should not be tainted by the failed attachment submit.
	// This verifies that the failed submit did not add taint to the conversation.
	req.SubmissionID = "s3"
	req.Input = "third question"
	req.ConversationID = &conv
	reply2, err := h.Engine.Submit(req)
	if err != nil {
		t.Fatalf("third submit in same conversation: %v", err)
	}
	wantEnd(t, h, reply2.TurnID, engine.StateSoloComplete, "solo_ok")

	// A refused submit should create no turn, so only two turns should exist.
	turns, err := h.Store.GetConversationTurns(conv)
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 2 {
		t.Fatalf("turns = %d, expected 2 (failed submit creates no turn)", len(turns))
	}
}

func TestAttachmentExtendsTaint(t *testing.T) {
	rosterTOML := createTaintTestRoster()
	h := enginetest.New(t, rosterTOML, map[string][]fake.Step{
		"author":   {enginetest.Reply("answer one"), enginetest.Reply("answer two")},
		"reviewer": {approveStep()},
	})

	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "data.txt"), []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}

	preset := "solo"
	// First turn with an attachment (tool-free preset).
	req := wire.SubmitRequest{
		SubmissionID: "s1",
		Input:        "first question",
		Preset:       &preset,
		Workspace:    workspace,
		Attachments:  []string{"data.txt"},
	}
	reply1, err := h.Engine.Submit(req)
	if err != nil {
		t.Fatalf("first submit with attachment: %v", err)
	}
	wantEnd(t, h, reply1.TurnID, engine.StateSoloComplete, "solo_ok")
	conv := reply1.ConversationID

	// Second turn without attachment in same conversation (taint should be set).
	req.SubmissionID = "s2"
	req.Input = "second question"
	req.ConversationID = &conv
	req.Attachments = nil
	reply2, err := h.Engine.Submit(req)
	if err != nil {
		t.Fatalf("second submit: %v", err)
	}
	wantEnd(t, h, reply2.TurnID, engine.StateSoloComplete, "solo_ok")

	// Now try a review turn with a reviewer on an unauthorized endpoint.
	// This should be refused with egress/route history because the conversation is tainted.
	presetReview := "review-unauthorized"
	req.SubmissionID = "s3"
	req.Input = "third question"
	req.Preset = &presetReview
	_, err = h.Engine.Submit(req)
	wantEgressRefusal(t, err, "reviewer-unauthorized", "history")

	// Try the same but with context trimmed to after the first turn (which has the attachment).
	// The history route should still refuse due to transitive taint.
	trimReq := wire.SubmitRequest{
		SubmissionID:    "s4",
		Input:           "fourth question",
		Preset:          &presetReview,
		ConversationID:  &conv,
		ContextFromTurn: &reply1.TurnID,
	}
	_, err = h.Engine.Submit(trimReq)
	wantEgressRefusal(t, err, "reviewer-unauthorized", "history")

	// Verify only 2 turns were created (s3 and s4 were refused).
	turns, err := h.Store.GetConversationTurns(conv)
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 2 {
		t.Fatalf("turns = %d, expected 2 (unauthorized submissions refused)", len(turns))
	}
}

func createUnauthorizedRoster() string {
	r := enginetest.RosterTOML
	// Update policy to allow remote
	r = replace(r, "local_only = true", "local_only = false")
	// Add unauthorized endpoint before policy section
	r = replace(r, "[policy]", `[[endpoints]]
name = "unauthorized"
base_url = "http://192.168.1.100:11434/v1"
declared_location = "lan"
declared_execution = "remote"
declared_billing = "none"
network_dependency = "none"
dialect = "chat_completions_v1"
models = ["llama3"]
tokens_per_byte_max = 1.0

[[participants]]
id = "reviewer-unauthorized"
model = "llama3"
route = "api"
endpoint = "unauthorized"
eligible_roles = ["reviewer"]
pool = "default"
accept_unknown_truncation = true

[policy]`)
	// Update the main review preset to use the unauthorized reviewer
	r = replace(r, "required_reviewers = [\"reviewer\"]", `required_reviewers = ["reviewer-unauthorized"]`)
	return r
}

func createWebOnlyRoster() string {
	r := enginetest.RosterTOML
	// Add referee model to the endpoint for web support
	r = replace(r, "models = [\"llama2\", \"llama3\"]\ntoken", `models = ["llama2", "llama3"]
referee_model = "llama3"
token`)
	// Add webreview preset
	r = replace(r, "[presets.solo]", `[presets.webreview]
mode = "review"
author = "author"
required_reviewers = ["reviewer"]
reviewer_tools = ["web"]
allow_web_with_read = false

[presets.solo]`)
	return r
}

func replace(s, old, new string) string {
	return strings.Replace(s, old, new, 1)
}

func createTaintTestRoster() string {
	r := enginetest.RosterTOML
	// Update policy to allow remote
	r = replace(r, "local_only = true", "local_only = false")
	// Add unauthorized endpoint before policy section
	r = replace(r, "[policy]", `[[endpoints]]
name = "unauthorized"
base_url = "http://192.168.1.100:11434/v1"
declared_location = "lan"
declared_execution = "remote"
declared_billing = "none"
network_dependency = "none"
dialect = "chat_completions_v1"
models = ["llama3"]
tokens_per_byte_max = 1.0

[[participants]]
id = "reviewer-unauthorized"
model = "llama3"
route = "api"
endpoint = "unauthorized"
eligible_roles = ["reviewer"]
pool = "default"
accept_unknown_truncation = true

[presets.review-unauthorized]
mode = "review"
author = "author"
required_reviewers = ["reviewer-unauthorized"]

[policy]`)
	return r
}

func wantEgressRefusal(t *testing.T, err error, participant, route string) {
	t.Helper()
	we := wantRefusal(t, err, wire.ErrorCodeEgress)
	if we.Participant == nil || *we.Participant != participant || we.Route == nil || *we.Route != route {
		t.Fatalf("egress refusal = %+v, want participant %q route %q", we, participant, route)
	}
}

func TestAttachmentHashBackwardCompatibility(t *testing.T) {
	rosterTOML := enginetest.RosterTOML
	h := enginetest.New(t, rosterTOML, map[string][]fake.Step{
		"author": {enginetest.Reply("answer one"), enginetest.Reply("answer two")},
	})

	preset := "solo"
	// Submit without attachments.
	req1 := wire.SubmitRequest{
		SubmissionID: "s1",
		Input:        "question one",
		Preset:       &preset,
	}
	reply1, err := h.Engine.Submit(req1)
	if err != nil {
		t.Fatalf("first submit: %v", err)
	}
	wantEnd(t, h, reply1.TurnID, engine.StateSoloComplete, "solo_ok")

	// Replay with identical fields - should get the same turn.
	again1, err := h.Engine.Submit(req1)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if again1.TurnID != reply1.TurnID {
		t.Fatalf("replay gave different turn: %q vs %q", again1.TurnID, reply1.TurnID)
	}

	// Now submit the same request but with empty attachments list.
	// Due to omitempty, this should hash the same and replay.
	req1.Attachments = []string{}
	again2, err := h.Engine.Submit(req1)
	if err != nil {
		t.Fatalf("empty attachments replay: %v", err)
	}
	if again2.TurnID != reply1.TurnID {
		t.Fatalf("empty attachments should replay the same: %q vs %q", again2.TurnID, reply1.TurnID)
	}

	// Verify the hash matches the pre-upgrade hex value.
	wantHash := "78f4796ca721163b01fe3e0e5dfe60f4f527958fdd54c3f24099135fdcf8c0df"
	var row *journal.TurnRow
	if err := h.Store.Tx(func(tx *journal.Tx) error {
		var err error
		row, _, err = tx.FindSubmission("s1")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if row == nil {
		t.Fatal("submission not found")
	}
	if row.SubmissionHash != wantHash {
		t.Fatalf("hash = %q, want %q", row.SubmissionHash, wantHash)
	}
}
