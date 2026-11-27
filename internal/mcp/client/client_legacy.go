package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/boldfield/syndic/internal/mcp/wire"
)

// LegacyRevision is the older protocol revision this file speaks: the last
// that requires the handshake (design v9, section 3, "Legacy").
const LegacyRevision = "2025-11-25"

// SessionHeader is the header that carries a legacy HTTP session identifier.
// Header names are case-insensitive.
const SessionHeader = "Mcp-Session-Id"

// ErrLegacyVersion is wrapped by the error for an initialize result that
// names a revision other than LegacyRevision. The attempt has ended and
// notifications/initialized was not sent.
var ErrLegacyVersion = errors.New("mcp: initialize result names another revision")

// ErrLegacyInitialize is wrapped by the error for an initialize result that
// is not well formed, or for a session identifier that is not usable.
var ErrLegacyInitialize = errors.New("mcp: malformed initialize result")

// cancelParams returns the params of notifications/cancelled.
func cancelParams(id int64, reason string) json.RawMessage {
	m := map[string]any{"requestId": id}
	if reason != "" {
		m["reason"] = reason
	}
	b, _ := json.Marshal(m)
	return b
}

// noCopy makes go vet reject a copy of the struct holding it.
type noCopy struct{}

func (*noCopy) Lock()   {}
func (*noCopy) Unlock() {}

// InitializeParams returns the params of the legacy initialize request: the
// legacy revision, empty capabilities and the client's identity.
func InitializeParams(info ClientInfo) (json.RawMessage, error) {
	return json.Marshal(map[string]any{
		"protocolVersion": LegacyRevision,
		"capabilities":    struct{}{},
		"clientInfo":      info,
	})
}

// CheckInitializeResult checks the result of initialize before anything else
// is sent: it must be a JSON object with a protocolVersion string, a
// capabilities object and a serverInfo object with a name and a version, and it must name
// LegacyRevision. Size and depth are bounded by the wire layer that read it.
func CheckInitializeResult(resp wire.Response) error {
	if resp.Error != nil {
		return fmt.Errorf("mcp: initialize refused: %w", resp.Error)
	}
	var r struct {
		ProtocolVersion *string         `json:"protocolVersion"`
		Capabilities    json.RawMessage `json:"capabilities"`
		ServerInfo      *struct {
			Name    *string `json:"name"`
			Version *string `json:"version"`
		} `json:"serverInfo"`
	}
	if err := json.Unmarshal(resp.Result, &r); err != nil {
		return fmt.Errorf("%w: %v", ErrLegacyInitialize, err)
	}
	if r.ProtocolVersion == nil {
		return fmt.Errorf("%w: no protocolVersion", ErrLegacyInitialize)
	}
	var caps map[string]json.RawMessage
	if json.Unmarshal(r.Capabilities, &caps) != nil || caps == nil {
		return fmt.Errorf("%w: capabilities is not an object", ErrLegacyInitialize)
	}
	if r.ServerInfo == nil || r.ServerInfo.Name == nil || r.ServerInfo.Version == nil {
		return fmt.Errorf("%w: no serverInfo name and version", ErrLegacyInitialize)
	}
	if *r.ProtocolVersion != LegacyRevision {
		return fmt.Errorf("%w: %q", ErrLegacyVersion, *r.ProtocolVersion)
	}
	return nil
}

// LegacyStdio is one legacy session over a stdio server. It has one owner
// and is neither shared nor resumed: it cannot be copied, and it is not safe
// for concurrent use. Ending it closes nothing; the owner owns the process.
type LegacyStdio struct {
	_ noCopy

	w      io.Writer
	s      *wire.Stream
	ids    wire.IDs
	closed bool
}

// OpenLegacyStdio performs the handshake with a server whose standard input
// is w and whose output is s: initialize, the check of its result, and only
// then notifications/initialized.
func OpenLegacyStdio(w io.Writer, s *wire.Stream, info ClientInfo) (*LegacyStdio, error) {
	l := &LegacyStdio{w: w, s: s}
	params, err := InitializeParams(info)
	if err != nil {
		return nil, err
	}
	resp, err := l.Call("initialize", params)
	if err != nil {
		return nil, err
	}
	if err := CheckInitializeResult(resp); err != nil {
		return nil, err
	}
	if err := l.Notify("notifications/initialized", nil); err != nil {
		return nil, err
	}
	return l, nil
}

// NextID returns a request id not used before in this session, so the owner
// knows the id to name in Cancel.
func (l *LegacyStdio) NextID() int64 { return l.ids.Next() }

// Call sends one request under a fresh id and reads until its response.
func (l *LegacyStdio) Call(method string, params json.RawMessage) (wire.Response, error) {
	return l.Send(l.NextID(), method, params)
}

// Send sends one request under id, which came from NextID, and reads until
// its response. A JSON-RPC error response is returned as it is. Nothing is
// retried.
func (l *LegacyStdio) Send(id int64, method string, params json.RawMessage) (wire.Response, error) {
	if l.closed {
		return wire.Response{}, errors.New("mcp: session ended")
	}
	if err := wire.WriteLine(l.w, wire.NewRequest(id, method, params)); err != nil {
		return wire.Response{}, fmt.Errorf("mcp: writing request: %w", err)
	}
	return l.s.Await(id)
}

// Cancel sends notifications/cancelled for the request sent under id. Under
// this revision that notification is how a request is cancelled.
func (l *LegacyStdio) Cancel(id int64, reason string) error {
	return l.Notify("notifications/cancelled", cancelParams(id, reason))
}

// Notify sends one notification.
func (l *LegacyStdio) Notify(method string, params json.RawMessage) error {
	if l.closed {
		return errors.New("mcp: session ended")
	}
	if err := wire.WriteLine(l.w, wire.NewNotification(method, params)); err != nil {
		return fmt.Errorf("mcp: writing notification: %w", err)
	}
	return nil
}

// Close ends the session. Over stdio there is nothing to send.
func (l *LegacyStdio) Close() { l.closed = true }

// LegacyHTTPConfig configures a legacy HTTP session.
type LegacyHTTPConfig struct {
	// URL is the one endpoint.
	URL string
	// Transport carries the requests; the client dials nothing itself.
	Transport http.RoundTripper
	// Header holds extra request headers, such as credentials. The headers
	// the revision requires override any of the same name.
	Header http.Header
	// Info names the client.
	Info ClientInfo
	// Limits bounds what is read; a zero field takes the design's value.
	Limits wire.Limits
}

// LegacyHTTP is one legacy session over HTTP. It has one owner and is
// neither shared nor resumed: it cannot be copied, it is not safe for
// concurrent use, and the identifier it holds is not exposed.
type LegacyHTTP struct {
	_ noCopy

	cfg     LegacyHTTPConfig
	client  *http.Client
	ids     wire.IDs
	session string
	closed  bool
}

// OpenLegacyHTTP performs the handshake: initialize, the check of its
// result, and only then notifications/initialized. A session identifier in
// the reply to initialize is kept and returned on every later request; none
// there means none is sent.
func OpenLegacyHTTP(ctx context.Context, cfg LegacyHTTPConfig) (*LegacyHTTP, error) {
	if cfg.Transport == nil {
		return nil, errors.New("mcp: no transport; the client dials nothing itself")
	}
	l := &LegacyHTTP{cfg: cfg, client: &http.Client{
		Transport: cfg.Transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}}
	params, err := InitializeParams(cfg.Info)
	if err != nil {
		return nil, err
	}
	id := l.ids.Next()
	resp, hdr, _, err := l.post(ctx, wire.NewRequest(id, "initialize", params), id, false)
	if err != nil {
		return nil, err
	}
	// A usable identifier is kept before the result is judged, so that every
	// failure after it ends the session.
	if sid := hdr.Get(SessionHeader); sid != "" {
		if !validSessionID(sid) {
			return nil, fmt.Errorf("%w: unusable session identifier", ErrLegacyInitialize)
		}
		l.session = sid
	}
	if err := CheckInitializeResult(resp); err != nil {
		l.end(ctx)
		return nil, err
	}
	if err := l.Notify(ctx, "notifications/initialized", nil); err != nil {
		l.end(ctx)
		return nil, err
	}
	return l, nil
}

// validSessionID reports whether s is visible ASCII only, as the revision
// requires of an identifier.
func validSessionID(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x21 || s[i] > 0x7e {
			return false
		}
	}
	return true
}

// NextID returns a request id not used before in this session, so the owner
// knows the id to name in Cancel.
func (l *LegacyHTTP) NextID() int64 { return l.ids.Next() }

// Call sends one request under a fresh id; see Send.
func (l *LegacyHTTP) Call(ctx context.Context, method string, params json.RawMessage) (HTTPReply, error) {
	return l.Send(ctx, l.NextID(), method, params)
}

// Send sends one request under id, which came from NextID, and reads its
// response, which is a JSON body or an event stream. A JSON-RPC error
// response is returned as it is. Nothing is retried. Cancelling ctx stops
// this client waiting, but under this revision it does not cancel the
// request: the owner sends Cancel with id, from another context.
func (l *LegacyHTTP) Send(ctx context.Context, id int64, method string, params json.RawMessage) (HTTPReply, error) {
	if l.closed {
		return HTTPReply{}, errors.New("mcp: session ended")
	}
	resp, _, status, err := l.post(ctx, wire.NewRequest(id, method, params), id, true)
	if err != nil {
		return HTTPReply{}, err
	}
	return HTTPReply{Status: status, Response: resp}, nil
}

// Cancel sends notifications/cancelled for the request sent under id. Under
// this revision that notification is how a request is cancelled.
func (l *LegacyHTTP) Cancel(ctx context.Context, id int64, reason string) error {
	return l.Notify(ctx, "notifications/cancelled", cancelParams(id, reason))
}

// Notify sends one notification and expects an acknowledgement without a
// response to read.
func (l *LegacyHTTP) Notify(ctx context.Context, method string, params json.RawMessage) error {
	if l.closed {
		return errors.New("mcp: session ended")
	}
	hr, err := l.newRequest(ctx, http.MethodPost, wire.NewNotification(method, params), true)
	if err != nil {
		return err
	}
	resp, err := l.client.Do(hr)
	if err != nil {
		return fmt.Errorf("mcp: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &StatusError{Status: resp.StatusCode}
	}
	return nil
}

// Close ends the session: when the server issued an identifier it sends a
// DELETE carrying it, which a server may answer 405. The session cannot be
// used afterwards. Ending a session is best effort and reports nothing.
func (l *LegacyHTTP) Close(ctx context.Context) {
	if l.closed {
		return
	}
	l.end(ctx)
}

func (l *LegacyHTTP) end(ctx context.Context) {
	l.closed = true
	if l.session == "" {
		return
	}
	hr, err := l.newRequest(ctx, http.MethodDelete, nil, true)
	if err != nil {
		return
	}
	if resp, err := l.client.Do(hr); err == nil {
		_ = resp.Body.Close()
	}
}

// newRequest builds a request to the endpoint. A nil msg sends no body.
// After the handshake (later) it carries the protocol version and, when the
// server issued one, the session identifier.
func (l *LegacyHTTP) newRequest(ctx context.Context, method string, msg any, later bool) (*http.Request, error) {
	var body io.Reader
	if msg != nil {
		b, err := wire.Encode(msg)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(b)
	}
	hr, err := http.NewRequestWithContext(ctx, method, l.cfg.URL, body)
	if err != nil {
		return nil, err
	}
	for k, v := range l.cfg.Header {
		hr.Header[http.CanonicalHeaderKey(k)] = append([]string(nil), v...)
	}
	if msg != nil {
		hr.Header.Set("Accept", "application/json, text/event-stream")
		hr.Header.Set("Content-Type", "application/json")
	}
	hr.Header.Del(SessionHeader)
	if later {
		hr.Header.Set("MCP-Protocol-Version", LegacyRevision)
		if l.session != "" {
			hr.Header.Set(SessionHeader, l.session)
		}
	}
	return hr, nil
}

// post sends one request and reads the response to id.
func (l *LegacyHTTP) post(ctx context.Context, req wire.Request, id int64, later bool) (wire.Response, http.Header, int, error) {
	hr, err := l.newRequest(ctx, http.MethodPost, req, later)
	if err != nil {
		return wire.Response{}, nil, 0, err
	}
	resp, err := l.client.Do(hr)
	if err != nil {
		return wire.Response{}, nil, 0, fmt.Errorf("mcp: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// A JSON-RPC error may ride a failing status; the caller judges it.
		var r wire.Response
		mr, err := wire.NewResponseReader(resp, l.cfg.Limits)
		if err == nil {
			s := &wire.Stream{Messages: mr, Closer: resp.Body, Limits: l.cfg.Limits}
			r, err = s.Await(id)
			if err == nil && r.Error != nil {
				return r, resp.Header, resp.StatusCode, nil
			}
		}
		return wire.Response{}, nil, 0, &StatusError{Status: resp.StatusCode, Err: err}
	}
	mr, err := wire.NewResponseReader(resp, l.cfg.Limits)
	if err != nil {
		return wire.Response{}, nil, 0, err
	}
	s := &wire.Stream{Messages: mr, Closer: resp.Body, Limits: l.cfg.Limits}
	r, err := s.Await(id)
	if err != nil {
		return wire.Response{}, nil, 0, err
	}
	return r, resp.Header, resp.StatusCode, nil
}
