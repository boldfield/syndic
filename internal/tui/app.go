package tui

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/boldfield/syndic/internal/client"
	"github.com/boldfield/syndic/internal/wire"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

type tab int

const (
	tabAnswer tab = iota
	tabTranscript
	tabParticipants
	numTabs
)

// inputHeight is the bottom region: a refusal line and the input line.
const inputHeight = 2

// App is the root Bubble Tea model. All of its state changes in Update; the
// commands it starts only carry results back as messages.
type App struct {
	ctx    context.Context
	cancel context.CancelFunc
	socket string

	// gen numbers connection attempts. A message from an older one is stale.
	gen int
	cl  *client.Client

	width, height int
	state         State
	tab           tab
	input         textinput.Model
	refusal       string         // the one-line message under the body: a refusal, or what a command did
	panel         []string       // lines shown in place of the tab until dismissed: /help, /why, the release warning
	confirm       *releasePrompt // a /release waiting for its reason; it takes the next line typed

	statusRefreshPending bool
	statusDirty          bool // a trigger arrived while a refresh was in flight
	now                  func() time.Time
	tickRunning          bool
	onEvent              func(wire.Frame)

	content           *ContentCache
	drafts            *bool // the user's drafts toggle; nil until set, then it overrides the quick profile
	answerScrollRow   int   // first visible line of the answer tab
	candidateIDByTurn map[string]string
	openBlocksByTurn  map[string][]wire.OpenBlock // open blocks from turn replies
	overrideReasons   map[string]string           // the reason of each override event

	// The transcript tab: the loaded items of one turn, their rendering
	// at the current width, and the scroll position within it.
	tTurn       string
	tItems      []wire.TranscriptItem
	tLoading    bool // a load is in flight
	tReload     bool // a trigger arrived while it was
	tStale      bool // tLines no longer matches what is known
	tLines      []string
	tRow        int  // first visible line
	tFollow     bool // the view stays at the bottom as lines arrive, until the user scrolls away
	tHad        bool // the lines last drawn had items, every body loaded and no reload pending
	tHome       bool // the user pressed Home, so the view stays at the top
	quarantined map[string]bool
	blockDetail map[string]wire.OpenBlock // evidence and remedy of each block seen in a turn reply
	pSelected   int                       // index of the selected row of the participants tab, in id order
	pScrolled   bool                      // the participants tab shows its full content from pScroll, not fitted around the selection
	pScroll     int                       // first line shown while pScrolled
}

// Option configures an App.
type Option func(*App)

// WithEventObserver calls f for every event frame the App applies, in order.
func WithEventObserver(f func(wire.Frame)) Option {
	return func(a *App) { a.onEvent = f }
}

// NewApp returns an App that talks to the engine socket at socketPath.
func NewApp(ctx context.Context, socketPath string, opts ...Option) *App {
	ctx, cancel := context.WithCancel(ctx)
	a := &App{
		ctx:               ctx,
		cancel:            cancel,
		socket:            socketPath,
		input:             newInput(),
		width:             80,
		height:            24,
		state:             NewState(),
		now:               time.Now,
		content:           NewContentCache(),
		candidateIDByTurn: make(map[string]string),
		openBlocksByTurn:  make(map[string][]wire.OpenBlock),
		overrideReasons:   make(map[string]string),
		quarantined:       make(map[string]bool),
		blockDetail:       make(map[string]wire.OpenBlock),
	}
	a.state.ConnState = ConnStateConnecting
	for _, o := range opts {
		o(a)
	}
	return a
}

// CurrentTurn is the open turn the UI knows of, or "".
func (a *App) CurrentTurn() string { return a.state.CurrentTurn }

// SetDraftsEnabled sets the drafts toggle.
func (a *App) SetDraftsEnabled(enabled bool) { a.drafts = &enabled }

// draftsOn reports whether the author's unreviewed output is shown for the
// turn: the user's toggle if set, else the turn's quick profile.
func (a *App) draftsOn(turnID string) bool {
	if a.drafts != nil {
		return *a.drafts
	}
	if ti, ok := a.state.Turns[turnID]; ok && ti.QuickProfile != nil {
		return a.state.QuickProfiles[*ti.QuickProfile].Drafts
	}
	return false
}

// answerIn gathers what the answer tab needs for the turn.
func (a *App) answerIn(turnID string) answerInput {
	return answerInput{
		content:        a.content,
		drafts:         a.draftsOn(turnID),
		candidateID:    a.candidateIDByTurn[turnID],
		openBlocks:     a.openBlocksByTurn[turnID],
		overrideReason: a.overrideReasons[turnID],
	}
}

// fetchAnswerContent starts the fetches the answer tab is waiting on.
func (a *App) fetchAnswerContent() tea.Cmd {
	turnID := answerTurn(a.state)
	return tea.Batch(fetchRefs(a.ctx, a.cl, a.content, answerRefs(a.state, turnID, a.answerIn(turnID)))...)
}

// transcriptIn gathers what the transcript tab needs.
func (a *App) transcriptIn() transcriptInput {
	return transcriptInput{items: a.tItems, content: a.content, quarantined: a.quarantined, blockDetail: a.blockDetail, reloading: a.tLoading || a.tReload}
}

// fetchTranscriptContent starts the fetches the transcript tab is waiting on.
func (a *App) fetchTranscriptContent() tea.Cmd {
	return tea.Batch(fetchRefs(a.ctx, a.cl, a.content, transcriptRefs(a.tItems))...)
}

// syncTranscript points the transcript at the turn the UI is showing, and
// starts loading it when that changes.
func (a *App) syncTranscript() tea.Cmd {
	t := answerTurn(a.state)
	if t == a.tTurn {
		return nil
	}
	a.tTurn, a.tItems = t, nil
	a.tRow, a.tFollow, a.tHome, a.tStale = 0, false, false, true
	a.tLines, a.tHad = nil, false
	return a.reloadTranscript()
}

// reloadTranscript loads the transcript again, or notes that it must be once
// the load in flight ends.
func (a *App) reloadTranscript() tea.Cmd {
	if a.cl == nil || a.tTurn == "" {
		return nil
	}
	if a.tLoading {
		a.tReload = true
		return nil
	}
	a.tLoading = true
	return transcriptCmd(a.ctx, a.gen, a.cl, a.tTurn)
}

// onTranscript replaces the loaded items with a complete reload.
func (a *App) onTranscript(msg transcriptMsg) (tea.Model, tea.Cmd) {
	if msg.gen != a.gen {
		return a, nil
	}
	a.tLoading = false
	if msg.err != nil {
		a.refusal = "transcript: " + formatRefusal(msg.err)
		a.tReload = false
		return a, nil
	}
	if msg.turn == a.tTurn {
		a.tItems = msg.items
	}
	if a.tReload || msg.turn != a.tTurn {
		a.tReload = false
		return a, a.reloadTranscript()
	}
	return a, nil
}

// transcriptTrigger reloads the transcript when an event concerns the turn it shows.
func (a *App) transcriptTrigger(turnID string) tea.Cmd {
	cmd := a.syncTranscript()
	if turnID != "" && turnID == a.tTurn {
		return tea.Batch(cmd, a.reloadTranscript())
	}
	return cmd
}

// refreshTranscript renders the tab again when something it shows changed, and
// keeps the view where it was, or at the bottom if it was there.
func (a *App) refreshTranscript() {
	if a.tab != tabTranscript || !a.tStale {
		return
	}
	a.tStale = false
	// Already at the bottom of what was shown: follow from here on, through
	// the frames that wait on the new bodies, until the user scrolls away.
	if !a.tHome && a.tHad && a.tRow >= a.maxTranscriptRow() {
		a.tFollow = true
	}
	a.tLines = transcriptLines(a.state, a.tTurn, a.transcriptIn(), a.width)
	a.tRow = a.clampTranscriptRow(a.tRow)
	// A frame still waiting on bodies, or on a newer snapshot of the items, is
	// not a bottom the user chose, so following never starts from one.
	a.tHad = len(a.tItems) > 0 && !a.tLoading && !a.tReload && !transcriptPending(a.tItems, a.content)
	if a.tFollow {
		a.tRow = a.maxTranscriptRow()
	}
}

func (a *App) maxTranscriptRow() int { return max(len(a.tLines)-a.bodyHeight(), 0) }

func (a *App) clampTranscriptRow(row int) int { return min(max(row, 0), a.maxTranscriptRow()) }

// scrollTranscript handles a scroll key; the view follows new lines only when
// it ends at the bottom.
func (a *App) scrollTranscript(key string) {
	page := max(a.bodyHeight()-1, 1)
	switch key {
	case "up":
		a.tRow--
	case "down":
		a.tRow++
	case "pgup":
		a.tRow -= page
	case "pgdown":
		a.tRow += page
	case "home":
		a.tRow = 0
	case "end":
		a.tRow = a.maxTranscriptRow()
	}
	a.tRow = a.clampTranscriptRow(a.tRow)
	a.tHome = key == "home" || (a.tHome && a.tRow == 0)
	a.tFollow = !a.tHome && a.tRow >= a.maxTranscriptRow()
}

// Init starts the first connection, from the beginning of the journal.
func (a *App) Init() tea.Cmd {
	return tea.Batch(textinput.Blink, connectCmd(a.ctx, a.gen, a.socket, 0))
}

// Update handles one message, then starts any content fetch the answer tab
// now needs. A content reply itself starts none, so a failing fetch is retried
// on the next event or tick, not in a loop.
func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	_, cmd := a.update(msg)
	cmds := []tea.Cmd{cmd, a.syncTranscript()}
	switch m := msg.(type) {
	case tea.KeyMsg:
		a.tStale = a.tStale || m.String() == "tab" || m.String() == "shift+tab"
	case tickMsg:
	default:
		a.tStale = true
	}
	a.refreshTranscript()
	if _, ok := msg.(contentMsg); !ok {
		cmds = append(cmds, a.fetchAnswerContent(), a.fetchTranscriptContent())
	}
	return a, tea.Batch(cmds...)
}

func (a *App) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.width, a.height = msg.Width, msg.Height
		return a, nil
	case tea.KeyMsg:
		return a.updateKey(msg)
	case connectedMsg:
		return a.onConnected(msg)
	case eventMsg:
		return a.onFrame(msg)
	case submitMsg:
		return a, a.onSubmit(msg)
	case statusMsg:
		return a.onStatusRefresh(msg)
	case turnMsg:
		return a.onTurn(msg)
	case commandMsg:
		return a.onCommand(msg)
	case whyMsg:
		return a.onWhy(msg)
	case contentMsg:
		return a.onContent(msg)
	case transcriptMsg:
		return a.onTranscript(msg)
	case tickMsg:
		// The message itself re-renders; keep the one tick chain going while a turn is open.
		a.tickRunning = false
		return a, a.ensureTick()
	}
	var cmd tea.Cmd
	a.input, cmd = a.input.Update(msg)
	return a, cmd
}

func (a *App) updateKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if a.confirm != nil {
		// A confirmation owns the keyboard: the line is its answer.
		switch msg.String() {
		case "ctrl+c":
			return a, a.quit()
		case "esc":
			a.cancelConfirm("release cancelled; nothing sent")
			return a, nil
		case "enter":
			return a, a.confirmEnter()
		}
		var cmd tea.Cmd
		a.input, cmd = a.input.Update(msg)
		return a, cmd
	}
	switch msg.String() {
	case "ctrl+c":
		return a, a.quit()
	case "esc":
		a.panel, a.refusal = nil, ""
		return a, nil
	case "tab":
		a.panel = nil
		a.tab = (a.tab + 1) % numTabs
		a.answerScrollRow = 0
		return a, nil
	case "shift+tab":
		a.panel = nil
		a.tab = (a.tab + numTabs - 1) % numTabs
		a.answerScrollRow = 0
		return a, nil
	case "enter":
		return a, a.enter()
	}

	if a.tab == tabTranscript {
		switch msg.String() {
		case "up", "down", "pgup", "pgdown", "home", "end":
			a.scrollTranscript(msg.String())
			return a, nil
		}
	}
	if a.tab == tabAnswer {
		page := max(a.bodyHeight()-1, 1)
		switch msg.String() {
		case "up":
			a.scrollAnswer(-1)
			return a, nil
		case "down":
			a.scrollAnswer(1)
			return a, nil
		case "pgup":
			a.scrollAnswer(-page)
			return a, nil
		case "pgdown":
			a.scrollAnswer(page)
			return a, nil
		}
	}

	if a.tab == tabParticipants {
		switch msg.String() {
		case "up":
			a.pSelected = max(a.selectedParticipant()-1, 0)
			a.pScrolled = false
			return a, nil
		case "down":
			a.pSelected = a.selectedParticipant() + 1
			a.pScrolled = false
			return a, nil
		case "pgup", "pgdown":
			// Page through the full content, which the fitted view cuts: the
			// pools and orphaned holds below the detail.
			page := max(a.bodyHeight()-1, 1)
			if msg.String() == "pgup" {
				page = -page
			}
			if !a.pScrolled {
				a.pScrolled, a.pScroll = true, 0
			}
			last := a.participantsMaxScroll()
			a.pScroll = min(max(min(a.pScroll, last)+page, 0), last)
			if a.pScroll == 0 && page < 0 {
				a.pScrolled = false
			}
			return a, nil
		}
	}

	var cmd tea.Cmd
	a.input, cmd = a.input.Update(msg)
	return a, cmd
}

// enter runs a command or submits the line. A command clears the line; a
// submit clears it only once the engine accepts it.
func (a *App) enter() tea.Cmd {
	if a.confirm != nil {
		return a.confirmEnter()
	}
	line := a.input.Value()
	if strings.TrimSpace(line) == "" {
		return nil
	}
	a.refusal, a.panel = "", nil
	if isCommand(line) {
		a.input.SetValue("")
		return dispatch(a, line)
	}
	if a.cl == nil {
		a.refusal = "engine: not connected"
		return nil
	}
	return submitCmd(a.ctx, a.cl, line, a.state.NextPreset)
}

// ensureTick starts the one-second tick chain if a turn is open and none runs.
func (a *App) ensureTick() tea.Cmd {
	if a.state.CurrentTurn == "" || a.tickRunning {
		return nil
	}
	a.tickRunning = true
	return tickCmd()
}

// ensureTurnInfo makes sure the open turn has a TurnInfo for the strip to show.
func (a *App) ensureTurnInfo() {
	id := a.state.CurrentTurn
	if id == "" {
		return
	}
	if _, ok := a.state.Turns[id]; ok {
		return
	}
	turns := copyMap(a.state.Turns)
	state := a.state.TurnState[id]
	if state == "" {
		state = "submitted"
	}
	turns[id] = TurnInfo{ID: id, State: state, Preset: a.state.Run.DefaultPreset, ReviewerStates: map[string]string{}}
	a.state.Turns = turns
}

func (a *App) onSubmit(msg submitMsg) tea.Cmd {
	if msg.refusal != "" {
		a.refusal = msg.refusal
		return nil
	}
	a.input.SetValue("")
	a.state.NextPreset = "" // /mode applies to one submit
	if isTerminalState(a.state.TurnState[msg.turnID]) {
		return nil
	}
	oldTurn := a.state.CurrentTurn
	a.state.CurrentTurn = msg.turnID
	if oldTurn != msg.turnID {
		a.answerScrollRow = 0
	}
	a.ensureTurnInfo()
	return a.ensureTick()
}

func (a *App) onConnected(msg connectedMsg) (tea.Model, tea.Cmd) {
	if msg.gen != a.gen {
		_ = msg.cl.Close()
		return a, nil
	}
	a.cl = msg.cl
	a.state.Hello = msg.cl.Hello()
	a.state = MergeStatus(a.state, &msg.status)
	a.state.ConnState = ConnStateConnected
	a.statusRefreshPending, a.statusDirty = false, false
	cmds := []tea.Cmd{readCmd(a.ctx, a.gen, a.cl)}
	if len(msg.status.TurnsOpen) > 0 {
		oldTurn := a.state.CurrentTurn
		a.state.CurrentTurn = msg.status.TurnsOpen[0]
		if oldTurn != a.state.CurrentTurn {
			a.answerScrollRow = 0
		}
		a.ensureTurnInfo()
		// The limit and deadline of a turn that is already open come from its record.
		cmds = append(cmds, turnCmd(a.ctx, a.gen, a.cl, a.state.CurrentTurn))
	}
	cmds = append(cmds, a.ensureTick(), a.syncTranscript(), a.reloadTranscript())
	return a, tea.Batch(cmds...)
}

// onFrame applies a frame. Any stream error, including the engine's stopping
// notice, and the engine_stopping event itself, count as losing the connection.
func (a *App) onFrame(msg eventMsg) (tea.Model, tea.Cmd) {
	if msg.gen != a.gen {
		return a, nil
	}
	if msg.err != nil {
		return a, a.reconnect()
	}
	a.state = apply(a.state, msg.frame)
	if msg.frame.Kind == wire.EventKindTurnState {
		// The override's reason travels on the turn_state event; the turn record
		// that follows keeps only the literal "override".
		var body wire.EvTurnState
		if json.Unmarshal(msg.frame.Body, &body) == nil && body.State == "overridden" && body.Reason != nil {
			a.overrideReasons[body.TurnID] = *body.Reason
		}
	}
	if a.onEvent != nil {
		a.onEvent(msg.frame)
	}
	if msg.frame.Kind == wire.EventKindEngineStopping {
		return a, a.reconnect()
	}

	// The next read is always armed here, and only here, so there is exactly
	// one outstanding read; status and turn replies never re-arm it.
	cmds := []tea.Cmd{readCmd(a.ctx, a.gen, a.cl)}
	switch msg.frame.Kind {
	case wire.EventKindEngineStarted, wire.EventKindParticipantState, wire.EventKindCapacity:
		cmds = append(cmds, a.requestStatus())
	case wire.EventKindTurnSubmitted:
		var body wire.EvTurnSubmitted
		if json.Unmarshal(msg.frame.Body, &body) == nil {
			// The record carries the configured turn deadline and token limit.
			cmds = append(cmds, turnCmd(a.ctx, a.gen, a.cl, body.TurnID))
		}
	case wire.EventKindTurnState:
		var body wire.EvTurnState
		if json.Unmarshal(msg.frame.Body, &body) == nil && isTerminalState(body.State) {
			cmds = append(cmds, turnCmd(a.ctx, a.gen, a.cl, body.TurnID))
		}
	case wire.EventKindMessageAppended:
		var body wire.EvMessageAppended
		if json.Unmarshal(msg.frame.Body, &body) == nil {
			cmds = append(cmds, a.transcriptTrigger(body.TurnID))
		}
	case wire.EventKindAttemptBegun, wire.EventKindAttemptCompleted, wire.EventKindAttemptUnknown:
		var body struct {
			AttemptID string `json:"attempt_id"`
		}
		if json.Unmarshal(msg.frame.Body, &body) == nil {
			// An attempt begun after the turn ended (terminal delivery) has no turn of its own.
			turnID := a.state.Attempts[body.AttemptID].TurnID
			if turnID == "" {
				turnID = answerTurn(a.state)
			}
			cmds = append(cmds, a.transcriptTrigger(turnID))
			if msg.frame.Kind == wire.EventKindAttemptCompleted && a.state.Attempts[body.AttemptID].TurnID != "" && isTerminalState(a.state.TurnState[turnID]) {
				// An attempt of the turn that completes after the turn ended is a late result,
				// and only the turn record says it was quarantined.
				cmds = append(cmds, turnCmd(a.ctx, a.gen, a.cl, turnID))
			}
		}
	}
	if msg.frame.Kind == wire.EventKindCapacity {
		cmds = append(cmds, a.onLateResult(msg.frame))
	}
	cmds = append(cmds, a.ensureTick())
	return a, tea.Batch(cmds...)
}

// onLateResult handles the release of a hold by a late result. The result was
// quarantined and no attempt event says so, so the transcript and the turn
// record, which carries the mark, are read again.
func (a *App) onLateResult(f wire.Frame) tea.Cmd {
	var body wire.EvCapacity
	if json.Unmarshal(f.Body, &body) != nil || body.State != "released" || body.How == nil || *body.How != "late_result" {
		return nil
	}
	turnID := a.state.Attempts[body.HeldByAttempt].TurnID
	if turnID == "" {
		turnID = answerTurn(a.state)
	}
	if turnID == "" {
		return nil
	}
	return tea.Batch(a.transcriptTrigger(turnID), turnCmd(a.ctx, a.gen, a.cl, turnID))
}

// requestStatus asks for a fresh status, coalescing a burst: while one request
// is in flight a trigger only marks it dirty, and the reply sends one more.
func (a *App) requestStatus() tea.Cmd {
	if a.statusRefreshPending {
		a.statusDirty = true
		return nil
	}
	a.statusRefreshPending = true
	return statusCmd(a.ctx, a.gen, a.cl)
}

// onStatusRefresh applies a status reply. It never re-arms the event read.
func (a *App) onStatusRefresh(msg statusMsg) (tea.Model, tea.Cmd) {
	if msg.gen != a.gen {
		return a, nil
	}
	a.statusRefreshPending = false
	if msg.err != nil {
		a.refusal = "status: " + formatRefusal(msg.err)
		a.statusDirty = false
		return a, nil
	}
	a.state = MergeStatus(a.state, &msg.status)
	a.ensureTurnInfo()
	if a.statusDirty {
		a.statusDirty = false
		return a, a.requestStatus()
	}
	return a, nil
}

// onTurn merges a turn reply. It never re-arms the event read.
func (a *App) onTurn(msg turnMsg) (tea.Model, tea.Cmd) {
	if msg.gen != a.gen {
		return a, nil
	}
	if msg.err != nil {
		a.refusal = "turn: " + formatRefusal(msg.err)
		return a, nil
	}
	prevHash := a.state.Turns[msg.id].CandidateHash
	prevAttempts := a.state.Attempts
	a.state = MergeTurn(a.state, msg.id, msg.turn, a.now())
	// A reply read while an attempt was running can arrive after the event that
	// completed it; a completed attempt is never shown as running again.
	for id, before := range prevAttempts {
		if now := a.state.Attempts[id]; attemptDone(before) && !attemptDone(now) {
			attempts := copyMap(a.state.Attempts)
			attempts[id] = before
			a.state.Attempts = attempts
		}
	}
	// A reply read before the candidate was frozen can arrive after a newer one;
	// a candidate once seen is never forgotten.
	if ti, ok := a.state.Turns[msg.id]; ok && ti.CandidateHash == nil && prevHash != nil {
		turns := copyMap(a.state.Turns)
		ti.CandidateHash = prevHash
		turns[msg.id] = ti
		a.state.Turns = turns
	}
	for _, at := range msg.turn.Attempts {
		if at.Quarantined {
			a.quarantined[at.ID] = true
		}
	}
	for _, b := range msg.turn.OpenBlocks {
		a.blockDetail[b.ID] = b
	}
	if msg.turn.CandidateID != nil {
		a.candidateIDByTurn[msg.id] = *msg.turn.CandidateID
	}
	// A stale reply (read before the blocks were raised) carries an empty list
	// and can arrive after the terminal one, even claiming a terminal state; known
	// blocks are never wiped by it.
	if len(msg.turn.OpenBlocks) > 0 || len(a.openBlocksByTurn[msg.id]) == 0 {
		a.openBlocksByTurn[msg.id] = msg.turn.OpenBlocks
	}
	return a, nil
}

// attemptDone reports whether the attempt has a final status.
func attemptDone(ai AttemptInfo) bool { return ai.Status != "" && ai.Status != "in_progress" }

// onContent stores a fetched body.
func (a *App) onContent(msg contentMsg) (tea.Model, tea.Cmd) {
	a.content.Store(msg.ref, msg.data, msg.err)
	return a, nil
}

// reconnect drops the current connection and starts a new one that resumes
// after the last seq seen. Bumping gen fences off the old connection's
// outstanding commands. The replay is across runs, so the seq is kept when
// hello shows a new run; status is fetched on every connect.
func (a *App) reconnect() tea.Cmd {
	a.gen++
	if a.cl != nil {
		_ = a.cl.Close()
		a.cl = nil
	}
	a.state.ConnState = ConnStateReconnecting
	a.statusRefreshPending, a.statusDirty = false, false
	a.tLoading, a.tReload = false, false
	return connectCmd(a.ctx, a.gen, a.socket, a.state.LastSeq)
}

// quit leaves the engine alone: closing the socket never cancels a turn.
func (a *App) quit() tea.Cmd {
	a.gen++
	a.cancel()
	if a.cl != nil {
		_ = a.cl.Close()
		a.cl = nil
	}
	return tea.Quit
}

// bodyHeight is the number of lines left for the active tab.
func (a *App) bodyHeight() int {
	return max(a.height-len(renderBanners(a.state))-1-inputHeight, 0)
}

// answerLineCount is the length of the unclipped answer tab.
func (a *App) answerLineCount() int {
	turnID := answerTurn(a.state)
	return len(strings.Split(renderAnswer(a.state, turnID, a.answerIn(turnID)), "\n"))
}

// scrollAnswer moves the answer tab by delta lines, staying within the text.
func (a *App) scrollAnswer(delta int) {
	maxStart := max(a.answerLineCount()-a.bodyHeight(), 0)
	a.answerScrollRow = min(max(a.answerScrollRow+delta, 0), maxStart)
}

// View lays out banners, strip, the active tab and the input line.
func (a *App) View() string {
	w := a.width

	// Get banners and strip.
	banners := renderBanners(a.state)
	strip := renderStrip(a.state, w, a.now())

	bodyHeight := a.bodyHeight()

	var tabView string
	switch a.tab {
	case tabAnswer:
		turnID := answerTurn(a.state)
		tabView = renderAnswer(a.state, turnID, a.answerIn(turnID))
	case tabTranscript:
		tabView = strings.Join(a.tLines[min(a.tRow, len(a.tLines)):], "\n")
	case tabParticipants:
		if a.pScrolled {
			// A refresh can shorten the content under the offset, so the
			// start is kept within the last full page as it is now.
			full := strings.Split(a.participantsFull(w), "\n")
			tabView = strings.Join(full[min(a.pScroll, max(len(full)-bodyHeight, 0)):], "\n")
		} else {
			tabView = renderParticipants(a.state, w, bodyHeight, a.selectedParticipantID(), a.now())
		}
	}
	if len(a.panel) > 0 {
		tabView = renderPanel(a.panel, w)
	}
	lines := strings.Split(tabView, "\n")

	if a.tab == tabAnswer && len(a.panel) == 0 {
		start := min(a.answerScrollRow, max(len(lines)-bodyHeight, 0))
		lines = lines[start:]
	}
	for len(lines) < bodyHeight {
		lines = append(lines, "")
	}
	lines = lines[:bodyHeight]

	// Build output: banners, strip, body, input.
	var output []string
	output = append(output, banners...)
	output = append(output, strip)
	output = append(output, lines...)
	output = append(output, renderInput(a.input, a.refusal, w))
	return strings.Join(output, "\n")
}

// selectedParticipantID is the id of the selected participants row, or "".
func (a *App) selectedParticipantID() string {
	ids := participantIDs(a.state)
	if len(ids) == 0 {
		return ""
	}
	return ids[a.selectedParticipant()]
}

// participantsFull is the participants tab at full length, not fitted to the body.
func (a *App) participantsFull(w int) string {
	return renderParticipants(a.state, w, 0, a.selectedParticipantID(), a.now())
}

// participantsMaxScroll is the last first-line that still fills the body.
func (a *App) participantsMaxScroll() int {
	n := len(strings.Split(a.participantsFull(max(a.width, 1)), "\n"))
	return max(n-a.bodyHeight(), 0)
}

// selectedParticipant is the index of the selected participants row, kept
// within the roster as it stands.
func (a *App) selectedParticipant() int {
	return min(max(a.pSelected, 0), max(len(a.state.Participants)-1, 0))
}

// ExitMessage is what to print after the TUI exits: a turn still open keeps
// running without it.
func ExitMessage(a *App) string {
	if id := a.CurrentTurn(); id != "" {
		return "turn " + id + " is still running and will continue"
	}
	return ""
}
