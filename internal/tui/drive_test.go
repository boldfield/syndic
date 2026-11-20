package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/lockfile"
	tea "github.com/charmbracelet/bubbletea"
)

// driver runs an App against a real engine without a Program: it executes the
// commands the App returns and feeds their messages back on the test
// goroutine, so the test may read the App between messages without a race.
type driver struct {
	t     *testing.T
	paths lockfile.Paths
	app   *App
	msgs  chan tea.Msg
	// seen is every distinct strip the App showed, in order.
	seen []string
}

func newDriver(t *testing.T, socket string) *driver {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	d := &driver{t: t, app: NewApp(ctx, socket), msgs: make(chan tea.Msg, 1024)}
	d.app.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	d.run(d.app.Init())
	return d
}

func (d *driver) run(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	go func() {
		msg := cmd()
		if b, ok := msg.(tea.BatchMsg); ok {
			for _, c := range b {
				d.run(c)
			}
			return
		}
		if msg != nil {
			d.msgs <- msg
		}
	}()
}

func (d *driver) send(msg tea.Msg) {
	_, cmd := d.app.Update(msg)
	d.run(cmd)
	if st := d.strip(); len(d.seen) == 0 || d.seen[len(d.seen)-1] != st {
		d.seen = append(d.seen, st)
	}
}

func (d *driver) typeLine(s string) {
	d.send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)})
	d.send(tea.KeyMsg{Type: tea.KeyEnter})
}

// until feeds messages to the App until cond holds, calling each with the
// current View after every message.
func (d *driver) until(what string, cond func(view string) bool) {
	d.t.Helper()
	deadline := time.After(40 * time.Second)
	for {
		if cond(d.app.View()) {
			return
		}
		select {
		case m := <-d.msgs:
			d.send(m)
		case <-time.After(20 * time.Millisecond):
		case <-deadline:
			d.t.Fatalf("timed out waiting for %s; view:\n%s", what, d.app.View())
		}
	}
}

// strip is the status strip line of the current View: the first line below the banners.
func (d *driver) strip() string {
	for _, l := range strings.Split(d.app.View(), "\n") {
		if strings.HasPrefix(l, "[") || strings.HasPrefix(l, "engine: connecting") {
			return l
		}
	}
	return ""
}

// sawStrip reports whether any strip shown so far contained every part.
func (d *driver) sawStrip(parts ...string) bool {
	for _, st := range d.seen {
		ok := true
		for _, p := range parts {
			ok = ok && strings.Contains(st, p)
		}
		if ok {
			return true
		}
	}
	return false
}

func resizeMsg(w, h int) tea.Msg { return tea.WindowSizeMsg{Width: w, Height: h} }
