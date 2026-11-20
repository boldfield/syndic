package tui

import (
	"context"
	"errors"
	"time"

	"github.com/boldfield/syndic/internal/client"
	"github.com/boldfield/syndic/internal/wire"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/google/uuid"
)

const (
	minBackoff = 100 * time.Millisecond
	maxBackoff = 2 * time.Second
)

// connectedMsg reports a finished connect, hello, status and subscribe. gen
// says which connection attempt it belongs to; Update drops stale ones.
type connectedMsg struct {
	gen    int
	cl     *client.Client
	status wire.StatusReply
}

// eventMsg carries the next frame of a connection's event stream, or the
// error that ended it.
type eventMsg struct {
	gen   int
	frame wire.Frame
	err   error
}

// submitMsg is the outcome of a submit.
type submitMsg struct {
	turnID  string
	refusal string
}

// tickMsg is sent every second for elapsed time updates.
type tickMsg struct{}

// connectCmd dials until it succeeds, then fetches status and subscribes after
// sinceSeq. It touches no shared state; everything it learns travels in the
// returned message.
func connectCmd(ctx context.Context, gen int, socket string, sinceSeq int64) tea.Cmd {
	return func() tea.Msg {
		backoff := minBackoff
		for {
			if cl, status, err := connectOnce(ctx, socket, sinceSeq); err == nil {
				return connectedMsg{gen: gen, cl: cl, status: status}
			}
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(backoff):
			}
			backoff = min(backoff*2, maxBackoff)
		}
	}
}

func connectOnce(ctx context.Context, socket string, sinceSeq int64) (*client.Client, wire.StatusReply, error) {
	cl, err := client.Dial(ctx, socket, "syndic-tui", "0")
	if err != nil {
		return nil, wire.StatusReply{}, err
	}
	status, err := cl.Status(ctx)
	if err == nil {
		_, err = cl.Subscribe(ctx, sinceSeq)
	}
	if err != nil {
		_ = cl.Close()
		return nil, wire.StatusReply{}, err
	}
	return cl, status, nil
}

// readCmd waits for the connection's next event.
func readCmd(ctx context.Context, gen int, cl *client.Client) tea.Cmd {
	return func() tea.Msg {
		f, err := cl.NextEvent(ctx)
		return eventMsg{gen: gen, frame: f, err: err}
	}
}

// submitCmd submits input with a fresh submission id and optional preset.
func submitCmd(ctx context.Context, cl *client.Client, input, preset string) tea.Cmd {
	return func() tea.Msg {
		req := wire.SubmitRequest{SubmissionID: uuid.NewString(), Input: input}
		if preset != "" {
			req.Preset = &preset
		}
		reply, err := cl.Submit(ctx, req)
		if err != nil {
			return submitMsg{refusal: formatRefusal(err)}
		}
		return submitMsg{turnID: reply.TurnID}
	}
}

// statusMsg carries a refreshed status reply or the error that occurred.
type statusMsg struct {
	gen    int
	status wire.StatusReply
	err    error
}

// statusCmd requests an updated status from the engine.
func statusCmd(ctx context.Context, gen int, cl *client.Client) tea.Cmd {
	return func() tea.Msg {
		status, err := cl.Status(ctx)
		return statusMsg{gen: gen, status: status, err: err}
	}
}

// turnMsg carries a fetched turn reply or the error that occurred.
type turnMsg struct {
	gen  int
	turn *wire.TurnReply
	id   string
	err  error
}

// turnCmd requests the full turn record from the engine.
func turnCmd(ctx context.Context, gen int, cl *client.Client, turnID string) tea.Cmd {
	return func() tea.Msg {
		turn, err := cl.Turn(ctx, turnID)
		if err != nil {
			return turnMsg{gen: gen, id: turnID, err: err}
		}
		return turnMsg{gen: gen, turn: &turn, id: turnID}
	}
}

// tickCmd sends a tick message every second.
func tickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg {
		return tickMsg{}
	})
}

// formatRefusal renders a refusal as its code and detail on one line.
func formatRefusal(err error) string {
	var we *wire.Error
	if errors.As(err, &we) {
		if we.Detail != nil && *we.Detail != "" {
			return we.Code + ": " + *we.Detail
		}
		return we.Code
	}
	return err.Error()
}

// cancelMsg carries the result of a cancel request or the error that occurred.
type cancelMsg struct {
	turnID  string
	refusal string
}

// cancelCmd sends a cancel request to the engine.
func cancelCmd(ctx context.Context, cl *client.Client, turnID string) tea.Cmd {
	return func() tea.Msg {
		err := cl.Cancel(ctx, turnID)
		if err != nil {
			return cancelMsg{turnID: turnID, refusal: formatRefusal(err)}
		}
		return cancelMsg{turnID: turnID}
	}
}

// overrideMsg carries the result of an override request or the error that occurred.
type overrideMsg struct {
	turnID  string
	refusal string
}

// overrideCmd sends an override request to the engine.
func overrideCmd(ctx context.Context, cl *client.Client, turnID, reason string) tea.Cmd {
	return func() tea.Msg {
		err := cl.Override(ctx, turnID, reason)
		if err != nil {
			return overrideMsg{turnID: turnID, refusal: formatRefusal(err)}
		}
		return overrideMsg{turnID: turnID}
	}
}

// releaseMsg carries the result of a release request or the error that occurred.
type releaseMsg struct {
	refusal string
}

// releaseCmd sends a release request to the engine.
func releaseCmd(ctx context.Context, cl *client.Client, pool string, attemptID *string, reason string) tea.Cmd {
	return func() tea.Msg {
		req := wire.ReleaseRequest{Pool: pool, AttemptID: attemptID, Reason: reason}
		_, err := cl.Release(ctx, req)
		if err != nil {
			return releaseMsg{refusal: handleReleaseRefusal(err)}
		}
		return releaseMsg{}
	}
}

// transcriptPageSize is the page size asked of the engine, its maximum.
const transcriptPageSize = 200

// transcriptMsg carries every item of a turn's transcript, or the error that
// stopped the load.
type transcriptMsg struct {
	gen   int
	turn  string
	items []wire.TranscriptItem
	err   error
}

// transcriptCmd loads a turn's transcript, following next_cursor until there
// is none. It returns the whole list at once, so a reload never leaves a
// half-loaded transcript on screen.
func transcriptCmd(ctx context.Context, gen int, cl *client.Client, turnID string) tea.Cmd {
	return func() tea.Msg {
		var items []wire.TranscriptItem
		cursor := ""
		for {
			reply, err := cl.Transcript(ctx, turnID, cursor, transcriptPageSize)
			if err != nil {
				return transcriptMsg{gen: gen, turn: turnID, err: err}
			}
			items = append(items, reply.Items...)
			if reply.NextCursor == nil || *reply.NextCursor == "" {
				return transcriptMsg{gen: gen, turn: turnID, items: items}
			}
			cursor = *reply.NextCursor
		}
	}
}
