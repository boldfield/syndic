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

	content           *ContentCache
	draftsEnabled     bool
	draftsEnabledSet  bool // tracks if /drafts command was used
	answerScrollRow   int  // current scroll offset for answer tab
	candidateIDByTurn map[string]string
	lastTerminalTurn  string // last terminal turn for display when CurrentTurn is empty
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
func (a *App) SetDraftsEnabled(enabled bool) {
	a.draftsEnabled = enabled
	a.draftsEnabledSet = true
}

// Init starts the first connection, from the beginning of the journal.
func (a *App) Init() tea.Cmd {
	return tea.Batch(textinput.Blink, connectCmd(a.ctx, a.gen, a.socket, 0))
}

// Update handles one message.
func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
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
		return a, nil
	case "shift+tab":
		a.tab = (a.tab + numTabs - 1) % numTabs
		a.answerScrollRow = 0
		return a, nil
	case "enter":
		return a, a.enter()
	}

	// Scrolling in answer tab is clamped in View to prevent panics
	if a.tab == tabAnswer {
		switch msg.String() {
		case "up":
			if a.answerScrollRow > 0 {
				a.answerScrollRow--
			}
			return a, nil
		case "down":
			a.answerScrollRow++
			// Prevent unbounded growth; View() will clamp properly for rendering
			if a.answerScrollRow < 0 {
				a.answerScrollRow = 0
			}
			return a, nil
		case "pgup":
			a.answerScrollRow -= 10
			if a.answerScrollRow < 0 {
				a.answerScrollRow = 0
			}
			return a, nil
		case "pgdn":
			a.answerScrollRow += 10
			// Prevent unbounded growth
			if a.answerScrollRow < 0 {
				a.answerScrollRow = 0
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
		a.draftsEnabledSet = false // Reset user override when turn changes
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
			a.draftsEnabledSet = false
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
	a.state = MergeTurn(a.state, msg.id, msg.turn, a.now())
	a.applyQuickProfileDrafts(msg.id)

	// Store candidateID for later use in content fetching
	if msg.turn != nil && msg.turn.CandidateID != nil {
		a.candidateIDByTurn[msg.id] = *msg.turn.CandidateID
	}

	// Track the last terminal turn for display when CurrentTurn is empty
	if msg.turn != nil && isTerminalState(msg.turn.State) {
		a.lastTerminalTurn = msg.id
	}

	// Fetch content for the turn if it's terminal or if drafts are enabled
	contentCmds := a.fetchContentForTurn(msg.id)
	if len(contentCmds) > 0 {
		return a, tea.Batch(contentCmds...)
	}
	return a, nil
}

// applyQuickProfileDrafts applies the quick profile's drafts setting unless
// the user has explicitly set it with /drafts.
func (a *App) applyQuickProfileDrafts(turnID string) {
	if a.draftsEnabledSet {
		// User has explicitly set drafts; don't override
		return
	}

	ti, ok := a.state.Turns[turnID]
	if !ok || ti.QuickProfile == nil {
		return
	}

	qp, ok := a.state.QuickProfiles[*ti.QuickProfile]
	if !ok {
		return
	}

	a.draftsEnabled = qp.Drafts
}

// onContent stores fetched content in the cache.
func (a *App) onContent(msg contentMsg) (tea.Model, tea.Cmd) {
	a.content.Store(msg.ref, msg.data, msg.err)
	return a, nil
}

// fetchContentForTurn returns commands to fetch content refs for a turn.
func (a *App) fetchContentForTurn(turnID string) []tea.Cmd {
	var cmds []tea.Cmd
	if a.cl == nil {
		return cmds
	}

	ti, ok := a.state.Turns[turnID]
	if !ok {
		return cmds
	}

	// Fetch candidate if available and not already cached
	if ti.CandidateHash != nil {
		candidateID, ok := a.candidateIDByTurn[turnID]
		if ok && candidateID != "" {
			ref := "candidate:" + candidateID
			if !a.content.Get(ref).Ready {
				cmds = append(cmds, fetchContentCmd(a.ctx, a.cl, ref))
			}
		}
	}

	// Fetch attempt outputs if drafts are enabled or for display
	for attemptID, ai := range a.state.Attempts {
		if ai.TurnID == turnID && (ai.Phase == "author" || ai.Phase == "review") {
			ref := "attempt:" + attemptID
			if !a.content.Get(ref).Ready {
				cmds = append(cmds, fetchContentCmd(a.ctx, a.cl, ref))
			}
		}
	}

	return cmds
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

// View lays out banners, strip, the active tab and the input line.
func (a *App) View() string {
	w, h := a.width, a.height

	// Get banners and strip.
	banners := renderBanners(a.state)
	strip := renderStrip(a.state, w, a.now())
	numBanners := len(banners)

	// Calculate available space for body and input.
	bodyHeight := max(h-numBanners-1-inputHeight, 0)

	var tabView string
	switch a.tab {
	case tabAnswer:
		// Use CurrentTurn if available, otherwise use the last terminal turn
		turnID := a.state.CurrentTurn
		if turnID == "" {
			turnID = a.lastTerminalTurn
		}
		candidateID := a.candidateIDByTurn[turnID]
		tabView = renderAnswer(a.state, w, bodyHeight, a.content, a.draftsEnabled, a.answerScrollRow, candidateID, turnID)
	case tabTranscript:
		tabView = renderTranscript(a.state, w, bodyHeight)
	case tabParticipants:
		tabView = renderParticipants(a.state, w, bodyHeight)
	}
	lines := strings.Split(tabView, "\n")

	// Apply scrolling to answer tab
	if a.tab == tabAnswer && len(lines) > bodyHeight {
		start := a.answerScrollRow
		maxStart := len(lines) - bodyHeight
		if maxStart < 0 {
			maxStart = 0
		}
		if start > maxStart {
			start = maxStart
		}
		if start < 0 {
			start = 0
		}
		lines = lines[start : start+bodyHeight]
	}

	for len(lines) < bodyHeight {
		lines = append(lines, "")
	}
	if len(lines) > bodyHeight {
		lines = lines[:bodyHeight]
	}

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
