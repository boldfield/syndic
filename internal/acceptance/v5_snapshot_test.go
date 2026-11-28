package acceptance

import (
	"bytes"
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
max_rounds = 1

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

[[participants]]
id = "reviewer2"
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

[presets.review_concurrent]
mode = "review"
author = "author"
required_reviewers = ["reviewer", "reviewer2"]
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
	testFile := "test.txt"
	const originalContent = "original content"
	const mutatedContent = "mutated live content"

	s := newSuite(t, toolsRosterTOML, map[string][]fake.Step{
		"author": {
			stepWithToolCalls("", []transport.ToolCall{
				toolCall("read_file", "1", testFile, nil),
				toolCall("write_file", "2", testFile, map[string]string{"content": mutatedContent}),
				toolCall("read_file", "3", testFile, nil),
			}),
			enginetest.Reply("the answer is 42"),
		},
		"reviewer": {
			verdictV2Approve(0),
		},
	})
	c := s.Dial(t)

	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, testFile), []byte(originalContent), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	turnID := s.submitWithWorkspace(t, c, "snapshot-frozen", workspace)
	s.WaitTerminal(turnID)

	turn := s.Turn(t, c, turnID)
	if turn.State != engine.StateApproved {
		t.Fatalf("turn state = %q, want approved", turn.State)
	}

	var obs []journal.Observation
	err := s.Store.Tx(func(tx *journal.Tx) error {
		var err error
		obs, err = tx.Observations(turnID)
		return err
	})
	if err != nil {
		t.Fatalf("Observations: %v", err)
	}

	if len(obs) < 1 {
		t.Fatalf("expected at least 1 observation, got %d", len(obs))
	}

	fileObs := make(map[string]journal.Observation)
	for _, o := range obs {
		if o.Kind == journal.ObservationFile {
			if _, exists := fileObs[o.Path]; exists {
				t.Fatalf("path %s observed more than once", o.Path)
			}
			fileObs[o.Path] = o
		}
	}

	testOb, found := fileObs[testFile]
	if !found {
		t.Fatalf("no file observation for %s", testFile)
	}

	if testOb.ContentSHA256 == "" {
		t.Fatalf("observation for %s missing content sha", testFile)
	}

	content, err := s.Store.Content(testOb.ContentSHA256)
	if err != nil {
		t.Fatalf("Content: %v", err)
	}
	if !bytes.Equal(content, []byte(originalContent)) {
		t.Fatalf("second read returned content %q, want snapshot %q", string(content), originalContent)
	}
}

// TestSnapshot_FrozenListing verifies that a second list_dir returns
// the recorded entries even after a file is added live.
func TestSnapshot_FrozenListing(t *testing.T) {
	t.Parallel()
	s := newSuite(t, toolsRosterTOML, map[string][]fake.Step{
		"author": {
			stepWithToolCalls("", []transport.ToolCall{
				toolCall("list_dir", "1", ".", nil),
				toolCall("write_file", "2", "added.txt", map[string]string{"content": "new"}),
				toolCall("list_dir", "3", ".", nil),
			}),
			enginetest.Reply("the answer is 42"),
		},
		"reviewer": {
			verdictV2Approve(0),
		},
	})
	c := s.Dial(t)

	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "file1.txt"), []byte("content1"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	turnID := s.submitWithWorkspace(t, c, "snapshot-listing", workspace)
	s.WaitTerminal(turnID)

	turn := s.Turn(t, c, turnID)
	if turn.State != engine.StateApproved {
		t.Fatalf("turn state = %q, want approved", turn.State)
	}

	var obs []journal.Observation
	err := s.Store.Tx(func(tx *journal.Tx) error {
		var err error
		obs, err = tx.Observations(turnID)
		return err
	})
	if err != nil {
		t.Fatalf("Observations: %v", err)
	}

	listingObs := make([]journal.Observation, 0)
	for _, o := range obs {
		if o.Path == "." && o.Kind == journal.ObservationListing {
			listingObs = append(listingObs, o)
		}
	}

	if len(listingObs) != 1 {
		t.Fatalf("expected 1 listing observation for root, got %d", len(listingObs))
	}
	if listingObs[0].ContentSHA256 == "" {
		t.Fatalf("listing observation missing entries sha")
	}

	entries, err := s.Store.Content(listingObs[0].ContentSHA256)
	if err != nil {
		t.Fatalf("Content: %v", err)
	}
	if len(entries) == 0 {
		t.Fatalf("listing entries are empty")
	}
	if !bytes.Contains(entries, []byte("file1.txt")) {
		t.Fatalf("listing does not contain file1.txt")
	}
	if bytes.Contains(entries, []byte("added.txt")) {
		t.Fatalf("second list_dir returned added.txt (should be frozen from first read)")
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
			verdictV2Approve(0),
		},
	})
	c := s.Dial(t)

	// Create workspace with no missing.txt file.
	workspace := t.TempDir()

	// Submit a turn.
	turnID := s.submitWithWorkspace(t, c, "snapshot-absent", workspace)
	s.WaitTerminal(turnID)

	// Verify turn completed successfully.
	turn := s.Turn(t, c, turnID)
	if turn.State != engine.StateApproved {
		t.Fatalf("turn state = %q, want approved", turn.State)
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

	// Verify absent observations have no content sha and correct path.
	var foundAbsent bool
	for _, o := range obs {
		if o.Kind == journal.ObservationAbsent {
			foundAbsent = true
			if o.ContentSHA256 != "" {
				t.Fatalf("absent observation %q has content sha: %q", o.Path, o.ContentSHA256)
			}
			if o.Path != "missing.txt" {
				t.Fatalf("unexpected absent path %q, want missing.txt", o.Path)
			}
		}
	}
	if !foundAbsent {
		t.Fatalf("no absent observation recorded for missing.txt")
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
			verdictV2Approve(0),
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

	// Verify turn completed successfully.
	turn := s.Turn(t, c, turnID)
	if turn.State != engine.StateApproved {
		t.Fatalf("turn state = %q, want approved", turn.State)
	}

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

	// Find listing and absent observations
	var foundListing bool
	var foundAbsent bool
	for _, o := range obs {
		if o.Path == "." && o.Kind == journal.ObservationListing {
			foundListing = true
		}
		if o.Path == "missing.txt" && o.Kind == journal.ObservationAbsent {
			foundAbsent = true
		}
	}
	if !foundListing {
		t.Fatalf("no listing observation for root directory")
	}
	if !foundAbsent {
		t.Fatalf("no absent observation for missing.txt")
	}
}

// TestSnapshot_ConcurrentFirstReadsOneRecord verifies that two concurrent
// first reads of the same path by two reviewers produce one snapshot record.
func TestSnapshot_ConcurrentFirstReadsOneRecord(t *testing.T) {
	t.Parallel()
	testFile := "test.txt"

	rosterTOML := `
default_preset = "concurrent_review"

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
id = "reviewer1"
model = "llama3"
route = "api"
endpoint = "local"
eligible_roles = ["reviewer"]
pool = "default"
accept_unknown_truncation = true

[[participants]]
id = "reviewer2"
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

[presets.concurrent_review]
mode = "review"
author = "author"
required_reviewers = ["reviewer1", "reviewer2"]
tools = ["read"]
reviewer_tools = ["read"]

[policy]
local_only = true
allowed_destinations = []
`

	s := newSuite(t, rosterTOML, map[string][]fake.Step{
		"author": {
			stepWithToolCalls("", []transport.ToolCall{
				toolCall("read_file", "1", testFile, nil),
			}),
			enginetest.Reply("the answer is 42"),
		},
		"reviewer1": {
			stepWithToolCalls("", []transport.ToolCall{
				toolCall("read_file", "2", testFile, nil),
			}),
			verdictV2Approve(0),
		},
		"reviewer2": {
			stepWithToolCalls("", []transport.ToolCall{
				toolCall("read_file", "3", testFile, nil),
			}),
			verdictV2Approve(0),
		},
	})
	c := s.Dial(t)

	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, testFile), []byte("shared content"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	turnID := s.submitWithWorkspace(t, c, "snapshot-concurrent", workspace)
	s.WaitTerminal(turnID)

	turn := s.Turn(t, c, turnID)
	if turn.State != engine.StateApproved {
		t.Fatalf("turn state = %q, want approved", turn.State)
	}

	var obs []journal.Observation
	err := s.Store.Tx(func(tx *journal.Tx) error {
		var err error
		obs, err = tx.Observations(turnID)
		return err
	})
	if err != nil {
		t.Fatalf("Observations: %v", err)
	}

	pathCounts := make(map[string]int)
	for _, o := range obs {
		pathCounts[o.Path]++
	}

	if count, ok := pathCounts[testFile]; !ok || count != 1 {
		t.Fatalf("path %s observed %d times, want 1 (concurrent first reads should produce one record)", testFile, count)
	}
}

// TestSnapshot_NewlineFilenameDistinct verifies that directories with
// a file named "a\nb" vs files named "a" and "b" produce different entries_sha256.
func TestSnapshot_NewlineFilenameDistinct(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	dir1 := filepath.Join(workspace, "dir1")
	dir2 := filepath.Join(workspace, "dir2")
	if err := os.Mkdir(dir1, 0o755); err != nil {
		t.Fatalf("Mkdir dir1: %v", err)
	}
	if err := os.Mkdir(dir2, 0o755); err != nil {
		t.Fatalf("Mkdir dir2: %v", err)
	}

	// dir1 has a file with newline in name: "a\nb"
	newlineFile := "a\nb"
	if err := os.WriteFile(filepath.Join(dir1, newlineFile), []byte("content"), 0o644); err != nil {
		t.Fatalf("WriteFile newline: %v", err)
	}

	// dir2 has two files: "a" and "b"
	if err := os.WriteFile(filepath.Join(dir2, "a"), []byte("content1"), 0o644); err != nil {
		t.Fatalf("WriteFile a: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir2, "b"), []byte("content2"), 0o644); err != nil {
		t.Fatalf("WriteFile b: %v", err)
	}

	s := newSuite(t, toolsRosterTOML, map[string][]fake.Step{
		"author": {
			stepWithToolCalls("", []transport.ToolCall{
				toolCall("list_dir", "1", "dir1", nil),
				toolCall("list_dir", "2", "dir2", nil),
			}),
			enginetest.Reply("the answer is 42"),
		},
		"reviewer": {
			verdictV2Approve(0),
		},
	})
	c := s.Dial(t)
	turnID := s.submitWithWorkspace(t, c, "snapshot-newline-distinct", workspace)
	s.WaitTerminal(turnID)

	turn := s.Turn(t, c, turnID)
	if turn.State != engine.StateApproved {
		t.Fatalf("turn state = %q, want approved", turn.State)
	}

	var obs []journal.Observation
	err := s.Store.Tx(func(tx *journal.Tx) error {
		var err error
		obs, err = tx.Observations(turnID)
		return err
	})
	if err != nil {
		t.Fatalf("Observations: %v", err)
	}

	dir1SHA := ""
	dir2SHA := ""
	for _, o := range obs {
		if o.Path == "dir1" && o.Kind == journal.ObservationListing {
			dir1SHA = o.ContentSHA256
		}
		if o.Path == "dir2" && o.Kind == journal.ObservationListing {
			dir2SHA = o.ContentSHA256
		}
	}

	if dir1SHA == "" {
		t.Fatalf("no listing observation for dir1")
	}
	if dir2SHA == "" {
		t.Fatalf("no listing observation for dir2")
	}

	if dir1SHA == dir2SHA {
		t.Fatalf("dir1 (with a\\nb) and dir2 (with a,b) have same entries_sha256, should be different")
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
				toolCall("write_file", "2", "new.txt", map[string]string{"content": "staged content"}),
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

	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "existing.txt"), []byte("content"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	turnID := s.submitWithWorkspace(t, c, "snapshot-staged", workspace)
	s.WaitTerminal(turnID)

	turn := s.Turn(t, c, turnID)
	if turn.State != engine.StateApproved {
		t.Fatalf("turn state = %q, want approved", turn.State)
	}

	var obs []journal.Observation
	err := s.Store.Tx(func(tx *journal.Tx) error {
		var err error
		obs, err = tx.Observations(turnID)
		return err
	})
	if err != nil {
		t.Fatalf("Observations: %v", err)
	}

	listingObs := make([]journal.Observation, 0)
	for _, o := range obs {
		if o.Path == "." && o.Kind == journal.ObservationListing {
			listingObs = append(listingObs, o)
		}
	}

	if len(listingObs) != 1 {
		t.Fatalf("expected 1 listing observation, got %d", len(listingObs))
	}

	firstListingContent, err := s.Store.Content(listingObs[0].ContentSHA256)
	if err != nil {
		t.Fatalf("Content: %v", err)
	}

	if bytes.Contains(firstListingContent, []byte("existing.txt")) == false {
		t.Fatalf("first listing doesn't have existing.txt")
	}
	if bytes.Contains(firstListingContent, []byte("new.txt")) {
		t.Fatalf("first listing unexpectedly has new.txt")
	}

	if bytes.Contains(firstListingContent, []byte("new.txt")) {
		t.Fatalf("second listing has new.txt (should not appear in final snapshot)")
	}
}

// TestSnapshot_ReviewerReadsSameBytes verifies that a reviewer's read of
// a path the author read returns the same bytes.
func TestSnapshot_ReviewerReadsSameBytes(t *testing.T) {
	t.Parallel()
	testFile := "test.txt"
	testContent := []byte("same bytes for both")

	s := newSuite(t, toolsRosterTOML, map[string][]fake.Step{
		"author": {
			stepWithToolCalls("", []transport.ToolCall{
				toolCall("read_file", "1", testFile, nil),
			}),
			enginetest.Reply("the answer is 42"),
		},
		"reviewer": {
			stepWithToolCalls("", []transport.ToolCall{
				toolCall("read_file", "2", testFile, nil),
			}),
			verdictV2Approve(0),
		},
	})
	c := s.Dial(t)

	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, testFile), testContent, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	turnID := s.submitWithWorkspace(t, c, "snapshot-reviewer-read", workspace)
	s.WaitTerminal(turnID)

	turn := s.Turn(t, c, turnID)
	if turn.State != engine.StateApproved {
		t.Fatalf("turn state = %q, want approved", turn.State)
	}

	var obs []journal.Observation
	err := s.Store.Tx(func(tx *journal.Tx) error {
		var err error
		obs, err = tx.Observations(turnID)
		return err
	})
	if err != nil {
		t.Fatalf("Observations: %v", err)
	}

	fileObs := make([]journal.Observation, 0)
	for _, o := range obs {
		if o.Path == testFile && o.Kind == journal.ObservationFile {
			fileObs = append(fileObs, o)
		}
	}

	if len(fileObs) != 1 {
		t.Fatalf("expected 1 observation for %s, got %d", testFile, len(fileObs))
	}

	content, err := s.Store.Content(fileObs[0].ContentSHA256)
	if err != nil {
		t.Fatalf("Content: %v", err)
	}
	if !bytes.Equal(content, testContent) {
		t.Fatalf("content mismatch: got %q, want %q", string(content), string(testContent))
	}
}

// TestSnapshot_ReviewerFirstReadBumpsVersion verifies that a path first read
// by a reviewer (not the author) bumps snapshot_version, is recorded on the
// reviewer's verdict, does not change the candidate hash, and returns recorded
// bytes in next round.
func TestSnapshot_ReviewerFirstReadBumpsVersion(t *testing.T) {
	t.Parallel()
	testFile := "output.txt"

	s := newSuite(t, toolsRosterTOML, map[string][]fake.Step{
		"author": {
			stepWithToolCalls("", []transport.ToolCall{
				toolCall("read_file", "1", "other.txt", nil),
			}),
			enginetest.Reply("initial attempt"),
		},
		"reviewer": {
			stepWithToolCalls("", []transport.ToolCall{
				toolCall("read_file", "2", testFile, nil),
			}),
			verdictV2Approve(0),
		},
	})
	c := s.Dial(t)

	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "other.txt"), []byte("content"), 0o644); err != nil {
		t.Fatalf("WriteFile other.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, testFile), []byte("output data"), 0o644); err != nil {
		t.Fatalf("WriteFile output.txt: %v", err)
	}

	turnID := s.submitWithWorkspace(t, c, "snapshot-reviewer-bumps-version", workspace)
	s.WaitTerminal(turnID)

	turn := s.Turn(t, c, turnID)
	if turn.State != engine.StateApproved {
		t.Fatalf("turn state = %q, want approved", turn.State)
	}

	var obs []journal.Observation
	err := s.Store.Tx(func(tx *journal.Tx) error {
		var err error
		obs, err = tx.Observations(turnID)
		return err
	})
	if err != nil {
		t.Fatalf("Observations: %v", err)
	}

	outputObs := make([]journal.Observation, 0)
	for _, o := range obs {
		if o.Path == testFile && o.Kind == journal.ObservationFile {
			outputObs = append(outputObs, o)
		}
	}

	if len(outputObs) != 1 {
		t.Fatalf("expected 1 observation for %s, got %d", testFile, len(outputObs))
	}

	if outputObs[0].SnapshotVersion < 1 {
		t.Fatalf("snapshot_version should be >= 1, got %d", outputObs[0].SnapshotVersion)
	}

	if turn.CandidateHash == nil || *turn.CandidateHash == "" {
		t.Fatalf("turn has no candidate hash")
	}

	verdicts, err := s.Store.ListVerdicts(turnID)
	if err != nil {
		t.Fatalf("ListVerdicts: %v", err)
	}
	if len(verdicts) == 0 {
		t.Fatalf("no verdicts recorded")
	}
}
