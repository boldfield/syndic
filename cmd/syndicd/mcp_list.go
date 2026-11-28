package main

// The daemon's listing of remote MCP servers: the existing remote transport,
// revision detection and tool listing, joined into the engine's seam. Local
// servers have no transport here and stay unoffered.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"

	"github.com/boldfield/syndic/internal/config"
	"github.com/boldfield/syndic/internal/engine"
	"github.com/boldfield/syndic/internal/mcp"
	"github.com/boldfield/syndic/internal/mcp/client"
	"github.com/boldfield/syndic/internal/mcp/remote"
	"github.com/boldfield/syndic/internal/mcp/wire"
)

// newMCPOptions builds the engine's MCP seam from the secret resolver. The
// revision detected for each HTTP server is kept for the run (v9 delta,
// section 3, "Choosing the revision"); a later listing that fails in a way
// that contradicts it drops it, so the server is detected again next time.
func newMCPOptions(secrets config.SecretResolver) engine.MCPOptions {
	l := &remoteLister{secrets: secrets, kept: map[string]mcp.Decision{}}
	return engine.MCPOptions{
		List: func(ctx context.Context, name string, cfg config.MCPServer, opts mcp.ListOptions) (engine.MCPListed, error) {
			if cfg.Transport != "http" {
				return engine.MCPListed{}, engine.ErrMCPNoTransport
			}
			return l.list(ctx, name, cfg, opts)
		},
	}
}

// remoteLister lists remote servers and keeps each one's detected revision.
type remoteLister struct {
	secrets config.SecretResolver

	mu   sync.Mutex
	kept map[string]mcp.Decision
}

func (l *remoteLister) decision(ctx context.Context, name, protocol string, cur *client.CurrentHTTP) mcp.Decision {
	if protocol != mcp.ProtocolAuto {
		// A named revision sends nothing; there is nothing to keep.
		return client.DiscoverHTTP(ctx, protocol, cur)
	}
	l.mu.Lock()
	d, ok := l.kept[name]
	l.mu.Unlock()
	if ok {
		return d
	}
	d = client.DiscoverHTTP(ctx, protocol, cur)
	if d.Outcome == mcp.OutcomeCurrent || d.Outcome == mcp.OutcomeLegacy {
		l.mu.Lock()
		l.kept[name] = d
		l.mu.Unlock()
	}
	return d
}

func (l *remoteLister) forget(name string) {
	l.mu.Lock()
	delete(l.kept, name)
	l.mu.Unlock()
}

// contradicts reports whether a listing failure is evidence that the server
// does not speak the kept revision: a 400, 404 or 405 that carried no
// JSON-RPC answer, a JSON-RPC error answering the handshake or the listing,
// or an initialize result naming another revision. An authentication
// failure, a 5xx, a transport failure, a malformed success and a listing
// past its bounds are reported as what they are and keep the decision.
func contradicts(err error) bool {
	var se *client.StatusError
	if errors.As(err, &se) {
		return se.Status == 400 || se.Status == 404 || se.Status == 405
	}
	var we *wire.Error
	return errors.As(err, &we) || errors.Is(err, client.ErrLegacyVersion)
}

func (l *remoteLister) list(ctx context.Context, name string, cfg config.MCPServer, opts mcp.ListOptions) (engine.MCPListed, error) {
	got, err := l.listOnce(ctx, name, cfg, opts)
	if err != nil && contradicts(err) {
		l.forget(name)
	}
	return got, err
}

// listOnce lists cfg's tools under the kept or a freshly detected revision.
func (l *remoteLister) listOnce(ctx context.Context, name string, cfg config.MCPServer, opts mcp.ListOptions) (engine.MCPListed, error) {
	tr, err := remote.NewTransport(remote.Config{URL: cfg.URL, AddressAllow: cfg.AddressAllow, AuthOrigins: cfg.AuthOrigins})
	if err != nil {
		return engine.MCPListed{}, err
	}
	header := http.Header{}
	if cfg.Auth != nil {
		tok, err := l.secrets.Resolve(*cfg.Auth)
		if err != nil {
			return engine.MCPListed{}, fmt.Errorf("auth secret: %w", err)
		}
		header.Set("Authorization", "Bearer "+tok)
	}
	protocol := cfg.Protocol
	if protocol == "" {
		protocol = mcp.ProtocolAuto
	}
	info := client.DefaultClientInfo()
	var n int64
	next := func() int64 { n++; return n }
	cur := &client.CurrentHTTP{URL: cfg.URL, Transport: tr, Header: header, Info: info}
	d := l.decision(ctx, name, protocol, cur)
	switch d.Outcome {
	case mcp.OutcomeCurrent:
		ls, err := mcp.ListTools(func(cursor string) (json.RawMessage, error) {
			rep, err := cur.Send(ctx, next(), "tools/list", listParams(cursor))
			if err != nil {
				return nil, err
			}
			return result(rep)
		}, opts)
		if err != nil {
			return engine.MCPListed{}, err
		}
		return engine.MCPListed{Listing: ls, Revision: d.Revision}, nil
	case mcp.OutcomeLegacy:
		sess, err := client.OpenLegacyHTTP(ctx, client.LegacyHTTPConfig{URL: cfg.URL, Transport: tr, Header: header, Info: info})
		if err != nil {
			return engine.MCPListed{}, err
		}
		defer sess.Close(context.WithoutCancel(ctx))
		ls, err := mcp.ListTools(func(cursor string) (json.RawMessage, error) {
			rep, err := sess.Call(ctx, "tools/list", listParams(cursor))
			if err != nil {
				return nil, err
			}
			return result(rep)
		}, opts)
		if err != nil {
			return engine.MCPListed{}, err
		}
		return engine.MCPListed{Listing: ls, Revision: d.Revision}, nil
	}
	if d.Err != nil {
		return engine.MCPListed{}, d.Err
	}
	return engine.MCPListed{}, errors.New(d.Reason)
}

func listParams(cursor string) json.RawMessage {
	if cursor == "" {
		return json.RawMessage(`{}`)
	}
	b, _ := json.Marshal(map[string]string{"cursor": cursor})
	return b
}

func result(rep client.HTTPReply) (json.RawMessage, error) {
	if e := rep.Response.Error; e != nil {
		return nil, fmt.Errorf("server error: %w", e)
	}
	return rep.Response.Result, nil
}
