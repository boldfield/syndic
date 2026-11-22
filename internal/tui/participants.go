package tui

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/boldfield/syndic/internal/wire"
	"github.com/mattn/go-runewidth"
)

// unprobedHint is what to do about a participant the engine has no usable probe
// record for.
const unprobedHint = "run syndic probe, then restart the engine"

// modelFamily is the display-only family of a reported model name: lower-cased
// and cut at the first ':', '-', '.' or digit.
func modelFamily(name string) string {
	name = strings.ToLower(name)
	if i := strings.IndexFunc(name, func(r rune) bool { return r == ':' || r == '-' || r == '.' || unicode.IsDigit(r) }); i >= 0 {
		name = name[:i]
	}
	return name
}

// sameFamily reports whether two reported model names share a family. An empty
// family matches nothing.
func sameFamily(a, b string) bool {
	fa := modelFamily(a)
	return fa != "" && fa == modelFamily(b)
}

// participantIDs returns the roster's participant ids in display order.
func participantIDs(s State) []string {
	ids := make([]string, 0, len(s.Participants))
	for id := range s.Participants {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// currentAuthor is the author of the current turn and whether there is one.
func currentAuthor(s State) (ParticipantInfo, bool) {
	ti, ok := s.Turns[s.CurrentTurn]
	if !ok || ti.Author == "" {
		return ParticipantInfo{}, false
	}
	p, ok := s.Participants[ti.Author]
	if !ok {
		p = ParticipantInfo{ID: ti.Author}
	}
	return p, true
}

// countUnprobed returns the number of participants in the default preset that are unprobed.
func countUnprobed(s State) int {
	count := 0
	for _, p := range s.Participants {
		if p.InDefaultPreset && p.State == "unprobed" {
			count++
		}
	}
	return count
}

// markedSameFamily reports whether p is a reviewer of the current turn whose
// reported model is in the author's family.
func markedSameFamily(s State, p ParticipantInfo) bool {
	author, ok := currentAuthor(s)
	if !ok || p.ID == author.ID || !slices.Contains(s.Turns[s.CurrentTurn].Reviewers, p.ID) {
		return false
	}
	return sameFamily(p.ReportedModel, author.ReportedModel)
}

// renderParticipants returns the participants tab: the author line, the roster
// table, the selected participant's detail, and the pools with their holds.
// Every line is at most width cells wide.
func renderParticipants(s State, width, height int, selectedID string, now time.Time) string {
	width = max(width, 1)
	var head, table, detail, pools []string
	if author, ok := currentAuthor(s); ok {
		head = []string{fmt.Sprintf("author: %s (%s)", plain(author.ID), plain(orDash(author.ReportedModel)))}
	}
	if unprobed := countUnprobed(s); unprobed > 0 {
		head = append(head, fmt.Sprintf("participants: %d unprobed", unprobed))
	}
	ids := participantIDs(s)
	selIdx := 0
	if len(ids) == 0 {
		table = []string{"no participants"}
	} else {
		table = participantTable(s, ids, selectedID, width, now)
		for i, id := range ids {
			if id == selectedID {
				selIdx = i
			}
		}
		if p, ok := s.Participants[selectedID]; ok {
			detail = participantDetail(s, p, now)
		}
		// Check if selected is a probe subject
		subjKey := selectedID
		if subj, ok := s.ProbeSubjects[subjKey]; ok {
			detail = probeSubjectDetail(s, subj, now)
		}
	}
	pools = poolLines(s, now)
	refLines := probeSubjectLines(s, now)

	// Fit the height: the table scrolls around the selection first, and the
	// pools are cut last, leaving a marker that PgDn reaches the rest.
	if height > 0 {
		avail := func() int { return height - len(head) - 1 - len(detail) - len(pools) - len(refLines) }
		if len(ids) > 0 {
			rows := table[1:]
			keep := max(avail(), 1)
			if keep < len(rows) {
				start := min(max(selIdx-keep/2, 0), len(rows)-keep)
				rows = rows[start : start+keep]
			}
			table = append([]string{table[0]}, rows...)
		}
		if over := len(head) + len(table) + len(detail) + len(pools) + len(refLines) - height; over > 0 && (len(pools) > 0 || len(refLines) > 0) {
			cut := min(over+1, len(pools)+len(refLines))
			if cut <= len(pools) {
				pools = append(pools[:len(pools)-cut], fmt.Sprintf("… %d more lines (PgDn)", cut))
			} else {
				pools = nil
				refLines = append(refLines[:len(refLines)-(cut-len(pools))], fmt.Sprintf("… %d more lines (PgDn)", cut-len(pools)))
			}
		}
	}
	lines := append(append(append(append(head, table...), detail...), pools...), refLines...)
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = clipCells(l, width)
	}
	return strings.Join(out, "\n")
}

// column is one table column; those with the highest priority number are
// dropped first when the table does not fit.
type column struct {
	title    string
	priority int
	cell     func(p ParticipantInfo) string
}

// participantTable renders one row per participant, dropping the lowest
// priority columns until the table fits in width.
func participantTable(s State, ids []string, selectedID string, width int, now time.Time) []string {
	cols := []column{
		{"id", 0, func(p ParticipantInfo) string { return p.ID }},
		{"state", 0, func(p ParticipantInfo) string {
			if p.Detail != "" {
				return p.State + ":" + p.Detail
			}
			return p.State
		}},
		{"pool", 1, func(p ParticipantInfo) string { return p.Pool }},
		{"model", 1, func(p ParticipantInfo) string {
			m := orDash(p.ReportedModel)
			if markedSameFamily(s, p) {
				m += " same-family"
			}
			return m
		}},
		{"roles", 2, func(p ParticipantInfo) string { return orDash(strings.Join(p.EligibleRoles, ",")) }},
		{"probed", 3, func(p ParticipantInfo) string {
			if p.ProbeObservedAt == nil {
				return "-"
			}
			return formatAge(now.Sub(*p.ProbeObservedAt))
		}},
		{"ctx", 4, func(p ParticipantInfo) string {
			if p.ContextBound == nil {
				return "-"
			}
			return fmt.Sprint(*p.ContextBound)
		}},
	}
	const gap = 2
	const mark = 2 // the selection marker and its space
	for {
		total := mark + gap*(len(cols)-1)
		for _, c := range cols {
			total += columnWidth(s, ids, c)
		}
		if total <= width || len(cols) <= 2 {
			break
		}
		worst := 0
		for i, c := range cols {
			if c.priority >= cols[worst].priority {
				worst = i
			}
		}
		cols = append(cols[:worst], cols[worst+1:]...)
	}

	row := func(sel string, cell func(c column) string) string {
		parts := make([]string, len(cols))
		for i, c := range cols {
			parts[i] = cell(c)
			if i < len(cols)-1 {
				parts[i] = padCells(parts[i], columnWidth(s, ids, c))
			}
		}
		return sel + strings.Join(parts, strings.Repeat(" ", gap))
	}
	lines := []string{row("  ", func(c column) string { return c.title })}
	for _, id := range ids {
		p := s.Participants[id]
		sel := "  "
		if id == selectedID {
			sel = "> "
		}
		lines = append(lines, row(sel, func(c column) string { return plain(c.cell(p)) }))
	}
	return lines
}

func columnWidth(s State, ids []string, c column) int {
	w := runewidth.StringWidth(c.title)
	for _, id := range ids {
		w = max(w, runewidth.StringWidth(plain(c.cell(s.Participants[id]))))
	}
	return w
}

// participantDetail lists everything the status reply says about p.
func participantDetail(s State, p ParticipantInfo, now time.Time) []string {
	lines := []string{"detail: " + plain(p.ID)}
	state := p.State
	if p.Detail != "" {
		state += " (" + p.Detail + ")"
	}
	lines = append(lines, "  state: "+plain(state))
	if p.State == "unprobed" {
		lines = append(lines, "  hint: "+unprobedHint)
	}
	identity := p.Identity
	if identity == "" || identity == "unknown" {
		identity = "unknown (name-matched)"
	}
	billing := p.DeclaredBilling
	if p.DeclaredBilling == "metered" && p.SpendingControl == "external" {
		billing = "metered, external spending control"
	}
	lines = append(lines,
		"  pool: "+plain(p.Pool)+"  model: "+orDash(plain(p.ReportedModel)),
		"  identity: "+plain(identity),
		"  roles: "+orDash(plain(strings.Join(p.EligibleRoles, ", ")))+"  billing: "+orDash(plain(billing)),
	)
	if marked := markedSameFamily(s, p); marked {
		lines = append(lines, "  same-family: reported model is in the author's family")
	}
	if p.ProbeObservedAt != nil {
		probeTime := fmt.Sprintf("  probe observed_at: %s (%s ago)", p.ProbeObservedAt.UTC().Format("2006-01-02 15:04:05Z"), formatAge(now.Sub(*p.ProbeObservedAt)))
		if p.ProbeAtStart {
			probeTime += ", probed at start"
		}
		lines = append(lines, probeTime)
	} else {
		lines = append(lines, "  probe observed_at: none")
		if p.FailingStep != "" {
			lines = append(lines, "  probe failed at: "+plain(p.FailingStep))
		}
		if p.ServerStatus != "" {
			lines = append(lines, "  server status: "+plain(p.ServerStatus))
		}
	}
	if p.RetryCommand != "" {
		lines = append(lines, "  retry: "+plain(p.RetryCommand))
	}
	// The design takes the output bound from the probe record alone.
	lines = append(lines, "  context bound: "+bound(p.ContextBound, p.ContextSource)+"  output bound: "+bound(p.OutputBound, "probe"))
	bpt := "-"
	if p.BytesPerToken != nil {
		bpt = fmt.Sprintf("%.2f (%s)", *p.BytesPerToken, orDash(plain(p.BytesPerTokenSource)))
	}
	if p.Estimate == "exceeded" {
		bpt += "  estimate: exceeded"
	}
	lines = append(lines, "  bytes per token: "+bpt)
	if p.EligibleRequiredReviewer == "unknown" && hasRole(p, "reviewer") {
		truncationLine := "  truncation: unknown"
		if p.AcceptUnknownTruncation {
			truncationLine += " (accepted)"
		}
		lines = append(lines, truncationLine)
	}
	return lines
}

func hasRole(p ParticipantInfo, role string) bool {
	for _, r := range p.EligibleRoles {
		if r == role {
			return true
		}
	}
	return false
}

// bound shows a token bound with its source, or a dash when there is none.
func bound(v *int64, source string) string {
	if v == nil {
		return "-"
	}
	return fmt.Sprintf("%d (%s)", *v, orDash(plain(source)))
}

// poolLines lists each pool with its concurrency and holds, then any orphaned
// holds.
func poolLines(s State, now time.Time) []string {
	var lines []string
	names := make([]string, 0, len(s.Pools))
	for name := range s.Pools {
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) > 0 {
		lines = append(lines, "pools:")
	}
	for _, name := range names {
		pool := s.Pools[name]
		lines = append(lines, fmt.Sprintf("  %s: concurrency %d, %d held", plain(name), pool.Concurrency, len(pool.Holds)))
		for _, h := range pool.Holds {
			lines = append(lines, fmt.Sprintf("    hold %s, age %s%s", plain(h.AttemptID), holdAge(h.HeldSince, now), groupSuffix(h.Group)))
		}
	}
	if len(s.OrphanedHolds) > 0 {
		lines = append(lines, "orphaned holds:")
		for _, h := range s.OrphanedHolds {
			lines = append(lines, fmt.Sprintf("  pool %s, attempt %s%s", plain(h.Pool), plain(h.AttemptID), groupSuffix(h.Group)),
				fmt.Sprintf("    /release %s %s", plain(h.Pool), plain(h.AttemptID)))
		}
	}
	return lines
}

func groupSuffix(group []wire.GroupMember) string {
	if len(group) == 0 {
		return ""
	}
	parts := make([]string, len(group))
	for i, m := range group {
		parts[i] = plain(m.EndpointName + "/" + m.Model)
	}
	return " (" + strings.Join(parts, ", ") + ")"
}

func holdAge(heldSince string, now time.Time) string {
	t, err := time.Parse(time.RFC3339, heldSince)
	if err != nil {
		return "unknown"
	}
	return formatAge(now.Sub(t))
}

// formatAge shows an age at one or two units: 45s, 3m20s, 2h5m, 3d4h.
func formatAge(d time.Duration) string {
	d = max(d, 0).Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm%ds", int(d.Minutes()), int(d.Seconds())%60)
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dd%dh", int(d.Hours())/24, int(d.Hours())%24)
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// plain replaces control characters, so a name from an adapter cannot drive the terminal.
func plain(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return '·'
		}
		return r
	}, s)
}

// clipCells cuts s to at most width cells, ending in an ellipsis when it cuts.
func clipCells(s string, width int) string {
	if runewidth.StringWidth(s) <= width {
		return s
	}
	return runewidth.Truncate(s, width, "…")
}

// probeSubjectDetail lists information about a probe subject like a referee.
func probeSubjectDetail(s State, subj ProbeSubjectInfo, now time.Time) []string {
	var lines []string
	lines = append(lines, "detail: "+plain(subj.Kind+":"+subj.Endpoint+":"+subj.Model))
	state := subj.State
	if subj.Detail != "" {
		state += " (" + subj.Detail + ")"
	}
	lines = append(lines, "  state: "+plain(state))
	lines = append(lines, "  kind: "+plain(subj.Kind))
	lines = append(lines, "  endpoint: "+plain(subj.Endpoint)+"  model: "+plain(subj.Model))
	if subj.ReportedModel != "" {
		lines = append(lines, "  reported model: "+plain(subj.ReportedModel))
	}
	if subj.ProbeObservedAt != nil {
		probeTime := fmt.Sprintf("  probe observed_at: %s (%s ago)", subj.ProbeObservedAt.UTC().Format("2006-01-02 15:04:05Z"), formatAge(now.Sub(*subj.ProbeObservedAt)))
		if subj.ProbeAtStart {
			probeTime += ", probed at start"
		}
		lines = append(lines, probeTime)
	} else {
		lines = append(lines, "  probe observed_at: none")
		if subj.FailingStep != "" {
			lines = append(lines, "  probe failed at: "+plain(subj.FailingStep))
		}
		if subj.ServerStatus != "" {
			lines = append(lines, "  server status: "+plain(subj.ServerStatus))
		}
	}
	if subj.RetryCommand != "" {
		lines = append(lines, "  retry: "+plain(subj.RetryCommand))
	}
	lines = append(lines, "  context bound: "+bound(subj.ContextBound, "")+"  output bound: "+bound(subj.OutputBound, ""))
	bpt := "-"
	if subj.BytesPerToken != nil {
		bpt = fmt.Sprintf("%.2f (%s)", *subj.BytesPerToken, orDash(plain(subj.BytesPerTokenSource)))
	}
	lines = append(lines, "  bytes per token: "+bpt)
	return lines
}

// probeSubjectLines lists each probe subject (referees, etc.) if any exist.
func probeSubjectLines(s State, now time.Time) []string {
	var lines []string
	if len(s.ProbeSubjects) == 0 {
		return lines
	}
	var keys []string
	for k := range s.ProbeSubjects {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	lines = append(lines, "probe subjects:")
	for _, k := range keys {
		subj := s.ProbeSubjects[k]
		state := subj.State
		if subj.Detail != "" {
			state += ":" + subj.Detail
		}
		lines = append(lines, fmt.Sprintf("  %s/%s/%s: %s", plain(subj.Kind), plain(subj.Endpoint), plain(subj.Model), state))
	}
	return lines
}
