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

// submitCmd submits input with a fresh submission id and no preset.
func submitCmd(ctx context.Context, cl *client.Client, input string) tea.Cmd {
	return func() tea.Msg {
		reply, err := cl.Submit(ctx, wire.SubmitRequest{SubmissionID: uuid.NewString(), Input: input})
		if err != nil {
			return submitMsg{refusal: formatRefusal(err)}
		}
		return submitMsg{turnID: reply.TurnID}
	}
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
