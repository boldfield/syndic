package tui

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"unicode"

	"github.com/boldfield/syndic/internal/wire"
	"github.com/mattn/go-runewidth"
)

const (
	// minColumn is the narrowest a repair column may be before the repair is
	// stacked under its original instead.
	minColumn = 40
	// columnGap separates the two columns of a repair pair.
	columnGap = " │ "
	// tabStop is how many cells a tab in a body is shown as.
	tabStop = 4
	// headerSep separates the parts of a header.
	headerSep = " · "
)

// transcriptInput is what the transcript tab needs beyond State.
type transcriptInput struct {
	items       []wire.TranscriptItem // in the order the engine returned them
	content     *ContentCache
	quarantined map[string]bool           // attempts whose result arrived late
	blockDetail map[string]wire.OpenBlock // evidence and remedy by block id
	reloading   bool                      // a newer snapshot of the items is on its way
}

// transcriptLines renders the whole transcript tab for the turn, unclipped,
// wrapped to width cells.
func transcriptLines(s State, turnID string, in transcriptInput, width int) []string {
	width = max(width, 1)
	if turnID == "" {
		return []string{"no turn yet"}
	}
	lines := []string{"transcript of turn " + turnID, ""}
	if len(in.items) == 0 {
		return append(lines, "(no messages yet)")
	}

	present := map[string]bool{}
	for _, it := range in.items {
		present[it.Ref] = true
	}
	// A repair is drawn with its original; it is not listed again.
	repairOf := map[string]string{} // original attempt id -> repair attempt id
	isRepair := map[string]bool{}
	for _, it := range in.items {
		id, ok := strings.CutPrefix(it.Ref, "attempt:")
		if !ok {
			continue
		}
		if orig := s.Attempts[id].RepairOf; orig != nil && present["attempt:"+*orig] {
			if _, dup := repairOf[*orig]; !dup {
				repairOf[*orig] = id
				isRepair[id] = true
			}
		}
	}
	blocks := blocksByAttempt(s, in.blockDetail)

	for _, it := range in.items {
		id, isAttempt := strings.CutPrefix(it.Ref, "attempt:")
		if isAttempt && isRepair[id] {
			continue
		}
		if rid, ok := repairOf[id]; isAttempt && ok {
			lines = append(lines, repairPair(s, in, it, itemOf(in.items, "attempt:"+rid), width)...)
			lines = append(lines, blockRowLines(blocks[id], width)...)
			lines = append(lines, blockRowLines(blocks[rid], width)...)
		} else {
			lines = append(lines, wrapHeader(headerParts(s, in, it), width)...)
			lines = append(lines, SafeTextWithPrefix(bodyText(s, in, it), width)...)
			if isAttempt {
				lines = append(lines, blockRowLines(blocks[id], width)...)
			}
		}
		lines = append(lines, "")
	}
	return lines
}

func itemOf(items []wire.TranscriptItem, ref string) wire.TranscriptItem {
	for _, it := range items {
		if it.Ref == ref {
			return it
		}
	}
	return wire.TranscriptItem{Ref: ref}
}

// headerParts describes an item as the parts of its header line.
func headerParts(s State, in transcriptInput, it wire.TranscriptItem) []string {
	if id, ok := strings.CutPrefix(it.Ref, "attempt:"); ok {
		ai, known := s.Attempts[id]
		if !known {
			return []string{"attempt " + id}
		}
		status := ai.Status
		if !attemptDone(ai) {
			status = "running"
		}
		parts := []string{"attempt " + id, ai.Participant, ai.Phase, "status " + status}
		if ai.TransportStatus != "" && ai.TransportStatus != "ok" {
			parts = append(parts, "transport "+ai.TransportStatus)
		}
		parts = append(parts, fmt.Sprintf("scrubs %d", ai.ScrubCount), fmt.Sprintf("est. %d tokens", ai.EstimatedTokens))
		var reported []string
		if ai.ReportedPrompt != nil {
			reported = append(reported, fmt.Sprintf("prompt %d", *ai.ReportedPrompt))
		}
		if ai.ReportedCompletion != nil {
			reported = append(reported, fmt.Sprintf("completion %d", *ai.ReportedCompletion))
		}
		if len(reported) > 0 {
			parts = append(parts, "reported "+strings.Join(reported, ", "))
		}
		if ai.VerdictClass != "" {
			parts = append(parts, "verdict "+ai.VerdictClass)
		}
		if in.quarantined[id] {
			parts = append(parts, "quarantined (late)")
		}
		return parts
	}
	id := strings.TrimPrefix(it.Ref, "msg:")
	if m, ok := s.Messages[id]; ok {
		return []string{"message", m.Sender, m.Phase}
	}
	return []string{"message " + id}
}

// wrapHeader lays the parts of a header out on lines of at most width cells,
// breaking between parts; a part wider than a line is wrapped inside itself.
func wrapHeader(parts []string, width int) []string {
	var out []string
	cur := ""
	for _, p := range parts {
		switch {
		case cur == "":
			cur = p
		case runewidth.StringWidth(cur)+runewidth.StringWidth(headerSep)+runewidth.StringWidth(p) <= width:
			cur += headerSep + p
		default:
			out = append(out, wrapText(cur, width)...)
			cur = p
		}
	}
	return append(out, wrapText(cur, width)...)
}

// bodyText is the complete body of an item, or a note saying why there is none.
// Untrusted content is escaped to prevent control characters from reaching the terminal.
func bodyText(s State, in transcriptInput, it wire.TranscriptItem) string {
	if it.ByteLen == 0 {
		if id, ok := strings.CutPrefix(it.Ref, "attempt:"); ok {
			if ai, known := s.Attempts[id]; known {
				switch {
				case !attemptDone(ai):
					return "(waiting for output)"
				case in.reloading:
					// The snapshot may predate the output.
					return "(loading…)"
				}
			}
		}
		return "(no output)"
	}
	c := in.content.Get(it.Ref)
	switch {
	case !c.Ready:
		return "(loading…)"
	case c.Err != nil:
		return "(could not load: " + ToSafeText([]byte(c.Err.Error())) + ")"
	}
	return ToSafeText([]byte(c.Data))
}

// transcriptRefs lists the bodies the tab wants fetched.
func transcriptRefs(items []wire.TranscriptItem) []string {
	var refs []string
	for _, it := range items {
		if it.ByteLen > 0 {
			refs = append(refs, it.Ref)
		}
	}
	return refs
}

// transcriptPending reports whether any body the items point at is still being fetched.
func transcriptPending(items []wire.TranscriptItem, cc *ContentCache) bool {
	return slices.ContainsFunc(transcriptRefs(items), func(ref string) bool { return !cc.Get(ref).Ready })
}

// repairPair draws an attempt beside its repair, or stacked when the width
// does not give each column minColumn cells.
func repairPair(s State, in transcriptInput, orig, repair wire.TranscriptItem, width int) []string {
	col := (width - runewidth.StringWidth(columnGap)) / 2
	side := func(label string, it wire.TranscriptItem, w int) []string {
		lines := []string{label}
		lines = append(lines, wrapHeader(headerParts(s, in, it), w)...)
		return append(lines, SafeTextWithPrefix(bodyText(s, in, it), w)...)
	}
	if col < minColumn {
		return append(side("original", orig, width), side("repair", repair, width)...)
	}
	left, right := side("original", orig, col), side("repair", repair, col)
	var out []string
	for i := 0; i < max(len(left), len(right)); i++ {
		l, r := "", ""
		if i < len(left) {
			l = left[i]
		}
		if i < len(right) {
			r = right[i]
		}
		out = append(out, padCells(l, col)+columnGap+r)
	}
	return out
}

// blockRow is one block to draw under the attempt whose verdict opened it.
type blockRow struct {
	id, severity, state, evidence, remedy string
}

// blocksByAttempt maps each attempt to the blocks its verdict opened. A block
// id is "<verdict id>:<reviewer's block id>"; the verdict names the attempt.
func blocksByAttempt(s State, detail map[string]wire.OpenBlock) map[string][]blockRow {
	rows := map[string]blockRow{}
	for id, b := range s.Blocks {
		rows[id] = blockRow{id: id, severity: b.Severity, state: b.State}
	}
	for id, b := range detail {
		r, ok := rows[id]
		if !ok {
			r = blockRow{id: id, severity: b.Severity, state: b.State}
		}
		r.evidence, r.remedy = b.Evidence, b.Remedy
		rows[id] = r
	}
	out := map[string][]blockRow{}
	for id, r := range rows {
		for vid, v := range s.Verdicts {
			if strings.HasPrefix(id, vid+":") {
				out[v.AttemptID] = append(out[v.AttemptID], r)
				break
			}
		}
	}
	for _, rs := range out {
		sort.Slice(rs, func(i, j int) bool { return rs[i].id < rs[j].id })
	}
	return out
}

func blockRowLines(rows []blockRow, width int) []string {
	var out []string
	for _, r := range rows {
		out = append(out, wrapText(fmt.Sprintf("  block %s · severity %s · state %s", ToSafeText([]byte(r.id)), ToSafeText([]byte(r.severity)), ToSafeText([]byte(r.state))), width)...)
		if r.evidence != "" {
			out = append(out, SafeTextWithPrefix("evidence: "+r.evidence, width)...)
		}
		if r.remedy != "" {
			out = append(out, SafeTextWithPrefix("remedy: "+r.remedy, width)...)
		}
	}
	return out
}

// wrapText splits text into lines of at most width cells. Every rune is kept;
// a line longer than width continues on the next row. Control characters are
// shown as a dot so a body cannot drive the terminal.
func wrapText(text string, width int) []string {
	width = max(width, 1)
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.TrimSuffix(text, "\n")
	var out []string
	for _, para := range strings.Split(text, "\n") {
		var cur strings.Builder
		cells := 0
		for _, r := range para {
			switch {
			case r == '\t':
				for i := 0; i < tabStop; i++ {
					if cells == width {
						out, cur, cells = append(out, cur.String()), strings.Builder{}, 0
					}
					cur.WriteByte(' ')
					cells++
				}
				continue
			case unicode.IsControl(r):
				r = '·'
			}
			w := runewidth.RuneWidth(r)
			if cells > 0 && cells+w > width {
				out, cur, cells = append(out, cur.String()), strings.Builder{}, 0
			}
			cur.WriteRune(r)
			cells += w
		}
		out = append(out, cur.String())
	}
	return out
}

// padCells pads s with spaces to width cells.
func padCells(s string, width int) string {
	if n := width - runewidth.StringWidth(s); n > 0 {
		return s + strings.Repeat(" ", n)
	}
	return s
}
