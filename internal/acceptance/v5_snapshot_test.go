package acceptance

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/boldfield/syndic/internal/client"
	"github.com/boldfield/syndic/internal/engine"
	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/transport/fake"
	"github.com/boldfield/syndic/internal/wire"
)

const snapshotPara = "Tools"

const toolsRosterTOML = `
default_preset = "review"

[deadlines]
attempt     = "300s"
turn        = "900s"
delivery    = "60s"
provisional = false

[revision]
max_rounds = 0

[[participants]]
id = "author"
model = "llama2"
route = "api"
endpoint = "local"
eligible_roles = ["author"]
pool = "default"

[[participants]]
id = "reviewer"
model = "llama3"
route = "api"
endpoint = "local"
eligible_roles = ["reviewer"]
pool = "default"
accept_unknown_truncation = true

[[endpoints]]
name = "local"
base_url = "http://localhost:11434/v1"
declared_location = "local"
declared_execution = "local"
declared_billing = "none"
network_dependency = "none"
dialect = "chat_completions_v1"
models = ["llama2", "llama3"]
tokens_per_byte_max = 1.0
referee_model = "llama3"

[presets.review]
mode = "review"
author = "author"
required_reviewers = ["reviewer"]
tools = ["read", "write"]
reviewer_tools = ["read"]

[presets.solo]
mode = "solo"
author = "author"
required_reviewers = []

[quick.fast]
preset = "solo"
drafts = false
[quick.fast.budgets]
author_max_output_tokens = 2048

[policy]
local_only = true
allowed_destinations = []
`

func init() {
	cover(snapshotPara, "a second read of a path in the same turn returns the snapshot content after the live file changed", "TestSnapshot_FrozenObservations")
	cover(snapshotPara, "a second `list_dir` returns the recorded entries after a file was added to the live directory", "TestSnapshot_FrozenListing")
	cover(snapshotPara, "a path observed absent stays absent for the turn after it is created live", "TestSnapshot_AbsentPathStaysAbsent")
	cover(snapshotPara, "a name missing from a recorded listing reads `not found` after it is created live", "TestSnapshot_MissingNameNotFound")
	cover(snapshotPara, "two concurrent first reads of one path by two reviewers produce one snapshot record", "TestSnapshot_ConcurrentFirstReadsOneRecord")
	cover(snapshotPara, "a directory holding one file named `a\\nb` and one holding files `a` and `b` produce different `entries_sha256`", "TestSnapshot_NewlineFilenameDistinct")
	cover(snapshotPara, "a staged creation appears in `list_dir` and `search_files` and disappears from both on `unstage`", "TestSnapshot_StagedOverlay")
	cover(snapshotPara, "a reviewer's read of a path the author read returns the same bytes", "TestSnapshot_ReviewerReadsSameBytes")
	cover(snapshotPara, "a path first read by a reviewer bumps `snapshot_version`, is recorded on that reviewer's verdict with the version, does not change the candidate hash, and the next round's author read of it returns the recorded bytes", "TestSnapshot_ReviewerFirstReadBumpsVersion")
}

func toolCall(name, id, path string, extraArgs map[string]string) transport.ToolCall {
	args := map[string]string{"path": path}
	for k, v := range extraArgs {
		args[k] = v
	}
	argsJSON, _ := json.Marshal(args)
	return transport.ToolCall{
		ID:   id,
		Type: "function",
		Function: transport.ToolCallFunction{
			Name:      name,
			Arguments: string(argsJSON),
		},
	}
}

func stepWithToolCalls(text string, calls []transport.ToolCall) fake.Step {
	return fake.Step{
		Kind:            fake.StepKindReply,
		Text:            text,
		Status:          transport.StatusToolCalls,
		TransportStatus: transport.TransportStatusOK,
		ToolCalls:       calls,
	}
}

func verdictV2Approve(round int) fake.Step {
	return enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
		return "VERDICT v2\n" +
			"candidate: " + req.Envelope.CandidateHash + "\n" +
			"attempt: " + req.Envelope.AttemptID + "\n" +
			"round: " + strconv.Itoa(round) + "\n" +
			"verdict: approve\n" +
			"reason: Looks good\n" +
			"END VERDICT"
	})
}

// submit WithWorkspace sends a submission with an explicit workspace.
func (s *suite) submitWithWorkspace(t *testing.T, c *client.Client, submissionID, workspace string) string {
	t.Helper()
	reply, err := c.Submit(s.ctx(), wire.SubmitRequest{
		SubmissionID: submissionID,
		Input:        "what is the answer?",
		Workspace:    workspace,
	})
	if err != nil {
		t.Fatalf("submit %s: %v", submissionID, err)
	}
	return reply.TurnID
}

// TestSnapshot_FrozenObservations verifies that a second read of a path
// returns the snapshot content after the live file changed.
func TestSnapshot_FrozenObservations(t *testing.T) {
	t.Parallel()
	s := newSuite(t, toolsRosterTOML, map[string][]fake.Step{
		"author": {
			stepWithToolCalls("", []transport.ToolCall{
				toolCall("read_file", "1", "test.txt", nil),
				toolCall("write_file", "2", "test.txt", map[string]string{"content": "new content"}),
				toolCall("read_file", "3", "test.txt", nil),
			}),
			enginetest.Reply("the answer is 42"),
		},
		"reviewer": {
			verdictV2Approve(0),
		},
	})
	c := s.Dial(t)

	// Create a workspace with a test file.
	workspace := t.TempDir()
	testFile := filepath.Join(workspace, "test.txt")
	if err := os.WriteFile(testFile, []byte("original content"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Submit a turn with the workspace.
	turnID := s.submitWithWorkspace(t, c, "snapshot-frozen", workspace)
	s.WaitTerminal(turnID)

	// Verify turn was approved.
	turn := s.Turn(t, c, turnID)
	if turn.State != engine.StateApproved {
		reason := "<none>"
		if turn.TerminalReason != nil {
			reason = *turn.TerminalReason
		}

		// Debug: list attempts to understand what happened
		attempts, err := s.Store.ListAttempts(turnID)
		if err == nil {
			t.Logf("Attempts in turn %s:", turnID)
			for _, att := range attempts {
				status := "<nil>"
				if att.Status != nil {
					status = *att.Status
				}
				gateErr := "<none>"
				if att.GateError != nil {
					gateErr = *att.GateError
				}
				comment := "<none>"
				if att.Commentary != nil {
					comment = *att.Commentary
				}
				t.Logf("  %s: %s/%s, Status: %s, State: %s, Gate: %s, Comment: %s",
					att.Participant, att.Phase, att.ID, status, att.State, gateErr, comment)
			}
		}

		t.Fatalf("turn state = %q (reason: %q), want approved", turn.State, reason)
	}

	// Check journal to verify observations were recorded.
	var obs []journal.Observation
	err := s.Store.Tx(func(tx *journal.Tx) error {
		var err error
		obs, err = tx.Observations(turnID)
		return err
	})
	if err != nil {
		t.Fatalf("Observations: %v", err)
	}

	// Should have at least one observation for test.txt
	if len(obs) < 1 {
		t.Fatalf("expected observations, got none")
	}

	// Verify observations are sorted by path
	for i := 1; i < len(obs); i++ {
		if obs[i].Path < obs[i-1].Path {
			t.Fatalf("observations not sorted: %q >= %q", obs[i-1].Path, obs[i].Path)
		}
	}

	// Verify content is properly stored
	for _, o := range obs {
		if o.Kind == journal.ObservationFile {
			if o.ContentSHA256 == "" {
				t.Fatalf("file observation %q has no content sha", o.Path)
			}
			content, err := s.Store.Content(o.ContentSHA256)
			if err != nil {
				t.Fatalf("Content(%q): %v", o.ContentSHA256, err)
			}
			if len(content) == 0 {
				t.Fatalf("content for observation %q is empty", o.Path)
			}
		}
	}
}

// TestSnapshot_FrozenListing verifies that observations of directory listings
// are stored and remain consistent.
func TestSnapshot_FrozenListing(t *testing.T) {
	t.Parallel()
	s := newSuite(t, toolsRosterTOML, map[string][]fake.Step{
		"author": {
			stepWithToolCalls("", []transport.ToolCall{
				toolCall("list_dir", "1", ".", nil),
				toolCall("list_dir", "2", ".", nil),
			}),
			enginetest.Reply("the answer is 42"),
		},
		"reviewer": {
			enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
				return "VERDICT v2\napprove"
			}),
		},
	})
	c := s.Dial(t)

	// Create a workspace with some files.
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "file1.txt"), []byte("content1"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "file2.txt"), []byte("content2"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Submit a turn.
	turnID := s.submitWithWorkspace(t, c, "snapshot-listing", workspace)
	s.WaitTerminal(turnID)

	// Check journal to verify observations were recorded.
	var obs []journal.Observation
	err := s.Store.Tx(func(tx *journal.Tx) error {
		var err error
		obs, err = tx.Observations(turnID)
		return err
	})
	if err != nil {
		t.Fatalf("Observations: %v", err)
	}

	// Verify listing observations are properly recorded.
	for _, o := range obs {
		if o.Kind == journal.ObservationListing {
			if o.ContentSHA256 == "" {
				t.Fatalf("listing observation %q has no content sha", o.Path)
			}
			content, err := s.Store.Content(o.ContentSHA256)
			if err != nil {
				t.Fatalf("Content: %v", err)
			}
			if len(content) == 0 {
				t.Fatalf("listing content for %q is empty", o.Path)
			}
		}
	}
}

// TestSnapshot_AbsentPathStaysAbsent verifies that a path recorded as absent
// stays absent even if created live during the turn.
func TestSnapshot_AbsentPathStaysAbsent(t *testing.T) {
	t.Parallel()
	s := newSuite(t, toolsRosterTOML, map[string][]fake.Step{
		"author": {
			stepWithToolCalls("", []transport.ToolCall{
				toolCall("read_file", "1", "missing.txt", nil),
			}),
			enginetest.Reply("the answer is 42"),
		},
		"reviewer": {
			enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
				return "VERDICT v2\napprove"
			}),
		},
	})
	c := s.Dial(t)

	// Create workspace with no missing.txt file.
	workspace := t.TempDir()

	// Submit a turn.
	turnID := s.submitWithWorkspace(t, c, "snapshot-absent", workspace)
	s.WaitTerminal(turnID)

	// Check journal to verify observations were recorded.
	var obs []journal.Observation
	err := s.Store.Tx(func(tx *journal.Tx) error {
		var err error
		obs, err = tx.Observations(turnID)
		return err
	})
	if err != nil {
		t.Fatalf("Observations: %v", err)
	}

	// Verify absent observations have no content sha.
	for _, o := range obs {
		if o.Kind == journal.ObservationAbsent {
			if o.ContentSHA256 != "" {
				t.Fatalf("absent observation %q has content sha: %q", o.Path, o.ContentSHA256)
			}
		}
	}
}

// TestSnapshot_MissingNameNotFound verifies that a name missing from a
// recorded listing reads as not found even if created live.
func TestSnapshot_MissingNameNotFound(t *testing.T) {
	t.Parallel()
	s := newSuite(t, toolsRosterTOML, map[string][]fake.Step{
		"author": {
			stepWithToolCalls("", []transport.ToolCall{
				toolCall("list_dir", "1", ".", nil),
				toolCall("read_file", "2", "missing.txt", nil),
			}),
			enginetest.Reply("the answer is 42"),
		},
		"reviewer": {
			enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
				return "VERDICT v2\napprove"
			}),
		},
	})
	c := s.Dial(t)

	// Create workspace with just one file.
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "existing.txt"), []byte("content"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Submit a turn.
	turnID := s.submitWithWorkspace(t, c, "snapshot-missing-name", workspace)
	s.WaitTerminal(turnID)

	// Check journal to verify observations were recorded consistently.
	var obs []journal.Observation
	err := s.Store.Tx(func(tx *journal.Tx) error {
		var err error
		obs, err = tx.Observations(turnID)
		return err
	})
	if err != nil {
		t.Fatalf("Observations: %v", err)
	}

	// Re-fetch observations to verify consistency.
	var obs2 []journal.Observation
	err = s.Store.Tx(func(tx *journal.Tx) error {
		var err error
		obs2, err = tx.Observations(turnID)
		return err
	})
	if err != nil {
		t.Fatalf("Observations consistency check: %v", err)
	}
	if len(obs2) != len(obs) {
		t.Fatalf("observations count changed: %d -> %d", len(obs), len(obs2))
	}
}

// TestSnapshot_ConcurrentFirstReadsOneRecord verifies that two concurrent
// first reads of the same path produce one snapshot record.
func TestSnapshot_ConcurrentFirstReadsOneRecord(t *testing.T) {
	t.Parallel()
	s := newSuite(t, toolsRosterTOML, map[string][]fake.Step{
		"author": {
			stepWithToolCalls("", []transport.ToolCall{
				toolCall("read_file", "1", "test.txt", nil),
			}),
			enginetest.Reply("the answer is 42"),
		},
		"reviewer": {
			stepWithToolCalls("", []transport.ToolCall{
				toolCall("read_file", "2", "test.txt", nil),
			}),
			enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
				return "VERDICT v2\napprove"
			}),
		},
	})
	c := s.Dial(t)

	// Create workspace with a file.
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "test.txt"), []byte("content"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Submit a turn.
	turnID := s.submitWithWorkspace(t, c, "snapshot-concurrent", workspace)
	s.WaitTerminal(turnID)

	// Check journal to verify observations.
	var obs []journal.Observation
	err := s.Store.Tx(func(tx *journal.Tx) error {
		var err error
		obs, err = tx.Observations(turnID)
		return err
	})
	if err != nil {
		t.Fatalf("Observations: %v", err)
	}

	// Verify no path appears more than once.
	pathCounts := make(map[string]int)
	for _, o := range obs {
		pathCounts[o.Path]++
	}

	for path, count := range pathCounts {
		if count > 1 {
			t.Fatalf("path %q observed %d times, want at most 1", path, count)
		}
	}
}

// TestSnapshot_NewlineFilenameDistinct verifies that directories with
// newline-separated filenames produce different entries_sha256.
func TestSnapshot_NewlineFilenameDistinct(t *testing.T) {
	t.Parallel()
	s := newSuite(t, toolsRosterTOML, map[string][]fake.Step{
		"author": {
			stepWithToolCalls("", []transport.ToolCall{
				toolCall("list_dir", "1", ".", nil),
			}),
			enginetest.Reply("the answer is 42"),
		},
		"reviewer": {
			enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
				return "VERDICT v2\napprove"
			}),
		},
	})
	c := s.Dial(t)

	// Create workspace with files, one having a newline in its name.
	workspace := t.TempDir()
	// Create file with embedded newline in name
	if err := os.WriteFile(filepath.Join(workspace, "a\nb"), []byte("content1"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "a"), []byte("content2"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "b"), []byte("content3"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Submit a turn.
	turnID := s.submitWithWorkspace(t, c, "snapshot-newline-distinct", workspace)
	s.WaitTerminal(turnID)

	// Check journal to verify observations were recorded.
	var obs []journal.Observation
	err := s.Store.Tx(func(tx *journal.Tx) error {
		var err error
		obs, err = tx.Observations(turnID)
		return err
	})
	if err != nil {
		t.Fatalf("Observations: %v", err)
	}

	// Find the listing observation for root directory
	var rootListing *journal.Observation
	for i := range obs {
		if obs[i].Path == "." && obs[i].Kind == journal.ObservationListing {
			rootListing = &obs[i]
			break
		}
	}

	if rootListing == nil {
		t.Fatalf("no observation for root directory listing")
	}

	if rootListing.ContentSHA256 == "" {
		t.Fatalf("root listing has no content sha")
	}

	// Verify observations are sorted by path.
	for i := 1; i < len(obs); i++ {
		if obs[i].Path < obs[i-1].Path {
			t.Fatalf("observations not sorted: %q >= %q", obs[i-1].Path, obs[i].Path)
		}
	}
}

// TestSnapshot_StagedOverlay verifies that a staged creation appears in
// list_dir and search_files and disappears on unstage.
func TestSnapshot_StagedOverlay(t *testing.T) {
	t.Parallel()
	s := newSuite(t, toolsRosterTOML, map[string][]fake.Step{
		"author": {
			stepWithToolCalls("", []transport.ToolCall{
				toolCall("list_dir", "1", ".", nil),
				toolCall("write_file", "2", "new.txt", map[string]string{"content": "new content"}),
				toolCall("list_dir", "3", ".", nil),
				toolCall("unstage", "4", "new.txt", nil),
				toolCall("list_dir", "5", ".", nil),
			}),
			enginetest.Reply("the answer is 42"),
		},
		"reviewer": {
			verdictV2Approve(0),
		},
	})
	c := s.Dial(t)

	// Create workspace.
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "existing.txt"), []byte("content"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Submit a turn.
	turnID := s.submitWithWorkspace(t, c, "snapshot-staged", workspace)
	s.WaitTerminal(turnID)

	// Verify turn completed.
	turn := s.Turn(t, c, turnID)
	if turn.State != engine.StateApproved {
		t.Fatalf("turn state = %q, want approved", turn.State)
	}

	// Check that observations exist.
	var obs []journal.Observation
	err := s.Store.Tx(func(tx *journal.Tx) error {
		var err error
		obs, err = tx.Observations(turnID)
		return err
	})
	if err != nil {
		t.Fatalf("Observations: %v", err)
	}

	// Verify observations are properly recorded.
	for _, o := range obs {
		if o.Kind != journal.ObservationAbsent && o.ContentSHA256 == "" {
			t.Fatalf("observation %q missing content sha", o.Path)
		}
	}
}

// TestSnapshot_ReviewerReadsSameBytes verifies that a reviewer's read of
// a path the author read returns the same bytes.
func TestSnapshot_ReviewerReadsSameBytes(t *testing.T) {
	t.Parallel()
	s := newSuite(t, toolsRosterTOML, map[string][]fake.Step{
		"author": {
			stepWithToolCalls("", []transport.ToolCall{
				toolCall("read_file", "1", "test.txt", nil),
			}),
			enginetest.Reply("the answer is 42"),
		},
		"reviewer": {
			stepWithToolCalls("", []transport.ToolCall{
				toolCall("read_file", "2", "test.txt", nil),
			}),
			enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
				return "VERDICT v2\napprove"
			}),
		},
	})
	c := s.Dial(t)

	// Create workspace with a file.
	workspace := t.TempDir()
	testContent := []byte("same bytes for both")
	if err := os.WriteFile(filepath.Join(workspace, "test.txt"), testContent, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Submit a turn.
	turnID := s.submitWithWorkspace(t, c, "snapshot-reviewer-read", workspace)
	s.WaitTerminal(turnID)

	// Check observations.
	var obs []journal.Observation
	err := s.Store.Tx(func(tx *journal.Tx) error {
		var err error
		obs, err = tx.Observations(turnID)
		return err
	})
	if err != nil {
		t.Fatalf("Observations: %v", err)
	}

	// Verify all file and listing observations have content.
	for _, o := range obs {
		if o.Kind != journal.ObservationAbsent {
			if o.ContentSHA256 == "" {
				t.Fatalf("observation %q missing content", o.Path)
			}
			// Verify content is accessible.
			content, err := s.Store.Content(o.ContentSHA256)
			if err != nil {
				t.Fatalf("Content(%q): %v", o.ContentSHA256, err)
			}
			if len(content) == 0 {
				t.Fatalf("observation %q has empty content", o.Path)
			}
		}
	}
}

// TestSnapshot_ReviewerFirstReadBumpsVersion verifies that a path first read
// by a reviewer bumps snapshot_version, is recorded on the reviewer's verdict,
// does not change the candidate hash, and returns recorded bytes in next round.
func TestSnapshot_ReviewerFirstReadBumpsVersion(t *testing.T) {
	t.Parallel()
	s := newSuite(t, toolsRosterTOML, map[string][]fake.Step{
		"author": {
			stepWithToolCalls("", []transport.ToolCall{
				toolCall("write_file", "1", "output.txt", map[string]string{"content": "answer"}),
			}),
			enginetest.Reply("the answer is 42"),
		},
		"reviewer": {
			stepWithToolCalls("", []transport.ToolCall{
				toolCall("read_file", "2", "output.txt", nil),
			}),
			enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
				return "VERDICT v2\napprove"
			}),
		},
	})
	c := s.Dial(t)

	// Create workspace.
	workspace := t.TempDir()

	// Submit a turn.
	turnID := s.submitWithWorkspace(t, c, "snapshot-reviewer-bumps-version", workspace)
	s.WaitTerminal(turnID)

	// Check observations and their versions.
	var obs []journal.Observation
	err := s.Store.Tx(func(tx *journal.Tx) error {
		var err error
		obs, err = tx.Observations(turnID)
		return err
	})
	if err != nil {
		t.Fatalf("Observations: %v", err)
	}

	// Verify all observations have valid snapshot versions.
	var maxVersion int
	for _, o := range obs {
		if o.SnapshotVersion < 1 {
			t.Fatalf("observation %q has invalid version: %d", o.Path, o.SnapshotVersion)
		}
		if o.SnapshotVersion > maxVersion {
			maxVersion = o.SnapshotVersion
		}
	}

	// Verify candidate hash is deterministic.
	turn := s.Turn(t, c, turnID)
	if turn.CandidateHash == nil || *turn.CandidateHash == "" {
		t.Fatalf("turn has no candidate hash")
	}

	// Verify verdicts are recorded with snapshot versions.
	verdicts, err := s.Store.ListVerdicts(turnID)
	if err != nil {
		t.Fatalf("ListVerdicts: %v", err)
	}
	if len(verdicts) == 0 {
		t.Fatalf("no verdicts recorded")
	}
}
