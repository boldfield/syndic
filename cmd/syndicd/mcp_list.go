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

	"github.com/boldfield/syndic/internal/config"
	"github.com/boldfield/syndic/internal/engine"
	"github.com/boldfield/syndic/internal/mcp"
	"github.com/boldfield/syndic/internal/mcp/client"
	"github.com/boldfield/syndic/internal/mcp/remote"
)

// newMCPOptions builds the engine's MCP seam from the secret resolver.
func newMCPOptions(secrets config.SecretResolver) engine.MCPOptions {
	return engine.MCPOptions{
		List: func(ctx context.Context, _ string, cfg config.MCPServer, opts mcp.ListOptions) (engine.MCPListed, error) {
			if cfg.Transport != "http" {
				return engine.MCPListed{}, engine.ErrMCPNoTransport
			}
			return listRemoteMCP(ctx, secrets, cfg, opts)
		},
	}
}

func listRemoteMCP(ctx context.Context, secrets config.SecretResolver, cfg config.MCPServer, opts mcp.ListOptions) (engine.MCPListed, error) {
	tr, err := remote.NewTransport(remote.Config{URL: cfg.URL, AddressAllow: cfg.AddressAllow, AuthOrigins: cfg.AuthOrigins})
	if err != nil {
		return engine.MCPListed{}, err
	}
	header := http.Header{}
	if cfg.Auth != nil {
		tok, err := secrets.Resolve(*cfg.Auth)
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
	d := client.DiscoverHTTP(ctx, protocol, cur)
	switch d.Outcome {
	case mcp.OutcomeCurrent:
		l, err := mcp.ListTools(func(cursor string) (json.RawMessage, error) {
			rep, err := cur.Send(ctx, next(), "tools/list", listParams(cursor))
			if err != nil {
				return nil, err
			}
			return result(rep)
		}, opts)
		if err != nil {
			return engine.MCPListed{}, err
		}
		return engine.MCPListed{Listing: l, Revision: d.Revision}, nil
	case mcp.OutcomeLegacy:
		s, err := client.OpenLegacyHTTP(ctx, client.LegacyHTTPConfig{URL: cfg.URL, Transport: tr, Header: header, Info: info})
		if err != nil {
			return engine.MCPListed{}, err
		}
		defer s.Close(context.WithoutCancel(ctx))
		l, err := mcp.ListTools(func(cursor string) (json.RawMessage, error) {
			rep, err := s.Call(ctx, "tools/list", listParams(cursor))
			if err != nil {
				return nil, err
			}
			return result(rep)
		}, opts)
		if err != nil {
			return engine.MCPListed{}, err
		}
		return engine.MCPListed{Listing: l, Revision: d.Revision}, nil
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
		return nil, fmt.Errorf("server error %d: %s", e.Code, e.Message)
	}
	return rep.Response.Result, nil
}
