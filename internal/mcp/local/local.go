// Package local binds the local server instances of package mcp (v9 delta,
// section 6) to the sandbox backend, its termination sequence, the run's
// execution gate and the journal. Package mcp imports none of them.
package local

import (
	"context"
	"errors"
	"io"

	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/mcp"
	"github.com/boldfield/syndic/internal/mcp/wire"
	"github.com/boldfield/syndic/internal/sandbox"
)

// ErrNoServerPipes is returned when the backend's process has no separate
// pipes for the protocol.
var ErrNoServerPipes = errors.New("mcp: sandbox backend gives a server no protocol pipes")

// Process is the sandbox.Process of a server started with Spec.ServerPipes:
// standard input, standard output and standard error are three separate
// pipes to the engine. A backend whose Process is not one cannot run a
// server.
type Process interface {
	sandbox.Process
	Stdin() io.WriteCloser
	Stdout() io.ReadCloser
	Stderr() io.ReadCloser
}

// Config configures Instances.
type Config struct {
	// Backend starts server processes.
	Backend sandbox.Backend
	// Sequence is the termination sequence.
	Sequence sandbox.Sequence
	// Gate is the run's execution gate; nil is one that stays open.
	Gate *sandbox.Gate
	// Journal records the instances.
	Journal *journal.Store
	// StderrLimit is mcp.max_stderr_bytes; zero takes the design's value.
	StderrLimit int
	// Spec builds what the backend starts for a request; ServerPipes is set
	// for it. req.Args is the caller's.
	Spec func(req mcp.InstanceRequest) sandbox.Spec
	// Scrub is the engine's scrub list; a server's standard error is scrubbed
	// with it before it is read. Nil scrubs nothing.
	Scrub *mcp.ScrubList
	// Notice receives the line for a server that runs unconfined.
	Notice func(line string)
}

// New returns the instances of a run. Under the backend sandbox.NoneName a
// server runs unconfined and Notice receives v7's line naming it.
func New(c Config) *mcp.Instances {
	m := &mcp.Instances{
		Journal: journalAdapter{c.Journal},
		Notice:  c.Notice,
		Start: func(ctx context.Context, req mcp.InstanceRequest) (mcp.ServerProcess, error) {
			spec := c.Spec(req)
			spec.ServerPipes = true
			p, err := c.Backend.Start(ctx, spec)
			if err != nil {
				return nil, err
			}
			sp, ok := p.(Process)
			if !ok {
				// The process started, so it is ended by the same
				// containment-checked sequence as any instance.
				return unpiped{p}, ErrNoServerPipes
			}
			return sp, nil
		},
		Terminate: func(p mcp.ServerProcess) bool { return c.Sequence.Run(p, false, nil) },
		Stderr: func(string) io.Writer {
			sink := wire.NewStderrSink(c.StderrLimit)
			if c.Scrub != nil {
				sink.SetScrub(func(b []byte) []byte { out, _ := c.Scrub.ScrubBytes(b); return out })
			}
			return sink
		},
	}
	if c.Gate != nil {
		m.Gate = c.Gate
	}
	if c.Backend.Name() == sandbox.NoneName {
		m.Unconfined = sandbox.NoneNotice
	}
	return m
}

// unpiped is a started process with no server pipes: it is only ended.
type unpiped struct{ sandbox.Process }

func (unpiped) Stdin() io.WriteCloser { return nil }
func (unpiped) Stdout() io.ReadCloser { return nil }
func (unpiped) Stderr() io.ReadCloser { return nil }

type journalAdapter struct{ s *journal.Store }

func (j journalAdapter) Started(r mcp.InstanceRecord) (string, error) {
	var rec journal.MCPInstance
	err := j.s.Tx(func(tx *journal.Tx) error {
		var err error
		rec, err = tx.RecordMCPInstanceStarted(journal.MCPInstanceInput{
			Server: r.Server, Kind: r.Kind, Owner: r.Owner,
			StateDir: r.StateDir, TempDir: r.TempDir, WorkspaceDir: r.WorkspaceDir,
		})
		return err
	})
	return rec.ID, err
}

func (j journalAdapter) Ended(id string) error {
	return j.s.Tx(func(tx *journal.Tx) error { return tx.RecordMCPInstanceEnded(id) })
}

func (j journalAdapter) ContainmentFailed(id, detail string) error {
	return j.s.Tx(func(tx *journal.Tx) error { return tx.RecordMCPContainmentFailure(id, detail) })
}
