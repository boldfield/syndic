package tui

import (
	"context"
	"fmt"
	"time"

	"github.com/boldfield/syndic/internal/client"
	"github.com/boldfield/syndic/internal/wire"
)

// ConnMsg indicates a connection state change.
type ConnMsg struct {
	State ConnectionState
	Err   error
}

// EventMsg carries an event from the stream.
type EventMsg struct {
	Frame wire.Frame
	Err   error
}

// connector manages the socket connection and event stream.
type connector struct {
	socketPath string
	client     *client.Client
	lastSeq    int64
}

// connect establishes a connection to the engine socket and returns messages.
func (c *connector) connect(ctx context.Context) chan interface{} {
	msgs := make(chan interface{}, 1)
	go func() {
		// Dial the socket
		cl, err := client.Dial(ctx, c.socketPath, "syndic-tui", "1.0")
		if err != nil {
			msgs <- ConnMsg{State: ConnStateClosed, Err: err}
			return
		}

		c.client = cl
		msgs <- ConnMsg{State: ConnStateConnected, Err: nil}

		// Subscribe from last known seq
		_, err = cl.Subscribe(ctx, c.lastSeq+1)
		if err != nil {
			msgs <- ConnMsg{State: ConnStateClosed, Err: err}
			return
		}

		// Start reading events
		for {
			frame, err := cl.NextEvent(ctx)
			if err != nil {
				msgs <- ConnMsg{State: ConnStateClosed, Err: err}
				return
			}
			msgs <- EventMsg{Frame: frame, Err: nil}
		}
	}()
	return msgs
}

// reconnectWithBackoff attempts to reconnect with exponential backoff.
// It returns messages on the channel until a connection succeeds.
func (c *connector) reconnectWithBackoff(ctx context.Context) chan interface{} {
	msgs := make(chan interface{}, 1)
	go func() {
		backoff := 100 * time.Millisecond
		maxBackoff := 10 * time.Second
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
				// Attempt to connect
				connMsgs := c.connect(ctx)
				for msg := range connMsgs {
					msgs <- msg
					if cm, ok := msg.(ConnMsg); ok && cm.State == ConnStateConnected && cm.Err == nil {
						close(msgs)
						return
					}
				}
				// Connection failed, increase backoff
				if backoff < maxBackoff {
					backoff = backoff * 2
					if backoff > maxBackoff {
						backoff = maxBackoff
					}
				}
			}
		}
	}()
	return msgs
}

// fetchStatus fetches the current status.
func (c *connector) fetchStatus(ctx context.Context) (wire.StatusReply, error) {
	if c.client == nil {
		return wire.StatusReply{}, fmt.Errorf("not connected")
	}
	return c.client.Status(ctx)
}
