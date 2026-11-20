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
	refusal       string

	statusRefreshPending bool
	statusDirty          bool // a trigger arrived while a refresh was in flight
	now                  func() time.Time
	tickRunning          bool
	onEvent              func(wire.Frame)

	content                *ContentCache
	drafts                 *bool // the user's drafts toggle; nil until set, then it overrides the quick profile
	answerScrollRow        int   // first visible line of the answer tab
	transcriptRow          int   // first visible line of the transcript tab
	candidateIDByTurn      map[string]string
	transcriptsByTurn      map[string][]wire.TranscriptItem // transcript items from turn replies
	transcriptCursorByTurn map[string]string                // next_cursor for pagination
	openBlocksByTurn       map[string][]wire.OpenBlock      // open blocks from turn replies
	overrideReasons        map[string]string                // the reason of each override event
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
		ctx:                    ctx,
		cancel:                 cancel,
		socket:                 socketPath,
		input:                  newInput(),
		width:                  80,
		height:                 24,
		state:                  NewState(),
		now:                    time.Now,
		content:                NewContentCache(),
		candidateIDByTurn:      make(map[string]string),
		transcriptsByTurn:      make(map[string][]wire.TranscriptItem),
		transcriptCursorByTurn: make(map[string]string),
		openBlocksByTurn:       make(map[string][]wire.OpenBlock),
		overrideReasons:        make(map[string]string),
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

// transcriptIn gathers what the transcript tab needs for the turn.
func (a *App) transcriptIn(turnID string) transcriptInput {
	return transcriptInput{
		content: a.content,
		items:   a.transcriptsByTurn[turnID],
		blocks:  a.openBlocksByTurn[turnID],
	}
}

// transcriptRefs extracts all content refs from transcript items.
func transcriptRefs(items []wire.TranscriptItem) []string {
	var refs []string
	for _, item := range items {
		refs = append(refs, item.Ref)
	}
	return refs
}

// fetchAnswerContent starts the fetches the answer tab is waiting on.
func (a *App) fetchAnswerContent() tea.Cmd {
	turnID := answerTurn(a.state)
	return tea.Batch(fetchRefs(a.ctx, a.cl, a.content, answerRefs(a.state, turnID, a.answerIn(turnID)))...)
}

// fetchTranscriptContent starts fetches for the transcript tab.
func (a *App) fetchTranscriptContent() tea.Cmd {
	turnID := transcriptTurn(a.state)
	items := a.transcriptsByTurn[turnID]
	return tea.Batch(fetchRefs(a.ctx, a.cl, a.content, transcriptRefs(items))...)
}

// Init starts the first connection, from the beginning of the journal.
func (a *App) Init() tea.Cmd {
	return tea.Batch(textinput.Blink, connectCmd(a.ctx, a.gen, a.socket, 0))
}

// Update handles one message, then starts any content fetch the current tab
// now needs. A content reply itself starts none, so a failing fetch is retried
// on the next event or tick, not in a loop.
func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	_, cmd := a.update(msg)
	if _, ok := msg.(contentMsg); !ok {
		var fetchCmd tea.Cmd
		if a.tab == tabTranscript {
			fetchCmd = a.fetchTranscriptContent()
		} else {
			fetchCmd = a.fetchAnswerContent()
		}
		cmd = tea.Batch(cmd, fetchCmd)
	}
	return a, cmd
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
	case transcriptMsg:
		return a.onTranscript(msg)
	case contentMsg:
		return a.onContent(msg)
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
	switch msg.String() {
	case "ctrl+c":
		return a, a.quit()
	case "tab":
		a.tab = (a.tab + 1) % numTabs
		a.answerScrollRow = 0
		a.transcriptRow = 0
		return a, nil
	case "shift+tab":
		a.tab = (a.tab + numTabs - 1) % numTabs
		a.answerScrollRow = 0
		a.transcriptRow = 0
		return a, nil
	case "enter":
		return a, a.enter()
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

	if a.tab == tabTranscript {
		page := max(a.bodyHeight()-1, 1)
		switch msg.String() {
		case "up":
			a.scrollTranscript(-1)
			return a, nil
		case "down":
			a.scrollTranscript(1)
			return a, nil
		case "pgup":
			a.scrollTranscript(-page)
			return a, nil
		case "pgdown":
			a.scrollTranscript(page)
			return a, nil
		case "home":
			a.transcriptRow = 0
			return a, nil
		case "end":
			a.scrollTranscriptEnd()
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
	line := a.input.Value()
	if strings.TrimSpace(line) == "" {
		return nil
	}
	a.refusal = ""
	if isCommand(line) {
		a.input.SetValue("")
		return dispatch(a, line)
	}
	if a.cl == nil {
		a.refusal = "engine: not connected"
		return nil
	}
	return submitCmd(a.ctx, a.cl, line)
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
	cmds = append(cmds, a.ensureTick())
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
	}
	cmds = append(cmds, a.ensureTick())
	return a, tea.Batch(cmds...)
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
	a.state = MergeTurn(a.state, msg.id, msg.turn, a.now())
	// A reply read before the candidate was frozen can arrive after a newer one;
	// a candidate once seen is never forgotten.
	if ti, ok := a.state.Turns[msg.id]; ok && ti.CandidateHash == nil && prevHash != nil {
		turns := copyMap(a.state.Turns)
		ti.CandidateHash = prevHash
		turns[msg.id] = ti
		a.state.Turns = turns
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
	// Fetch transcript items for the turn
	cmd := transcriptCmd(a.ctx, a.gen, a.cl, msg.id, "")
	return a, cmd
}

// onTranscript stores fetched transcript items and fetches more if there are more pages.
func (a *App) onTranscript(msg transcriptMsg) (tea.Model, tea.Cmd) {
	if msg.gen != a.gen {
		return a, nil
	}
	if msg.err == nil {
		// Append items to existing items for this turn
		a.transcriptsByTurn[msg.id] = append(a.transcriptsByTurn[msg.id], msg.items...)
		// Store the cursor for the next page
		a.transcriptCursorByTurn[msg.id] = msg.nextCursor
		// If there are more items, fetch them
		if msg.nextCursor != "" {
			return a, transcriptCmd(a.ctx, a.gen, a.cl, msg.id, msg.nextCursor)
		}
	}
	return a, nil
}

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

// transcriptLineCount is the length of the unclipped transcript tab.
func (a *App) transcriptLineCount() int {
	turnID := transcriptTurn(a.state)
	return len(strings.Split(renderTranscript(a.state, a.width, a.bodyHeight(), a.transcriptIn(turnID)), "\n"))
}

// scrollTranscript moves the transcript tab by delta lines, staying within the text.
func (a *App) scrollTranscript(delta int) {
	maxStart := max(a.transcriptLineCount()-a.bodyHeight(), 0)
	a.transcriptRow = min(max(a.transcriptRow+delta, 0), maxStart)
}

// scrollTranscriptEnd scrolls to the end of the transcript.
func (a *App) scrollTranscriptEnd() {
	maxStart := max(a.transcriptLineCount()-a.bodyHeight(), 0)
	a.transcriptRow = maxStart
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
		turnID := transcriptTurn(a.state)
		tabView = renderTranscript(a.state, w, bodyHeight, a.transcriptIn(turnID))
	case tabParticipants:
		tabView = renderParticipants(a.state, w, bodyHeight)
	}
	lines := strings.Split(tabView, "\n")

	var scrollRow int
	switch a.tab {
	case tabAnswer:
		scrollRow = a.answerScrollRow
	case tabTranscript:
		scrollRow = a.transcriptRow
	}

	if a.tab == tabAnswer || a.tab == tabTranscript {
		start := min(scrollRow, max(len(lines)-bodyHeight, 0))
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

// ExitMessage is what to print after the TUI exits: a turn still open keeps
// running without it.
func ExitMessage(a *App) string {
	if id := a.CurrentTurn(); id != "" {
		return "turn " + id + " is still running and will continue"
	}
	return ""
}
