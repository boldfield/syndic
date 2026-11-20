package tui

import (
	"context"
	"fmt"
	"strings"

	"github.com/boldfield/syndic/internal/wire"
	tea "github.com/charmbracelet/bubbletea"
)

type TabName string

const (
	TabAnswer       TabName = "answer"
	TabTranscript   TabName = "transcript"
	TabParticipants TabName = "participants"
)

// StatusUpdateMsg carries a status update from the engine.
type StatusUpdateMsg struct {
	Status wire.StatusReply
}

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

	// Message channel from the last command
	connMsgs chan ConnMsg
}

// NewApp creates a new App instance.
func NewApp(ctx context.Context, socketPath string) *App {
	childCtx, cancel := context.WithCancel(ctx)
	app := &App{
		ctx:       childCtx,
		cancel:    cancel,
		activeTab: TabAnswer,
		connector: &connector{socketPath: socketPath, lastSeq: 0},
		connMsgs:  make(chan ConnMsg, 1),
	}

	return app
}

// Init returns initial commands to run.
func (a *App) Init() tea.Cmd {
	return a.connectCmd()
}

// Update handles messages and updates state.
func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q":
			return a, a.quitCmd()
		case "tab":
			a.activeTab = a.nextTab()
			return a, nil
		case "shift+tab":
			a.activeTab = a.prevTab()
			return a, nil
		case "enter":
			if a.inputLine != "" {
				cmd := a.submitCmd()
				a.inputLine = ""
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

	case tea.WindowSizeMsg:
		a.width = msg.Width
		a.height = msg.Height
		return a, nil

	case ConnMsg:
		if msg.Err != nil {
			a.state.ConnState = ConnStateClosed
			// Reconnect with backoff
			return a, tea.Batch(a.showReconnectingCmd(), a.reconnectCmd())
		}
		if msg.State == ConnStateConnected {
			a.state.ConnState = ConnStateConnected
			return a, a.fetchStatusCmd()
		}
		return a, nil

	case EventMsg:
		if msg.Err != nil {
			a.state.ConnState = ConnStateClosed
			return a, tea.Batch(a.showReconnectingCmd(), a.reconnectCmd())
		}
		a.state = Apply(a.state, msg.Frame)
		return a, nil

	case StatusUpdateMsg:
		a.state.Status = msg.Status
		return a, nil

	case tea.QuitMsg:
		return a, tea.Quit
	}

	return a, nil
}

// View renders the UI.
func (a *App) View() string {
	if a.state.ConnState != ConnStateConnected {
		return "Connecting to engine...\n"
	}

	if a.width == 0 || a.height == 0 {
		return ""
	}

	// Calculate region heights (at least 3 for bottom input)
	bottomHeight := 3
	topHeight := 1
	middleHeight := a.height - topHeight - bottomHeight
	if middleHeight < 1 {
		middleHeight = 1
	}

	var out strings.Builder

	// Status strip (top)
	out.WriteString(a.renderStrip(a.width))
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

// Commands for async operations

func (a *App) connectCmd() tea.Cmd {
	return func() tea.Msg {
		connMsgs := a.connector.connect(a.ctx)
		for msg := range connMsgs {
			return msg
		}
		return ConnMsg{State: ConnStateClosed, Err: fmt.Errorf("connection closed")}
	}
}

func (a *App) reconnectCmd() tea.Cmd {
	return func() tea.Msg {
		// Give backoff a chance to work
		connMsgs := a.connector.reconnectWithBackoff(a.ctx)
		for msg := range connMsgs {
			return msg
		}
		return ConnMsg{State: ConnStateClosed, Err: fmt.Errorf("reconnection failed")}
	}
}

func (a *App) showReconnectingCmd() tea.Cmd {
	return func() tea.Msg {
		a.state.ConnState = ConnStateReconnecting
		return nil
	}
}

func (a *App) fetchStatusCmd() tea.Cmd {
	return func() tea.Msg {
		status, err := a.connector.fetchStatus(a.ctx)
		if err != nil {
			return ConnMsg{State: ConnStateClosed, Err: err}
		}
		return StatusUpdateMsg{Status: status}
	}
}

func (a *App) submitCmd() tea.Cmd {
	return func() tea.Msg {
		// Submit will be implemented later
		return nil
	}
}

func (a *App) quitCmd() tea.Cmd {
	return func() tea.Msg {
		return tea.Quit()
	}
}
