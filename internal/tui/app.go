package tui

import (
	"context"
	"encoding/json"
	"strings"

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

	onEvent func(wire.Frame)
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
		ctx:    ctx,
		cancel: cancel,
		socket: socketPath,
		input:  newInput(),
		width:  80,
		height: 24,
		state:  NewState(),
	}
	a.state.ConnState = ConnStateConnecting
	for _, o := range opts {
		o(a)
	}
	return a
}

// CurrentTurn is the open turn the UI knows of, or "".
func (a *App) CurrentTurn() string { return a.state.CurrentTurn }

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
		a.onSubmit(msg)
		return a, nil
	case statusMsg:
		return a.onStatusRefresh(msg)
	case turnMsg:
		return a.onTurn(msg)
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
		return a, nil
	case "shift+tab":
		a.tab = (a.tab + numTabs - 1) % numTabs
		return a, nil
	case "enter":
		return a, a.enter()
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

func (a *App) onSubmit(msg submitMsg) {
	if msg.refusal != "" {
		a.refusal = msg.refusal
		return
	}
	a.input.SetValue("")
	if !isTerminalState(a.state.TurnState[msg.turnID]) {
		a.state.CurrentTurn = msg.turnID
	}
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
	if len(msg.status.TurnsOpen) > 0 {
		a.state.CurrentTurn = msg.status.TurnsOpen[0]
	}
	return a, readCmd(a.ctx, a.gen, a.cl)
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

	// Trigger status refresh after certain events (coalesced by not requesting until the next read).
	var cmd tea.Cmd
	needsStatusRefresh := false
	requestTurnID := ""
	switch msg.frame.Kind {
	case wire.EventKindEngineStarted, wire.EventKindParticipantState, wire.EventKindCapacity:
		needsStatusRefresh = true
	case wire.EventKindTurnState:
		var body wire.EvTurnState
		if json.Unmarshal(msg.frame.Body, &body) == nil {
			if isTerminalState(body.State) {
				requestTurnID = body.TurnID
			}
		}
	}

	if needsStatusRefresh && a.cl != nil {
		cmd = statusCmd(a.ctx, a.cl)
	} else if requestTurnID != "" && a.cl != nil {
		cmd = turnCmd(a.ctx, a.cl, requestTurnID)
	} else {
		cmd = readCmd(a.ctx, a.gen, a.cl)
	}
	return a, cmd
}

// onStatusRefresh applies a status update and continues reading events.
func (a *App) onStatusRefresh(msg statusMsg) (tea.Model, tea.Cmd) {
	if msg.err == nil {
		a.state = MergeStatus(a.state, &msg.status)
	}
	if a.cl != nil {
		return a, readCmd(a.ctx, a.gen, a.cl)
	}
	return a, nil
}

// onTurn applies a turn reply and continues reading events.
func (a *App) onTurn(msg turnMsg) (tea.Model, tea.Cmd) {
	if msg.err == nil && msg.turn != nil {
		a.state = MergeTurn(a.state, msg.id, msg.turn)
	}
	if a.cl != nil {
		return a, readCmd(a.ctx, a.gen, a.cl)
	}
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
	strip := renderStrip(a.state, w)
	numBanners := len(banners)

	// Calculate available space for body and input.
	bodyHeight := max(h-numBanners-1-inputHeight, 0)

	var tabView string
	switch a.tab {
	case tabAnswer:
		tabView = renderAnswer(a.state, w, bodyHeight)
	case tabTranscript:
		tabView = renderTranscript(a.state, w, bodyHeight)
	case tabParticipants:
		tabView = renderParticipants(a.state, w, bodyHeight)
	}
	lines := strings.Split(tabView, "\n")
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
