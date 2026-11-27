package client

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"runtime/debug"
	"strings"

	"github.com/boldfield/syndic/internal/mcp/wire"
)

// CurrentRevision is the protocol revision this file speaks (design v9,
// section 3, "Current").
const CurrentRevision = "2026-07-28"

// Members of params._meta that every current-revision request carries.
const (
	MetaProtocolVersion    = "io.modelcontextprotocol/protocolVersion"
	MetaClientCapabilities = "io.modelcontextprotocol/clientCapabilities"
	MetaClientInfo         = "io.modelcontextprotocol/clientInfo"
)

// ClientInfo names the client in every current-revision request.
type ClientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// DefaultClientInfo returns the client's name and the version the build
// records, or "devel" when the build records none.
func DefaultClientInfo() ClientInfo {
	v := "devel"
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		v = bi.Main.Version
	}
	return ClientInfo{Name: "syndic", Version: v}
}

// CurrentParams returns params with the _meta members the current revision
// requires added. params is a JSON object or empty; it must not carry _meta.
// The client's capabilities are empty.
func CurrentParams(params json.RawMessage, info ClientInfo) (json.RawMessage, error) {
	obj := map[string]json.RawMessage{}
	if len(bytes.TrimSpace(params)) > 0 {
		if err := json.Unmarshal(params, &obj); err != nil || obj == nil {
			return nil, fmt.Errorf("mcp: params is not a JSON object: %v", err)
		}
	}
	if _, ok := obj["_meta"]; ok {
		return nil, errors.New("mcp: params already carries _meta")
	}
	meta, err := json.Marshal(map[string]any{
		MetaProtocolVersion:    CurrentRevision,
		MetaClientCapabilities: struct{}{},
		MetaClientInfo:         info,
	})
	if err != nil {
		return nil, err
	}
	obj["_meta"] = meta
	return json.Marshal(obj)
}

// CurrentRequest returns the request to send for method and params.
func CurrentRequest(id int64, method string, params json.RawMessage, info ClientInfo) (wire.Request, error) {
	p, err := CurrentParams(params, info)
	if err != nil {
		return wire.Request{}, err
	}
	return wire.NewRequest(id, method, p), nil
}

// SendCurrentStdio writes one current-revision request to a server's standard
// input and reads from s until its response. A JSON-RPC error response is
// returned as it is. Nothing is retried.
func SendCurrentStdio(w io.Writer, s *wire.Stream, id int64, method string, params json.RawMessage, info ClientInfo) (wire.Response, error) {
	req, err := CurrentRequest(id, method, params, info)
	if err != nil {
		return wire.Response{}, err
	}
	if err := wire.WriteLine(w, req); err != nil {
		return wire.Response{}, fmt.Errorf("mcp: writing request: %w", err)
	}
	return s.Await(id)
}

// CurrentHTTP sends current-revision requests to one endpoint, one POST each.
type CurrentHTTP struct {
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

// HTTPReply is an answer read over HTTP.
type HTTPReply struct {
	// Status is the HTTP status.
	Status int
	// Response is the JSON-RPC response; it may be an error, whatever Status.
	Response wire.Response
}

// StatusError is returned when a response that is not a success carries no
// JSON-RPC response, so the status is all there is to judge.
type StatusError struct {
	Status int
	// Err is why no response could be read; it may be nil.
	Err error
}

func (e *StatusError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("mcp: HTTP status %d: %v", e.Status, e.Err)
	}
	return fmt.Sprintf("mcp: HTTP status %d", e.Status)
}

func (e *StatusError) Unwrap() error { return e.Err }

// Send posts one request and reads the response, which is a JSON body or an
// event stream whose earlier messages the wire layer bounds and discards.
// Redirects are not followed and nothing is retried. Cancelling ctx closes
// the connection, which is how the revision cancels a request over HTTP.
func (c *CurrentHTTP) Send(ctx context.Context, id int64, method string, params json.RawMessage) (HTTPReply, error) {
	if c.Transport == nil {
		return HTTPReply{}, errors.New("mcp: no transport; the client dials nothing itself")
	}
	req, err := CurrentRequest(id, method, params, c.Info)
	if err != nil {
		return HTTPReply{}, err
	}
	body, err := wire.Encode(req)
	if err != nil {
		return HTTPReply{}, err
	}
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL, bytes.NewReader(body))
	if err != nil {
		return HTTPReply{}, err
	}
	for k, v := range c.Header {
		hr.Header[http.CanonicalHeaderKey(k)] = append([]string(nil), v...)
	}
	hr.Header.Set("Accept", "application/json, text/event-stream")
	hr.Header.Set("Content-Type", "application/json")
	hr.Header.Set("MCP-Protocol-Version", CurrentRevision)
	hr.Header.Set("Mcp-Method", EncodeHeaderValue(method))
	if method == "tools/call" {
		var p struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(params, &p); err != nil || p.Name == "" {
			return HTTPReply{}, errors.New("mcp: tools/call without a name")
		}
		hr.Header.Set("Mcp-Name", EncodeHeaderValue(p.Name))
	}

	client := &http.Client{
		Transport: c.Transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(hr)
	if err != nil {
		return HTTPReply{}, fmt.Errorf("mcp: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// A redirect is not followed, but a JSON-RPC response it carries is read
	// like that of any other status that is not a success.
	ok := resp.StatusCode >= 200 && resp.StatusCode < 300
	failure := func(err error) error {
		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			err = fmt.Errorf("redirect not followed: %w", err)
		}
		return &StatusError{Status: resp.StatusCode, Err: err}
	}
	mr, err := wire.NewResponseReader(resp, c.Limits)
	if err != nil {
		if ok {
			return HTTPReply{}, err
		}
		return HTTPReply{}, failure(err)
	}
	s := &wire.Stream{Messages: mr, Closer: resp.Body, Limits: c.Limits}
	r, err := s.Await(id)
	if err != nil {
		if ok {
			return HTTPReply{}, err
		}
		return HTTPReply{}, failure(err)
	}
	return HTTPReply{Status: resp.StatusCode, Response: r}, nil
}

// EncodeHeaderValue returns v as it is when that is safe in a header, and as
// =?base64?<Base64>?= otherwise: when it holds anything but printable ASCII,
// begins or ends with a space, or already looks like the encoded form.
func EncodeHeaderValue(v string) string {
	safe := v != "" && v[0] != ' ' && v[len(v)-1] != ' ' && !strings.HasPrefix(v, "=?base64?")
	for i := 0; safe && i < len(v); i++ {
		safe = v[i] >= 0x20 && v[i] <= 0x7e
	}
	if safe || v == "" {
		return v
	}
	return "=?base64?" + base64.StdEncoding.EncodeToString([]byte(v)) + "?="
}
