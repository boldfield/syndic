package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/boldfield/syndic/internal/mcp/wire"
)

// LegacyRevision is the older protocol revision this file speaks: the last
// that requires the handshake (design v9, section 3, "Legacy").
const LegacyRevision = "2025-11-25"

// SessionHeader is the header that carries a legacy HTTP session identifier.
// Header names are case-insensitive.
const SessionHeader = "Mcp-Session-Id"

// endTimeout bounds the DELETE that ends a session, which is sent even when
// the owner's context is already done.
const endTimeout = 10 * time.Second

// ErrLegacyVersion is wrapped by the error for an initialize result that
// names a revision other than LegacyRevision. The attempt has ended and
// notifications/initialized was not sent.
var ErrLegacyVersion = errors.New("mcp: initialize result names another revision")

// ErrLegacyInitialize is wrapped by the error for a reply to initialize that
// is not a well-formed result, whether it could not be read, was a JSON-RPC
// error, or lacked what a result must carry, and for a session identifier
// that is not usable. The attempt has ended and notifications/initialized
// was not sent.
var ErrLegacyInitialize = errors.New("mcp: malformed initialize result")

// errEnded is returned for any use of a session after it ended.
var errEnded = errors.New("mcp: session ended")

// noCopy makes go vet's copylocks check reject a copy of the struct holding
// it, so a session cannot be duplicated by accident.
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

// CheckInitializeResult checks the reply to initialize before anything else
// is sent. It must be a result, not an error; the result must be a JSON
// object with a protocolVersion string, a capabilities object and a
// serverInfo object whose name and version are strings; and it must name
// LegacyRevision. Size and depth are bounded by the wire layer that read it.
func CheckInitializeResult(resp wire.Response) error {
	if resp.Error != nil {
		return fmt.Errorf("%w: initialize refused: %w", ErrLegacyInitialize, resp.Error)
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
		return fmt.Errorf("%w: serverInfo lacks a name or a version", ErrLegacyInitialize)
	}
	if *r.ProtocolVersion != LegacyRevision {
		return fmt.Errorf("%w: %q", ErrLegacyVersion, *r.ProtocolVersion)
	}
	return nil
}

// cancelParams returns the params of notifications/cancelled.
func cancelParams(id int64, reason string) json.RawMessage {
	m := map[string]any{"requestId": id}
	if reason != "" {
		m["reason"] = reason
	}
	b, _ := json.Marshal(m)
	return b
}

// LegacyStdio is one legacy session over a stdio server. It has one owner
// and is never shared or resumed: it cannot be copied, it is not safe for
// concurrent use, and once ended it cannot be used again. Ending it closes
// nothing; the owner owns the process.
type LegacyStdio struct {
	_ noCopy

	w     io.Writer
	s     *wire.Stream
	ids   wire.IDs
	ended bool
}

// OpenLegacyStdio performs the handshake with a server whose standard input
// is w and whose output s yields: initialize, the check of its result, and
// only then notifications/initialized. A request the server sends while a
// response is awaited is refused through s.Reply, which the owner sets.
func OpenLegacyStdio(w io.Writer, s *wire.Stream, info ClientInfo) (*LegacyStdio, error) {
	l := &LegacyStdio{w: w, s: s}
	params, err := InitializeParams(info)
	if err != nil {
		return nil, err
	}
	resp, err := l.Call("initialize", params)
	if err != nil {
		l.ended = true
		return nil, err
	}
	if err := CheckInitializeResult(resp); err != nil {
		l.ended = true
		return nil, err
	}
	if err := l.Notify("notifications/initialized", nil); err != nil {
		l.ended = true
		return nil, err
	}
	return l, nil
}

// NextID returns a request id not used before in this session, so the owner
// knows the id to name in Cancel.
func (l *LegacyStdio) NextID() int64 { return l.ids.Next() }

// Call sends one request under a fresh id; see Send.
func (l *LegacyStdio) Call(method string, params json.RawMessage) (wire.Response, error) {
	return l.Send(l.NextID(), method, params)
}

// Send sends one request under id, which came from NextID, and reads until
// its response. A JSON-RPC error response is returned as it is. Nothing is
// retried.
func (l *LegacyStdio) Send(id int64, method string, params json.RawMessage) (wire.Response, error) {
	if l.ended {
		return wire.Response{}, errEnded
	}
	if err := wire.WriteLine(l.w, wire.NewRequest(id, method, params)); err != nil {
		return wire.Response{}, fmt.Errorf("mcp: writing request: %w", err)
	}
	return l.s.Await(id)
}

// SendContext is Send that stops writing and reading when ctx ends and
// returns ctx's error. A message read after that is kept for the next
// request in the session (wire.Stream.AwaitContext). The session's writer
// must keep concurrent writes whole (wire.WriteLineContext).
func (l *LegacyStdio) SendContext(ctx context.Context, id int64, method string, params json.RawMessage) (wire.Response, error) {
	if l.ended {
		return wire.Response{}, errEnded
	}
	if err := wire.WriteLineContext(ctx, l.w, wire.NewRequest(id, method, params)); err != nil {
		if ctx.Err() != nil {
			return wire.Response{}, err
		}
		return wire.Response{}, fmt.Errorf("mcp: writing request: %w", err)
	}
	return l.s.AwaitContext(ctx, id)
}

// CancelContext is Cancel that returns ctx's error when ctx ends before the
// write does.
func (l *LegacyStdio) CancelContext(ctx context.Context, id int64, reason string) error {
	if l.ended {
		return errEnded
	}
	if err := wire.WriteLineContext(ctx, l.w, wire.NewNotification("notifications/cancelled", cancelParams(id, reason))); err != nil {
		return fmt.Errorf("mcp: writing notification: %w", err)
	}
	return nil
}

// Cancel sends notifications/cancelled for the request sent under id, which
// is how this revision cancels a request.
func (l *LegacyStdio) Cancel(id int64, reason string) error {
	return l.Notify("notifications/cancelled", cancelParams(id, reason))
}

// Notify sends one notification.
func (l *LegacyStdio) Notify(method string, params json.RawMessage) error {
	if l.ended {
		return errEnded
	}
	if err := wire.WriteLine(l.w, wire.NewNotification(method, params)); err != nil {
		return fmt.Errorf("mcp: writing notification: %w", err)
	}
	return nil
}

// Close ends the session. Over stdio there is nothing to send.
func (l *LegacyStdio) Close() { l.ended = true }

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

// LegacyHTTP is one legacy session over HTTP. It has one owner and is never
// shared or resumed: it cannot be copied, it is not safe for concurrent use,
// the identifier it holds is not exposed and cannot be supplied, and once
// ended it cannot be used again.
type LegacyHTTP struct {
	_ noCopy

	cfg     LegacyHTTPConfig
	client  *http.Client
	ids     wire.IDs
	session string
	ended   bool
}

// OpenLegacyHTTP performs the handshake: initialize, the check of its
// result, and only then notifications/initialized. A session identifier in
// the reply to initialize is returned on every later request and the
// session is deleted when it ends; no identifier there means none is sent.
// When the handshake fails after the server issued an identifier, the
// session is deleted before the error is returned.
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
	// The identifier is taken before the reply is judged, so that every
	// failure after a reply arrived ends the session the server issued.
	if sid := hdr.Get(SessionHeader); sid != "" {
		if !validSessionID(sid) {
			l.ended = true
			return nil, fmt.Errorf("%w: unusable session identifier", ErrLegacyInitialize)
		}
		l.session = sid
	}
	if err != nil {
		l.end(ctx)
		if hdr != nil {
			// A reply arrived but could not be read as a result.
			return nil, fmt.Errorf("%w: %w", ErrLegacyInitialize, err)
		}
		return nil, err
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
// response, a JSON body or an event stream. A JSON-RPC error response is
// returned as it is, with the HTTP status that carried it. A request the
// server sends in the stream is answered with an error. Redirects are not
// followed and nothing is retried. Cancelling ctx stops this client reading,
// but under this revision it does not cancel the request: the owner sends
// Cancel with id.
func (l *LegacyHTTP) Send(ctx context.Context, id int64, method string, params json.RawMessage) (HTTPReply, error) {
	if l.ended {
		return HTTPReply{}, errEnded
	}
	resp, _, status, err := l.post(ctx, wire.NewRequest(id, method, params), id, true)
	if err != nil {
		return HTTPReply{}, err
	}
	return HTTPReply{Status: status, Response: resp}, nil
}

// Cancel sends notifications/cancelled for the request sent under id, which
// is how this revision cancels a request.
func (l *LegacyHTTP) Cancel(ctx context.Context, id int64, reason string) error {
	return l.Notify(ctx, "notifications/cancelled", cancelParams(id, reason))
}

// Notify sends one notification; the server acknowledges it with a success
// status and nothing to read.
func (l *LegacyHTTP) Notify(ctx context.Context, method string, params json.RawMessage) error {
	if l.ended {
		return errEnded
	}
	b, err := wire.Encode(wire.NewNotification(method, params))
	if err != nil {
		return err
	}
	return l.postRaw(ctx, b)
}

// Close ends the session: when the server issued an identifier it sends a
// DELETE carrying it, which a server may answer 405. The DELETE is sent even
// when ctx is done. Ending is best effort and reports nothing; the session
// cannot be used afterwards.
func (l *LegacyHTTP) Close(ctx context.Context) {
	if l.ended {
		return
	}
	l.end(ctx)
}

func (l *LegacyHTTP) end(ctx context.Context) {
	l.ended = true
	if l.session == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), endTimeout)
	defer cancel()
	hr, err := l.newRequest(ctx, http.MethodDelete, nil, true)
	if err != nil {
		return
	}
	if resp, err := l.client.Do(hr); err == nil {
		_ = resp.Body.Close()
	}
}

// newRequest builds a request to the endpoint with body, which may be nil.
// After the handshake has begun (later) it carries the protocol version and,
// when the server issued one, the session identifier.
func (l *LegacyHTTP) newRequest(ctx context.Context, method string, body []byte, later bool) (*http.Request, error) {
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	hr, err := http.NewRequestWithContext(ctx, method, l.cfg.URL, r)
	if err != nil {
		return nil, err
	}
	for k, v := range l.cfg.Header {
		hr.Header[http.CanonicalHeaderKey(k)] = append([]string(nil), v...)
	}
	if body != nil {
		hr.Header.Set("Accept", "application/json, text/event-stream")
		hr.Header.Set("Content-Type", "application/json")
	}
	hr.Header.Del(SessionHeader)
	hr.Header.Del("MCP-Protocol-Version")
	if later {
		hr.Header.Set("MCP-Protocol-Version", LegacyRevision)
		if l.session != "" {
			hr.Header.Set(SessionHeader, l.session)
		}
	}
	return hr, nil
}

// postRaw posts one message that expects no response, a notification or an
// answer to a server's request, and checks that it was accepted.
func (l *LegacyHTTP) postRaw(ctx context.Context, msg []byte) error {
	hr, err := l.newRequest(ctx, http.MethodPost, msg, true)
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

// post sends one request and reads the response to id. Whenever a reply
// arrived, its header and status are returned, an error included, so that
// a session the server issued can still be ended. A failing status is an
// error unless it carries a JSON-RPC error response, which the caller
// judges.
func (l *LegacyHTTP) post(ctx context.Context, req wire.Request, id int64, later bool) (wire.Response, http.Header, int, error) {
	body, err := wire.Encode(req)
	if err != nil {
		return wire.Response{}, nil, 0, err
	}
	hr, err := l.newRequest(ctx, http.MethodPost, body, later)
	if err != nil {
		return wire.Response{}, nil, 0, err
	}
	resp, err := l.client.Do(hr)
	if err != nil {
		return wire.Response{}, nil, 0, fmt.Errorf("mcp: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	hdr, status := resp.Header, resp.StatusCode
	ok := status >= 200 && status < 300
	if status >= 300 && status < 400 {
		return wire.Response{}, hdr, status, &StatusError{Status: status, Err: errors.New("redirect not followed")}
	}
	mr, err := wire.NewResponseReader(resp, l.cfg.Limits)
	if err != nil {
		if ok {
			return wire.Response{}, hdr, status, err
		}
		return wire.Response{}, hdr, status, &StatusError{Status: status, Err: err}
	}
	s := &wire.Stream{Messages: mr, Closer: resp.Body, Limits: l.cfg.Limits}
	if later {
		// The refusal is posted from a context of its own, since the
		// owner's may be what ends the read.
		s.Reply = func(msg []byte) error {
			rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), endTimeout)
			defer cancel()
			return l.postRaw(rctx, msg)
		}
	}
	r, err := s.Await(id)
	switch {
	case err != nil && ok:
		return wire.Response{}, hdr, status, err
	case err != nil:
		return wire.Response{}, hdr, status, &StatusError{Status: status, Err: err}
	case !ok && r.Error == nil:
		return wire.Response{}, hdr, status, &StatusError{Status: status, Err: errors.New("a result on a failing status")}
	}
	return r, hdr, status, nil
}
