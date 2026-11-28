package execproxy

import (
	"errors"
	"sync/atomic"

	"github.com/boldfield/syndic/internal/journal"
)

// Instance proxy (design v9 delta, section 6, "The proxy"). A local server
// instance with a non-empty network_allow has one listener and one credential,
// as a v7 command has, with matching, the address rule and the journal rules
// unchanged and the differences below.

// ErrInstanceNotLive is the check's error when the instance has begun to
// terminate.
var ErrInstanceNotLive = errors.New("instance is terminating")

// MCPStoreJournal journals one instance's connections in the journal store
// (H-36): each with the server, the instance, the call in flight if there is
// one, the host and the port. The record is made before the connection opens.
type MCPStoreJournal struct {
	Store      *journal.Store
	InstanceID string
	// Call returns the id of the call in flight, or "" when there is none. It
	// is read when each connection is recorded; nil means no call.
	Call func() string
}

func (j MCPStoreJournal) call() string {
	if j.Call == nil {
		return ""
	}
	return j.Call()
}

// RecordConnection implements Journal.
func (j MCPStoreJournal) RecordConnection(host string, port int, allowed bool, status int) (string, error) {
	var id string
	err := j.Store.Tx(func(tx *journal.Tx) error {
		c, err := tx.RecordMCPConnection(j.InstanceID, j.call(), host, port, allowed, status)
		id = c.ID
		return err
	})
	return id, err
}

// CloseConnection implements Journal.
func (j MCPStoreJournal) CloseConnection(id string, up, down int64) error {
	return j.Store.Tx(func(tx *journal.Tx) error {
		_, err := tx.CloseMCPConnection(id, up, down)
		return err
	})
}

// InstanceConfig describes one instance's proxy.
type InstanceConfig struct {
	// Patterns is mcp.<server>.network_allow; it must not be empty.
	Patterns []string
	// Network and Addr are as in Config.
	Network string
	Addr    string
	// Journal records the instance's connections, normally an MCPStoreJournal.
	Journal Journal
	// OwnerLive reports whether the owner is live: a turn that is open, an
	// apply that is running, a listing in progress, or the run. A non-nil
	// error refuses the connection.
	OwnerLive func() error
	// Resolver defaults to net.DefaultResolver.
	Resolver Resolver
}

// InstanceProxy is one instance's listener.
type InstanceProxy struct {
	*Proxy
	terminating atomic.Bool
}

// NewInstance opens the instance's listener and starts serving. An upstream
// connection opens only while the instance is alive and its owner is.
func NewInstance(cfg InstanceConfig) (*InstanceProxy, error) {
	if cfg.OwnerLive == nil {
		return nil, errors.New("instance proxy needs an owner check")
	}
	ip := &InstanceProxy{}
	p, err := New(Config{
		Patterns: cfg.Patterns,
		Network:  cfg.Network,
		Addr:     cfg.Addr,
		Journal:  cfg.Journal,
		Resolver: cfg.Resolver,
		Check: func() error {
			if ip.terminating.Load() {
				return ErrInstanceNotLive
			}
			return cfg.OwnerLive()
		},
	})
	if err != nil {
		return nil, err
	}
	ip.Proxy = p
	return ip, nil
}

// BeginTermination is called at the moment the instance's termination
// begins, before any signal is sent: it closes the listener and its tunnels
// and kills the credential, and no upstream connection opens after it
// returns. It is safe to call more than once.
func (ip *InstanceProxy) BeginTermination() {
	ip.terminating.Store(true)
	ip.Revoke()
}
