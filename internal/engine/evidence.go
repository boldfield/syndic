package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/boldfield/syndic/internal/candidate"
	"github.com/boldfield/syndic/internal/config"
	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/observation"
	"github.com/boldfield/syndic/internal/wire"
)

const (
	diffContext = 3
	// maxDiffCells bounds the line-matching table; a pair of files too large
	// for it is shown as one replaced block, which is still a valid diff.
	maxDiffCells = 4 << 20
)

// frozenInputs is what a candidate's freeze binds besides its text: the
// author's tool results, the staged change set and the snapshot it rested on.
type frozenInputs struct {
	evidence     []candidate.EvidencePair
	changes      []observation.Change
	snapshotHash string
	changesHash  string
	// version is the snapshot version the candidate was frozen on.
	version int
	// lines are the canonical change-set lines the unchanged-revision
	// comparison uses.
	lines []string
}

// frozenInputsFor reads the turn's author tool results, staging map and
// snapshot record in tx and computes the hashes the candidate binds. A turn
// with nothing staged has no change set and an empty changes hash.
func frozenInputsFor(tx *journal.Tx, turnID, author string) (frozenInputs, error) {
	var fi frozenInputs
	calls, err := tx.ToolCallsByTurn(turnID, author)
	if err != nil {
		return fi, err
	}
	for _, c := range calls {
		// A refused or abandoned call has no result bytes to bind.
		if c.Result == nil || c.Result.ResultSHA256 == "" {
			continue
		}
		fi.evidence = append(fi.evidence, candidate.EvidencePair{CallID: c.ID, ResultSHA256: c.Result.ResultSHA256})
	}
	if fi.version, err = tx.SnapshotVersion(turnID); err != nil {
		return fi, err
	}
	staged, err := tx.Staging(turnID)
	if err != nil {
		return fi, err
	}
	if len(staged) == 0 {
		return fi, nil
	}
	for _, e := range staged {
		body, err := tx.Content(e.NewSHA256)
		if err != nil {
			return fi, err
		}
		c := observation.Change{Path: e.Path, Op: observation.OpModify, BaseSHA: e.BaseSHA256, NewSHA: e.NewSHA256, NewLen: int64(len(body))}
		if e.BaseSHA256 == "" {
			c.Op, c.BaseSHA = observation.OpCreate, observation.Absent
		}
		fi.changes = append(fi.changes, c)
	}
	obs, err := tx.Observations(turnID)
	if err != nil {
		return fi, err
	}
	records := make([]observation.Record, len(obs))
	for i, o := range obs {
		records[i] = observation.Record{Kind: o.Kind[0], Path: o.Path, Value: o.ContentSHA256}
	}
	if fi.snapshotHash, err = observation.SnapshotHash(records); err != nil {
		return fi, err
	}
	if fi.changesHash, err = observation.ChangesHash(fi.changes, fi.snapshotHash); err != nil {
		return fi, err
	}
	framed, err := observation.FrameChangeSetLines(fi.changes)
	if err != nil {
		return fi, err
	}
	fi.lines = strings.SplitAfter(string(framed), "\n")
	fi.lines = fi.lines[:len(fi.lines)-1]
	return fi, nil
}

// egressRefusal returns the refusal of a submit under eff to a conversation
// whose journal records it file-tainted (tainted), or nil. Every route by
// which a turn under eff could carry file-derived content, the diffs and
// evidence this file builds among them, must end on an endpoint the roster
// allows it; roster load holds a preset to the same rule untainted, so here
// it is the carry-forward that bites. In a tainted conversation every
// participant is reached by history, whatever tools the preset grants and
// wherever the trim is set, and a preset that turns web with file content
// off may not give any participant web.
func egressRefusal(r *config.Roster, eff config.Effective, tainted bool) *wire.Error {
	refuse := func(participant, route string) *wire.Error {
		we := refusal(wire.ErrorCodeEgress)
		we.Participant, we.Route = strPtr(participant), strPtr(route)
		return we
	}
	if bad := r.FileEgressViolations(eff.Preset, tainted); len(bad) > 0 {
		return refuse(bad[0].Participant, bad[0].Route)
	}
	if web := r.WebWithFileViolation(eff.Preset, tainted); len(web) > 0 {
		route := config.EgressRouteHistory
		if !tainted {
			for _, fr := range config.FileRoutes(eff.Preset, false) {
				if fr.Participant == web[0] {
					route = fr.Route
				}
			}
		}
		return refuse(web[0], route)
	}
	return nil
}

// reviewerPacket is the data a reviewer receives beside the request, the
// history and the candidate text.
type reviewerPacket struct {
	diffs    string
	evidence string
}

// packetFor regenerates the packet for the turn's current candidate from the
// journal alone: the frozen change set's stored base and new contents, and
// the author's recorded calls and results. The live workspace is never read,
// so the same bytes come out at any later time. Only the author's calls
// appear; a reviewer's own lookups are recorded under its verdict.
func packetFor(tx *journal.Tx, turnID, author string) (reviewerPacket, error) {
	var p reviewerPacket
	cs, err := tx.ChangeSetForTurn(turnID)
	switch {
	case errors.Is(err, journal.ErrNoChangeSet):
	case err != nil:
		return p, err
	default:
		var b strings.Builder
		for _, path := range cs.Paths {
			var base, body []byte
			if path.BaseSHA256 != "" {
				if base, err = tx.Content(path.BaseSHA256); err != nil {
					return p, err
				}
			}
			if body, err = tx.Content(path.NewSHA256); err != nil {
				return p, err
			}
			b.WriteString(unifiedDiff(path.Path, path.Op == observation.OpCreate, base, body))
		}
		p.diffs = b.String()
	}
	calls, err := tx.ToolCallsByTurn(turnID, author)
	if err != nil {
		return p, err
	}
	var b strings.Builder
	for i, c := range calls {
		args, err := tx.Content(c.ArgsSHA256)
		if err != nil {
			return p, err
		}
		fmt.Fprintf(&b, "call %d: %s (id %s)\narguments:\n%s\n", i+1, c.Name, c.ID, args)
		switch {
		case c.Result == nil:
			b.WriteString("result: none\n")
		case c.Result.ResultSHA256 == "":
			fmt.Fprintf(&b, "result: %s\n", c.Result.Status)
		default:
			res, err := tx.Content(c.Result.ResultSHA256)
			if err != nil {
				return p, err
			}
			fmt.Fprintf(&b, "result (%s", c.Result.Status)
			if c.Result.ErrorCode != "" {
				fmt.Fprintf(&b, ", %s", c.Result.ErrorCode)
			}
			fmt.Fprintf(&b, "):\n%s\n", res)
		}
	}
	p.evidence = b.String()
	return p, nil
}

// recordReviewerEvidence journals what the reviewer's accepted verdict rested
// on: the snapshot version it saw, the results of its own calls and the
// observations it added. The version is the candidate's frozen one, raised to
// the newest observation the reviewer added; a later first touch by the other
// reviewer is not one this reviewer saw. It returns each result's content by id, which is what a block
// citing one transfers to the author. Only the reviewer's own calls count;
// the author's and the other reviewer's never appear.
func recordReviewerEvidence(tx *journal.Tx, turnID, participant, verdictID string, frozenVersion int) (map[string]string, error) {
	calls, err := tx.ToolCallsByTurn(turnID, participant)
	if err != nil {
		return nil, err
	}
	var resultIDs []string
	results := map[string]string{}
	for _, c := range calls {
		// An abandoned or refused call has no result bytes to receive or cite.
		if c.Result == nil || c.Result.ResultSHA256 == "" {
			continue
		}
		body, err := tx.Content(c.Result.ResultSHA256)
		if err != nil {
			return nil, err
		}
		resultIDs = append(resultIDs, c.Result.ID)
		results[c.Result.ID] = string(body)
	}
	obsIDs, err := tx.UnboundObservationIDs(turnID, participant)
	if err != nil {
		return nil, err
	}
	// A verdict that rested on no lookups has no evidence to journal, and
	// recording it would add an event to turns that never used tools.
	if len(resultIDs) == 0 && len(obsIDs) == 0 {
		return results, nil
	}
	version := frozenVersion
	if len(obsIDs) > 0 {
		added := map[string]bool{}
		for _, id := range obsIDs {
			added[id] = true
		}
		all, err := tx.Observations(turnID)
		if err != nil {
			return nil, err
		}
		for _, o := range all {
			if added[o.ID] {
				version = max(version, o.SnapshotVersion)
			}
		}
	}
	if err := tx.RecordVerdictEvidence(verdictID, version, resultIDs, obsIDs); err != nil {
		return nil, err
	}
	return results, nil
}

// openBlockEvidence returns the evidence fields of the reviewer's blocks
// still open once its verdict is applied, one per line in the order they were
// opened. A block opened by this verdict is among them, and so is a block
// opened in an earlier round and maintained since: its citation transfers on
// every revision while it stays open. A withdrawn block, and the other
// reviewer's blocks, cite nothing.
func openBlockEvidence(tx *journal.Tx, turnID, participant string) (string, error) {
	blocks, err := tx.ReviewerOpenBlocks(turnID, participant)
	if err != nil {
		return "", err
	}
	parts := make([]string, 0, len(blocks))
	for _, b := range blocks {
		if b.Evidence != "" {
			parts = append(parts, b.Evidence)
		}
	}
	return strings.Join(parts, "\n"), nil
}

// diffLine is one line of a file, with its newline when it has one.
type diffLine struct {
	text  string
	noEOL bool
}

func splitDiffLines(b []byte) []diffLine {
	var out []diffLine
	s := string(b)
	for len(s) > 0 {
		i := strings.IndexByte(s, '\n')
		if i < 0 {
			out = append(out, diffLine{text: s, noEOL: true})
			break
		}
		out = append(out, diffLine{text: s[:i]})
		s = s[i+1:]
	}
	return out
}

func isBinary(b []byte) bool {
	return strings.IndexByte(string(b), 0) >= 0 || !utf8.Valid(b)
}

func diffPath(p string) string {
	for _, r := range p {
		if r < 0x20 || r == 0x7f || r == ' ' || r == '"' || r == utf8.RuneError {
			return strconv.Quote(p)
		}
	}
	return p
}

// unifiedDiff renders one changed path as a unified diff of its stored base
// against its new content, with three lines of context. A created file is
// the new content in full against /dev/null. A file that is not valid UTF-8
// or holds a NUL byte is described by hashes and length, not shown. The
// output is a pure function of its arguments.
func unifiedDiff(path string, create bool, base, body []byte) string {
	from, to := diffPath("a/"+path), diffPath("b/"+path)
	if create {
		from = "/dev/null"
		base = nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "--- %s\n+++ %s\n", from, to)
	if isBinary(base) || isBinary(body) {
		fmt.Fprintf(&b, "Binary content, not shown: base sha256 %s, new sha256 %s, new length %d\n",
			shaOrAbsent(base, create), shaOf(body), len(body))
		return b.String()
	}
	a, c := splitDiffLines(base), splitDiffLines(body)
	for _, h := range hunks(editScript(a, c), diffContext) {
		writeHunk(&b, h)
	}
	return b.String()
}

type edit struct {
	op   byte // ' ', '-', '+'
	line diffLine
}

// editScript returns the line edits turning a into b. The common prefix and
// suffix are trimmed and the middle is matched by longest common subsequence.
func editScript(a, b []diffLine) []edit {
	pre := 0
	for pre < len(a) && pre < len(b) && a[pre] == b[pre] {
		pre++
	}
	suf := 0
	for suf < len(a)-pre && suf < len(b)-pre && a[len(a)-1-suf] == b[len(b)-1-suf] {
		suf++
	}
	ma, mb := a[pre:len(a)-suf], b[pre:len(b)-suf]
	var out []edit
	for _, l := range a[:pre] {
		out = append(out, edit{' ', l})
	}
	out = append(out, middleEdits(ma, mb)...)
	for _, l := range a[len(a)-suf:] {
		out = append(out, edit{' ', l})
	}
	return out
}

func middleEdits(a, b []diffLine) []edit {
	var out []edit
	if len(a) == 0 || len(b) == 0 || (len(a)+1)*(len(b)+1) > maxDiffCells {
		for _, l := range a {
			out = append(out, edit{'-', l})
		}
		for _, l := range b {
			out = append(out, edit{'+', l})
		}
		return out
	}
	// lcs[i][j] is the length of the longest common subsequence of a[i:], b[j:].
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
			out = append(out, edit{' ', a[i]})
			i++
			j++
		case lcs[(i+1)*w+j] >= lcs[i*w+j+1]:
			out = append(out, edit{'-', a[i]})
			i++
		default:
			out = append(out, edit{'+', b[j]})
			j++
		}
	}
	for ; i < len(a); i++ {
		out = append(out, edit{'-', a[i]})
	}
	for ; j < len(b); j++ {
		out = append(out, edit{'+', b[j]})
	}
	return out
}

type hunk struct {
	aStart, aCount, bStart, bCount int
	edits                          []edit
}

// hunks groups an edit script into hunks with context lines around each run
// of changes; runs closer than twice the context share a hunk.
func hunks(script []edit, ctx int) []hunk {
	var out []hunk
	aLine, bLine := 1, 1 // line numbers of script[i] in each file
	i := 0
	for i < len(script) {
		if script[i].op == ' ' {
			aLine++
			bLine++
			i++
			continue
		}
		// Start the hunk up to ctx context lines back.
		start := i
		for start > 0 && i-start < ctx && script[start-1].op == ' ' {
			start--
		}
		back := i - start
		h := hunk{aStart: aLine - back, bStart: bLine - back}
		end := i // exclusive end of the last change seen
		k := i
		for k < len(script) {
			if script[k].op != ' ' {
				end = k + 1
				k++
				continue
			}
			// Context run: it ends the hunk when it is longer than 2*ctx or the script ends.
			run := k
			for run < len(script) && script[run].op == ' ' {
				run++
			}
			if run == len(script) || run-k > 2*ctx {
				break
			}
			k = run
		}
		stop := min(end+ctx, len(script))
		for ; i < stop; i++ {
			switch script[i].op {
			case ' ':
				aLine++
				bLine++
			case '-':
				aLine++
			case '+':
				bLine++
			}
		}
		h.edits = script[start:stop]
		for _, e := range h.edits {
			if e.op != '+' {
				h.aCount++
			}
			if e.op != '-' {
				h.bCount++
			}
		}
		out = append(out, h)
	}
	return out
}

func writeHunk(b *strings.Builder, h hunk) {
	// A side with no lines starts at the line before the hunk.
	as, bs := h.aStart, h.bStart
	if h.aCount == 0 {
		as--
	}
	if h.bCount == 0 {
		bs--
	}
	fmt.Fprintf(b, "@@ -%s +%s @@\n", rangeOf(as, h.aCount), rangeOf(bs, h.bCount))
	for _, e := range h.edits {
		b.WriteByte(e.op)
		b.WriteString(e.line.text)
		b.WriteByte('\n')
		if e.line.noEOL {
			b.WriteString("\\ No newline at end of file\n")
		}
	}
}

func rangeOf(start, count int) string {
	if count == 1 {
		return strconv.Itoa(start)
	}
	return fmt.Sprintf("%d,%d", start, count)
}

func shaOf(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func shaOrAbsent(b []byte, create bool) string {
	if create {
		return observation.Absent
	}
	return shaOf(b)
}
