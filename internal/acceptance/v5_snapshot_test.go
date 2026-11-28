package acceptance

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/engine"
	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/observation"
	"github.com/boldfield/syndic/internal/tools"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/transport/fake"
	"github.com/boldfield/syndic/internal/wire"
)

const snapshotPara = "Snapshot tools: observations and confined opens"

func init() {
	cover(snapshotPara, "a second read of a path in the same turn returns the snapshot content after the live file changed", "TestSnapshot_SecondReadFrozen")
	cover(snapshotPara, "a second `list_dir` returns the recorded entries after a file was added to the live directory", "TestSnapshot_SecondListDirFrozen")
	cover(snapshotPara, "a second `search_files` over the same scope does not see a file created after the first", "TestSnapshot_SecondSearchFrozen")
	cover(snapshotPara, "a path observed absent stays absent for the turn after it is created live", "TestSnapshot_AbsentStaysAbsent")
	cover(snapshotPara, "a name missing from a recorded listing reads `not found` after it is created live", "TestSnapshot_MissingNameNotFound")
	cover(snapshotPara, "a path recorded absent is left out of a later listing of its parent though the live directory has it", "TestSnapshot_AbsentOmittedFromListing")
	cover(snapshotPara, "a listed name whose file is deleted before its first read returns `snapshot_conflict` and records nothing", "TestSnapshot_ListedDeletedConflict")
	cover(snapshotPara, "a staged creation appears in `list_dir` and `search_files` and disappears from both on `unstage`", "TestSnapshot_StagedCreationOverlay")
	cover(snapshotPara, "a directory holding one file named `a\\nb` and one holding files `a` and `b` produce different `entries_sha256`", "TestSnapshot_NewlineNamesDistinctEntries")
	cover(snapshotPara, "two concurrent first reads of one path by two reviewers produce one snapshot record", "TestSnapshot_ConcurrentFirstReadsOneRecord")
	cover(snapshotPara, "a reviewer's read of a path the author read returns the same bytes", "TestSnapshot_ReviewerReadsAuthorBytes")
	cover(snapshotPara, "a path first read by a reviewer bumps `snapshot_version`, is recorded on that reviewer's verdict with the version, does not change the candidate hash, and the next round's author read of it returns the recorded bytes", "TestSnapshot_ReviewerFirstReadBumpsVersion")
	cover(snapshotPara, "a path outside the workspace, through a symbolic link, or matching a protected pattern is refused and journaled", "TestSnapshot_ConfinedPathsRefused")
	cover(snapshotPara, "a path swapped for a symbolic link between the check and the open is refused at open", "TestSnapshot_SwapForSymlinkRefusedAtOpen")
}

const (
	snapAnswer  = "the answer is 41"
	snapRevised = "the answer is 42, and here is why"
)

// snapRoster is revRoster with the review preset giving the author read and
// write and every reviewer read, and generous call bounds.
func snapRoster(rounds int, reviewers ...string) string {
	r := revRoster(rounds, rounds, reviewers...)
	r = strings.Replace(r, "[presets.review]\nmode = \"review\"\n",
		"[presets.review]\nmode = \"review\"\ntools = [\"read\", \"write\"]\nreviewer_tools = [\"read\"]\n", 1)
	return r + "\n[tools]\nmax_calls_per_turn = 30\nmax_reviewer_calls = 30\n"
}

// snapArgs is the JSON arguments object of key/value string pairs.
func snapArgs(kv ...string) string {
	m := map[string]string{}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i]] = kv[i+1]
	}
	b, err := json.Marshal(m)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func snapCall(id, name string, kv ...string) transport.ToolCall {
	return transport.ToolCall{ID: id, Type: "function", Function: transport.ToolCallFunction{Name: name, Arguments: snapArgs(kv...)}}
}

func snapRead(id, path string) transport.ToolCall {
	return snapCall(id, tools.NameReadFile, "path", path)
}

func snapList(id, path string) transport.ToolCall {
	return snapCall(id, tools.NameListDir, "path", path)
}

func snapSearch(id, pattern string) transport.ToolCall {
	return snapCall(id, tools.NameSearchFiles, "pattern", pattern)
}

// snapStep is a tool step carrying calls. before runs when the step's request
// is made, so after the results of every earlier step were executed and
// before this step's calls are: it is where a test changes the live tree.
func snapStep(before func(), calls ...transport.ToolCall) fake.Step {
	return fake.Step{
		Kind:            fake.StepKindReply,
		Status:          transport.StatusToolCalls,
		TransportStatus: transport.TransportStatusOK,
		ToolCalls:       calls,
		Build: func(transport.CompleteRequest) string {
			if before != nil {
				before()
			}
			return "using tools"
		},
	}
}

func snapApprove() fake.Step {
	return enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
		return fake.VerdictApprove(req.Envelope.CandidateHash, req.Envelope.AttemptID)
	})
}

// snapWrite writes a file of the live workspace.
func snapWrite(t testing.TB, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// snapMutator returns a before-hook that writes content to name.
func snapMutator(t testing.TB, dir, name, content string) func() {
	return func() { snapWrite(t, dir, name, content) }
}

func snapSHA(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// snapRun submits one review turn on dir under the preset review and waits
// for it to end approved.
func snapRun(t *testing.T, roster string, scripts map[string][]fake.Step, dir string, opts ...enginetest.Option) (*enginetest.Harness, string) {
	t.Helper()
	h := enginetest.New(t, roster, scripts, opts...)
	preset := "review"
	reply, err := h.Engine.Submit(wire.SubmitRequest{SubmissionID: "s1", Input: "question", Preset: &preset, Workspace: dir})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		row, err := h.Store.GetTurn(reply.TurnID)
		if err != nil {
			t.Fatal(err)
		}
		if row.TerminalState != "" {
			if row.TerminalState != engine.StateApproved || row.TerminalReason != "all_approved" {
				t.Fatalf("turn ended %q %q, want approved all_approved", row.TerminalState, row.TerminalReason)
			}
			return h, reply.TurnID
		}
		if time.Now().After(deadline) {
			t.Fatalf("turn never ended; states %v", h.TurnStates(reply.TurnID))
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// snapResult is what one call returned to the model, as journaled.
type snapResult struct {
	Name, Status, Code, Body string
}

// snapResults returns the participant's journaled results by the call id the
// model chose.
func snapResults(t *testing.T, h *enginetest.Harness, turnID, participant string) map[string]snapResult {
	t.Helper()
	calls, err := h.Store.ToolCallsByTurn(turnID, participant)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]snapResult{}
	for _, c := range calls {
		r := snapResult{Name: c.Name, Status: c.Status}
		if c.Result != nil {
			r.Code = c.Result.ErrorCode
			if c.Result.ResultSHA256 != "" {
				body, err := h.Store.Content(c.Result.ResultSHA256)
				if err != nil {
					t.Fatal(err)
				}
				r.Body = string(body)
			}
		}
		out[c.ProviderCallID] = r
	}
	return out
}

// snapWantResult checks one call's journaled result: executed with exactly
// body, or an error with code.
func snapWantBody(t *testing.T, res map[string]snapResult, id, body string) {
	t.Helper()
	r, ok := res[id]
	if !ok {
		t.Fatalf("call %s was never journaled; have %v", id, res)
	}
	if r.Status != journal.ToolCallExecuted || r.Code != "" || r.Body != body {
		t.Fatalf("call %s = %+v, want executed with body %q", id, r, body)
	}
}

func snapWantError(t *testing.T, res map[string]snapResult, id, code string) {
	t.Helper()
	r, ok := res[id]
	if !ok {
		t.Fatalf("call %s was never journaled; have %v", id, res)
	}
	if r.Status == journal.ToolCallExecuted || r.Code != code {
		t.Fatalf("call %s = %+v, want an error with code %q", id, r, code)
	}
}

// snapObservations is the turn's snapshot record by path.
func snapObservations(t *testing.T, h *enginetest.Harness, turnID string) map[string]journal.Observation {
	t.Helper()
	all, err := h.Store.Observations(turnID)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]journal.Observation{}
	for _, o := range all {
		if _, dup := out[o.Path]; dup {
			t.Fatalf("path %q observed twice", o.Path)
		}
		out[o.Path] = o
	}
	return out
}

func snapWantObservation(t *testing.T, obs map[string]journal.Observation, path, kind string) journal.Observation {
	t.Helper()
	o, ok := obs[path]
	if !ok || o.Kind != kind {
		t.Fatalf("observation of %q = %+v (recorded: %v), want kind %q", path, o, obs, kind)
	}
	return o
}

func snapContent(t *testing.T, h *enginetest.Harness, sha string) string {
	t.Helper()
	body, err := h.Store.Content(sha)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func snapReadLive(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// snapAuthorScripts runs the author's steps then its answer, against one
// approving reviewer.
func snapAuthorScripts(steps ...fake.Step) map[string][]fake.Step {
	return map[string][]fake.Step{
		"author":   append(steps, enginetest.Reply(snapAnswer)),
		"reviewer": {snapApprove()},
	}
}

func TestSnapshot_SecondReadFrozen(t *testing.T) {
	dir := t.TempDir()
	snapWrite(t, dir, "f.txt", "original\n")
	h, turnID := snapRun(t, snapRoster(1), snapAuthorScripts(
		snapStep(nil, snapRead("c1", "f.txt")),
		snapStep(snapMutator(t, dir, "f.txt", "changed live\n"), snapRead("c2", "f.txt")),
	), dir)

	res := snapResults(t, h, turnID, "author")
	snapWantBody(t, res, "c1", "original\n")
	snapWantBody(t, res, "c2", "original\n")
	obs := snapObservations(t, h, turnID)
	o := snapWantObservation(t, obs, "f.txt", journal.ObservationFile)
	if o.SnapshotVersion != 1 || o.ContentSHA256 != snapSHA("original\n") {
		t.Fatalf("record = %+v, want version 1 over the original bytes", o)
	}
	if got := snapContent(t, h, o.ContentSHA256); got != "original\n" {
		t.Fatalf("recorded content rewritten to %q", got)
	}
	if got := snapReadLive(t, dir, "f.txt"); got != "changed live\n" {
		t.Fatalf("live file = %q: the live edit was not made, so the test proved nothing", got)
	}
	if len(obs) != 1 {
		t.Fatalf("record holds %d observations, want only f.txt: %v", len(obs), obs)
	}
}

func TestSnapshot_SecondListDirFrozen(t *testing.T) {
	dir := t.TempDir()
	snapWrite(t, dir, "a.txt", "a\n")
	h, turnID := snapRun(t, snapRoster(1), snapAuthorScripts(
		snapStep(nil, snapList("c1", ".")),
		snapStep(snapMutator(t, dir, "b.txt", "b\n"), snapList("c2", ".")),
	), dir)

	res := snapResults(t, h, turnID, "author")
	snapWantBody(t, res, "c1", "a.txt\n")
	snapWantBody(t, res, "c2", "a.txt\n")
	obs := snapObservations(t, h, turnID)
	o := snapWantObservation(t, obs, ".", journal.ObservationListing)
	want, err := observation.EntriesHash([]observation.Entry{{Type: "f", Name: "a.txt"}})
	if err != nil {
		t.Fatal(err)
	}
	if o.ContentSHA256 != want || o.SnapshotVersion != 1 {
		t.Fatalf("listing record = %+v, want version 1 over entries %s", o, want)
	}
	if _, err := os.Stat(filepath.Join(dir, "b.txt")); err != nil {
		t.Fatalf("b.txt was not added live: %v", err)
	}
}

func TestSnapshot_SecondSearchFrozen(t *testing.T) {
	dir := t.TempDir()
	snapWrite(t, dir, "a.txt", "needle one\nhay\n")
	h, turnID := snapRun(t, snapRoster(1), snapAuthorScripts(
		snapStep(nil, snapSearch("c1", "needle")),
		snapStep(snapMutator(t, dir, "b.txt", "needle two\n"), snapSearch("c2", "needle")),
	), dir)

	res := snapResults(t, h, turnID, "author")
	snapWantBody(t, res, "c1", "a.txt:1:needle one\n")
	snapWantBody(t, res, "c2", "a.txt:1:needle one\n")
	obs := snapObservations(t, h, turnID)
	snapWantObservation(t, obs, ".", journal.ObservationListing)
	snapWantObservation(t, obs, "a.txt", journal.ObservationFile)
	if _, seen := obs["b.txt"]; seen || len(obs) != 2 {
		t.Fatalf("record = %v, want only the scope and a.txt", obs)
	}
	if _, err := os.Stat(filepath.Join(dir, "b.txt")); err != nil {
		t.Fatalf("b.txt was not created live: %v", err)
	}
}

func TestSnapshot_AbsentStaysAbsent(t *testing.T) {
	dir := t.TempDir()
	snapWrite(t, dir, "a.txt", "a\n")
	h, turnID := snapRun(t, snapRoster(1), snapAuthorScripts(
		snapStep(nil, snapRead("c1", "missing.txt")),
		snapStep(snapMutator(t, dir, "missing.txt", "now it exists\n"), snapRead("c2", "missing.txt")),
	), dir)

	res := snapResults(t, h, turnID, "author")
	snapWantError(t, res, "c1", tools.ErrCodeNotFound)
	snapWantError(t, res, "c2", tools.ErrCodeNotFound)
	o := snapWantObservation(t, snapObservations(t, h, turnID), "missing.txt", journal.ObservationAbsent)
	if o.SnapshotVersion != 1 || o.ContentSHA256 != "" {
		t.Fatalf("absence record = %+v", o)
	}
	if got := snapReadLive(t, dir, "missing.txt"); got != "now it exists\n" {
		t.Fatalf("live file = %q", got)
	}
}

func TestSnapshot_MissingNameNotFound(t *testing.T) {
	dir := t.TempDir()
	snapWrite(t, dir, "a.txt", "a\n")
	h, turnID := snapRun(t, snapRoster(1), snapAuthorScripts(
		snapStep(nil, snapList("c1", ".")),
		snapStep(snapMutator(t, dir, "late.txt", "arrived after the listing\n"), snapRead("c2", "late.txt")),
	), dir)

	res := snapResults(t, h, turnID, "author")
	snapWantBody(t, res, "c1", "a.txt\n")
	snapWantError(t, res, "c2", tools.ErrCodeNotFound)
	if !strings.Contains(res["c2"].Body, "late.txt") {
		t.Fatalf("error %q does not name the path", res["c2"].Body)
	}
	for path, o := range snapObservations(t, h, turnID) {
		if path == "late.txt" && o.Kind == journal.ObservationFile {
			t.Fatalf("the live file late.txt was recorded: %+v", o)
		}
	}
	if got := snapReadLive(t, dir, "late.txt"); got == "" {
		t.Fatal("late.txt was not created live")
	}
}

func TestSnapshot_AbsentOmittedFromListing(t *testing.T) {
	dir := t.TempDir()
	snapWrite(t, dir, "a.txt", "a\n")
	h, turnID := snapRun(t, snapRoster(1), snapAuthorScripts(
		snapStep(nil, snapRead("c1", "missing.txt")),
		snapStep(snapMutator(t, dir, "missing.txt", "created live\n"), snapList("c2", ".")),
	), dir)

	res := snapResults(t, h, turnID, "author")
	snapWantError(t, res, "c1", tools.ErrCodeNotFound)
	snapWantBody(t, res, "c2", "a.txt\n")
	obs := snapObservations(t, h, turnID)
	snapWantObservation(t, obs, "missing.txt", journal.ObservationAbsent)
	listing := snapWantObservation(t, obs, ".", journal.ObservationListing)
	want, err := observation.EntriesHash([]observation.Entry{{Type: "f", Name: "a.txt"}})
	if err != nil {
		t.Fatal(err)
	}
	if listing.ContentSHA256 != want {
		t.Fatalf("recorded listing %q hashes to %s, want the live listing without missing.txt (%s)",
			snapContent(t, h, listing.ContentSHA256), listing.ContentSHA256, want)
	}
	if listing.SnapshotVersion <= obs["missing.txt"].SnapshotVersion {
		t.Fatalf("listing version %d is not after the absence's %d", listing.SnapshotVersion, obs["missing.txt"].SnapshotVersion)
	}
}

func TestSnapshot_ListedDeletedConflict(t *testing.T) {
	dir := t.TempDir()
	snapWrite(t, dir, "a.txt", "a\n")
	snapWrite(t, dir, "gone.txt", "soon deleted\n")
	remove := func() {
		if err := os.Remove(filepath.Join(dir, "gone.txt")); err != nil {
			t.Error(err)
		}
	}
	h, turnID := snapRun(t, snapRoster(1), snapAuthorScripts(
		snapStep(nil, snapList("c1", ".")),
		snapStep(remove, snapRead("c2", "gone.txt"), snapRead("c3", "a.txt")),
	), dir)

	res := snapResults(t, h, turnID, "author")
	snapWantBody(t, res, "c1", "a.txt\ngone.txt\n")
	snapWantError(t, res, "c2", tools.ErrCodeSnapshotConflict)
	if !strings.Contains(res["c2"].Body, "gone.txt") {
		t.Fatalf("conflict %q does not name the path", res["c2"].Body)
	}
	snapWantBody(t, res, "c3", "a\n")
	obs := snapObservations(t, h, turnID)
	if o, ok := obs["gone.txt"]; ok {
		t.Fatalf("the conflicting read recorded %+v", o)
	}
	snapWantObservation(t, obs, "a.txt", journal.ObservationFile)
	if _, err := os.Stat(filepath.Join(dir, "gone.txt")); err == nil {
		t.Fatal("gone.txt still exists live")
	}
}

func TestSnapshot_StagedCreationOverlay(t *testing.T) {
	dir := t.TempDir()
	snapWrite(t, dir, "a.txt", "needle base\n")
	listAndSearch := func(suffix string) []transport.ToolCall {
		return []transport.ToolCall{snapList("l"+suffix, "."), snapSearch("s"+suffix, "needle")}
	}
	h, turnID := snapRun(t, snapRoster(1), snapAuthorScripts(
		snapStep(nil, listAndSearch("0")...),
		snapStep(nil, append([]transport.ToolCall{snapCall("w1", tools.NameWriteFile, "path", "new.txt", "content", "needle new\n")}, listAndSearch("1")...)...),
		snapStep(nil, append([]transport.ToolCall{snapCall("u1", tools.NameUnstage, "path", "new.txt")}, listAndSearch("2")...)...),
	), dir)

	res := snapResults(t, h, turnID, "author")
	snapWantBody(t, res, "l0", "a.txt\n")
	snapWantBody(t, res, "s0", "a.txt:1:needle base\n")
	if r := res["w1"]; r.Status != journal.ToolCallExecuted {
		t.Fatalf("write_file = %+v", r)
	}
	snapWantBody(t, res, "l1", "a.txt\nnew.txt\n")
	snapWantBody(t, res, "s1", "a.txt:1:needle base\nnew.txt:1:needle new\n")
	if r := res["u1"]; r.Status != journal.ToolCallExecuted {
		t.Fatalf("unstage = %+v", r)
	}
	snapWantBody(t, res, "l2", "a.txt\n")
	snapWantBody(t, res, "s2", "a.txt:1:needle base\n")
	if _, err := os.Stat(filepath.Join(dir, "new.txt")); err == nil {
		t.Fatal("the staged creation reached the live workspace")
	}
}

// The framed entry lists the two directories of TestSnapshot_NewlineNames
// record, pinned: names are length-framed, so a newline inside a name cannot
// pass for a separator between two.
const (
	snapFramedOneName = "f 3\na\nb\n"
	snapFramedTwoName = "f 1\na\nf 1\nb\n"
)

func TestSnapshot_NewlineNamesDistinctEntries(t *testing.T) {
	dir := t.TempDir()
	snapWrite(t, dir, "one/a\nb", "x\n")
	snapWrite(t, dir, "two/a", "x\n")
	snapWrite(t, dir, "two/b", "x\n")
	h, turnID := snapRun(t, snapRoster(1), snapAuthorScripts(
		snapStep(nil, snapList("c1", "one"), snapList("c2", "two")),
	), dir)

	res := snapResults(t, h, turnID, "author")
	snapWantBody(t, res, "c1", "\"a\\nb\"\n")
	snapWantBody(t, res, "c2", "a\nb\n")
	obs := snapObservations(t, h, turnID)
	one := snapWantObservation(t, obs, "one", journal.ObservationListing)
	two := snapWantObservation(t, obs, "two", journal.ObservationListing)
	if one.ContentSHA256 == two.ContentSHA256 {
		t.Fatalf("both directories hash to %s", one.ContentSHA256)
	}
	if got := snapContent(t, h, one.ContentSHA256); got != snapFramedOneName {
		t.Fatalf("one's entry list = %q, want %q", got, snapFramedOneName)
	}
	if got := snapContent(t, h, two.ContentSHA256); got != snapFramedTwoName {
		t.Fatalf("two's entry list = %q, want %q", got, snapFramedTwoName)
	}
	// Golden identities: SHA-256 of the framed lists above.
	const goldenOne = "afbda6162f3986a7b1d489b10b6936d08af1a07f1977f5aa5cf4938dfa2889de"
	const goldenTwo = "9db1fb0b0767c711add8e665fa789908247feb96f99409c16bb288da80fed950"
	if one.ContentSHA256 != goldenOne || two.ContentSHA256 != goldenTwo {
		t.Fatalf("entries_sha256 = %s and %s, want goldens %s and %s", one.ContentSHA256, two.ContentSHA256, goldenOne, goldenTwo)
	}
}

func TestSnapshot_ConcurrentFirstReadsOneRecord(t *testing.T) {
	runs := 8
	if testing.Short() {
		runs = 3
	}
	for i := 0; i < runs; i++ {
		dir := t.TempDir()
		snapWrite(t, dir, "shared.txt", "shared bytes\n")
		// Each reviewer waits for the other before making its request's
		// calls, so both first touches are in flight together.
		var arrived atomic.Int32
		barrier := func() {
			arrived.Add(1)
			deadline := time.Now().Add(5 * time.Second)
			for arrived.Load() < 2 && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
		}
		h, turnID := snapRun(t, snapRoster(1, "r1", "r2"), map[string][]fake.Step{
			"author": {enginetest.Reply(snapAnswer)},
			"r1":     {snapStep(barrier, snapRead("c1", "shared.txt")), snapApprove()},
			"r2":     {snapStep(barrier, snapRead("c1", "shared.txt")), snapApprove()},
		}, dir)

		obs := snapObservations(t, h, turnID)
		o := snapWantObservation(t, obs, "shared.txt", journal.ObservationFile)
		if len(obs) != 1 || o.SnapshotVersion != 1 || o.ContentSHA256 != snapSHA("shared bytes\n") {
			t.Fatalf("run %d: record = %v, want one observation of shared.txt at version 1", i, obs)
		}
		for _, id := range []string{"r1", "r2"} {
			snapWantBody(t, snapResults(t, h, turnID, id), "c1", "shared bytes\n")
		}
		owners := 0
		rows, err := h.Store.ListVerdicts(turnID)
		if err != nil || len(rows) != 2 {
			t.Fatalf("verdicts = %+v, err %v", rows, err)
		}
		for _, row := range rows {
			ev, err := h.Store.GetVerdictEvidence(row.ID)
			if err != nil {
				t.Fatal(err)
			}
			for _, id := range ev.ObservationIDs {
				if id != o.ID {
					t.Fatalf("run %d: verdict %s binds observation %s, not the record %s", i, row.ID, id, o.ID)
				}
				owners++
			}
		}
		if owners != 1 {
			t.Fatalf("run %d: %d verdicts bind the one record, want exactly the first toucher's", i, owners)
		}
	}
}

func TestSnapshot_ReviewerReadsAuthorBytes(t *testing.T) {
	dir := t.TempDir()
	snapWrite(t, dir, "f.txt", "author saw this\n")
	h, turnID := snapRun(t, snapRoster(1), map[string][]fake.Step{
		"author": {snapStep(nil, snapRead("a1", "f.txt")), enginetest.Reply(snapAnswer)},
		"reviewer": {
			snapStep(snapMutator(t, dir, "f.txt", "edited before the reviewer looked\n"), snapRead("r1", "f.txt")),
			snapApprove(),
		},
	}, dir)

	author := snapResults(t, h, turnID, "author")["a1"]
	reviewer := snapResults(t, h, turnID, "reviewer")["r1"]
	if author.Body != "author saw this\n" || reviewer.Body != author.Body || reviewer.Status != journal.ToolCallExecuted {
		t.Fatalf("author read %+v, reviewer read %+v, want the same bytes", author, reviewer)
	}
	obs := snapObservations(t, h, turnID)
	if len(obs) != 1 || obs["f.txt"].SnapshotVersion != 1 {
		t.Fatalf("record = %v, want the author's one observation", obs)
	}
	// The reviewer added nothing, so its verdict binds no observation.
	rows, err := h.Store.ListVerdicts(turnID)
	if err != nil || len(rows) != 1 {
		t.Fatalf("verdicts = %+v, err %v", rows, err)
	}
	ev, err := h.Store.GetVerdictEvidence(rows[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(ev.ObservationIDs) != 0 {
		t.Fatalf("the reviewer's verdict binds %v, though it observed nothing", ev.ObservationIDs)
	}
}

func TestSnapshot_ReviewerFirstReadBumpsVersion(t *testing.T) {
	dir := t.TempDir()
	snapWrite(t, dir, "f.txt", "author read\n")
	snapWrite(t, dir, "g.txt", "reviewer first\n")
	block := enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
		return fake.VerdictBlock(req.Envelope.CandidateHash, req.Envelope.AttemptID, "b1", "high", "evidence", "remedy", "reason")
	})
	withdraw := verdict(func(hash, attempt string) string {
		return strings.Join([]string{"VERDICT v2", "candidate: " + hash, "attempt: " + attempt, "round: 1",
			"block-state: b1 withdrawn", "verdict: approve", "reason: r", "END VERDICT"}, "\n")
	})
	h, turnID := snapRun(t, snapRoster(1), map[string][]fake.Step{
		"author": {
			snapStep(nil, snapRead("a1", "f.txt")),
			enginetest.Reply(snapAnswer),
			// Round 1: the live file has changed since the reviewer's read.
			snapStep(snapMutator(t, dir, "g.txt", "changed after the record\n"), snapRead("a2", "g.txt")),
			enginetest.Reply(snapRevised),
		},
		"reviewer": {snapStep(nil, snapRead("r1", "g.txt")), block, withdraw},
	}, dir)

	obs := snapObservations(t, h, turnID)
	f := snapWantObservation(t, obs, "f.txt", journal.ObservationFile)
	g := snapWantObservation(t, obs, "g.txt", journal.ObservationFile)
	if f.SnapshotVersion != 1 || g.SnapshotVersion != 2 || g.ContentSHA256 != snapSHA("reviewer first\n") {
		t.Fatalf("records = %+v and %+v, want the reviewer's first read at version 2", f, g)
	}

	var frozen []string
	for _, e := range h.Events() {
		if p, ok := e.Payload.(*wire.EvCandidateFrozen); ok && e.TurnID == turnID {
			frozen = append(frozen, p.CandidateHash)
		}
	}
	reqs := h.Adapters["reviewer"].Calls()
	if len(frozen) != 2 || len(reqs) != 3 {
		t.Fatalf("frozen candidates %v, reviewer requests %d, want two rounds", frozen, len(reqs))
	}

	// The reviewer's round 0 verdict records the version it saw and what it
	// added; its round 1 verdict adds nothing.
	rows, err := h.Store.ListVerdicts(turnID)
	if err != nil || len(rows) != 2 {
		t.Fatalf("verdicts = %+v, err %v", rows, err)
	}
	var round0 journal.VerdictRow
	for _, row := range rows {
		if row.CandidateHash == frozen[0] {
			round0 = row
		}
	}
	if round0.ID == "" {
		t.Fatalf("no verdict on the round 0 candidate %s: %+v", frozen[0], rows)
	}
	ev, err := h.Store.GetVerdictEvidence(round0.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ev.SnapshotVersion != 2 || len(ev.ObservationIDs) != 1 || ev.ObservationIDs[0] != g.ID {
		t.Fatalf("round 0 verdict evidence = %+v, want version 2 binding %s", ev, g.ID)
	}

	// The candidate it judged is the one frozen: the hash did not move when
	// the record was extended.
	if reqs[0].Request.Envelope.CandidateHash != frozen[0] || reqs[1].Request.Envelope.CandidateHash != frozen[0] {
		t.Fatalf("round 0 candidate hash moved: frozen %s, requests %s and %s",
			frozen[0], reqs[0].Request.Envelope.CandidateHash, reqs[1].Request.Envelope.CandidateHash)
	}

	// The next round's author read of it returns the recorded bytes.
	snapWantBody(t, snapResults(t, h, turnID, "author"), "a2", "reviewer first\n")
	if got := snapReadLive(t, dir, "g.txt"); got != "changed after the record\n" {
		t.Fatalf("live g.txt = %q: the live edit was not made", got)
	}
}

// snapOutside makes a file outside the workspace holding secret.
func snapOutside(t *testing.T, name, secret string) string {
	t.Helper()
	outside := t.TempDir()
	snapWrite(t, outside, name, secret)
	return outside
}

func TestSnapshot_ConfinedPathsRefused(t *testing.T) {
	const secret = "SECRET-OUTSIDE-THE-WORKSPACE"
	dir := t.TempDir()
	outside := snapOutside(t, "secret.txt", secret)
	snapWrite(t, dir, "ok.txt", "inside\n")
	snapWrite(t, dir, ".env", secret)
	snapWrite(t, dir, ".git/config", secret)
	if err := os.Symlink(outside, filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(dir, "file-link")); err != nil {
		t.Fatal(err)
	}
	h, turnID := snapRun(t, snapRoster(1), snapAuthorScripts(
		snapStep(nil,
			snapRead("dotdot", "../"+filepath.Base(outside)+"/secret.txt"),
			snapRead("abs", filepath.Join(outside, "secret.txt")),
			snapRead("viadir", "link/secret.txt"),
			snapList("listlink", "link"),
			snapRead("filelink", "file-link"),
			snapRead("env", ".env"),
			snapRead("git", ".git/config"),
			snapRead("fine", "ok.txt"),
		),
	), dir)

	res := snapResults(t, h, turnID, "author")
	for _, id := range []string{"dotdot", "abs", "viadir", "listlink", "filelink", "env", "git"} {
		r, ok := res[id]
		if !ok {
			t.Fatalf("call %s was not journaled", id)
		}
		if r.Status == journal.ToolCallExecuted || r.Code == "" {
			t.Fatalf("call %s = %+v, want a journaled refusal", id, r)
		}
		if strings.Contains(r.Body, secret) {
			t.Fatalf("call %s leaked the secret: %q", id, r.Body)
		}
	}
	snapWantError(t, res, "env", tools.ErrCodeProtected)
	snapWantError(t, res, "git", tools.ErrCodeProtected)
	snapWantError(t, res, "viadir", tools.ErrCodeNotRegular)
	snapWantError(t, res, "filelink", tools.ErrCodeNotRegular)
	snapWantBody(t, res, "fine", "inside\n")
	for path, o := range snapObservations(t, h, turnID) {
		if o.Kind == journal.ObservationFile && snapContent(t, h, o.ContentSHA256) == secret {
			t.Fatalf("%q was recorded with the secret", path)
		}
	}
}

func TestSnapshot_SwapForSymlinkRefusedAtOpen(t *testing.T) {
	const secret = "SECRET-REACHED-THROUGH-A-SWAP"
	dir := t.TempDir()
	outside := snapOutside(t, "f.txt", secret)
	snapWrite(t, dir, "plain.txt", "plain\n")
	snapWrite(t, dir, "swapfile.txt", "regular at check time\n")
	snapWrite(t, dir, "swapdir/f.txt", "regular at check time\n")

	// The swap lands after the engine accepted the call and before it opens
	// the path: the first call's file, then the second call's directory,
	// become links out of the workspace.
	var calls atomic.Int32
	var mu sync.Mutex
	swap := func(turnID, attemptID, name string) {
		mu.Lock()
		defer mu.Unlock()
		switch calls.Add(1) {
		case 1:
			must := func(err error) {
				if err != nil {
					t.Error(err)
				}
			}
			must(os.Remove(filepath.Join(dir, "swapfile.txt")))
			must(os.Symlink(filepath.Join(outside, "f.txt"), filepath.Join(dir, "swapfile.txt")))
		case 2:
			must := func(err error) {
				if err != nil {
					t.Error(err)
				}
			}
			must(os.RemoveAll(filepath.Join(dir, "swapdir")))
			must(os.Symlink(outside, filepath.Join(dir, "swapdir")))
		}
	}
	h, turnID := snapRun(t, snapRoster(1), snapAuthorScripts(
		snapStep(nil, snapRead("file", "swapfile.txt"), snapRead("dir", "swapdir/f.txt"), snapRead("plain", "plain.txt")),
	), dir, enginetest.WithBeforeToolEffect(swap))

	res := snapResults(t, h, turnID, "author")
	if calls.Load() != 3 {
		t.Fatalf("the swap hook ran %d times, want once per call", calls.Load())
	}
	snapWantError(t, res, "file", tools.ErrCodeNotRegular)
	snapWantError(t, res, "dir", tools.ErrCodeNotRegular)
	for _, id := range []string{"file", "dir"} {
		if strings.Contains(res[id].Body, secret) {
			t.Fatalf("call %s read through the swapped link: %q", id, res[id].Body)
		}
	}
	snapWantBody(t, res, "plain", "plain\n")
	for path, o := range snapObservations(t, h, turnID) {
		if o.Kind == journal.ObservationFile && snapContent(t, h, o.ContentSHA256) == secret {
			t.Fatalf("%q was recorded with the secret", path)
		}
	}
}
