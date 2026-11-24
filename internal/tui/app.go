package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/boldfield/syndic/internal/client"
	"github.com/boldfield/syndic/internal/wire"
	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/google/uuid"
)

type tab int

const (
	tabChat tab = iota
	tabTranscript
	tabParticipants
	numTabs
)

// tabBarHeight and stripHeight are the one-line regions at the top and bottom.
const (
	tabBarHeight = 1
	stripHeight  = 1
)

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
	input         textarea.Model
	refusal       string         // the one-line message under the body: a refusal, or what a command did
	panel         []string       // lines shown in place of the tab until dismissed: /help, /why, the release warning
	panelRow      int            // first visible line of the panel; PgUp and PgDn move it
	question      *question      // the yes/no question being answered: a quick profile approval or provisional deadlines
	questions     []*question    // questions waiting behind it, or behind confirm
	confirm       *releasePrompt // a /release waiting for its reason; it takes the next line typed

	statusRefreshPending bool
	statusDirty          bool // a trigger arrived while a refresh was in flight
	now                  func() time.Time
	tickRunning          bool
	onEvent              func(wire.Frame)
	onSubmitRequest      func(wire.SubmitRequest)
	allowProvisional     bool // the user accepted provisional deadlines for this process

	content              *ContentCache
	drafts               *bool           // the user's drafts toggle; nil until set, then it overrides the quick profile
	quickSelection       *QuickSelection // quick profile state
	workDir              string          // where the quick profile search starts; the process directory when empty
	answerScrollRow      int             // first visible line of the answer tab
	candidateIDByTurn    map[string]string
	inputs               map[string]string           // what the user typed for each turn submitted by this process
	answerFollow         bool                        // the chat shows its last lines until the user scrolls up
	openBlocksByTurn     map[string][]wire.OpenBlock // open blocks from turn replies
	overrideReasons      map[string]string           // the reason of each override event
	terminalCapabilities TerminalCapabilities        // terminal color, alt-screen, mouse support
	want                 regionWant                  // optional regions the layout reserves lines for; none in milestone D
	inputOverflow        bool                        // the input's text was taller than it shows, so it may be scrolled
	rawMode              bool                        // /raw on|off; true disables Markdown rendering
	mouseEnabled         bool                        // /mouse on|off; default false

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

	reviewsDrawer    *ReviewsDrawer  // the reviews drawer state
	reviewCountFinal map[string]bool // true once the count was read from a terminal turn
	reviewCounts     map[string]*int // review_count per turn, from conversation info; a key marks the count as read
	reviewsLoading   map[string]bool // turns whose reviews are being fetched
	lineToTurnID     map[int]string  // map from chat view line to turn ID for click handling

	// Conversation startup options
	conversationID string   // the open conversation; empty until the first submit creates one
	convTurns      int      // turns the open conversation holds
	openTurns      []string // turns the conversation held when it was opened, oldest first
	roundsIsSet    bool     // a round limit was set, by --rounds or /rounds
	rounds         int      // round limit for this process
	roundsSource   string   // where the round limit came from: "--rounds" or "/rounds"

	// Notification state: process-scoped, not persisted
	notify                 bool              // /notify on|off; default false
	liveFromSeq            int64             // boundary seq between replay and live events
	lastProcessedSeq       int64             // highest seq processed, used to deduplicate bells
	missedTerminalEvents   map[string]string // turn ids -> outcome, for display on reconnect; keyed by turn id
	missedTerminalEventsTm map[string]string // turn ids -> timestamp for missed terminal events
	isFirstConnect         bool              // true until first successful connection
	emitBell               func()            // injectable bell emitter; if nil, bells are not emitted
}

// Option configures an App.
type Option func(*App)

// WithEventObserver calls f for every event frame the App applies, in order.
func WithEventObserver(f func(wire.Frame)) Option {
	return func(a *App) { a.onEvent = f }
}

// WithSubmitObserver calls f with every submit request the App sends, from
// the goroutine that sends it.
func WithSubmitObserver(f func(wire.SubmitRequest)) Option {
	return func(a *App) { a.onSubmitRequest = f }
}

// WithWorkDir starts the quick profile search in dir rather than the process's working directory.
func WithWorkDir(dir string) Option {
	return func(a *App) { a.workDir = dir }
}

// WithTerminalCapabilities sets the terminal rendering capabilities.
func WithTerminalCapabilities(caps TerminalCapabilities) Option {
	return func(a *App) { a.terminalCapabilities = caps }
}

// WithNotify enables terminal notifications for terminal events.
func WithNotify(notify bool) Option {
	return func(a *App) { a.notify = notify }
}

// WithMouse enables mouse input for tabs and drawer interaction.
func WithMouse(mouse bool) Option {
	return func(a *App) { a.mouseEnabled = mouse }
}

// WithBellEmitter sets a custom bell emitter function; used for testing.
func WithBellEmitter(emit func()) Option {
	return func(a *App) { a.emitBell = emit }
}

// WithConversation starts the app on the conversation id, which holds the
// turns with the given ids, oldest first. The caller has already resolved the
// id and checked its workspace. An empty id starts an empty conversation that
// the first submit creates. The chat shows only this conversation's turns.
func WithConversation(id string, turnIDs []string) Option {
	return func(a *App) { a.openConversation(id, turnIDs) }
}

// WithRounds sets the round limit for this process.
func WithRounds(n int) Option {
	return func(a *App) {
		a.setRounds(n, "--rounds")
	}
}

// NewApp returns an App that talks to the engine socket at socketPath.
func NewApp(ctx context.Context, socketPath string, opts ...Option) *App {
	ctx, cancel := context.WithCancel(ctx)
	a := &App{
		ctx:                    ctx,
		cancel:                 cancel,
		socket:                 socketPath,
		input:                  newInput(),
		answerFollow:           true,
		width:                  80,
		height:                 24,
		state:                  NewState(),
		now:                    time.Now,
		content:                NewContentCache(),
		candidateIDByTurn:      make(map[string]string),
		inputs:                 make(map[string]string),
		openBlocksByTurn:       make(map[string][]wire.OpenBlock),
		overrideReasons:        make(map[string]string),
		quarantined:            make(map[string]bool),
		blockDetail:            make(map[string]wire.OpenBlock),
		reviewsDrawer:          NewReviewsDrawer(),
		reviewCounts:           map[string]*int{},
		reviewsLoading:         make(map[string]bool),
		lineToTurnID:           make(map[int]string),
		missedTerminalEvents:   make(map[string]string),
		missedTerminalEventsTm: make(map[string]string),
		isFirstConnect:         true,
		emitBell:               func() { _, _ = fmt.Fprint(os.Stdout, "\x07") },
	}
	a.state.ConnState = ConnStateConnecting
	for _, o := range opts {
		o(a)
	}
	var errs []error
	if a.workDir == "" {
		wd, err := os.Getwd()
		if err != nil {
			errs = append(errs, fmt.Errorf("quick: cannot find the working directory: %w", err))
		}
		a.workDir = wd
	}
	qs, err := NewQuickSelection(a.workDir)
	if err != nil {
		errs = append(errs, err)
	}
	if len(errs) > 0 {
		a.refusal = errors.Join(errs...).Error()
	}
	a.quickSelection = qs
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
		plainMode:      !a.terminalCapabilities.Color,
		rawMode:        a.rawMode,
		now:            a.now(),
		width:          a.width,
		reviewCount:    a.reviewCounts[turnID],
	}
}

// chatIn gathers what the chat tab needs.
func (a *App) chatIn() chatInput {
	ci := chatInput{
		answer:               a.answerIn,
		inputs:               a.inputs,
		content:              a.content,
		width:                a.width,
		missedTerminalEvents: a.missedTerminalEvents,
		missedTerminalTimes:  a.missedTerminalEventsTm,
	}
	if a.reviewsDrawer.IsOpen() {
		ci.selectedDrawerTurnID = a.reviewsDrawer.selectedTurnID()
	}
	return ci
}

// fetchAnswerContent starts the fetches the answer tab is waiting on.
func (a *App) fetchAnswerContent() tea.Cmd {
	return tea.Batch(fetchRefs(a.ctx, a.cl, a.content, chatRefs(a.state, a.chatIn()))...)
}

// transcriptIn gathers what the transcript tab needs.
func (a *App) transcriptIn() transcriptInput {
	return transcriptInput{items: a.tItems, content: a.content, quarantined: a.quarantined, blockDetail: a.blockDetail, reloading: a.tLoading || a.tReload}
}

// fetchTranscriptContent starts the fetches the transcript tab is waiting on.
func (a *App) fetchTranscriptContent() tea.Cmd {
	return tea.Batch(fetchRefs(a.ctx, a.cl, a.content, transcriptRefs(a.tItems))...)
}

// fetchReviewsForTurn starts fetching reviews for a turn, paginating through all results.
func (a *App) fetchReviewsForTurn(turnID string) tea.Cmd {
	if turnID == "" || a.reviewsLoading[turnID] || a.cl == nil {
		return nil
	}
	a.reviewsLoading[turnID] = true
	return func() tea.Msg {
		var allItems []wire.ReviewItem
		cursor := (*string)(nil)
		limit := 50

		for {
			items, nextCursor, err := a.cl.VerifiedReviewsPage(a.ctx, turnID, cursor, &limit)
			if err != nil {
				return reviewsMsg{turnID: turnID, items: nil, content: map[string]string{}, err: err}
			}
			allItems = append(allItems, items...)
			if nextCursor == nil {
				break
			}
			cursor = nextCursor
		}

		content := make(map[string]string)
		var failed []string
		for i, item := range allItems {
			// Fetch oversized record if present
			if item.Oversized && item.Record != nil {
				fullItem, err := a.cl.FetchOversizedRecord(a.ctx, &item)
				if err != nil {
					failed = append(failed, formatRefusal(err))
					continue
				}
				allItems[i] = fullItem
				item = fullItem
			}

			// Fetch commentary
			if item.Commentary != nil {
				data, err := a.cl.FetchReviewContent(a.ctx, item.Commentary)
				if err != nil {
					failed = append(failed, formatRefusal(err))
				} else {
					content[item.Commentary.Ref] = string(data)
				}
			}

			// Fetch blocks' evidence and remedy
			for _, block := range item.Blocks {
				if block.Evidence != nil {
					data, err := a.cl.FetchReviewContent(a.ctx, block.Evidence)
					if err != nil {
						failed = append(failed, formatRefusal(err))
					} else {
						content[block.Evidence.Ref] = string(data)
					}
				}
				if block.Remedy != nil {
					data, err := a.cl.FetchReviewContent(a.ctx, block.Remedy)
					if err != nil {
						failed = append(failed, formatRefusal(err))
					} else {
						content[block.Remedy.Ref] = string(data)
					}
				}
			}
		}

		var warn error
		if len(failed) > 0 {
			warn = errors.New(strings.Join(failed, "; "))
		}
		return reviewsMsg{turnID: turnID, items: allItems, content: content, err: nil, warn: warn}
	}
}

// updateDrawerTurns updates the drawer with the current chat turns and roster order.
func (a *App) updateDrawerTurns() {
	turns := chatTurns(a.state)
	a.reviewsDrawer.SetTurns(turns)

	var participants []string
	if a.state.Status != nil {
		for _, p := range a.state.Status.Participants {
			participants = append(participants, p.ID)
		}
	}
	a.reviewsDrawer.SetRoster(participants)
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
	return tea.Batch(textarea.Blink, connectCmd(a.ctx, a.gen, a.socket, 0))
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

// update handles one message, then sets the input viewport the layout now
// gives it: View only reads the model.
func (a *App) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	w := a.input.Width()
	defer func() { fitInput(&a.input, a.inputRows(), a.input.Width() != w) }()
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.width, a.height = msg.Width, msg.Height
		if a.reviewsDrawer.IsOpen() && (a.width < regionMinWidth || a.height < regionMinHeight) {
			// A drawer too small to draw must not keep the keyboard.
			a.reviewsDrawer.Close()
			a.want.Drawer = false
		}
		a.input.SetWidth(max(a.width, 1))
		return a, nil
	case tea.KeyMsg:
		return a.updateKey(msg)
	case tea.MouseMsg:
		return a.updateMouse(msg)
	case connectedMsg:
		return a.onConnected(msg)
	case eventMsg:
		return a.onFrame(msg)
	case submitMsg:
		return a, a.onSubmit(msg)
	case statusMsg:
		return a.onStatusRefresh(msg)
	case reviewCountsMsg:
		return a.onReviewCounts(msg)
	case turnMsg:
		return a.onTurn(msg)
	case commandMsg:
		return a.onCommand(msg)
	case whyMsg:
		return a.onWhy(msg)
	case switchMsg:
		return a.onSwitch(msg)
	case conversationsMsg:
		return a.onConversations(msg)
	case contentMsg:
		return a.onContent(msg)
	case transcriptMsg:
		return a.onTranscript(msg)
	case reviewsMsg:
		return a.onReviews(msg)
	case tickMsg:
		// The message itself re-renders; keep the one tick chain going while a turn is open.
		a.tickRunning = false
		a.reviewsDrawer.AnimationTick()
		return a, a.ensureTick()
	}
	return a, a.updateInput(msg)
}

func (a *App) updateKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if a.question != nil {
		// A question owns the keyboard: only y/n and esc answer it, ctrl+c
		// and ctrl+d clear or detach as anywhere else, and the scroll keys
		// move a panel taller than the tab.
		if a.scrollPanel(msg.String()) {
			return a, nil
		}
		switch msg.String() {
		case "y", "Y":
			return a, a.answer(true)
		case "n", "N", "esc":
			return a, a.answer(false)
		case "ctrl+c", "ctrl+d":
			cmd, _ := a.detachKey(msg.String())
			return a, cmd
		}
		return a, nil
	}
	if a.confirm != nil {
		// A confirmation owns the keyboard: the line is its answer.
		switch msg.String() {
		case "ctrl+c", "ctrl+d":
			if cmd, ok := a.detachKey(msg.String()); ok {
				return a, cmd
			}
		case "esc":
			a.cancelConfirm("release cancelled; nothing sent")
			return a, nil
		case "enter":
			return a, a.confirmEnter()
		}
		return a, a.updateInput(msg)
	}

	// Drawer focus takes priority: it blocks input changes and navigation keys.
	if a.reviewsDrawer.IsOpen() && a.tab == tabChat {
		drawerHeight := a.layout().Drawer
		// Turns that arrived while the drawer is open become selectable.
		a.updateDrawerTurns()
		switch msg.String() {
		case "ctrl+c", "ctrl+d":
			// Only the empty-input detach passes through; the input is
			// not the drawer's to clear.
			if a.input.Value() == "" {
				return a, a.quit()
			}
			return a, nil
		case "up":
			a.reviewsDrawer.SelectUp()
			if turnID := a.reviewsDrawer.selectedTurnID(); turnID != "" {
				return a, a.fetchReviewsForTurn(turnID)
			}
			return a, nil
		case "down":
			a.reviewsDrawer.SelectDown()
			if turnID := a.reviewsDrawer.selectedTurnID(); turnID != "" {
				return a, a.fetchReviewsForTurn(turnID)
			}
			return a, nil
		case "left":
			a.reviewsDrawer.SelectRoundLeft()
			return a, nil
		case "right":
			a.reviewsDrawer.SelectRoundRight()
			return a, nil
		case "pgup":
			a.reviewsDrawer.ScrollUp(drawerHeight)
			return a, nil
		case "pgdown":
			a.reviewsDrawer.ScrollDown(drawerHeight, a.reviewsDrawer.ContentLines(a.width, a.content))
			return a, nil
		case "esc", "ctrl+o":
			a.reviewsDrawer.Close()
			a.want.Drawer = false
			return a, nil
		default:
			// Input cannot change while drawer is focused; ignore other keys
			return a, nil
		}
	}

	if msg.Paste {
		// A paste is text, whatever it contains: line breaks stay as typed.
		a.input.InsertString(normalizePaste(msg.Runes))
		return a, a.updateInput(nil)
	}
	switch msg.String() {
	case "ctrl+c", "ctrl+d":
		if cmd, ok := a.detachKey(msg.String()); ok {
			return a, cmd
		}
	case "esc":
		a.panel, a.refusal, a.panelRow = nil, "", 0
		a.reviewsDrawer.Close()
		a.want.Drawer = false
		return a, nil
	case "ctrl+o":
		if a.tab == tabChat {
			if a.reviewsDrawer.IsOpen() {
				a.reviewsDrawer.Close()
				a.want.Drawer = false
			} else if a.width >= regionMinWidth && a.height >= regionMinHeight {
				turns := chatTurns(a.state)
				if len(turns) > 0 {
					a.updateDrawerTurns()
					hs := a.layout()
					a.reviewsDrawer.SetDrawerHeight(hs.Drawer)
					a.reviewsDrawer.Open(len(turns) - 1)
					a.want.Drawer = true
					return a, a.fetchReviewsForTurn(turns[len(turns)-1])
				}
			}
		}
		return a, nil
	case "tab":
		a.panel = nil
		a.tab = (a.tab + 1) % numTabs
		a.answerScrollRow, a.answerFollow = 0, true
		if a.tab != tabChat {
			a.reviewsDrawer.Close()
			a.want.Drawer = false
		}
		a.updateDrawerTurns()
		return a, nil
	case "shift+tab":
		a.panel = nil
		a.tab = (a.tab + numTabs - 1) % numTabs
		a.answerScrollRow, a.answerFollow = 0, true
		if a.tab != tabChat {
			a.reviewsDrawer.Close()
			a.want.Drawer = false
		}
		a.updateDrawerTurns()
		return a, nil
	case "enter":
		return a, a.enter()
	}

	if len(a.panel) > 0 {
		// A panel stands in for the tab, so the scroll keys move it.
		if !a.scrollPanel(msg.String()) {
			return a, a.updateInput(msg)
		}
		return a, nil
	}

	if a.tab == tabTranscript {
		switch msg.String() {
		case "up", "down", "pgup", "pgdown", "home", "end":
			a.scrollTranscript(msg.String())
			return a, nil
		}
	}
	if a.tab == tabChat {
		page := max(a.answerHeight()-1, 1)
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

	return a, a.updateInput(msg)
}

// detachKey handles Ctrl+C and Ctrl+D. Ctrl+C clears a non-empty input;
// either detaches on an empty one, exactly as /quit. Neither cancels a turn:
// detaching leaves the engine alone. ok is false when the key is the input's,
// as Ctrl+D (delete forward) is on a non-empty line.
func (a *App) detachKey(k string) (cmd tea.Cmd, ok bool) {
	if a.input.Value() != "" {
		if k == "ctrl+d" {
			return nil, false
		}
		a.clearInput()
		return nil, true
	}
	return a.quit(), true
}

// updateMouse handles mouse events when mouse is enabled.
func (a *App) updateMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if !a.mouseEnabled || !a.terminalCapabilities.Color {
		return a, nil
	}

	switch msg.Button {
	case tea.MouseButtonLeft:
		switch msg.Action {
		case tea.MouseActionPress:
			return a, a.handleMouseClick(msg.X, msg.Y)
		}
	}
	return a, nil
}

// handleMouseClick processes a mouse click at the given coordinates.
func (a *App) handleMouseClick(x, y int) tea.Cmd {
	// Check for tab bar clicks (first line)
	if y == 0 && x >= 0 {
		return a.handleTabClick(x)
	}

	// Check for review count clicks in the chat tab
	if !a.reviewsDrawer.IsOpen() && y > 0 && y < a.height-2 {
		return a.handleReviewCountClick(x, y)
	}

	// Check for drawer round header clicks
	if a.reviewsDrawer.IsOpen() {
		drawerStart := a.height - a.reviewsDrawer.drawerHeight - 1
		if y >= drawerStart && y < drawerStart+1 {
			return a.handleDrawerRoundClick(x)
		}
	}

	return nil
}

// handleTabClick processes a click in the tab bar by calculating actual tab positions.
func (a *App) handleTabClick(x int) tea.Cmd {
	names := []string{"chat", "transcript", "participants"}

	pos := 0
	for i, name := range names {
		selected := tab(i) == a.tab
		if selected {
			name = "[" + name + "]"
		}
		tabStart := pos
		tabEnd := pos + len(name)

		if x >= tabStart && x < tabEnd {
			if tab(i) != a.tab {
				a.tab = tab(i)
			}
			return nil
		}

		pos = tabEnd + 2
	}

	return nil
}

// handleReviewCountClick processes a click on a review count in the chat.
func (a *App) handleReviewCountClick(x int, y int) tea.Cmd {
	if a.tab != tabChat {
		return nil
	}

	hs := a.layout()

	// Map screen y coordinate to chat view line
	chatStartY := tabBarHeight + hs.Banners
	if y < chatStartY || y >= chatStartY+hs.Body {
		return nil
	}

	viewLine := y - chatStartY + a.answerScrollRow

	// Find which turn this line belongs to
	turnID, ok := a.lineToTurnID[viewLine]
	if !ok || turnID == "" {
		return nil
	}

	// Check if this turn has reviews
	count, hasCount := a.reviewCounts[turnID]
	if !hasCount || count == nil || *count == 0 {
		return nil
	}

	// Find the turn index in the drawer's turn list
	ids := chatTurns(a.state)
	var turnIdx int
	for i, id := range ids {
		if id == turnID {
			turnIdx = i
			break
		}
	}

	a.reviewsDrawer.Open(turnIdx)
	a.reviewsDrawer.SetDrawerHeight(hs.Drawer)
	a.ensureTick()
	return nil
}

// handleDrawerRoundClick processes a click in the drawer header for round selection.
func (a *App) handleDrawerRoundClick(x int) tea.Cmd {
	rounds := a.reviewsDrawer.getRounds()
	if len(rounds) <= 1 {
		return nil
	}

	plainMode := !a.terminalCapabilities.Color
	drawerOutput := a.reviewsDrawer.render(a.width, a.content, plainMode)
	lines := strings.Split(drawerOutput, "\n")
	if len(lines) == 0 {
		return nil
	}

	headerLine := lines[0]
	a.reviewsDrawer.SelectRoundByClick(headerLine, x)
	return nil
}

// updateInput passes a message to the input. When its text shrinks back to
// fit, it is laid out again from its first line, so no line above the cursor
// stays scrolled out of sight.
func (a *App) updateInput(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	a.input, cmd = a.input.Update(msg)
	n := inputLines(a.input)
	if a.inputOverflow && n <= maxInputRows {
		row, li := a.input.Line(), a.input.LineInfo()
		a.input.SetValue(a.input.Value())
		for a.input.Line() > row {
			a.input.CursorUp()
		}
		a.input.SetCursor(li.StartColumn + li.ColumnOffset)
	}
	if n > maxInputRows {
		// The input scrolls to its cursor against the lines it last drew, so
		// it draws the new ones before scrolling again: a paste of many lines
		// shows its last line at once.
		_ = a.input.View()
		a.input, _ = a.input.Update(nil)
	}
	a.inputOverflow = n > maxInputRows
	return cmd
}

// scrollPanel moves a shown panel for a scroll key, and reports whether it did.
func (a *App) scrollPanel(k string) bool {
	if len(a.panel) == 0 {
		return false
	}
	page := max(a.bodyHeight()-1, 1)
	switch k {
	case "up":
		a.panelRow--
	case "down":
		a.panelRow++
	case "pgup":
		a.panelRow -= page
	case "pgdown":
		a.panelRow += page
	default:
		return false
	}
	a.panelRow = min(max(a.panelRow, 0), a.maxPanelRow())
	return true
}

// setPrompt changes the input's prompt.
func (a *App) setPrompt(p string) { setInputPrompt(&a.input, p, a.width) }

// clearInput empties the input.
func (a *App) clearInput() {
	a.input.Reset()
	a.inputOverflow = false
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
	a.refusal, a.panel, a.panelRow = "", nil, 0
	if isCommand(line) {
		a.clearInput()
		return dispatch(a, line)
	}
	if a.cl == nil {
		a.refusal = "engine: not connected"
		return nil
	}
	var profiles []wire.QuickProfile
	if a.state.Status != nil {
		profiles = a.state.Status.QuickProfiles
	}
	// The request is sent from another goroutine, so it points at copies,
	// never at fields that later messages change.
	var conversationID *string
	if id := a.conversationID; id != "" {
		conversationID = &id
	}
	var maxRounds *int
	if n := a.rounds; a.roundsIsSet {
		maxRounds = &n
	}
	var contextFrom *string
	if from := a.state.ContextFrom; from != "" {
		contextFrom = &from
	}
	return a.send(submission{
		input:            line,
		preset:           a.state.NextPreset,
		quickProfile:     a.quickSelection.selected(profiles),
		allowProvisional: a.allowProvisional,
		workspace:        a.workDir,
		conversationID:   conversationID,
		maxRounds:        maxRounds,
		contextFromTurn:  contextFrom,
	})
}

// send submits under a new submission id.
func (a *App) send(sub submission) tea.Cmd {
	sub.id = uuid.NewString()
	if a.state.Scope != nil {
		a.state.Scope.Submissions[sub.id] = true
	}
	// The submission id ties a /mode preset to the turn it started, whatever order the reply and the event arrive in.
	if sub.preset != "" {
		a.state.ModeSubmissionID = sub.id
	}
	return submitCmd(a.ctx, a.cl, sub, a.onSubmitRequest)
}

// ensureTick starts the one-second tick chain if a turn is open, drawer is animating, or none runs.
func (a *App) ensureTick() tea.Cmd {
	needsTick := a.state.CurrentTurn != "" || a.reviewsDrawer.IsAnimating()
	if !needsTick || a.tickRunning {
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
	var we *wire.Error
	if errors.As(msg.err, &we) && we.Code == wire.ErrorCodeProvisional && !msg.sub.allowProvisional {
		return a.onProvisional(msg.sub)
	}
	if msg.err != nil {
		if errors.As(msg.err, &we) && we.Code == wire.ErrorCodeBusy {
			// The open turn may be another conversation's, after /new or /switch.
			a.say(formatRefusal(msg.err) + a.openTurnOwner(we))
			return nil
		}
		a.refusal = formatRefusal(msg.err)
		return nil
	}
	// The reply names the conversation the turn joined; later submits continue it.
	if msg.conversationID != "" {
		a.conversationID = msg.conversationID
		if a.state.Scope != nil {
			a.state.Scope.ConversationID = msg.conversationID
		}
	}
	if a.state.Scope != nil {
		a.state.Scope.add(msg.turnID)
	}
	a.inputs[msg.turnID] = msg.sub.input
	a.state.TurnConversation = withTurnConversation(a.state.TurnConversation, msg.turnID, msg.conversationID)
	a.convTurns++
	a.clearInput()
	a.state.NextPreset = "" // /mode applies to one submit
	if isTerminalState(a.state.TurnState[msg.turnID]) {
		return nil
	}
	oldTurn := a.state.CurrentTurn
	a.state.CurrentTurn = msg.turnID
	if oldTurn != msg.turnID {
		a.answerScrollRow, a.answerFollow = 0, true
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
	a.updateQuickProfileState()
	a.state.ConnState = ConnStateConnected
	a.statusRefreshPending, a.statusDirty = false, false
	a.liveFromSeq = msg.liveFromSeq
	// The first connection's replay is history this process never saw live:
	// count it as processed so no notice or bell claims it. live_from_seq is
	// the first live seq, so the boundary event itself is not covered.
	if a.isFirstConnect {
		a.lastProcessedSeq = max(a.lastProcessedSeq, a.liveFromSeq-1)
		a.isFirstConnect = false
	}
	cmds := []tea.Cmd{readCmd(a.ctx, a.gen, a.cl)}
	open := ""
	for _, id := range msg.status.TurnsOpen {
		if a.state.inScope(id) {
			open = id
			break
		}
	}
	// The chat shows every turn of the conversation, so each turn's record is read.
	for _, id := range a.openTurns {
		if _, ok := a.state.Turns[id]; !ok && id != open {
			cmds = append(cmds, turnCmd(a.ctx, a.gen, a.cl, id))
		}
	}
	if open != "" {
		oldTurn := a.state.CurrentTurn
		a.state.CurrentTurn = open
		if oldTurn != a.state.CurrentTurn {
			a.answerScrollRow, a.answerFollow = 0, true
		}
		a.ensureTurnInfo()
		// The limit and deadline of a turn that is already open come from its record.
		cmds = append(cmds, turnCmd(a.ctx, a.gen, a.cl, a.state.CurrentTurn))
	}
	cmds = append(cmds, a.ensureTick(), a.syncTranscript(), a.reloadTranscript())
	return a, tea.Batch(cmds...)
}

// conversationHeader names the open conversation by id prefix and turn count.
func (a *App) conversationHeader() string {
	if a.conversationID == "" {
		return "conversation: new (created at the first submit) · workspace " + safeLine(a.workDir)
	}
	return fmt.Sprintf("conversation %s, %d turns · workspace %s", shortID(a.conversationID), a.convTurns, safeLine(a.workDir))
}

// withScrollInfo returns text with info flush against the right edge of a
// line w cells wide, or text alone when both do not fit: scroll info never
// costs the text a character.
func withScrollInfo(text string, w int, info string) string {
	gap := w - ansi.StringWidth(text) - ansi.StringWidth(info)
	if gap < 1 && text != "" {
		return text
	}
	return text + strings.Repeat(" ", max(gap, 0)) + info
}

// turnsAbove is how many turns lie wholly above line first, given the line
// each turn starts at and the total number of lines.
func turnsAbove(starts []int, total, first int) int {
	n := 0
	for i := range starts {
		end := total
		if i+1 < len(starts) {
			end = starts[i+1]
		}
		if end <= first {
			n++
		}
	}
	return n
}

// shortID is the first eight characters of an id.
func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// updateQuickProfileState sets the quick profile banners from the project
// selection and the roster's profiles, and opens the approval prompt when the
// operator has not yet answered for this state.
func (a *App) updateQuickProfileState() {
	a.state.UnapprovedProfile, a.state.QuickInvalid = nil, ""
	qs := a.quickSelection
	if qs.Override != nil {
		return // typed by the operator: no approval, no banner
	}
	if qs.Invalid != "" {
		a.state.QuickInvalid = qs.Invalid
		return
	}
	if qs.ProjectProfile == "" || a.state.Status == nil {
		return
	}
	var def *wire.QuickProfile
	for i := range a.state.Status.QuickProfiles {
		if a.state.Status.QuickProfiles[i].Name == qs.ProjectProfile {
			def = &a.state.Status.QuickProfiles[i]
		}
	}
	if def == nil {
		a.cancelApproval() // an absent profile cannot be approved
		msg := qs.ProjectProfile + " (engine has no such profile)"
		a.state.UnapprovedProfile = &msg
		return
	}
	if qs.approvedFor(def.DefinitionSHA256) {
		a.cancelApproval()
		return
	}
	a.state.UnapprovedProfile = &qs.ProjectProfile
	if qs.declined[qs.stateKey(def.DefinitionSHA256)] {
		return
	}
	a.requestApproval(*def)
}

// requestApproval shows the file, the profile and its effect and asks yes or
// no. An approval already pending for another state is replaced.
func (a *App) requestApproval(def wire.QuickProfile) {
	qs := a.quickSelection
	if ap := a.pendingApproval(); ap != nil {
		if ap.definitionSHA256 == def.DefinitionSHA256 && ap.fileSHA256 == qs.ProjectFileSHA {
			return
		}
		a.cancelApproval()
	}
	ap := &approvalPrompt{
		filePath:         qs.ProjectFilePath,
		profileName:      qs.ProjectProfile,
		fileSHA256:       qs.ProjectFileSHA,
		definitionSHA256: def.DefinitionSHA256,
	}
	budgets := "none"
	if def.Budgets != nil {
		if b, err := json.Marshal(def.Budgets); err == nil {
			budgets = string(b)
		}
	}
	a.ask(&question{approval: ap, yes: approvalYes(ap), no: approvalNo(ap), lines: []string{
		"Project file: " + safeLine(qs.ProjectFilePath),
		"selects quick profile \"" + safeLine(qs.ProjectProfile) + "\":",
		"",
		"  preset:  " + safeLine(def.Preset),
		fmt.Sprintf("  drafts:  %v", def.Drafts),
		"  budgets: " + budgets,
		"",
		"Approve this selection? (y/n)",
	}})
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

	// Handle notifications for terminal events
	bell, notice, turnID, outcome, at := a.shouldNotify(msg.frame)
	if notice {
		a.recordMissedTerminalEvent(turnID, outcome, at)
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
	if bell {
		bellCmd := a.bellCmd()
		if bellCmd != nil {
			cmds = append(cmds, bellCmd)
		}
	}
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
	a.updateQuickProfileState()
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
	refresh := a.refreshReviewCounts(msg.id)
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
	return a, tea.Batch(a.syncTranscript(), refresh)
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

// layout allocates the screen's lines to its regions; see allocate.
func (a *App) layout() heights {
	return allocate(layoutIn{
		Height:     a.height,
		Width:      a.width,
		Banners:    len(renderBanners(a.state)),
		InputLines: inputLines(a.input),
		Want:       a.want,
	})
}

// bodyHeight is the number of lines left for the active tab.
func (a *App) bodyHeight() int { return a.layout().Body }

// inputRows is how many lines the input viewport shows, 1 to maxInputViewport;
// a longer text scrolls inside it.
func (a *App) inputRows() int { return a.layout().Input }

// panelLines is the panel as shown, wrapped to the width.
func (a *App) panelLines() []string { return strings.Split(renderPanel(a.panel, a.width), "\n") }

// maxPanelRow is the last first-line that still fills the body.
func (a *App) maxPanelRow() int { return max(len(a.panelLines())-a.bodyHeight(), 0) }

// isTerminalTooSmall reports whether the terminal is below the minimum size (40×10).
func (a *App) isTerminalTooSmall() bool {
	return a.width < 40 || a.height < 10
}

// answerHeight is the lines of the chat tab below its conversation header.
func (a *App) answerHeight() int {
	return max(a.bodyHeight()-1, 0)
}

// chatStart is the first line of the chat the tab shows, given its line
// count and where the newest turn starts. The tab follows the newest turn
// until the user scrolls: its end when it fits, else its first line.
func (a *App) chatStart(total, lastTurn int) int {
	maxStart := max(total-a.answerHeight(), 0)
	if a.answerFollow {
		return min(lastTurn, maxStart)
	}
	return min(a.answerScrollRow, maxStart)
}

// scrollAnswer moves the chat tab by delta lines, staying within the text.
func (a *App) scrollAnswer(delta int) {
	lines, lastTurn := chatLines(a.state, a.chatIn())
	total := len(lines)
	maxStart := max(total-a.answerHeight(), 0)
	row := min(max(a.chatStart(total, lastTurn)+delta, 0), maxStart)
	a.answerScrollRow = row
	// Scrolling back to where following would stand resumes it, so that a
	// newer turn comes into view; at the bottom of a long turn it would not.
	a.answerFollow = row == min(lastTurn, maxStart)
}

// tabBar names the tabs, the active one in brackets.
func (a *App) tabBar() string {
	names := []string{"chat", "transcript", "participants"}
	parts := make([]string, len(names))
	for i, n := range names {
		if tab(i) == a.tab {
			n = "[" + n + "]"
		}
		parts[i] = n
	}
	return strings.Join(parts, "  ")
}

// statusLine is the strip with the hints at its right edge, w cells wide:
// ctrl+o: reviews while the chat tab is active, then keyHint.
func (a *App) statusLine(w int) string {
	return renderStatusLine(a.state, w, a.now(), a.tab == tabChat)
}

// View lays out the tab bar, the active tab, banners, the input and, last, the status line.
func (a *App) View() string {
	w := a.width

	// When the terminal is too small, show a resize message
	if a.isTerminalTooSmall() {
		msg := "Terminal too small. Resize to 40×10."
		if ansi.StringWidth(msg) > w {
			msg = "Terminal too small."
		}
		return ansi.Truncate(msg, w, "")
	}

	hs := a.layout()
	banners := foldBanners(renderBanners(a.state), hs.Banners)

	// Clip all banners to width
	for i, banner := range banners {
		banners[i] = ansi.Truncate(banner, w, "")
	}

	bodyHeight := hs.Body

	var tabView string
	lastTurn := 0
	var turnStarts []int
	switch a.tab {
	case tabChat:
		var chat []string
		chat, lastTurn, turnStarts = chatLinesWithStarts(a.state, a.chatIn())
		tabView = strings.Join(chat, "\n")

		a.lineToTurnID = make(map[int]string)
		turnIDs := chatTurns(a.state)
		for i, turnID := range turnIDs {
			if i < len(turnStarts) {
				startLine := turnStarts[i]
				endLine := len(chat)
				if i+1 < len(turnStarts) {
					endLine = turnStarts[i+1]
				}
				for line := startLine; line < endLine; line++ {
					a.lineToTurnID[line] = turnID
				}
			}
		}
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
	lines := strings.Split(tabView, "\n")
	if len(a.panel) > 0 {
		lines = a.panelLines()
		start := min(a.panelRow, a.maxPanelRow())
		lines = lines[start:]
		if len(lines) > bodyHeight && bodyHeight > 0 {
			// The panel goes on below: its last visible line says so.
			lines = append(lines[:bodyHeight-1], fmt.Sprintf("… %d more lines (PgDn)", len(lines)-bodyHeight+1))
		}
	}

	if a.tab == tabChat && len(a.panel) == 0 {
		answerHeight := max(bodyHeight-1, 0)
		start := a.chatStart(len(lines), lastTurn)
		total := len(lines)
		lines = lines[start:]
		for len(lines) < answerHeight {
			lines = append(lines, "")
		}
		// The first line of the chat names the open conversation, and at its
		// right how many turns are above the view and where the view stands.
		header := a.conversationHeader()
		lines = append([]string{withScrollInfo(header, w, fmt.Sprintf("%d earlier · %d%%",
			turnsAbove(turnStarts, total, start), scrollPercent(total, answerHeight, start)))}, lines[:answerHeight]...)
	}
	for len(lines) < bodyHeight {
		lines = append(lines, "")
	}
	lines = lines[:bodyHeight]

	// Clip all lines to width to handle wide characters and ANSI sequences
	for i, line := range lines {
		lines[i] = ansi.Truncate(line, w, "")
	}

	// Clip input lines to width
	inputLines := renderInput(a.input, ToSafeText([]byte(a.refusal)), w, hs.Input)
	inputLinesList := strings.Split(inputLines, "\n")
	for i, line := range inputLinesList {
		inputLinesList[i] = ansi.Truncate(line, w, "")
	}
	inputLines = strings.Join(inputLinesList, "\n")

	// Build output: tab bar, tab, banners, the drawer or list, the attached
	// line, short help, input, status line. The optional regions' lines are
	// reserved here and filled by the tasks that own them.
	tabBar := a.tabBar()
	if a.tab == tabTranscript {
		// The transcript shows where its view stands at the tab bar's right.
		tabBar = withScrollInfo(tabBar, w, fmt.Sprintf("%d%%", scrollPercent(len(a.tLines), bodyHeight, a.tRow)))
	}
	output := []string{ansi.Truncate(tabBar, w, "")}
	output = append(output, lines...)
	output = append(output, banners...)

	// Add drawer, list, attached, and help lines
	a.reviewsDrawer.SetDrawerHeight(hs.Drawer)
	plainMode := !a.terminalCapabilities.Color
	drawerLines := strings.Split(a.reviewsDrawer.render(w, a.content, plainMode), "\n")
	for i := 0; i < hs.Drawer; i++ {
		if i < len(drawerLines) {
			output = append(output, ansi.Truncate(drawerLines[i], w, ""))
		} else {
			output = append(output, "")
		}
	}
	for range hs.List + hs.Attached + hs.Help {
		output = append(output, "")
	}

	output = append(output, inputLines)
	output = append(output, a.statusLine(w))
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

// reviewsMsg carries the response from a reviews request.
type reviewsMsg struct {
	turnID  string
	items   []wire.ReviewItem
	content map[string]string // ref -> fetched content
	err     error
	warn    error // content that could not be fetched or verified; the rest is still shown
}

// onReviews processes a reviews response and stores fetched content.
func (a *App) onReviews(msg reviewsMsg) (tea.Model, tea.Cmd) {
	a.reviewsLoading[msg.turnID] = false
	if msg.err != nil {
		a.reviewsDrawer.SetReviewsError(msg.turnID, msg.err)
		a.say("reviews: " + formatRefusal(msg.err))
		return a, nil
	}
	for ref, data := range msg.content {
		a.content.Store(ref, data, nil)
	}
	a.reviewsDrawer.UpdateReviews(msg.turnID, msg.items)
	if msg.warn != nil {
		a.say("reviews: some content is unavailable: " + msg.warn.Error())
	}
	return a, nil
}

// ExitMessage is what to print after the TUI exits: a turn still open keeps
// running without it.
func ExitMessage(a *App) string {
	if id := a.CurrentTurn(); id != "" {
		return "turn " + id + " is still running and will continue"
	}
	return ""
}

// clipToWidth clips a line to a maximum display width, accounting for wide characters and ANSI sequences.
// This function handles ANSI escape sequences properly without breaking them or counting them as visible width.
func clipToWidth(line string, width int) string {
	if width <= 0 {
		return ""
	}
	return ansi.Truncate(line, width, "")
}
