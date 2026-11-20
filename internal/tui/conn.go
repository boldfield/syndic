package tui

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/boldfield/syndic/internal/client"
	"github.com/boldfield/syndic/internal/wire"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/google/uuid"
)

// Message types for the event loop.
type ConnStateMsg struct {
	State ConnectionState
	Err   error
}

type EventMsg struct {
	Frame wire.Frame
	Err   error
}

type HelloMsg struct {
	Hello wire.HelloReply
	Err   error
}

type StatusMsg struct {
	Status wire.StatusReply
	Err    error
}

type SubmitResultMsg struct {
	TurnID        string
	Err           error
	RefusalDetail string
}

// connector manages the socket connection and event stream.
type connector struct {
	socketPath  string
	client      *client.Client
	lastSeq     int64
	runFirstSeq int64
}

func newConnector(socketPath string) *connector {
	return &connector{
		socketPath:  socketPath,
		lastSeq:     0,
		runFirstSeq: 0,
	}
}

// listenCmd starts the connection with backoff retry.
func (c *connector) listenCmd(ctx context.Context, handleMsg func(tea.Msg) tea.Msg) tea.Cmd {
	return func() tea.Msg {
		backoff := 100 * time.Millisecond
		maxBackoff := 10 * time.Second

		for {
			select {
			case <-ctx.Done():
				return nil
			default:
			}

			cl, err := client.Dial(ctx, c.socketPath, "syndic-tui", "1.0")
			if err != nil {
				select {
				case <-time.After(backoff):
					if backoff < maxBackoff {
						backoff = backoff * 2
						if backoff > maxBackoff {
							backoff = maxBackoff
						}
					}
					continue
				case <-ctx.Done():
					return nil
				}
			}

			c.client = cl
			return ConnStateMsg{State: ConnStateConnected}
		}
	}
}

// helloCmd sends hello, subscribes from the appropriate offset, and starts streaming events.
func (c *connector) helloCmd(ctx context.Context) tea.Cmd {
	return func() tea.Msg {
		if c.client == nil {
			return HelloMsg{Err: fmt.Errorf("not connected")}
		}

		hello := c.client.Hello()
		c.runFirstSeq = hello.RunFirstSeq

		subscribeSeq := int64(0)
		if c.lastSeq > 0 {
			subscribeSeq = c.lastSeq
		}

		_, err := c.client.Subscribe(ctx, subscribeSeq)
		if err != nil {
			return HelloMsg{Err: err}
		}

		return HelloMsg{Hello: hello}
	}
}

// streamEventsCmd reads the next event from the stream and returns a command to read the next one.
func (c *connector) streamEventsCmd(ctx context.Context) tea.Cmd {
	return func() tea.Msg {
		if c.client == nil {
			return EventMsg{Err: fmt.Errorf("not connected")}
		}

		select {
		case <-ctx.Done():
			return nil
		default:
		}

		frame, err := c.client.NextEvent(ctx)
		if err != nil {
			return EventMsg{Err: err}
		}

		if frame.Seq > 0 {
			c.lastSeq = frame.Seq
		}

		return EventMsg{Frame: frame, Err: nil}
	}
}

// fetchStatusCmd fetches the current status.
func (c *connector) fetchStatusCmd(ctx context.Context) tea.Cmd {
	return func() tea.Msg {
		if c.client == nil {
			return StatusMsg{Err: fmt.Errorf("not connected")}
		}

		status, err := c.client.Status(ctx)
		if err != nil {
			return StatusMsg{Err: err}
		}

		return StatusMsg{Status: status}
	}
}

// submitCmd submits text input.
func (c *connector) submitCmd(ctx context.Context, input string) tea.Cmd {
	return func() tea.Msg {
		if c.client == nil {
			return SubmitResultMsg{Err: fmt.Errorf("not connected")}
		}

		submissionID := generateUUID()
		req := wire.SubmitRequest{
			SubmissionID: submissionID,
			Input:        input,
		}
		result, err := c.client.Submit(ctx, req)

		if err != nil {
			detail := formatRefusal(err)
			return SubmitResultMsg{Err: err, RefusalDetail: detail}
		}

		return SubmitResultMsg{TurnID: result.TurnID}
	}
}

// reconnectCmd reconnects with backoff.
func (c *connector) reconnectCmd(ctx context.Context) tea.Cmd {
	return func() tea.Msg {
		if c.client != nil {
			_ = c.client.Close()
			c.client = nil
		}

		backoff := 100 * time.Millisecond
		maxBackoff := 10 * time.Second

		for {
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(backoff):
				cl, err := client.Dial(ctx, c.socketPath, "syndic-tui", "1.0")
				if err != nil {
					if backoff < maxBackoff {
						backoff = backoff * 2
						if backoff > maxBackoff {
							backoff = maxBackoff
						}
					}
					continue
				}

				c.client = cl
				return ConnStateMsg{State: ConnStateConnected}
			}
		}
	}
}

func generateUUID() string {
	u := uuid.New()
	return u.String()
}

func formatRefusal(err error) string {
	var we *wire.Error
	if errors.As(err, &we) {
		if we.Detail != nil {
			return we.Code + ": " + *we.Detail
		}
		return we.Code
	}
	return err.Error()
}
