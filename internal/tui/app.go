package tui

import (
	"context"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

type TabName string

const (
	TabAnswer       TabName = "answer"
	TabTranscript   TabName = "transcript"
	TabParticipants TabName = "participants"
)

// App is the root Bubble Tea model for the TUI.
type App struct {
	ctx    context.Context
	cancel context.CancelFunc

	width  int
	height int

	state     State
	connector *connector
	activeTab TabName
	inputLine string
	refusal   string
}

// NewApp creates a new App instance.
func NewApp(ctx context.Context, socketPath string) *App {
	childCtx, cancel := context.WithCancel(ctx)
	conn := newConnector(socketPath)
	app := &App{
		ctx:       childCtx,
		cancel:    cancel,
		activeTab: TabAnswer,
		connector: conn,
	}
	return app
}

// Init returns initial commands to run.
func (a *App) Init() tea.Cmd {
	return a.connector.listenCmd(a.ctx, a.handleConnectorMsg)
}

// handleConnectorMsg processes messages from the connector.
func (a *App) handleConnectorMsg(msg tea.Msg) tea.Msg {
	return msg
}

// Update handles messages and updates state.
func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		return a.handleKeyMsg(msg)

	case tea.WindowSizeMsg:
		a.width = msg.Width
		a.height = msg.Height
		return a, nil

	case ConnStateMsg:
		return a.handleConnState(msg)

	case EventMsg:
		return a.handleEvent(msg)

	case HelloMsg:
		return a.handleHello(msg)

	case StatusMsg:
		return a.handleStatus(msg)

	case SubmitResultMsg:
		return a.handleSubmitResult(msg)

	case tea.QuitMsg:
		return a, tea.Quit
	}

	return a, nil
}

func (a *App) handleKeyMsg(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return a, tea.Quit
	case "tab":
		a.activeTab = a.nextTab()
		return a, nil
	case "shift+tab":
		a.activeTab = a.prevTab()
		return a, nil
	case "enter":
		if a.inputLine != "" {
			cmd := a.processInputCmd()
			return a, cmd
		}
	case "backspace":
		if len(a.inputLine) > 0 {
			a.inputLine = a.inputLine[:len(a.inputLine)-1]
		}
	default:
		if len(msg.String()) == 1 && msg.String()[0] >= 32 && msg.String()[0] < 127 {
			a.inputLine += msg.String()
		}
	}
	return a, nil
}

func (a *App) handleConnState(msg ConnStateMsg) (tea.Model, tea.Cmd) {
	if msg.Err != nil {
		a.state.ConnState = ConnStateClosed
		return a, a.connector.reconnectCmd(a.ctx)
	}

	switch msg.State {
	case ConnStateConnected:
		a.state.ConnState = ConnStateConnected
		return a, a.connector.helloCmd(a.ctx)
	default:
		a.state.ConnState = ConnStateReconnecting
		return a, nil
	}
}

func (a *App) handleHello(msg HelloMsg) (tea.Model, tea.Cmd) {
	if msg.Err != nil {
		a.state.ConnState = ConnStateClosed
		return a, a.connector.reconnectCmd(a.ctx)
	}
	a.state.Hello = msg.Hello
	a.state.ConnState = ConnStateConnected
	return a, a.connector.fetchStatusCmd(a.ctx)
}

func (a *App) handleStatus(msg StatusMsg) (tea.Model, tea.Cmd) {
	if msg.Err != nil {
		a.state.ConnState = ConnStateClosed
		return a, a.connector.reconnectCmd(a.ctx)
	}
	a.state.Status = msg.Status
	return a, a.connector.streamEventsCmd(a.ctx)
}

func (a *App) handleEvent(msg EventMsg) (tea.Model, tea.Cmd) {
	if msg.Err != nil {
		a.state.ConnState = ConnStateReconnecting
		return a, a.connector.reconnectCmd(a.ctx)
	}
	a.state = Apply(a.state, msg.Frame)
	return a, a.connector.streamEventsCmd(a.ctx)
}

func (a *App) handleSubmitResult(msg SubmitResultMsg) (tea.Model, tea.Cmd) {
	if msg.Err != nil {
		if msg.RefusalDetail != "" {
			a.refusal = msg.RefusalDetail
		}
		return a, nil
	}
	a.refusal = ""
	a.state.CurrentTurn = msg.TurnID
	a.inputLine = ""
	return a, nil
}

// processInputCmd processes the input line.
func (a *App) processInputCmd() tea.Cmd {
	input := strings.TrimSpace(a.inputLine)
	if strings.HasPrefix(input, "/") {
		return dispatchCommand(a, input)
	}
	return a.connector.submitCmd(a.ctx, input)
}

// View renders the UI.
func (a *App) View() string {
	if a.width == 0 || a.height == 0 {
		return ""
	}

	bottomHeight := 3
	topHeight := 1
	middleHeight := a.height - topHeight - bottomHeight
	if middleHeight < 1 {
		middleHeight = 1
	}

	var out strings.Builder

	// Status strip (top) - includes reconnecting message
	strip := a.renderStrip(a.width)
	out.WriteString(strip)
	out.WriteString("\n")

	// Active tab content (middle)
	out.WriteString(a.renderActiveTab(a.width, middleHeight))
	out.WriteString("\n")

	// Input line (bottom)
	out.WriteString(a.renderInput(a.width, bottomHeight))

	return out.String()
}

func (a *App) renderStrip(width int) string {
	strip := renderStrip(a.state, width)
	if len(strip) > width {
		strip = strip[:width]
	}
	return strip
}

func (a *App) renderActiveTab(width, height int) string {
	switch a.activeTab {
	case TabAnswer:
		return renderAnswer(a.state, width, height)
	case TabTranscript:
		return renderTranscript(a.state, width, height)
	case TabParticipants:
		return renderParticipants(a.state, width, height)
	}
	return ""
}

func (a *App) renderInput(width, height int) string {
	return renderInput(a.inputLine, a.refusal, width, height)
}

func (a *App) nextTab() TabName {
	switch a.activeTab {
	case TabAnswer:
		return TabTranscript
	case TabTranscript:
		return TabParticipants
	case TabParticipants:
		return TabAnswer
	}
	return TabAnswer
}

func (a *App) prevTab() TabName {
	switch a.activeTab {
	case TabAnswer:
		return TabParticipants
	case TabParticipants:
		return TabTranscript
	case TabTranscript:
		return TabAnswer
	}
	return TabAnswer
}

// CurrentTurn returns the ID of the currently open turn, if any.
func (a *App) CurrentTurn() string {
	return a.state.CurrentTurn
}
