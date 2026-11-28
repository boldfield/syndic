package tui

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/boldfield/syndic/internal/wire"
	tea "github.com/charmbracelet/bubbletea"
)

func sum(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// changeSetFixture is a change set of three paths whose contents are loaded.
func changeSetFixture(a *App, state string, outcomes ...string) *wire.ChangeSetRecord {
	files := []struct{ path, base, body string }{
		{"a.txt", "one\ntwo\nthree\n", "one\nTWO\nthree\n"},
		{"b.txt", "", "brand new\n"},
		{"c.txt", "keep\n", "changed\n"},
	}
	cs := &wire.ChangeSetRecord{ID: "cs1", State: state, ChangesHash: sum("changes"), SnapshotHash: sum("snap")}
	for i, f := range files {
		p := wire.ChangeSetPathRecord{Path: f.path, Op: "modify", NewSHA256: sum(f.body), NewLen: int64(len(f.body)),
			NewRef: wire.ContentRef(sum(f.body)), Outcome: outcomes[i]}
		a.content.Store(p.NewRef, f.body, nil)
		if f.base != "" {
			p.BaseSHA256 = sum(f.base)
			p.BaseRef = ptr(wire.ContentRef(sum(f.base)))
			a.content.Store(*p.BaseRef, f.base, nil)
		} else {
			p.Op = "create"
		}
		cs.Paths = append(cs.Paths, p)
	}
	return cs
}

func withChangeSet(a *App, turn string, cs *wire.ChangeSetRecord) {
	ti := a.state.Turns[turn]
	ti.ChangeSet = cs
	a.state.Turns[turn] = ti
}

func goTab(t *testing.T, a *App, want tab) {
	t.Helper()
	for i := 0; i < 6 && a.tab != want; i++ {
		a.Update(tea.KeyMsg{Type: tea.KeyTab})
	}
	if a.tab != want {
		t.Fatalf("could not reach tab %d", want)
	}
}

func TestChangesTabOnlyExistsWithAChangeSet(t *testing.T) {
	asciiProfile(t)
	a := chatApp(t)
	addTurn(a, "t1", 1, "approved", "q", "answer")
	approveBy(a, "t1", "rev")
	if bar := a.tabBar(); strings.Contains(bar, "changes") {
		t.Fatalf("a turn without a change set shows the changes tab: %q", bar)
	}
	for i := 0; i < 3; i++ {
		a.Update(tea.KeyMsg{Type: tea.KeyTab})
	}
	if a.tab != tabChat {
		t.Fatalf("tab cycled to %d, want a wrap to chat after 3 tabs", a.tab)
	}

	withChangeSet(a, "t1", changeSetFixture(a, "appliable", "pending", "pending", "pending"))
	if bar := a.tabBar(); !strings.Contains(bar, "changes") {
		t.Fatalf("tab bar lacks changes: %q", bar)
	}
	goTab(t, a, tabChanges)
	if v := a.View(); !strings.Contains(strings.Split(v, "\n")[0], "[changes]") {
		t.Errorf("tab bar does not mark changes active:\n%s", v)
	}
}

func TestChangesTabShowsExactDiffAndState(t *testing.T) {
	asciiProfile(t)
	a := chatApp(t)
	a.height = 40
	addTurn(a, "t1", 1, "approved", "q", "answer")
	approveBy(a, "t1", "rev")
	withChangeSet(a, "t1", changeSetFixture(a, "appliable", "pending", "pending", "pending"))
	goTab(t, a, tabChanges)
	v := a.View()
	for _, want := range []string{
		"changes of turn t1",
		"state: appliable · 0 of 3 paths applied",
		"base check: checking the workspace",
		"a.txt (modify): pending",
		"--- a/a.txt", "+++ b/a.txt", "@@ -1,3 +1,3 @@", "\n one", "\n-two", "\n+TWO", "\n three",
		"b.txt (create): pending", "--- /dev/null", "@@ -0,0 +1 @@", "\n+brand new",
	} {
		if !strings.Contains(v, want) {
			t.Errorf("view lacks %q:\n%s", want, v)
		}
	}
}

func TestChangesTabDiffNotLoadedYet(t *testing.T) {
	asciiProfile(t)
	a := chatApp(t)
	addTurn(a, "t1", 1, "approved", "q", "answer")
	cs := changeSetFixture(a, "appliable", "pending", "pending", "pending")
	a.content = NewContentCache()
	withChangeSet(a, "t1", cs)
	goTab(t, a, tabChanges)
	if v := a.View(); !strings.Contains(v, "(diff loading…)") {
		t.Errorf("an unloaded diff is not marked:\n%s", v)
	}
	a.content.Store(cs.Paths[0].NewRef, "", fmt.Errorf("gone"))
	a.content.Store(*cs.Paths[0].BaseRef, "x", nil)
	if v := a.View(); !strings.Contains(v, "(diff unavailable: gone)") {
		t.Errorf("a failed diff fetch is not shown:\n%s", v)
	}
}

// A change set discarded after a partial apply still shows which files were
// changed: applied and conflict stay, a pending path reads "not applied".
func TestChangesTabPartialDiscardShowsPathEffects(t *testing.T) {
	asciiProfile(t)
	a := chatApp(t)
	a.height = 40
	addTurn(a, "t1", 1, "approved", "q", "answer")
	withChangeSet(a, "t1", changeSetFixture(a, "discarded", "applied", "pending", "conflict"))
	goTab(t, a, tabChanges)
	v := a.View()
	for _, want := range []string{
		"state: discarded · 1 of 3 paths applied",
		"a.txt (modify): applied",
		"b.txt (create): not applied",
		"c.txt (modify): conflict",
	} {
		if !strings.Contains(v, want) {
			t.Errorf("view lacks %q:\n%s", want, v)
		}
	}
	if strings.Contains(v, "pending") {
		t.Errorf("a discarded change set shows a pending path:\n%s", v)
	}

	// Before the discard the same pending path is pending.
	withChangeSet(a, "t1", changeSetFixture(a, "partial", "applied", "pending", "conflict"))
	if v := a.View(); !strings.Contains(v, "b.txt (create): pending") {
		t.Errorf("a pending path of a partial change set is not pending:\n%s", v)
	}
}

func TestChangesTabOverrideAndBannersStay(t *testing.T) {
	asciiProfile(t)
	a := chatApp(t)
	a.height = 40
	addTurn(a, "t1", 1, "overridden", "q", "answer")
	a.overrideReasons["t1"] = "good enough"
	withChangeSet(a, "t1", changeSetFixture(a, "applied", "applied", "applied", "applied"))
	a.state.Status.RosterFileChanged = true
	goTab(t, a, tabChanges)
	v := a.View()
	for _, want := range []string{"applied under human override", "roster file changed since start"} {
		if !strings.Contains(v, want) {
			t.Errorf("changes view lacks %q:\n%s", want, v)
		}
	}
	a.tab = tabChat
	if v := a.View(); !strings.Contains(v, "HUMAN OVERRIDE: good enough") || !strings.Contains(v, "roster file changed") {
		t.Errorf("the chat lost its override line or banner:\n%s", v)
	}
}

func TestChangesTabBaseCheckAgainstTheWorkspace(t *testing.T) {
	asciiProfile(t)
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("a.txt", "one\ntwo\nthree\n")
	write("c.txt", "keep\n")
	a := chatApp(t)
	a.workDir = dir
	a.height = 40
	addTurn(a, "t1", 1, "approved", "q", "answer")
	withChangeSet(a, "t1", changeSetFixture(a, "appliable", "pending", "pending", "pending"))

	check := func() string {
		a.Update(a.checkBase("t1")())
		goTab(t, a, tabChanges)
		for _, l := range strings.Split(a.View(), "\n") {
			if strings.HasPrefix(l, "base check:") {
				return l
			}
		}
		t.Fatalf("no base check line:\n%s", a.View())
		return ""
	}
	if got := check(); got != "base check: every path is at its base" {
		t.Errorf("clean workspace: %q", got)
	}
	write("c.txt", "edited meanwhile\n")
	if got := check(); got != "base check: base changed: c.txt" {
		t.Errorf("edited path: %q", got)
	}
	write("c.txt", "changed\n")
	write("b.txt", "brand new\n")
	if got := check(); got != "base check: 1 at base, 2 at new content" {
		t.Errorf("applied paths: %q", got)
	}
	write("b.txt", "someone else's file\n")
	if got := check(); got != "base check: base changed: b.txt" {
		t.Errorf("created path that now exists: %q", got)
	}
	// A path that leaves the workspace is never read.
	cs := changeSetFixture(a, "appliable", "pending", "pending", "pending")
	cs.Paths[0].Path = "../outside"
	if bc := classifyPaths(dir, *cs); bc.paths["../outside"] != "conflict" {
		t.Errorf("escaping path classified %q", bc.paths["../outside"])
	}
	if bc := classifyPaths(filepath.Join(dir, "missing"), *cs); bc.err == "" {
		t.Error("missing workspace not reported")
	}
}

func TestChangesTabFollowsTheChangeSetAsItIsApplied(t *testing.T) {
	asciiProfile(t)
	a := chatApp(t)
	a.height = 40
	addTurn(a, "t1", 1, "approved", "q", "answer")
	withChangeSet(a, "t1", changeSetFixture(a, "appliable", "pending", "pending", "pending"))
	goTab(t, a, tabChanges)

	for _, kind := range []string{wire.EventKindApplyStarted, wire.EventKindApplyFile, wire.EventKindApplyFinished, wire.EventKindChangeSetDiscarded} {
		if !changeSetEvent(kind) {
			t.Errorf("%s is not a change-set event", kind)
		}
	}
	if changeSetEvent(wire.EventKindToolResult) {
		t.Error("tool_result is a change-set event")
	}
	if id := changeSetTurn(a.state, json.RawMessage(`{"change_set_id":"cs1"}`)); id != "t1" {
		t.Errorf("change_set_id cs1 names turn %q", id)
	}

	// The refreshed record replaces what the tab shows.
	reply := &wire.TurnReply{State: "approved", Mode: "review", ChangeSet: changeSetFixture(a, "partial", "applied", "pending", "pending")}
	a.Update(turnMsg{gen: a.gen, id: "t1", turn: reply})
	if v := a.View(); !strings.Contains(v, "state: partial · 1 of 3 paths applied") || !strings.Contains(v, "a.txt (modify): applied") {
		t.Errorf("the tab did not follow the record:\n%s", v)
	}
	reply = &wire.TurnReply{State: "approved", Mode: "review", ChangeSet: changeSetFixture(a, "discarded", "applied", "pending", "pending")}
	a.Update(turnMsg{gen: a.gen, id: "t1", turn: reply})
	if v := a.View(); !strings.Contains(v, "state: discarded · 1 of 3 paths applied") || !strings.Contains(v, "b.txt (create): not applied") {
		t.Errorf("the tab did not show the discard:\n%s", v)
	}
}

func TestChangesTabLeavesWhenTheTurnOnScreenHasNone(t *testing.T) {
	asciiProfile(t)
	a := chatApp(t)
	addTurn(a, "t1", 1, "approved", "q", "answer")
	withChangeSet(a, "t1", changeSetFixture(a, "appliable", "pending", "pending", "pending"))
	goTab(t, a, tabChanges)

	// A newer turn without one takes the screen.
	addTurn(a, "t2", 2, "approved", "q2", "answer2")
	a.state.CurrentTurn = "t2"
	a.Update(tickMsg{})
	if a.tab != tabChat {
		t.Fatalf("tab = %d after the change set went away, want chat", a.tab)
	}
	v := a.View()
	if !strings.Contains(strings.Split(v, "\n")[0], "[chat]") || strings.Contains(v, "changes") {
		t.Errorf("tab bar after the change set went away:\n%s", v)
	}
	a.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	if a.tab != tabParticipants {
		t.Errorf("shift+tab from chat = %d, want participants", a.tab)
	}
}

func TestChangesTabScrollsLongContent(t *testing.T) {
	asciiProfile(t)
	a := chatApp(t)
	addTurn(a, "t1", 1, "approved", "q", "answer")
	cs := changeSetFixture(a, "appliable", "pending", "pending", "pending")
	withChangeSet(a, "t1", cs)
	goTab(t, a, tabChanges)
	if v := a.View(); strings.Contains(v, "c.txt (modify)") {
		t.Fatalf("the last path fits in 24 rows; the test needs content that does not:\n%s", v)
	}
	a.Update(tea.KeyMsg{Type: tea.KeyEnd})
	v := a.View()
	if !strings.Contains(v, "c.txt (modify)") || !strings.Contains(v, "100%") {
		t.Errorf("end did not reach the last path:\n%s", v)
	}
	if strings.Contains(v, "changes of turn") {
		t.Errorf("the head is still shown after end:\n%s", v)
	}
	a.Update(tea.KeyMsg{Type: tea.KeyHome})
	if v := a.View(); !strings.Contains(v, "changes of turn t1") {
		t.Errorf("home did not return to the head:\n%s", v)
	}
}

func TestUnifiedDiffLines(t *testing.T) {
	old := "1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n11\n12\n13\n14\n15\n"
	new := strings.Replace(strings.Replace(old, "2\n", "two\n", 1), "14\n", "14\n14b\n", 1)
	got := strings.Join(unifiedDiffLines("a/f", "b/f", old, new), "\n")
	want := strings.Join([]string{
		"--- a/f", "+++ b/f",
		"@@ -1,5 +1,5 @@", " 1", "-2", "+two", " 3", " 4", " 5",
		"@@ -12,4 +12,5 @@", " 12", " 13", " 14", "+14b", " 15",
	}, "\n")
	if got != want {
		t.Errorf("diff:\n%s\nwant:\n%s", got, want)
	}
	if got := unifiedDiffLines("a/f", "b/f", "same\n", "same\n"); len(got) != 2 {
		t.Errorf("identical files diff = %v", got)
	}
	if got := strings.Join(unifiedDiffLines("/dev/null", "b/f", "", "x\ny\n"), "\n"); got != "--- /dev/null\n+++ b/f\n@@ -0,0 +1,2 @@\n+x\n+y" {
		t.Errorf("created file diff:\n%s", got)
	}
}

func TestChangesTabBinaryContentIsNotShown(t *testing.T) {
	asciiProfile(t)
	a := chatApp(t)
	addTurn(a, "t1", 1, "approved", "q", "answer")
	cs := changeSetFixture(a, "appliable", "pending", "pending", "pending")
	a.content.Store(cs.Paths[1].NewRef, "bin\x00ary", nil)
	withChangeSet(a, "t1", cs)
	goTab(t, a, tabChanges)
	a.Update(tea.KeyMsg{Type: tea.KeyEnd})
	if v := a.View(); !strings.Contains(v, "binary content, not shown") || strings.Contains(v, "\x00") {
		t.Errorf("binary path:\n%s", v)
	}
}

// The journal's own field names (internal/journal/tools.go): a call is
// "call_id", a result "result_id" and "call_id", the evidence "result_ids".
func toolFrames(t *testing.T) []wire.Frame {
	t.Helper()
	f := func(seq int64, kind string, body any) wire.Frame { return chatFrame(t, seq, kind, body) }
	return []wire.Frame{
		f(10, wire.EventKindToolCallAccepted, map[string]any{"call_id": "c1", "attempt_id": "att-author", "participant": "auth", "call_index": 0, "provider_call_id": "p1", "name": "read_file", "class": "read", "args_sha256": sum(`{"path":"main.go"}`)}),
		f(11, wire.EventKindToolResult, map[string]any{"result_id": "r1", "call_id": "c1", "status": "ok", "journal_seq": 11, "sha256": sum("x"), "byte_len": 1}),
		f(12, wire.EventKindToolCallAccepted, map[string]any{"call_id": "c2", "attempt_id": "att-r1", "participant": "rev-a", "name": "web_fetch", "class": "web", "args_sha256": sum(`{"url":"https://example.test/doc"}`)}),
		f(13, wire.EventKindWebRefereeDecision, map[string]any{"call_id": "c2", "attempt_id": "att-ref", "verdict": "deny", "reason_sha256": sum("no")}),
		f(14, wire.EventKindToolResult, map[string]any{"result_id": "r2", "call_id": "c2", "status": "error", "journal_seq": 14, "error_code": "referee_denied"}),
		f(15, wire.EventKindToolCallAccepted, map[string]any{"call_id": "c3", "attempt_id": "att-r2", "participant": "rev-b", "name": "search_files", "class": "read", "args_sha256": sum(`{"pattern":"TODO"}`)}),
		f(16, wire.EventKindToolResult, map[string]any{"result_id": "r3", "call_id": "c3", "status": "ok", "journal_seq": 16}),
		f(17, wire.EventKindToolCallAccepted, map[string]any{"call_id": "c4", "attempt_id": "att-r2", "participant": "rev-b", "name": "read_file", "class": "read", "args_sha256": sum(`{"path":"x.go"}`)}),
		f(18, wire.EventKindToolResult, map[string]any{"result_id": "r4", "call_id": "c4", "status": "ok", "journal_seq": 18}),
		f(19, wire.EventKindVerdictEvidence, map[string]any{"verdict_id": "v-r1", "attempt_id": "att-r1v", "participant": "rev-a", "snapshot_version": 1, "result_ids": []string{"r2"}, "observation_ids": []string{}}),
		f(20, wire.EventKindVerdictEvidence, map[string]any{"verdict_id": "v-r2", "attempt_id": "att-r2v", "participant": "rev-b", "snapshot_version": 1, "result_ids": []string{"r3", "r4"}, "observation_ids": []string{}}),
	}
}

var toolArgs = map[string]string{
	sum(`{"path":"main.go"}`):                 `{"path":"main.go"}`,
	sum(`{"url":"https://example.test/doc"}`): `{"url":"https://example.test/doc"}`,
	sum(`{"pattern":"TODO"}`):                 `{"pattern":"TODO"}`,
	sum(`{"path":"x.go"}`):                    `{"path":"x.go"}`,
}

// toolApp is a running review turn with an author and two reviewers.
func toolApp(t *testing.T, state string) *App {
	t.Helper()
	a := chatApp(t)
	a.height = 40
	addTurn(a, "t1", 1, state, "q", "")
	ti := a.state.Turns["t1"]
	ti.Author, ti.Reviewers = "auth", []string{"rev-a", "rev-b"}
	ti.ReviewerStates = map[string]string{"rev-a": "running", "rev-b": "running"}
	a.state.Turns["t1"] = ti
	a.state.CurrentTurn = "t1"
	for id, who := range map[string]string{"att-author": "auth", "att-r1": "rev-a", "att-r1v": "rev-a", "att-ref": "rev-a", "att-r2": "rev-b", "att-r2v": "rev-b"} {
		phase := "review"
		if who == "auth" {
			phase = "author"
		}
		a.state.Attempts[id] = AttemptInfo{ID: id, TurnID: "t1", Participant: who, Phase: phase, Status: "complete"}
	}
	return a
}

func feed(a *App, frames ...wire.Frame) {
	for _, f := range frames {
		a.Update(eventMsg{gen: a.gen, frame: f})
	}
}

func loadArgs(a *App) {
	for sha, body := range toolArgs {
		a.Update(contentMsg{ref: wire.ContentRef(sha), data: body})
	}
}

func TestToolProgressLinesNameRoleToolTargetAndStatus(t *testing.T) {
	asciiProfile(t)
	a := toolApp(t, "reviewing")
	feed(a, toolFrames(t)...)
	loadArgs(a)
	v := a.View()
	for _, want := range []string{
		"author read_file main.go ok",
		"reviewer 1 web_fetch https://example.test/doc error · referee: deny",
		"reviewer 2 search_files TODO ok",
		"reviewer 2 read_file x.go ok",
	} {
		if !strings.Contains(v, want) {
			t.Errorf("progress lacks %q:\n%s", want, v)
		}
	}
	if strings.Contains(v, "reviewer 1 web_fetch https://example.test/doc ok") || strings.Count(v, "referee: deny") != 1 {
		t.Errorf("referee denial misplaced:\n%s", v)
	}
}

func TestToolProgressShowsACallWithoutResultAsRunning(t *testing.T) {
	asciiProfile(t)
	a := toolApp(t, "reviewing")
	feed(a, toolFrames(t)[0])
	if v := a.View(); !strings.Contains(v, "author read_file running") {
		t.Errorf("a call without a result is not running:\n%s", v)
	}
}

func TestToolProgressCallsAreDedupedAndOrderIndependent(t *testing.T) {
	asciiProfile(t)
	frames := toolFrames(t)
	a := toolApp(t, "reviewing")
	feed(a, frames...)
	feed(a, frames...) // a replay of the same events
	b := toolApp(t, "reviewing")
	// Results and decisions can arrive before their calls.
	for i := len(frames) - 1; i >= 0; i-- {
		feed(b, frames[i])
	}
	loadArgs(a)
	loadArgs(b)
	if len(a.state.ToolCalls) != 4 {
		t.Fatalf("calls = %d, want 4", len(a.state.ToolCalls))
	}
	va, vb := a.View(), b.View()
	if va != vb {
		t.Errorf("order changed the view:\n%s\n---\n%s", va, vb)
	}
}

// A reattached TUI rebuilds progress from the stored evidence and runs
// nothing: the same events through an evidence page give the live view.
func TestEvidencePageRebuildsProgressLikeTheLiveStream(t *testing.T) {
	asciiProfile(t)
	live := toolApp(t, "reviewing")
	feed(live, toolFrames(t)...)
	loadArgs(live)

	var items []wire.EvidenceItem
	for _, f := range toolFrames(t) {
		items = append(items, wire.EvidenceItem{Seq: f.Seq, Kind: f.Kind, Data: f.Body})
	}
	re := toolApp(t, "reviewing")
	// The attempts carry no turn yet: only the evidence page names the turn.
	for id, ai := range re.state.Attempts {
		ai.TurnID = ""
		re.state.Attempts[id] = ai
	}
	re.Update(evidenceMsg{gen: re.gen, turn: "t1", items: items})
	for id, ai := range live.state.Attempts {
		re.state.Attempts[id] = ai
	}
	loadArgs(re)
	if got, want := re.View(), live.View(); got != want {
		t.Errorf("rebuilt view differs from the live one:\n%s\n---\n%s", got, want)
	}
	for _, c := range re.state.ToolCalls {
		if c.TurnID != "t1" {
			t.Errorf("call %s belongs to turn %q", c.ID, c.TurnID)
		}
	}
	// A stale generation is dropped.
	before := len(re.state.ToolCalls)
	re.Update(evidenceMsg{gen: re.gen + 1, turn: "t1", items: []wire.EvidenceItem{{Seq: 99, Kind: wire.EventKindToolCallAccepted, Data: json.RawMessage(`{"call_id":"zz","attempt_id":"att-author"}`)}}})
	if len(re.state.ToolCalls) != before {
		t.Error("a stale evidence page was applied")
	}
}

func TestVerdictEvidenceSurvivesTheTerminalRecord(t *testing.T) {
	a := toolApp(t, "reviewing")
	feed(a, toolFrames(t)...)
	feed(a, chatFrame(t, 21, wire.EventKindVerdictRecorded, wire.EvVerdictRecorded{VerdictID: "v-r2", AttemptID: "att-r2v", Parsed: true, ValidationStatus: "valid"}))
	v := a.state.Verdicts["v-r2"]
	if v.Participant != "rev-b" || len(v.ResultIDs) != 2 || !v.Parsed {
		t.Fatalf("verdict after verdict_recorded = %+v", v)
	}
	// The terminal record lists the same ids; one without them wipes nothing.
	reply := &wire.TurnReply{State: "approved", Mode: "review", Verdicts: []wire.VerdictRecord{
		{ID: "v-r2", AttemptID: "att-r2v", Parsed: true, ValidationStatus: "valid", ResultIDs: []string{"r3", "r4"}},
		{ID: "v-r1", AttemptID: "att-r1v", Parsed: true, ValidationStatus: "valid"},
	}}
	a.state = MergeTurn(a.state, "t1", reply, chatNow)
	if got := a.state.Verdicts["v-r2"].ResultIDs; len(got) != 2 {
		t.Errorf("result ids after the record = %v", got)
	}
	if got := a.state.Verdicts["v-r1"].ResultIDs; len(got) != 1 {
		t.Errorf("a record without result ids wiped the event's: %v", got)
	}
	// With nothing seen live the record supplies them.
	b := toolApp(t, "approved")
	b.state = MergeTurn(b.state, "t1", reply, chatNow)
	if got := b.state.Verdicts["v-r2"].ResultIDs; len(got) != 2 {
		t.Errorf("result ids from the record alone = %v", got)
	}
}

func TestApprovalLineIsFollowedByTheLookupCount(t *testing.T) {
	asciiProfile(t)
	a := toolApp(t, "approved")
	addTurn(a, "t1", 1, "approved", "q", "answer")
	ti := a.state.Turns["t1"]
	ti.Author, ti.Reviewers = "auth", []string{"rev-a", "rev-b"}
	a.state.Turns["t1"] = ti
	approveBy(a, "t1", "rev-a")
	feed(a, toolFrames(t)...)
	lines := chatView(a)
	i := lineOf(t, lines, "approved by")
	if i+1 >= len(lines) || !strings.Contains(lines[i+1], "checked with 3 lookups") {
		t.Errorf("approval line is not followed by the lookup count:\n%s", strings.Join(lines, "\n"))
	}
	if !strings.HasPrefix(lines[i+1], contentPrefix) {
		t.Errorf("the lookup line is not a content line: %q", lines[i+1])
	}

	// No lookups, no line.
	b := chatApp(t)
	addTurn(b, "t1", 1, "approved", "q", "answer")
	approveBy(b, "t1", "rev")
	if v := b.View(); strings.Contains(v, "lookups") {
		t.Errorf("a turn without lookups shows a count:\n%s", v)
	}
}

func TestTranscriptShowsReviewerToolsUnderTheirOwnVerdict(t *testing.T) {
	asciiProfile(t)
	a := toolApp(t, "approved")
	feed(a, toolFrames(t)...)
	loadArgs(a)
	items := []wire.TranscriptItem{
		{Ref: "attempt:att-author"},
		{Ref: "attempt:att-r1"},
		{Ref: "attempt:att-r1v"},
		{Ref: "attempt:att-r2"},
		{Ref: "attempt:att-r2v"},
	}
	a.Update(transcriptMsg{gen: a.gen, turn: "t1", items: items})
	a.tTurn = "t1"
	a.Update(transcriptMsg{gen: a.gen, turn: "t1", items: items})
	goTab(t, a, tabTranscript)
	lines := transcriptLines(a.state, "t1", a.transcriptIn(), 100)
	text := strings.Join(lines, "\n")

	// Find the block of lines of each reviewer's attempts: the calls sit in the
	// attempt that made them, the lookup count in the verdict's.
	idx := func(sub string) int {
		for i, l := range lines {
			if strings.Contains(l, sub) {
				return i
			}
		}
		t.Fatalf("no line with %q:\n%s", sub, text)
		return -1
	}
	if call := idx("reviewer 1 web_fetch"); call <= idx("rev-a") || call >= idx("reviewer 2 search_files") {
		t.Errorf("reviewer 1's call is not under reviewer 1:\n%s", text)
	}
	if strings.Count(text, "lookups before this verdict: 1") != 1 || strings.Count(text, "lookups before this verdict: 2") != 1 {
		t.Errorf("each verdict should show its own count (1 and 2):\n%s", text)
	}
	if strings.Count(text, "reviewer 1 ") != 1 || strings.Count(text, "reviewer 2 ") != 2 {
		t.Errorf("calls shown under the wrong reviewer:\n%s", text)
	}
	if !strings.Contains(text, "author read_file main.go ok") {
		t.Errorf("the author's call is missing:\n%s", text)
	}
}

func TestEvidenceReducerIgnoresMalformedBodies(t *testing.T) {
	s := NewState()
	for _, kind := range []string{wire.EventKindToolCallAccepted, wire.EventKindToolResult, wire.EventKindWebRefereeDecision, wire.EventKindVerdictEvidence} {
		s = applyEvidence(s, "t1", 1, kind, json.RawMessage(`{"id":"x"}`))
		s = applyEvidence(s, "t1", 1, kind, json.RawMessage(`not json`))
	}
	if len(s.ToolCalls) != 0 || len(s.Verdicts) != 0 {
		t.Errorf("malformed bodies changed the state: %+v", s)
	}
	if got := applyEvidence(State{}, "t1", 1, wire.EventKindToolCallAccepted, json.RawMessage(`{"call_id":"c","attempt_id":"a"}`)); got.ToolCalls["c"].TurnID != "t1" {
		t.Error("a zero State cannot take a call")
	}
}

func TestUnifiedDiffEOFNewlineOnlyChange(t *testing.T) {
	got := strings.Join(unifiedDiffLines("a/f", "b/f", "x\n", "x"), "\n")
	want := "--- a/f\n+++ b/f\n@@ -1 +1 @@\n-x\n+x\n\\ No newline at end of file"
	if got != want {
		t.Errorf("newline removed:\n%s", got)
	}
	got = strings.Join(unifiedDiffLines("a/f", "b/f", "x", "x\n"), "\n")
	want = "--- a/f\n+++ b/f\n@@ -1 +1 @@\n-x\n\\ No newline at end of file\n+x"
	if got != want {
		t.Errorf("newline added:\n%s", got)
	}
	got = strings.Join(unifiedDiffLines("a/f", "b/f", "a\nb", "a\nc"), "\n")
	want = "--- a/f\n+++ b/f\n@@ -1,2 +1,2 @@\n a\n-b\n\\ No newline at end of file\n+c\n\\ No newline at end of file"
	if got != want {
		t.Errorf("both without newline:\n%s", got)
	}
}

func TestChangesTabBaseCheckRejectsSymlinks(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "real.txt"), []byte("one\ntwo\nthree\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "d"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "d", "f.txt"), []byte("one\ntwo\nthree\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if os.Symlink("real.txt", filepath.Join(dir, "a.txt")) != nil || os.Symlink("d", filepath.Join(dir, "link")) != nil {
		t.Skip("symlinks unavailable")
	}
	a := chatApp(t)
	cs := changeSetFixture(a, "appliable", "pending", "pending", "pending")
	cs.Paths[0].Path = "a.txt"
	cs.Paths[1].Path = "link/f.txt"
	cs.Paths[2].Path = "real.txt"
	base := sum("one\ntwo\nthree\n")
	for i := range cs.Paths {
		cs.Paths[i].BaseSHA256 = base
	}
	bc := classifyPaths(dir, *cs)
	if bc.paths["a.txt"] != "conflict" || bc.paths["link/f.txt"] != "conflict" || bc.paths["real.txt"] != "pending" {
		t.Errorf("symlink classification: %v", bc.paths)
	}
}

func TestRefereeDecisionEventUsesTheJournalsDecisionField(t *testing.T) {
	s := applyEvidence(State{}, "t1", 1, wire.EventKindToolCallAccepted, json.RawMessage(`{"call_id":"c","attempt_id":"a","participant":"p","name":"bash","class":"exec"}`))
	s = applyEvidence(s, "t1", 2, wire.EventKindRefereeDecision, json.RawMessage(`{"decision_id":"d","call_id":"c","decision":"deny","reason":"x","cause":"y"}`))
	if got := s.ToolCalls["c"].Referee; got != "deny" {
		t.Errorf("referee = %q", got)
	}
}
