package tui

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/boldfield/syndic/internal/client"
	"github.com/boldfield/syndic/internal/wire"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// engineMaxRounds is the most revisions any turn may have; no roster raises
// it (v5 delta section 4).
const engineMaxRounds = 5

// openConversation makes the chat show the conversation id, which holds the
// turns with the given ids, oldest first; an empty id is a new conversation
// that the next submit creates. The trim point is the old conversation's, so
// it is dropped. A running turn is left alone: it keeps running wherever it
// belongs, and the status line still shows it.
func (a *App) openConversation(id string, turnIDs []string) {
	a.conversationID = id
	a.convTurns = len(turnIDs)
	a.openTurns = turnIDs
	scope := &TurnScope{ConversationID: id, Turns: map[string]bool{}, Submissions: map[string]bool{}}
	for _, t := range turnIDs {
		scope.add(t)
	}
	a.state.Scope = scope
	a.state.ContextFrom = ""
	a.chatFollow.End()
}

// runningElsewhere names the open turn when it belongs to a conversation
// other than the one the chat shows, or says nothing.
func (a *App) runningElsewhere() string {
	turn := a.state.CurrentTurn
	if turn == "" || a.state.inScope(turn) {
		return ""
	}
	if conv := a.state.TurnConversation[turn]; conv != "" {
		return fmt.Sprintf("; turn %s keeps running in conversation %s", shortID(turn), shortID(conv))
	}
	return fmt.Sprintf("; turn %s keeps running", shortID(turn))
}

// openTurnOwner names the conversation the open turn belongs to, for a
// submit refused as busy.
func (a *App) openTurnOwner(we *wire.Error) string {
	turn := a.state.CurrentTurn
	if we.TurnID != nil && *we.TurnID != "" {
		turn = *we.TurnID
	}
	if turn == "" && a.state.Status != nil && len(a.state.Status.TurnsOpen) > 0 {
		turn = a.state.Status.TurnsOpen[0]
	}
	if turn == "" {
		return ""
	}
	switch conv := a.state.TurnConversation[turn]; conv {
	case "":
		return fmt.Sprintf("; turn %s is open", shortID(turn))
	case a.conversationID:
		return fmt.Sprintf("; turn %s is open in this conversation", shortID(turn))
	default:
		return fmt.Sprintf("; the open turn %s belongs to conversation %s", shortID(turn), shortID(conv))
	}
}

func cmdNew(a *App, _ []string) tea.Cmd {
	a.openConversation("", nil)
	a.say("new conversation; the next submit creates it" + a.runningElsewhere())
	return nil
}

// switchMsg carries the conversation /switch resolved, or why it cannot be opened.
type switchMsg struct {
	gen  int
	conv wire.ConversationReply
	err  error
}

func cmdSwitch(a *App, args []string) tea.Cmd {
	if len(args) != 1 {
		a.refusal = "usage: /switch <id>"
		return nil
	}
	if a.cl == nil {
		a.refusal = "engine: not connected"
		return nil
	}
	gen, cl, ctx, prefix, ws := a.gen, a.cl, a.ctx, args[0], a.workDir
	return func() tea.Msg {
		conv, err := resolveConversation(ctx, cl, prefix, ws)
		return switchMsg{gen: gen, conv: conv, err: err}
	}
}

// resolveConversation resolves prefix, any unique prefix of a conversation
// id, as --conversation does: an unknown or ambiguous prefix, or a
// conversation whose workspace is another directory, is refused. A null
// workspace matches any directory.
func resolveConversation(ctx context.Context, cl *client.Client, prefix, workspace string) (wire.ConversationReply, error) {
	conv, err := cl.Conversation(ctx, prefix)
	if err != nil {
		return conv, err
	}
	var found *wire.ConversationSummary
	err = eachConversation(ctx, cl, nil, func(c wire.ConversationSummary) bool {
		if c.ID == conv.ID {
			found = &c
		}
		return found == nil
	})
	switch {
	case err != nil:
		return conv, err
	case found == nil:
		return conv, fmt.Errorf("conversation %q is not listed", prefix)
	case found.Workspace != nil && *found.Workspace != workspace:
		return conv, fmt.Errorf("conversation %s belongs to %s, not %s", shortID(conv.ID), *found.Workspace, workspace)
	}
	return conv, nil
}

// eachConversation calls visit for every listed conversation, following
// next_cursor, until visit returns false or the list ends.
func eachConversation(ctx context.Context, cl *client.Client, workspace *string, visit func(wire.ConversationSummary) bool) error {
	cursor := ""
	for {
		reply, err := cl.Conversations(ctx, workspace, cursor, wire.MaxConversationListSize)
		if err != nil {
			return err
		}
		for _, c := range reply.Items {
			if !visit(c) {
				return nil
			}
		}
		if reply.NextCursor == nil || *reply.NextCursor == "" {
			return nil
		}
		cursor = *reply.NextCursor
	}
}

func (a *App) onSwitch(msg switchMsg) (tea.Model, tea.Cmd) {
	if msg.gen != a.gen {
		return a, nil
	}
	if msg.err != nil {
		a.say("switch: " + formatRefusal(msg.err) + "; not switched")
		return a, nil
	}
	ids := make([]string, 0, len(msg.conv.Turns))
	for _, t := range msg.conv.Turns {
		ids = append(ids, t.ID)
	}
	a.openConversation(msg.conv.ID, ids)
	// The chat shows every turn of the conversation, so each turn's record is read.
	var cmds []tea.Cmd
	a.applyReviewCounts(msg.conv)
	for _, t := range msg.conv.Turns {
		if _, ok := a.state.Turns[t.ID]; !ok {
			cmds = append(cmds, turnCmd(a.ctx, a.gen, a.cl, t.ID))
		}
	}
	a.say(fmt.Sprintf("switched to conversation %s, %d turns", shortID(msg.conv.ID), len(ids)) + a.runningElsewhere())
	return a, tea.Batch(cmds...)
}

// conversationsMsg carries the list /conversations asked for.
type conversationsMsg struct {
	gen   int
	epoch int // which opening of the list asked
	items []wire.ConversationSummary
	err   error
}

// cmdConversations opens the list and reads every directory's conversations;
// those of another directory are shown dimmed and cannot be chosen.
func cmdConversations(a *App, _ []string) tea.Cmd {
	if a.cl == nil {
		a.refusal = "engine: not connected"
		return nil
	}
	a.convEpoch++
	a.conv = &convChooser{sel: -1, epoch: a.convEpoch}
	gen, epoch, cl, ctx := a.gen, a.convEpoch, a.cl, a.ctx
	return func() tea.Msg {
		var items []wire.ConversationSummary
		err := eachConversation(ctx, cl, nil, func(c wire.ConversationSummary) bool {
			items = append(items, c)
			return true
		})
		return conversationsMsg{gen: gen, epoch: epoch, items: items, err: err}
	}
}

// onConversations fills the list once. A list closed meanwhile stays closed, and a reply to an earlier opening is dropped.
func (a *App) onConversations(msg conversationsMsg) (tea.Model, tea.Cmd) {
	if msg.gen != a.gen || a.conv == nil || a.conv.loaded || msg.epoch != a.conv.epoch {
		return a, nil
	}
	a.conv.loaded, a.conv.err, a.conv.items = true, msg.err, msg.items
	a.resetSel()
	return a, nil
}

// activity shows a last_submitted_at time in local time, to the minute.
func activity(at string) string {
	t, err := time.Parse(time.RFC3339, at)
	switch {
	case at == "":
		return "-"
	case err != nil:
		return at
	}
	return t.Local().Format("2006-01-02 15:04")
}

// conversationTurns is the open conversation's turns, oldest first.
func (a *App) conversationTurns() []string {
	if a.state.Scope != nil {
		return a.state.Scope.Order
	}
	return chatTurns(a.state)
}

// resolveTurn finds the turn of ids that prefix names: its id, or a unique prefix of it.
func resolveTurn(ids []string, prefix string) (string, error) {
	var found []string
	for _, id := range ids {
		if id == prefix {
			return id, nil
		}
		if strings.HasPrefix(id, prefix) {
			found = append(found, id)
		}
	}
	switch len(found) {
	case 0:
		return "", fmt.Errorf("no turn %q in this conversation", prefix)
	case 1:
		return found[0], nil
	}
	return "", fmt.Errorf("turn prefix %q is ambiguous: %d turns match", prefix, len(found))
}

// turnInputLine is the first line of what the user typed for the turn.
func (a *App) turnInputLine(id string) string {
	text, ok := a.inputs[id]
	if !ok {
		ref := a.state.Turns[id].InputRef
		if ref == "" {
			return "(input not loaded)"
		}
		text = bodyRaw(a.content, ref)
	}
	return safeLine(firstLine(text))
}

func cmdTrim(a *App, args []string) tea.Cmd {
	if len(args) != 1 {
		a.refusal = "usage: /trim <turn>"
		return nil
	}
	ids := a.conversationTurns()
	if len(ids) == 0 {
		a.refusal = "this conversation has no turns; nothing to trim"
		return nil
	}
	from, err := resolveTurn(ids, args[0])
	if err != nil {
		a.refusal = "trim: " + err.Error()
		return nil
	}
	a.ask(&question{
		lines: a.trimPreview(ids, from),
		yes: func(a *App) tea.Cmd {
			a.state.ContextFrom = from
			a.refusal = fmt.Sprintf("later submits in this conversation start their history at turn %s", shortID(from))
			return nil
		},
		no: func(a *App) tea.Cmd {
			a.refusal = "trim cancelled; the history is unchanged"
			return nil
		},
	})
	return nil
}

// trimPreview shows what a trim to from drops and what the history of later
// submits still includes: the turns before from, and from onward.
func (a *App) trimPreview(ids []string, from string) []string {
	var dropped, kept []string
	for i, id := range ids {
		if id == from {
			dropped, kept = ids[:i], ids[i:]
			break
		}
	}
	turn := func(id string) string {
		return ansi.Truncate(fmt.Sprintf("  %s  you: %s", shortID(id), a.turnInputLine(id)), max(a.width, 1), "…")
	}
	lines := []string{
		fmt.Sprintf("trim to turn %s: later history drops %d turns, keeps %d. Trim? (y/n)", shortID(from), len(dropped), len(kept)),
		"",
		"dropped (" + notInContext + "):",
	}
	if len(dropped) == 0 {
		lines = append(lines, "  none")
	}
	for _, id := range dropped {
		lines = append(lines, turn(id))
	}
	lines = append(lines, "kept in the history:")
	for _, id := range kept {
		lines = append(lines, turn(id))
	}
	return lines
}

// setRounds sets the round limit this process sends with every submit.
func (a *App) setRounds(n int, source string) {
	a.rounds, a.roundsIsSet, a.roundsSource = n, true, source
	a.state.RoundLimit = &n
}

// roundsCeiling is the most rounds a submit may ask for under the preset in
// force, and where that bound comes from.
func (a *App) roundsCeiling() (int, string) {
	if a.state.Status == nil {
		return engineMaxRounds, "the engine's limit; the roster's is not known until connected"
	}
	return a.state.Status.Revision.Ceiling, "preset " + a.state.Status.DefaultPreset
}

// roundsLine shows the round limit in force, where it came from, and the ceiling.
func (a *App) roundsLine() string {
	ceiling, bound := a.roundsCeiling()
	var value string
	switch {
	case a.roundsIsSet:
		value = fmt.Sprintf("%d (set by %s)", a.rounds, a.roundsSource)
	case a.state.Status != nil:
		value = fmt.Sprintf("%d (the roster default for preset %s)", a.state.Status.Revision.Default, a.state.Status.DefaultPreset)
	default:
		value = "the roster default, not known until connected"
	}
	return fmt.Sprintf("rounds: %s; ceiling %d (%s)", value, ceiling, bound)
}

func cmdRounds(a *App, args []string) tea.Cmd {
	switch len(args) {
	case 0:
		a.say(a.roundsLine())
		return nil
	case 1:
	default:
		a.refusal = "usage: /rounds [n]"
		return nil
	}
	ceiling, bound := a.roundsCeiling()
	n, err := strconv.Atoi(args[0])
	if err != nil || n < 0 || n > ceiling {
		a.say(fmt.Sprintf("rounds: %q is not a whole number from 0 to the ceiling %d (%s); unchanged", args[0], ceiling, bound))
		return nil
	}
	a.setRounds(n, "/rounds")
	a.say(fmt.Sprintf("rounds set to %d for this process; ceiling %d (%s)", n, ceiling, bound))
	return nil
}

func cmdBudget(a *App, _ []string) tea.Cmd {
	id := answerTurn(a.state)
	if id == "" {
		a.refusal = "no turn yet; no budget to show"
		return nil
	}
	if a.cl == nil {
		a.refusal = "engine: not connected"
		return nil
	}
	gen, cl, ctx := a.gen, a.cl, a.ctx
	return func() tea.Msg {
		tr, err := cl.Turn(ctx, id)
		if err != nil {
			return commandMsg{gen: gen, err: err}
		}
		return commandMsg{gen: gen, text: budgetLine(id, tr.Budget)}
	}
}

// budgetLine shows a turn's tokens reserved, charged and the limit; a figure
// the record does not carry shows as "-".
func budgetLine(id string, b wire.BudgetRecord) string {
	return fmt.Sprintf("budget of turn %s: reserved %s, charged %s, limit %s tokens", shortID(id), figure(b.Reserved), figure(b.Charged), figure(b.Limit))
}

// reviewCountsMsg carries the conversation a review_count refresh read.
type reviewCountsMsg struct {
	gen  int
	conv wire.ConversationReply
	err  error
}

// applyReviewCounts records each turn's review_count from the conversation.
func (a *App) applyReviewCounts(conv wire.ConversationReply) {
	if a.reviewCounts == nil {
		a.reviewCounts = map[string]*int{}
	}
	if a.reviewCountFinal == nil {
		a.reviewCountFinal = map[string]bool{}
	}
	for _, t := range conv.Turns {
		a.reviewCounts[t.ID] = t.ReviewCount
		// A count read from a terminal turn no longer changes.
		a.reviewCountFinal[t.ID] = isTerminalState(t.State)
	}
}

// refreshReviewCounts reads the open conversation again when a turn has
// reached a terminal state and its review_count is not known yet.
func (a *App) refreshReviewCounts(turnID string) tea.Cmd {
	if a.cl == nil || a.conversationID == "" || !isTerminalState(a.state.Turns[turnID].State) {
		return nil
	}
	if a.reviewCountFinal[turnID] {
		return nil
	}
	gen, cl, ctx, id := a.gen, a.cl, a.ctx, a.conversationID
	return func() tea.Msg {
		conv, err := cl.Conversation(ctx, id)
		return reviewCountsMsg{gen: gen, conv: conv, err: err}
	}
}

func (a *App) onReviewCounts(msg reviewCountsMsg) (tea.Model, tea.Cmd) {
	if msg.gen != a.gen || msg.err != nil || msg.conv.ID != a.conversationID {
		return a, nil
	}
	a.applyReviewCounts(msg.conv)
	return a, nil
}
