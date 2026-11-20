package tui

import (
	"context"
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
	socketPath string
	client     *client.Client
	lastSeq    int64
}

func newConnector(socketPath string) *connector {
	return &connector{
		socketPath: socketPath,
		lastSeq:    0,
	}
}

// listenCmd starts the connection and listens for events.
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
			return ConnStateMsg{State: ConnStateConnecting}
		}
	}
}

// helloCmd sends hello and waits for response.
func (c *connector) helloCmd(ctx context.Context) tea.Cmd {
	return func() tea.Msg {
		if c.client == nil {
			return HelloMsg{Err: fmt.Errorf("not connected")}
		}

		hello := c.client.Hello()
		c.lastSeq = hello.RunFirstSeq - 1

		_, err := c.client.Subscribe(ctx, c.lastSeq+1)
		if err != nil {
			return HelloMsg{Err: err}
		}

		go func() {
			c.streamEventsCmd(ctx)
		}()

		return HelloMsg{Hello: hello}
	}
}

// streamEventsCmd continuously reads events from the stream.
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
			detail := err.Error()
			return SubmitResultMsg{Err: err, RefusalDetail: detail}
		}

		return SubmitResultMsg{TurnID: result.TurnID}
	}
}

// reconnectCmd reconnects with backoff.
func (c *connector) reconnectCmd(ctx context.Context) tea.Cmd {
	return func() tea.Msg {
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
				return ConnStateMsg{State: ConnStateReconnecting}
			}
		}
	}
}

func generateUUID() string {
	u := uuid.New()
	return u.String()
}
