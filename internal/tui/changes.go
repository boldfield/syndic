package tui

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/boldfield/syndic/internal/client"
	"github.com/boldfield/syndic/internal/wire"
	tea "github.com/charmbracelet/bubbletea"
)

// ToolCallInfo is one tool call of a turn: who made it, what it was, and how
// it ended. The TUI only reads these records; nothing here runs a tool.
type ToolCallInfo struct {
	ID          string
	TurnID      string
	AttemptID   string
	Participant string
	Name        string
	Class       string
	ArgsSHA256  string
	// Target is the path, url, query or pattern the call named, once its
	// arguments are loaded.
	Target string
	// Status is the result's status; empty while the call has no result.
	Status string
	// Referee is the referee's verdict on the call, "allow" or "deny", or empty.
	Referee  string
	ResultID string
	// ByteLen and ErrorCode describe the result: its size, or why it failed.
	ByteLen   int64
	ErrorCode string
	Seq       int64
}

// The payloads of the journaled tool events, with the field names the journal
// writes (internal/journal/tools.go).
type toolCallBody struct {
	CallID      string `json:"call_id"`
	AttemptID   string `json:"attempt_id"`
	Participant string `json:"participant"`
	Name        string `json:"name"`
	Class       string `json:"class"`
	ArgsSHA256  string `json:"args_sha256"`
}

type toolResultBody struct {
	ResultID  string `json:"result_id"`
	CallID    string `json:"call_id"`
	Status    string `json:"status"`
	ByteLen   int64  `json:"byte_len"`
	ErrorCode string `json:"error_code"`
}

type refereeBody struct {
	CallID    string `json:"call_id"`
	AttemptID string `json:"attempt_id"`
	Verdict   string `json:"verdict"`  // web_referee_decision
	Decision  string `json:"decision"` // referee_decision
}

type verdictEvidenceBody struct {
	VerdictID   string   `json:"verdict_id"`
	AttemptID   string   `json:"attempt_id"`
	Participant string   `json:"participant"`
	ResultIDs   []string `json:"result_ids"`
}

// applyEvidence folds one tool, referee or verdict-evidence event into the
// state. Live frames and replayed evidence pages take this one path, keyed by
// call, result and verdict ids, so seeing an event twice changes nothing. turnID
// names the turn when the caller knows it; tool events do not carry it.
func applyEvidence(s State, turnID string, seq int64, kind string, body json.RawMessage) State {
	calls := copyMap(s.ToolCalls)
	results := copyMap(s.ResultCall)
	turnOf := func(attemptID string) string {
		if t := s.Attempts[attemptID].TurnID; t != "" {
			return t
		}
		if turnID != "" {
			return turnID
		}
		return s.CurrentTurn
	}
	switch kind {
	case wire.EventKindToolCallAccepted:
		var b toolCallBody
		if json.Unmarshal(body, &b) != nil || b.CallID == "" {
			return s
		}
		c := calls[b.CallID]
		c.ID, c.AttemptID, c.Participant = b.CallID, b.AttemptID, b.Participant
		c.Name, c.Class, c.ArgsSHA256 = b.Name, b.Class, b.ArgsSHA256
		c.TurnID = turnOf(b.AttemptID)
		if c.Seq == 0 || seq < c.Seq {
			c.Seq = seq
		}
		calls[b.CallID] = c
	case wire.EventKindToolResult:
		var b toolResultBody
		if json.Unmarshal(body, &b) != nil || b.CallID == "" {
			return s
		}
		c := calls[b.CallID]
		c.ID, c.Status, c.ResultID = b.CallID, b.Status, b.ResultID
		c.ByteLen, c.ErrorCode = b.ByteLen, b.ErrorCode
		if c.TurnID == "" {
			c.TurnID = turnOf(c.AttemptID)
		}
		calls[b.CallID] = c
		if b.ResultID != "" {
			results[b.ResultID] = b.CallID
		}
	case wire.EventKindWebRefereeDecision, wire.EventKindRefereeDecision:
		var b refereeBody
		if json.Unmarshal(body, &b) != nil || b.CallID == "" {
			return s
		}
		c := calls[b.CallID]
		c.ID, c.Referee = b.CallID, b.Verdict
		if b.Verdict == "" {
			c.Referee = b.Decision
		}
		if c.AttemptID == "" {
			c.AttemptID = b.AttemptID
		}
		if c.TurnID == "" {
			c.TurnID = turnOf(c.AttemptID)
		}
		calls[b.CallID] = c
	case wire.EventKindVerdictEvidence:
		var b verdictEvidenceBody
		if json.Unmarshal(body, &b) != nil || b.VerdictID == "" {
			return s
		}
		verdicts := copyMap(s.Verdicts)
		v := verdicts[b.VerdictID]
		v.ID, v.Participant = b.VerdictID, b.Participant
		if v.AttemptID == "" {
			v.AttemptID = b.AttemptID
		}
		v.ResultIDs = append([]string(nil), b.ResultIDs...)
		verdicts[b.VerdictID] = v
		s.Verdicts = verdicts
		return s
	default:
		return s
	}
	s.ToolCalls, s.ResultCall = calls, results
	return s
}

// targetOf is the first of the arguments a tool call names its subject by.
func targetOf(args []byte) string {
	var m map[string]json.RawMessage
	if json.Unmarshal(args, &m) != nil {
		return ""
	}
	for _, k := range []string{"path", "url", "query", "pattern"} {
		var v string
		if raw, ok := m[k]; ok && json.Unmarshal(raw, &v) == nil && v != "" {
			return v
		}
	}
	return ""
}

// fillTargets sets the target of every call whose arguments are in the cache.
func fillTargets(s State, cc *ContentCache) State {
	if cc == nil {
		return s
	}
	var calls map[string]ToolCallInfo
	for id, c := range s.ToolCalls {
		if c.Target != "" || c.ArgsSHA256 == "" {
			continue
		}
		it := cc.Get(wire.ContentRef(c.ArgsSHA256))
		if !it.Ready || it.Err != nil {
			continue
		}
		if t := targetOf([]byte(it.Data)); t != "" {
			if calls == nil {
				calls = copyMap(s.ToolCalls)
			}
			c.Target = t
			calls[id] = c
		}
	}
	if calls != nil {
		s.ToolCalls = calls
	}
	return s
}

// toolArgRefs are the content refs of the arguments of the turn's tool calls
// that still have no target.
func toolArgRefs(s State, turnID string) []string {
	var refs []string
	for _, c := range sortedCalls(s, turnID) {
		if c.Target == "" && c.ArgsSHA256 != "" {
			refs = append(refs, wire.ContentRef(c.ArgsSHA256))
		}
	}
	return refs
}

// sortedCalls are the turn's tool calls in journal order.
func sortedCalls(s State, turnID string) []ToolCallInfo {
	var out []ToolCallInfo
	for _, c := range s.ToolCalls {
		if c.TurnID == turnID {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Seq != out[j].Seq {
			return out[i].Seq < out[j].Seq
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// roleOf names who made the call: author, or reviewer 1, reviewer 2 in the
// order the turn's reviewers began.
func roleOf(s State, c ToolCallInfo) string {
	ti := s.Turns[c.TurnID]
	who := c.Participant
	if who == "" {
		who = s.Attempts[c.AttemptID].Participant
	}
	phase := s.Attempts[c.AttemptID].Phase
	if phase == "author" || (phase == "" && who != "" && who == ti.Author) {
		return "author"
	}
	if i := slices.Index(ti.Reviewers, who); i >= 0 {
		return fmt.Sprintf("reviewer %d", i+1)
	}
	if who == "" {
		return "tool"
	}
	return who
}

// toolLine is one tool call as one line: role, name, target, status, and the
// referee's refusal when it fired.
func toolLine(s State, c ToolCallInfo) string {
	status := c.Status
	if status == "" {
		status = "running"
	}
	parts := []string{roleOf(s, c), c.Name}
	if c.Target != "" {
		parts = append(parts, c.Target)
	}
	parts = append(parts, status)
	line := strings.Join(parts, " ")
	if c.Referee == "deny" {
		line += " · referee: deny"
	}
	return safeLine(line)
}

// toolProgressLines are the lines the chat's progress line is followed by
// while the turn runs: one per tool call.
func toolProgressLines(s State, turnID string) []string {
	var lines []string
	for _, c := range sortedCalls(s, turnID) {
		lines = append(lines, contentPrefix+toolLine(s, c))
	}
	return lines
}

// verdictCalls are the calls whose results a verdict received, in the order
// the verdict lists them.
func verdictCalls(s State, v VerdictInfo) []ToolCallInfo {
	var out []ToolCallInfo
	for _, rid := range v.ResultIDs {
		if c, ok := s.ToolCalls[s.ResultCall[rid]]; ok {
			out = append(out, c)
		}
	}
	return out
}

// resultLine is what a call's result was: its status, and its size or error.
func resultLine(c ToolCallInfo) string {
	line := "result " + c.Status
	switch {
	case c.ErrorCode != "":
		line += " " + c.ErrorCode
	case c.ByteLen > 0:
		line += fmt.Sprintf(" %d bytes", c.ByteLen)
	}
	if c.ResultID != "" {
		line += " (" + c.ResultID + ")"
	}
	return safeLine(line)
}

// attemptToolLines are the lines the transcript shows under an attempt. A
// reviewer's calls and their results sit under the verdict that received them;
// any other call sits under the attempt that made it.
func attemptToolLines(s State, turnID, attemptID string) []string {
	var lines []string
	reported := map[string]bool{}
	for _, v := range s.Verdicts {
		for _, c := range verdictCalls(s, v) {
			reported[c.ID] = true
		}
	}
	for _, c := range sortedCalls(s, turnID) {
		if c.AttemptID == attemptID && !reported[c.ID] {
			lines = append(lines, contentPrefix+toolLine(s, c))
		}
	}
	for _, v := range s.Verdicts {
		if v.AttemptID != attemptID || len(v.ResultIDs) == 0 {
			continue
		}
		lines = append(lines, contentPrefix+fmt.Sprintf("lookups before this verdict: %d", len(v.ResultIDs)))
		calls := verdictCalls(s, v)
		sort.SliceStable(calls, func(i, j int) bool { return calls[i].Seq < calls[j].Seq })
		for _, c := range calls {
			lines = append(lines, contentPrefix+toolLine(s, c), contentPrefix+"  "+resultLine(c))
		}
	}
	return lines
}

// lookupCount is how many tool results the turn's reviewers received before
// giving their verdicts.
func lookupCount(s State, turnID string) int {
	n := 0
	for _, v := range s.Verdicts {
		if s.Attempts[v.AttemptID].TurnID == turnID {
			n += len(v.ResultIDs)
		}
	}
	return n
}

// evidenceMsg carries every evidence item of a turn, read page by page.
type evidenceMsg struct {
	gen   int
	turn  string
	items []wire.EvidenceItem
	err   error
}

// evidenceCmd reads the turn's stored tool, referee and change-set events. It
// only reads: a reattached TUI rebuilds progress from it and runs nothing.
func evidenceCmd(ctx context.Context, gen int, cl *client.Client, turnID string) tea.Cmd {
	return func() tea.Msg {
		var items []wire.EvidenceItem
		cursor := ""
		for {
			reply, err := cl.Evidence(ctx, turnID, cursor, transcriptPageSize)
			if err != nil {
				return evidenceMsg{gen: gen, turn: turnID, err: err}
			}
			items = append(items, reply.Items...)
			if reply.NextCursor == nil || *reply.NextCursor == "" {
				return evidenceMsg{gen: gen, turn: turnID, items: items}
			}
			cursor = *reply.NextCursor
		}
	}
}

// foldEvidence applies a page of evidence items.
func foldEvidence(s State, turnID string, items []wire.EvidenceItem) State {
	for _, it := range items {
		s = applyEvidence(s, turnID, it.Seq, it.Kind, it.Data)
	}
	return s
}

// changeSetEvent reports whether the event kind belongs to a change set's
// lifecycle: freezing, applying, recovering or discarding.
func changeSetEvent(kind string) bool {
	switch kind {
	case wire.EventKindChangeSetFrozen, wire.EventKindApplyStarted, wire.EventKindApplyFile,
		wire.EventKindApplyFinished, wire.EventKindApplyRecovered, wire.EventKindChangeSetDiscarded:
		return true
	}
	return false
}

// changeSetTurn is the turn whose change set the event names, or the turn on
// screen when the event is the freeze that creates one.
func changeSetTurn(s State, body json.RawMessage) string {
	var b struct {
		ChangeSetID string `json:"change_set_id"`
	}
	if json.Unmarshal(body, &b) == nil && b.ChangeSetID != "" {
		for id, ti := range s.Turns {
			if ti.ChangeSet != nil && ti.ChangeSet.ID == b.ChangeSetID {
				return id
			}
		}
	}
	return answerTurn(s)
}

// baseCheck is the live classification of a change set's paths against the
// workspace: pending (at its base), done (at its new content) or conflict.
type baseCheck struct {
	changeSet string
	state     string
	paths     map[string]string
	err       string
	done      bool
}

// baseCheckMsg carries a finished base check.
type baseCheckMsg struct {
	turn  string
	check baseCheck
}

// baseCheckCmd classifies every path of the change set from the live
// workspace, as the engine does before an apply. It writes nothing.
func baseCheckCmd(workspace, turnID string, cs wire.ChangeSetRecord) tea.Cmd {
	return func() tea.Msg {
		return baseCheckMsg{turn: turnID, check: classifyPaths(workspace, cs)}
	}
}

func classifyPaths(workspace string, cs wire.ChangeSetRecord) baseCheck {
	bc := baseCheck{changeSet: cs.ID, state: cs.State, paths: map[string]string{}, done: true}
	root, err := os.OpenRoot(workspace)
	if err != nil {
		bc.err = "workspace not readable"
		return bc
	}
	defer func() { _ = root.Close() }()
	for _, p := range cs.Paths {
		bc.paths[p.Path] = classifyPath(root, p)
	}
	return bc
}

func classifyPath(root *os.Root, p wire.ChangeSetPathRecord) string {
	// The engine opens every component with O_NOFOLLOW, so a symlink anywhere
	// on the path, or a non-regular leaf, blocks the apply.
	parts := strings.Split(p.Path, "/")
	for i := range parts {
		st, err := root.Lstat(strings.Join(parts[:i+1], "/"))
		if errors.Is(err, fs.ErrNotExist) {
			break
		}
		if err != nil || st.Mode()&fs.ModeSymlink != 0 || (i < len(parts)-1 && !st.IsDir()) {
			return "conflict"
		}
		// A special file at the leaf blocks an open; the engine calls it a conflict.
		if i == len(parts)-1 && !st.Mode().IsRegular() {
			return "conflict"
		}
	}
	f, err := root.Open(p.Path)
	if errors.Is(err, fs.ErrNotExist) {
		if p.BaseSHA256 == "" || p.BaseSHA256 == "absent" {
			return "pending"
		}
		return "conflict"
	}
	if err != nil {
		return "conflict"
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		return "conflict"
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "conflict"
	}
	sum := hex.EncodeToString(h.Sum(nil))
	switch sum {
	case p.NewSHA256:
		return "done"
	case p.BaseSHA256:
		return "pending"
	}
	return "conflict"
}

// baseCheckLine is the base check's result in one line.
func baseCheckLine(cs wire.ChangeSetRecord, bc *baseCheck) string {
	if bc == nil || !bc.done || bc.changeSet != cs.ID {
		return "base check: checking the workspace…"
	}
	if bc.err != "" {
		return "base check: " + bc.err
	}
	var pending, done, conflict []string
	for _, p := range cs.Paths {
		switch bc.paths[p.Path] {
		case "pending":
			pending = append(pending, p.Path)
		case "done":
			done = append(done, p.Path)
		default:
			conflict = append(conflict, p.Path)
		}
	}
	switch {
	case len(conflict) > 0:
		return "base check: base changed: " + strings.Join(conflict, ", ")
	case len(pending) == 0:
		return "base check: every path is at its new content"
	case len(done) > 0:
		return fmt.Sprintf("base check: %d at base, %d at new content", len(pending), len(done))
	}
	return "base check: every path is at its base"
}

// outcomeText is a path's own outcome as the changes tab shows it. A path that
// was never applied reads "not applied" once the change set is discarded; an
// applied or conflicting one keeps its outcome, since a discard rewrites none.
func outcomeText(csState, outcome string) string {
	if outcome == "pending" && csState == "discarded" {
		return "not applied"
	}
	return outcome
}

// changesInput is what the changes tab needs beyond State.
type changesInput struct {
	content *ContentCache
	check   *baseCheck
}

// changesRefs are the content refs the tab shows diffs from.
func changesRefs(cs *wire.ChangeSetRecord) []string {
	if cs == nil {
		return nil
	}
	var refs []string
	for _, p := range cs.Paths {
		if p.BaseRef != nil {
			refs = append(refs, *p.BaseRef)
		}
		refs = append(refs, p.NewRef)
	}
	return refs
}

// changesLines renders the changes tab for the turn, unclipped: the change
// set's state, the live base check, and for every path its own outcome and its
// staged diff against the stored base.
func changesLines(s State, turnID string, in changesInput) []string {
	ti, ok := s.Turns[turnID]
	if !ok || turnID == "" {
		return []string{"no turn yet"}
	}
	cs := ti.ChangeSet
	if cs == nil {
		return []string{"changes of turn " + turnID, "", "no change set"}
	}
	applied := 0
	for _, p := range cs.Paths {
		if p.Outcome == "applied" {
			applied++
		}
	}
	lines := []string{
		safeLine("changes of turn " + turnID),
		"",
		fmt.Sprintf("state: %s · %d of %d paths applied", cs.State, applied, len(cs.Paths)),
		baseCheckLine(*cs, in.check),
		safeLine("changes " + shortHash(cs.ChangesHash) + " · snapshot " + shortHash(cs.SnapshotHash)),
	}
	if ti.State == "overridden" && (cs.State == "applied" || applied > 0) {
		lines = append(lines, "applied under human override")
	}
	for _, p := range cs.Paths {
		lines = append(lines, "", safeLine(fmt.Sprintf("%s (%s): %s", p.Path, p.Op, outcomeText(cs.State, p.Outcome))))
		lines = append(lines, pathDiffLines(p, in.content)...)
	}
	return lines
}

func shortHash(hash string) string { return hash[:min(12, len(hash))] }

// pathDiffLines is the path's staged change as a unified diff from the stored
// base, or why the diff is not shown yet.
func pathDiffLines(p wire.ChangeSetPathRecord, cc *ContentCache) []string {
	if cc == nil {
		return []string{"(diff not loaded)"}
	}
	var base string
	if p.BaseRef != nil {
		it := cc.Get(*p.BaseRef)
		if !it.Ready {
			return []string{"(diff loading…)"}
		}
		if it.Err != nil {
			return []string{safeLine("(diff unavailable: " + it.Err.Error() + ")")}
		}
		base = it.Data
	}
	it := cc.Get(p.NewRef)
	if !it.Ready {
		return []string{"(diff loading…)"}
	}
	if it.Err != nil {
		return []string{safeLine("(diff unavailable: " + it.Err.Error() + ")")}
	}
	if !utf8.ValidString(base) || !utf8.ValidString(it.Data) || strings.IndexByte(base+it.Data, 0) >= 0 {
		return []string{fmt.Sprintf("binary content, not shown: base %s, new %s, %d bytes", shortHash(p.BaseSHA256), shortHash(p.NewSHA256), p.NewLen)}
	}
	from := "a/" + p.Path
	if p.BaseRef == nil {
		from = "/dev/null"
	}
	var out []string
	for _, l := range unifiedDiffLines(from, "b/"+p.Path, base, it.Data) {
		out = append(out, safeLine(l))
	}
	return out
}

// diffContext is the lines of context around a change, and maxDiffCells bounds
// the work of matching the two sides line by line.
const (
	diffContext  = 3
	maxDiffCells = 4_000_000
)

type diffEdit struct {
	op   byte // ' ', '-', '+'
	text string
	noNL bool // the line ends its file without a newline
}

// unifiedDiffLines is a unified diff of old against new.
func unifiedDiffLines(from, to, old, new string) []string {
	out := []string{"--- " + from, "+++ " + to}
	oldLines, oldNL := diffSplit(old)
	newLines, newNL := diffSplit(new)
	script := editScript(oldLines, newLines)
	script = markEOFChange(script, oldNL, newNL)
	n := len(script)
	// aPos[i] and bPos[i] are the line numbers script[i] has in each file.
	aPos, bPos := make([]int, n+1), make([]int, n+1)
	aPos[0], bPos[0] = 1, 1
	for i, e := range script {
		aPos[i+1], bPos[i+1] = aPos[i], bPos[i]
		if e.op != '+' {
			aPos[i+1]++
		}
		if e.op != '-' {
			bPos[i+1]++
		}
	}
	for i := 0; i < n; {
		if script[i].op == ' ' {
			i++
			continue
		}
		start := max(i-diffContext, 0)
		// The hunk runs over changes and over gaps of context no longer than
		// two contexts, then takes one context after the last change.
		j, end := i, i
		for {
			for j < n && script[j].op != ' ' {
				j++
			}
			k := j
			for k < n && script[k].op == ' ' {
				k++
			}
			if k == n || k-j > 2*diffContext {
				end = min(j+diffContext, n)
				break
			}
			j = k
		}
		out = append(out, fmt.Sprintf("@@ -%s +%s @@",
			hunkRange(aPos[start], aPos[end]-aPos[start]), hunkRange(bPos[start], bPos[end]-bPos[start])))
		for _, e := range script[start:end] {
			out = append(out, string(e.op)+e.text)
			if e.noNL {
				out = append(out, `\ No newline at end of file`)
			}
		}
		i = end
	}
	return out
}

func hunkRange(start, n int) string {
	if n == 1 {
		return fmt.Sprint(start)
	}
	if n == 0 {
		start--
	}
	return fmt.Sprintf("%d,%d", start, n)
}

// diffSplit is s's lines and whether the last one ends in a newline.
func diffSplit(s string) ([]string, bool) {
	if s == "" {
		return nil, true
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n"), strings.HasSuffix(s, "\n")
}

// markEOFChange makes the script carry the newline state of each file's last
// line: a final context line that differs only in its newline becomes a removal
// and an addition, and a last line without a newline is flagged.
func markEOFChange(script []diffEdit, oldNL, newNL bool) []diffEdit {
	if last := len(script) - 1; last >= 0 && script[last].op == ' ' && oldNL != newNL {
		t := script[last].text
		script = append(script[:last:last], diffEdit{'-', t, !oldNL}, diffEdit{'+', t, !newNL})
		return script
	}
	script = append([]diffEdit(nil), script...)
	for i := len(script) - 1; i >= 0; i-- {
		if script[i].op != '+' {
			script[i].noNL = !oldNL
			break
		}
	}
	for i := len(script) - 1; i >= 0; i-- {
		if script[i].op == '+' {
			script[i].noNL = !newNL
			break
		}
		if script[i].op == ' ' {
			break
		}
	}
	return script
}

// editScript is the line edits turning a into b: the common ends trimmed, the
// middle matched by longest common subsequence.
func editScript(a, b []string) []diffEdit {
	pre := 0
	for pre < len(a) && pre < len(b) && a[pre] == b[pre] {
		pre++
	}
	suf := 0
	for suf < len(a)-pre && suf < len(b)-pre && a[len(a)-1-suf] == b[len(b)-1-suf] {
		suf++
	}
	var out []diffEdit
	for _, l := range a[:pre] {
		out = append(out, diffEdit{' ', l, false})
	}
	out = append(out, middleEdits(a[pre:len(a)-suf], b[pre:len(b)-suf])...)
	for _, l := range a[len(a)-suf:] {
		out = append(out, diffEdit{' ', l, false})
	}
	return out
}

func middleEdits(a, b []string) []diffEdit {
	var out []diffEdit
	if len(a) == 0 || len(b) == 0 || (len(a)+1)*(len(b)+1) > maxDiffCells {
		for _, l := range a {
			out = append(out, diffEdit{'-', l, false})
		}
		for _, l := range b {
			out = append(out, diffEdit{'+', l, false})
		}
		return out
	}
	w := len(b) + 1
	lcs := make([]int32, (len(a)+1)*w)
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i*w+j] = lcs[(i+1)*w+j+1] + 1
			} else {
				lcs[i*w+j] = max(lcs[(i+1)*w+j], lcs[i*w+j+1])
			}
		}
	}
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		switch {
		case a[i] == b[j]:
			out = append(out, diffEdit{' ', a[i], false})
			i++
			j++
		case lcs[(i+1)*w+j] >= lcs[i*w+j+1]:
			out = append(out, diffEdit{'-', a[i], false})
			i++
		default:
			out = append(out, diffEdit{'+', b[j], false})
			j++
		}
	}
	for ; i < len(a); i++ {
		out = append(out, diffEdit{'-', a[i], false})
	}
	for ; j < len(b); j++ {
		out = append(out, diffEdit{'+', b[j], false})
	}
	return out
}
