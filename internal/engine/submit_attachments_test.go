package engine_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/boldfield/syndic/internal/engine"
	"github.com/boldfield/syndic/internal/engine/enginetest"
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
	rosterTOML := enginetest.RosterTOML
	// Replace the file_egress_destinations with an empty list to disallow metered endpoints.
	rosterTOML = strings.Replace(rosterTOML, "[tools]", "[tools]\nfile_egress_destinations = []", 1)

	h := enginetest.New(t, rosterTOML, map[string][]fake.Step{
		"author":   {enginetest.Reply("answer")},
		"reviewer": {approveStep()},
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
	if we.Route == nil || *we.Route != "attachment" || we.Participant == nil || *we.Participant != "reviewer" {
		t.Fatalf("egress refusal: got %+v", we)
	}
}

func TestAttachmentWebOnlyRefusal(t *testing.T) {
	t.Skip("TODO: Requires custom preset with web-only participant and allow_web_with_read=false")
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

	// Now submit without attachments - should not be tainted by the failed submit.
	req.SubmissionID = "s2"
	req.Attachments = nil
	reply, err := h.Engine.Submit(req)
	if err != nil {
		t.Fatalf("second submit: %v", err)
	}
	wantEnd(t, h, reply.TurnID, engine.StateSoloComplete, "solo_ok")

	// A refused submit should create no turn, so only one turn should exist.
	turns, err := h.Store.GetConversationTurns(reply.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 1 {
		t.Fatalf("turns = %d, expected 1 (failed submit creates no turn)", len(turns))
	}
}

func TestAttachmentExtendsTaint(t *testing.T) {
	rosterTOML := enginetest.RosterTOML
	h := enginetest.New(t, rosterTOML, map[string][]fake.Step{
		"author": {enginetest.Reply("answer one"), enginetest.Reply("answer two")},
	})

	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "data.txt"), []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}

	preset := "solo"
	// First turn with an attachment.
	req := wire.SubmitRequest{
		SubmissionID: "s1",
		Input:        "first question",
		Preset:       &preset,
		Workspace:    workspace,
		Attachments:  []string{"data.txt"},
	}
	reply1, err := h.Engine.Submit(req)
	if err != nil {
		t.Fatalf("first submit: %v", err)
	}
	wantEnd(t, h, reply1.TurnID, engine.StateSoloComplete, "solo_ok")

	// Second turn without attachment should succeed (taint doesn't prevent solo submissions).
	req.SubmissionID = "s2"
	req.Input = "second question"
	req.ConversationID = &reply1.ConversationID
	req.Attachments = nil
	reply2, err := h.Engine.Submit(req)
	if err != nil {
		t.Fatalf("second submit: %v", err)
	}
	wantEnd(t, h, reply2.TurnID, engine.StateSoloComplete, "solo_ok")

	// Verify both turns were created.
	turns, err := h.Store.GetConversationTurns(reply2.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 2 {
		t.Fatalf("turns = %d, expected 2", len(turns))
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
}
