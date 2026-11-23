package tui

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/boldfield/syndic/internal/client"
	"github.com/boldfield/syndic/internal/wire"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/mattn/go-runewidth"
)

// commandHandler runs a command. It reports a problem by setting a.refusal.
type commandHandler func(a *App, args []string) tea.Cmd

var commands = map[string]commandHandler{
	"quit":          cmdQuit,
	"resume":        cmdResume,
	"mode":          cmdMode,
	"quick":         cmdQuick,
	"participants":  cmdParticipants,
	"cancel":        cmdCancel,
	"why":           cmdWhy,
	"override":      cmdOverride,
	"release":       cmdRelease,
	"drafts":        cmdDrafts,
	"raw":           cmdRaw,
	"help":          cmdHelp,
	"new":           cmdNew,
	"conversations": cmdConversations,
	"switch":        cmdSwitch,
	"trim":          cmdTrim,
	"rounds":        cmdRounds,
	"budget":        cmdBudget,
}

// commandHelp is one line per command, in the order /help lists them.
var commandHelp = []string{
	"/new                      start a new conversation; a running turn continues",
	"/conversations            list this directory's conversations",
	"/switch <id>              open the conversation with this id or unique prefix",
	"/trim <turn>              leave the turns before this one out of later history",
	"/rounds [n]               show, or set for this process, the revisions per turn",
	"/budget                   tokens reserved, charged and the limit of the turn",
	"/mode <preset>            use the preset for the next submit only",
	"/quick <name>|off         select or deselect a quick profile for this process",
	"/participants             show the participants tab",
	"/cancel                   cancel the open turn",
	"/why                      why the current or last turn ended, with the detail",
	"/override <reason>        accept an incomplete turn's candidate, with a reason",
	"/release <pool> [attempt] release held capacity, after a confirmation",
	"/drafts on|off            show or hide the author's unreviewed draft",
	"/raw on|off               show or hide Markdown rendering in answer text",
	"/resume                   reconnect to the engine",
	"/quit                     leave; an open turn keeps running",
	"/help                     this list",
}

// keyHelp is one line per key, in the order /help lists them.
var keyHelp = []string{
	"Tab, Shift+Tab            switch tabs",
	"PgUp, PgDn                scroll the active tab, or this panel",
	"Enter                     submit the input, or run the command",
	"Alt+Enter                 insert a newline; a multi-line paste is kept as typed",
	"Ctrl+C                    clear the input; on an empty input, detach",
	"Ctrl+D                    on an empty input, detach, exactly as /quit",
	"Esc                       close this panel",
	"Detaching never cancels a turn; only /cancel does.",
}

// isCommand reports whether the input line is a command rather than a prompt.
func isCommand(line string) bool { return strings.HasPrefix(line, "/") }

// dispatch runs the command named by line, which starts with "/".
func dispatch(a *App, line string) tea.Cmd {
	fields := strings.Fields(line)
	name := "/"
	if len(fields) > 0 {
		name = fields[0]
		fields = fields[1:]
	}
	h, ok := commands[strings.TrimPrefix(name, "/")]
	if !ok {
		a.refusal = "unknown command: " + name + " (try /help)"
		return nil
	}
	return h(a, fields)
}

func cmdQuit(a *App, _ []string) tea.Cmd { return a.quit() }

// cmdResume forces the same reconnect a lost connection gets.
func cmdResume(a *App, _ []string) tea.Cmd { return a.reconnect() }

func cmdHelp(a *App, _ []string) tea.Cmd {
	a.panel = append([]string{"commands:"}, commandHelp...)
	a.panel = append(append(a.panel, "", "keys:"), keyHelp...)
	return nil
}

func cmdParticipants(a *App, _ []string) tea.Cmd {
	a.tab = tabParticipants
	a.answerScrollRow, a.answerFollow = 0, true
	a.panel = nil
	return nil
}

// knownPresets is the preset list status reports for the current roster.
func knownPresets(s State) []string {
	if s.Status == nil {
		return nil
	}
	out := append([]string(nil), s.Status.Presets...)
	sort.Strings(out)
	return out
}

func cmdMode(a *App, args []string) tea.Cmd {
	if len(args) != 1 {
		a.refusal = "usage: /mode <preset>"
		return nil
	}
	if a.state.Status == nil {
		a.refusal = "engine: not connected"
		return nil
	}
	known := knownPresets(a.state)
	for _, p := range known {
		if p == args[0] {
			a.state.NextPreset = p
			a.refusal = "next submit uses preset " + p
			return nil
		}
	}
	a.refusal = fmt.Sprintf("unknown preset %q; presets: %s", args[0], strings.Join(known, ", "))
	return nil
}

func cmdQuick(a *App, args []string) tea.Cmd {
	if len(args) != 1 {
		a.refusal = "usage: /quick <name>|off"
		return nil
	}
	name := args[0]
	if name != "off" {
		if a.state.Status == nil {
			a.refusal = "engine: not connected"
			return nil
		}
		names := make([]string, 0, len(a.state.Status.QuickProfiles))
		for _, qp := range a.state.Status.QuickProfiles {
			names = append(names, qp.Name)
		}
		sort.Strings(names)
		if !slices.Contains(names, name) {
			a.refusal = fmt.Sprintf("unknown quick profile %q; profiles: %s", name, strings.Join(names, ", "))
			return nil
		}
	}
	a.quickSelection.Override = &name
	a.updateQuickProfileState()
	if name == "off" {
		a.refusal = "quick profile off for this process"
	} else {
		a.refusal = "quick profile " + name + " selected for this process"
	}
	return nil
}

func cmdDrafts(a *App, args []string) tea.Cmd {
	if len(args) != 1 || (args[0] != "on" && args[0] != "off") {
		a.refusal = "usage: /drafts on|off"
		return nil
	}
	a.SetDraftsEnabled(args[0] == "on")
	a.refusal = "drafts " + args[0]
	return nil
}

func cmdRaw(a *App, args []string) tea.Cmd {
	if len(args) != 1 || (args[0] != "on" && args[0] != "off") {
		a.refusal = "usage: /raw on|off"
		return nil
	}
	a.rawMode = args[0] == "on"
	a.refusal = "raw " + args[0]
	return nil
}

// commandMsg is the outcome of a command that sent a request: the line to
// show on success, or the engine's refusal.
type commandMsg struct {
	gen  int
	text string
	err  error
}

func (a *App) onCommand(msg commandMsg) (tea.Model, tea.Cmd) {
	if msg.gen != a.gen {
		return a, nil
	}
	line := msg.text
	if msg.err != nil {
		line = formatRefusal(msg.err)
	}
	a.say(line)
	return a, nil
}

// say shows a command's one-line outcome. A line wider than the screen, as an
// engine refusal with many members can be, is shown whole in the panel and
// its first word stays on the message line; while a question owns the panel
// it is only clipped.
func (a *App) say(line string) {
	if a.question != nil || runewidth.StringWidth(line) <= a.width {
		a.refusal = line
		return
	}
	a.panel = []string{line}
	a.refusal = strings.TrimSuffix(strings.Fields(line)[0], ":")
}

func cmdCancel(a *App, _ []string) tea.Cmd {
	if a.state.CurrentTurn == "" {
		a.refusal = "no open turn; nothing sent"
		return nil
	}
	if a.cl == nil {
		a.refusal = "engine: not connected"
		return nil
	}
	gen, cl, turn := a.gen, a.cl, a.state.CurrentTurn
	return func() tea.Msg {
		if err := cl.Cancel(context.Background(), turn); err != nil {
			return commandMsg{gen: gen, err: err}
		}
		return commandMsg{gen: gen, text: "cancel sent for turn " + turn}
	}
}

func cmdOverride(a *App, args []string) tea.Cmd {
	reason := strings.Join(args, " ")
	if reason == "" {
		a.refusal = "usage: /override <reason>"
		return nil
	}
	turn := answerTurn(a.state)
	if turn == "" {
		a.refusal = "no turn to override; nothing sent"
		return nil
	}
	if a.cl == nil {
		a.refusal = "engine: not connected"
		return nil
	}
	gen, cl := a.gen, a.cl
	return func() tea.Msg {
		if err := cl.Override(context.Background(), turn, reason); err != nil {
			return commandMsg{gen: gen, err: err}
		}
		return commandMsg{gen: gen, text: "turn " + turn + " overridden: " + reason}
	}
}

// approvalPrompt is a quick profile selection waiting for yes or no; the panel shows what it does.
type approvalPrompt struct {
	filePath         string
	profileName      string
	fileSHA256       string
	definitionSHA256 string
}

// releasePrompt is a /release waiting for its reason.
type releasePrompt struct {
	pool      string
	attemptID string
}

func cmdRelease(a *App, args []string) tea.Cmd {
	if len(args) < 1 || len(args) > 2 {
		a.refusal = "usage: /release <pool> [attempt_id]"
		return nil
	}
	if a.cl == nil {
		a.refusal = "engine: not connected"
		return nil
	}
	p := &releasePrompt{pool: args[0]}
	target := "pool " + p.pool
	if len(args) == 2 {
		p.attemptID = args[1]
		target += ", attempt " + p.attemptID
	}
	a.confirm = p
	a.panel = []string{
		"release held capacity on " + target + "?",
		"The server may still be computing the held request, and the next",
		"dispatch to this pool may overlap it.",
		"Type a reason and press enter to release; an empty reason or esc cancels.",
	}
	a.setPrompt(reasonPrompt)
	a.refusal = ""
	return nil
}

// cancelConfirm drops a pending confirmation without sending anything, then
// opens any question that waited behind it.
func (a *App) cancelConfirm(why string) {
	a.confirm = nil
	a.panel = nil
	a.setPrompt(defaultPrompt)
	a.clearInput()
	a.refusal = why
	a.nextQuestion()
}

// confirmEnter answers the pending confirmation with the input line.
func (a *App) confirmEnter() tea.Cmd {
	reason := strings.TrimSpace(a.input.Value())
	p := a.confirm
	if reason == "" {
		a.cancelConfirm("release cancelled; nothing sent")
		return nil
	}
	a.cancelConfirm("")
	if a.cl == nil {
		a.refusal = "engine: not connected"
		return nil
	}
	req := wire.ReleaseRequest{Pool: p.pool, Reason: reason}
	if p.attemptID != "" {
		req.AttemptID = &p.attemptID
	}
	gen, cl := a.gen, a.cl
	return func() tea.Msg {
		reply, err := cl.Release(context.Background(), req)
		if err != nil {
			return commandMsg{gen: gen, err: err}
		}
		return commandMsg{gen: gen, text: fmt.Sprintf("released on pool %s: %s", req.Pool, strings.Join(reply.Released, ", "))}
	}
}

// question is a yes/no prompt: its panel shows what is asked, and it owns
// the keyboard until it is answered. Only one is open at a time; one asked
// while another, or a /release confirmation, is open waits behind it.
type question struct {
	lines    []string
	yes, no  func(*App) tea.Cmd // esc answers no
	approval *approvalPrompt    // set when the question is a quick profile approval
	refused  *submission        // set when it asks whether to resend a submit refused as provisional
}

// ask opens the question, or queues it behind the one the user is answering.
func (a *App) ask(q *question) {
	if a.question != nil || a.confirm != nil {
		a.questions = append(a.questions, q)
		return
	}
	a.question = q
	a.panel, a.panelRow = q.lines, 0
	a.setPrompt(yesNoPrompt)
	a.refusal = ""
}

// answer closes the open question, runs the chosen answer, then opens the
// next question waiting. The input line keeps what the user had typed.
func (a *App) answer(yes bool) tea.Cmd {
	q := a.question
	a.closeQuestion("")
	var cmd tea.Cmd
	if yes {
		cmd = q.yes(a)
	} else {
		cmd = q.no(a)
	}
	a.nextQuestion()
	return cmd
}

// closeQuestion takes the open question down without answering it.
func (a *App) closeQuestion(why string) {
	a.question = nil
	a.panel = nil
	a.setPrompt(defaultPrompt)
	a.refusal = why
}

// nextQuestion opens the first question waiting, if nothing else is open.
func (a *App) nextQuestion() {
	if a.question != nil || a.confirm != nil || len(a.questions) == 0 {
		return
	}
	q := a.questions[0]
	a.questions = a.questions[1:]
	why := a.refusal
	a.ask(q)
	a.refusal = why // what the last answer did stays visible
}

// pendingApproval is the quick profile approval open or waiting, if any.
func (a *App) pendingApproval() *approvalPrompt {
	if a.question != nil && a.question.approval != nil {
		return a.question.approval
	}
	for _, q := range a.questions {
		if q.approval != nil {
			return q.approval
		}
	}
	return nil
}

// cancelApproval drops a pending approval, open or waiting, without approving.
func (a *App) cancelApproval() {
	a.questions = slices.DeleteFunc(a.questions, func(q *question) bool { return q.approval != nil })
	if a.question != nil && a.question.approval != nil {
		a.closeQuestion("")
		a.nextQuestion()
	}
}

// approvalYes records the approval and puts the selection into effect.
func approvalYes(ap *approvalPrompt) func(*App) tea.Cmd {
	return func(a *App) tea.Cmd {
		qs := a.quickSelection
		err := qs.approve(ap.definitionSHA256)
		if err != nil {
			qs.declined[qs.stateKey(ap.definitionSHA256)] = true // do not ask again for what cannot be saved
		}
		a.updateQuickProfileState()
		if err != nil {
			a.refusal = fmt.Sprintf("quick: cannot save approval: %v", err)
		} else {
			a.refusal = fmt.Sprintf("quick profile %s approved for this project", ap.profileName)
		}
		return nil
	}
}

// approvalNo leaves the selection unapproved; it is not asked again in this process for this state.
func approvalNo(ap *approvalPrompt) func(*App) tea.Cmd {
	return func(a *App) tea.Cmd {
		a.refusal = "quick profile not approved; it will not be sent"
		a.quickSelection.declined[a.quickSelection.stateKey(ap.definitionSHA256)] = true
		return nil
	}
}

// whyMsg carries the turn record /why asked for.
type whyMsg struct {
	gen  int
	id   string
	turn *wire.TurnReply
	err  error
}

func cmdWhy(a *App, _ []string) tea.Cmd {
	id := answerTurn(a.state)
	if id == "" {
		a.refusal = "no turn yet"
		return nil
	}
	// Whether the turn has ended is read from the engine's turn record, not
	// from local state: the turn_state event and an earlier turn reply can
	// arrive in either order.
	if a.cl == nil {
		a.refusal = "engine: not connected"
		return nil
	}
	gen, cl := a.gen, a.cl
	return func() tea.Msg { return whyFetch(gen, cl, id) }
}

func whyFetch(gen int, cl *client.Client, id string) tea.Msg {
	tr, err := cl.Turn(context.Background(), id)
	if err != nil {
		return whyMsg{gen: gen, id: id, err: err}
	}
	return whyMsg{gen: gen, id: id, turn: &tr}
}

func (a *App) onWhy(msg whyMsg) (tea.Model, tea.Cmd) {
	if msg.gen != a.gen {
		return a, nil
	}
	if a.question != nil {
		return a, nil // the open question owns the panel
	}
	if msg.err != nil {
		a.refusal = "why: " + formatRefusal(msg.err)
		return a, nil
	}
	if !isTerminalState(msg.turn.State) {
		a.panel = []string{"turn " + msg.id + " is still running", "state: " + msg.turn.State}
		return a, nil
	}
	a.panel = whyLines(msg.id, msg.turn, a.state.LastBudgetNotice)
	return a, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func figure(n *int64) string {
	if n == nil {
		return "-"
	}
	return fmt.Sprint(*n)
}

// whyLines says why a finished turn ended, from its record: the terminal
// reason verbatim, and the gate, parser, reviewer or budget detail behind it.
func whyLines(id string, tr *wire.TurnReply, notice *BudgetNotice) []string {
	lines := whyLinesRaw(id, tr, notice)
	for i, l := range lines {
		lines[i] = safeLine(l)
	}
	return lines
}

func whyLinesRaw(id string, tr *wire.TurnReply, notice *BudgetNotice) []string {
	reason := deref(tr.TerminalReason)
	lines := []string{fmt.Sprintf("turn %s: %s", id, tr.State), "reason: " + reason}

	participantOf := map[string]string{}
	for _, at := range tr.Attempts {
		participantOf[at.ID] = at.Participant
	}
	reasons := func(p string) {
		for _, v := range tr.Verdicts {
			if participantOf[v.AttemptID] == p && v.Reason != "" {
				lines = append(lines, "reviewer reason ("+p+"): "+v.Reason)
			}
		}
	}
	classes := func(p string) string {
		var out []string
		for _, v := range tr.Verdicts {
			if participantOf[v.AttemptID] == p {
				out = append(out, v.ValidationStatus)
			}
		}
		return strings.Join(out, ", ")
	}
	lastAttempt := func(p string) *wire.AttemptRecord {
		var last *wire.AttemptRecord
		for i := range tr.Attempts {
			if tr.Attempts[i].Participant == p {
				last = &tr.Attempts[i]
			}
		}
		return last
	}
	failed := func(p, rest string) {
		if gate, ok := strings.CutPrefix(rest, "gate:"); ok {
			lines = append(lines, "gate error ("+p+"): "+gate)
		}
		if at := lastAttempt(p); at != nil {
			lines = append(lines, fmt.Sprintf("attempt %s (%s %s): status %s, transport %s", at.ID, p, at.Phase, at.Status, at.TransportStatus))
		}
		if c := classes(p); c != "" {
			lines = append(lines, "parser classification ("+p+"): "+c)
		}
	}

	switch {
	case strings.HasPrefix(reason, "author_failed:"):
		failed("author", strings.TrimPrefix(reason, "author_failed:"))
	case strings.HasPrefix(reason, "reviewer_failed:"):
		if p, rest, ok := strings.Cut(strings.TrimPrefix(reason, "reviewer_failed:"), ":"); ok {
			failed(p, rest)
		}
	case strings.HasPrefix(reason, "malformed_twice:"), strings.HasPrefix(reason, "revision_request:"):
		_, p, _ := strings.Cut(reason, ":")
		lines = append(lines, "parser classification ("+p+"): "+classes(p))
		reasons(p)
	case strings.HasPrefix(reason, "block:"):
		for _, b := range tr.OpenBlocks {
			lines = append(lines, fmt.Sprintf("block %s [%s]", b.ID, b.Severity), "  evidence: "+b.Evidence, "  remedy: "+b.Remedy)
		}
		for _, v := range tr.Verdicts {
			if v.Reason != "" {
				lines = append(lines, "reviewer reason ("+participantOf[v.AttemptID]+"): "+v.Reason)
			}
		}
	case strings.HasPrefix(reason, "budget:"):
		name := reason
		if notice != nil && notice.TurnID == id {
			name = notice.Kind
			line := "budget " + name
			if notice.Participant != nil {
				line += " (" + *notice.Participant + ")"
			}
			line += ": "
			if notice.Admitted != nil {
				line += fmt.Sprintf("admission %d, ", *notice.Admitted)
			}
			if notice.Estimated != nil {
				line += fmt.Sprintf("estimated %d, ", *notice.Estimated)
			}
			lines = append(lines, line+fmt.Sprintf("limit %d", notice.Limit))
		} else {
			lines = append(lines, "budget "+name)
		}
		b := tr.Budget
		lines = append(lines, fmt.Sprintf("tokens: reserved %s, charged %s, limit %s (projected %s)", figure(b.Reserved), figure(b.Charged), figure(b.Limit), figure(b.Projected)))
	}
	return lines
}
