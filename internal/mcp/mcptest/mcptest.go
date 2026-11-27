// Package mcptest provides scriptable fake MCP servers for tests (v9 delta,
// sections 3 and 11). One Server speaks either or both protocol revisions, over
// an in-memory stdio pair (Stdio) or over httptest (HTTP), and records every
// message it receives so a test can assert on what a client sent, and on what
// it did not.
//
// It contacts nothing outside the test binary and needs no credential.
package mcptest

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// The two revisions a server may speak.
const (
	Current = "2026-07-28"
	Legacy  = "2025-11-25"
)

// Metadata keys a current-revision request carries in params._meta.
const (
	MetaVersion      = "io.modelcontextprotocol/protocolVersion"
	MetaCapabilities = "io.modelcontextprotocol/clientCapabilities"
	MetaClientInfo   = "io.modelcontextprotocol/clientInfo"
)

// JSON-RPC error codes the current revision defines.
const (
	CodeInvalidParams      = -32602
	CodeMethodNotFound     = -32601
	CodeHeaderMismatch     = -32020
	CodeMissingCapability  = -32021
	CodeUnsupportedVersion = -32022
)

// RPCError is a JSON-RPC error object.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// Reply scripts one answer. The zero Reply is the server's default answer to
// the request. Fields are applied in this order: Delay, Silent, Drop, Body,
// then the envelope built from Error, Oversize or Result.
type Reply struct {
	// Result is the JSON-RPC result. Nil leaves the default for the method.
	Result any
	// Error, when set, is sent as the JSON-RPC error instead of a result.
	Error *RPCError
	// Status is the HTTP status. Zero means 200 (see Config.Statuses).
	Status int
	// Body, when non-nil, is sent verbatim as the one message: a line on
	// stdio, the response body over HTTP. Use it for a malformed reply.
	Body []byte
	// Silent sends nothing (a request with no answer; over HTTP, an empty
	// body with Status).
	Silent bool
	// Drop ends the exchange without a reply. Scripted Notifications and
	// ServerRequests are sent first: over HTTP they form a 200 event stream
	// that then ends with no response for the request, and over stdio they are
	// written before the pair is closed. With none, the HTTP connection is
	// closed before any response header.
	Drop bool
	// Delay waits before replying. A cancelled request abandons the wait: over
	// stdio by notifications/cancelled, over HTTP by the client closing the
	// request (a notifications/cancelled POST is recorded but cancels nothing).
	Delay time.Duration
	// Notifications is the number of notifications sent before the reply;
	// over HTTP they turn the response into an event stream.
	Notifications int
	// ServerRequests is the number of requests the server sends to the
	// client before the reply, in the same way as Notifications.
	ServerRequests int
	// Oversize, when positive, makes the reply message at least that many
	// bytes.
	Oversize int
}

// InputRequired is a tools/call reply asking the client for input.
func InputRequired() Reply {
	return Reply{Result: map[string]any{"resultType": "input_required", "inputRequests": map[string]any{}}}
}

// Task is a tools/call reply deferring the result to a task.
func Task() Reply {
	return Reply{Result: map[string]any{"resultType": "task", "task": map[string]any{"taskId": "task-1"}}}
}

// TextResult is a complete tools/call reply of one text block.
func TextResult(text string) Reply {
	return Reply{Result: map[string]any{
		"content": []any{map[string]any{"type": "text", "text": text}},
	}}
}

// ErrorReply is a JSON-RPC error reply.
func ErrorReply(code int, message string) Reply {
	return Reply{Error: &RPCError{Code: code, Message: message}}
}

// NestedJSON returns a JSON array nested depth deep, for a Reply.Body that
// passes a nesting bound.
func NestedJSON(depth int) []byte {
	return []byte(strings.Repeat("[", depth) + strings.Repeat("]", depth))
}

// NewTool returns the definition of a tool with an object input schema.
func NewTool(name, description string) json.RawMessage {
	b, err := json.Marshal(map[string]any{
		"name":        name,
		"description": description,
		"inputSchema": map[string]any{"type": "object"},
	})
	if err != nil {
		panic(err)
	}
	return b
}

// Config configures a Server. The zero Config speaks the current revision
// only, with no tools.
type Config struct {
	// Revisions lists the revisions spoken. Empty means Current only.
	Revisions []string
	// Name and Version identify the server; defaults "fake" and "0".
	Name, Version string
	// Tools are the definitions listed, each a JSON object with a name.
	Tools []json.RawMessage
	// PageSize splits tools/list into pages of this many tools. Zero lists
	// every tool in one page.
	PageSize int
	// Calls scripts tools/call replies by tool name, used in order; once a
	// tool's script is spent the last reply repeats. A tool without a script
	// answers with the text "ok".
	Calls map[string][]Reply
	// Discover replaces the answer to server/discover.
	Discover *Reply
	// Initialize replaces the answer to initialize.
	Initialize *Reply
	// List replaces the answer to every tools/list.
	List *Reply
	// Statuses sets the HTTP status of the answer to a method.
	Statuses map[string]int
	// SessionID, when set, is returned in the Mcp-Session-Id header of the
	// reply to initialize over HTTP, for a server that speaks Legacy.
	SessionID string
	// DeleteStatus is the status answering an HTTP DELETE; default 200.
	DeleteStatus int
}

// Request is one message received.
type Request struct {
	Method string          // "" for a response from the client
	ID     json.RawMessage // nil for a notification
	Params json.RawMessage
	Raw    []byte
	// HTTP is set for a request received over HTTP. Header is its headers;
	// HTTPMethod is POST or DELETE.
	HTTP       bool
	HTTPMethod string
	Header     http.Header
}

// Server is a fake MCP server. Its methods are safe for concurrent use.
type Server struct {
	cfg Config

	mu      sync.Mutex
	reqs    []Request
	used    map[string]int
	aborted int
	cancels map[string]*context.CancelFunc
	nextSrv int
}

// New returns a server for cfg.
func New(cfg Config) *Server {
	if cfg.Name == "" {
		cfg.Name = "fake"
	}
	if cfg.Version == "" {
		cfg.Version = "0"
	}
	if len(cfg.Revisions) == 0 {
		cfg.Revisions = []string{Current}
	}
	return &Server{cfg: cfg, used: map[string]int{}, cancels: map[string]*context.CancelFunc{}}
}

func (s *Server) speaks(rev string) bool {
	for _, r := range s.cfg.Revisions {
		if r == rev {
			return true
		}
	}
	return false
}

// Requests returns every message received, in order.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.reqs...)
}

// Methods returns the method of every message received, in order. An HTTP
// DELETE is "DELETE".
func (s *Server) Methods() []string {
	var out []string
	for _, r := range s.Requests() {
		out = append(out, r.label())
	}
	return out
}

func (r Request) label() string {
	if r.HTTPMethod == http.MethodDelete {
		return "DELETE"
	}
	return r.Method
}

// Count returns how many messages carried method.
func (s *Server) Count(method string) int {
	n := 0
	for _, r := range s.Requests() {
		if r.label() == method {
			n++
		}
	}
	return n
}

// Called reports whether a message carried method.
func (s *Server) Called(method string) bool { return s.Count(method) > 0 }

// Aborted counts requests abandoned during a Delay: over HTTP because the
// client went away, over stdio because of notifications/cancelled.
func (s *Server) Aborted() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.aborted
}

func (s *Server) record(r Request) {
	s.mu.Lock()
	s.reqs = append(s.reqs, r)
	s.mu.Unlock()
}

// outcome is what a transport writes for one request.
type outcome struct {
	status  int
	header  http.Header
	msgs    [][]byte // notifications and server requests, then the reply
	silent  bool
	drop    bool
	aborted bool
	stream  bool // write msgs as an event stream even if there is one
}

type envelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  any             `json:"params,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

func mustMarshal(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// parse reads one message.
func parse(raw []byte) Request {
	var m struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	req := Request{Raw: append([]byte(nil), raw...)}
	if json.Unmarshal(raw, &m) == nil {
		req.Method, req.ID, req.Params = m.Method, m.ID, m.Params
		if string(req.ID) == "null" {
			req.ID = nil
		}
	}
	return req
}

func (r Request) meta() map[string]json.RawMessage {
	var p struct {
		Meta map[string]json.RawMessage `json:"_meta"`
	}
	_ = json.Unmarshal(r.Params, &p)
	return p.Meta
}

// handle computes the answer to req. ctx ends when the client cancels.
func (s *Server) handle(ctx context.Context, req Request) outcome {
	if req.Method == "" || req.ID == nil {
		return outcome{status: http.StatusAccepted, silent: true}
	}
	rep, status := s.script(req)
	if rep.Delay > 0 {
		select {
		case <-time.After(rep.Delay):
		case <-ctx.Done():
			s.mu.Lock()
			s.aborted++
			s.mu.Unlock()
			return outcome{aborted: true, silent: true}
		}
	}
	if status == 0 {
		status = http.StatusOK
	}
	if rep.Status != 0 {
		status = rep.Status
	} else if st, ok := s.cfg.Statuses[req.Method]; ok {
		status = st
	}
	out := outcome{status: status, header: http.Header{}}
	switch {
	case rep.Silent:
		out.silent = true
		return out
	case rep.Body != nil:
		out.msgs = [][]byte{rep.Body}
		return out
	}
	for i := 0; i < rep.Notifications; i++ {
		out.msgs = append(out.msgs, mustMarshal(envelope{
			JSONRPC: "2.0", Method: "notifications/message",
			Params: map[string]any{"level": "info", "data": "note " + strconv.Itoa(i)},
		}))
	}
	for i := 0; i < rep.ServerRequests; i++ {
		s.mu.Lock()
		s.nextSrv++
		id := s.nextSrv
		s.mu.Unlock()
		out.msgs = append(out.msgs, mustMarshal(envelope{
			JSONRPC: "2.0", ID: json.RawMessage(fmt.Sprintf(`"srv-%d"`, id)),
			Method: "sampling/createMessage", Params: map[string]any{},
		}))
	}
	if rep.Drop {
		out.drop = true
		return out
	}
	env := envelope{JSONRPC: "2.0", ID: req.ID}
	switch {
	case rep.Error != nil:
		env.Error = rep.Error
	case rep.Oversize > 0:
		env.Result = map[string]any{"content": []any{map[string]any{
			"type": "text", "text": strings.Repeat("x", rep.Oversize),
		}}}
	default:
		env.Result = rep.Result
		if env.Result == nil {
			env.Result = map[string]any{}
		}
	}
	out.msgs = append(out.msgs, mustMarshal(env))
	if req.Method == "initialize" && rep.Error == nil && s.cfg.SessionID != "" {
		out.header.Set("Mcp-Session-Id", s.cfg.SessionID)
	}
	return out
}

// script picks the reply for req, with the default HTTP status for it.
func (s *Server) script(req Request) (Reply, int) {
	switch req.Method {
	case "server/discover":
		if s.cfg.Discover != nil {
			return *s.cfg.Discover, 0
		}
		if !s.speaks(Current) {
			return ErrorReply(CodeMethodNotFound, "unknown method"), http.StatusNotFound
		}
		return Reply{Result: map[string]any{
			"supportedVersions": s.cfg.Revisions,
			"capabilities":      map[string]any{"tools": map[string]any{}},
			"serverInfo":        map[string]any{"name": s.cfg.Name, "version": s.cfg.Version},
			"resultType":        "complete",
			"ttlMs":             0,
			"cacheScope":        "none",
		}}, 0
	case "initialize":
		if s.cfg.Initialize != nil {
			return *s.cfg.Initialize, 0
		}
		if !s.speaks(Legacy) {
			return ErrorReply(CodeMethodNotFound, "unknown method"), http.StatusNotFound
		}
		return Reply{Result: map[string]any{
			"protocolVersion": Legacy,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": s.cfg.Name, "version": s.cfg.Version},
		}}, 0
	case "tools/list":
		if bad := s.versionError(req); bad != nil {
			return *bad, http.StatusBadRequest
		}
		if s.cfg.List != nil {
			return *s.cfg.List, 0
		}
		return s.listPage(req), 0
	case "tools/call":
		if bad := s.versionError(req); bad != nil {
			return *bad, http.StatusBadRequest
		}
		return s.callReply(req), 0
	}
	return ErrorReply(CodeMethodNotFound, "unknown method "+req.Method), http.StatusNotFound
}

// versionError is the unsupported-version error for a current-revision
// request naming a revision the server does not speak.
func (s *Server) versionError(req Request) *Reply {
	raw, ok := req.meta()[MetaVersion]
	if !ok {
		return nil
	}
	var v string
	_ = json.Unmarshal(raw, &v)
	if s.speaks(v) {
		return nil
	}
	r := Reply{Error: &RPCError{
		Code: CodeUnsupportedVersion, Message: "unsupported protocol version",
		Data: map[string]any{"supported": s.cfg.Revisions, "requested": v},
	}}
	return &r
}

func (s *Server) listPage(req Request) Reply {
	var p struct {
		Cursor string `json:"cursor"`
	}
	_ = json.Unmarshal(req.Params, &p)
	start := 0
	if strings.HasPrefix(p.Cursor, "page-") {
		start, _ = strconv.Atoi(strings.TrimPrefix(p.Cursor, "page-"))
	}
	tools := s.cfg.Tools
	if start > len(tools) {
		start = len(tools)
	}
	end, next := len(tools), ""
	if s.cfg.PageSize > 0 && start+s.cfg.PageSize < len(tools) {
		end = start + s.cfg.PageSize
		next = "page-" + strconv.Itoa(end)
	}
	page := append([]json.RawMessage{}, tools[start:end]...)
	res := map[string]any{"tools": page}
	if next != "" {
		res["nextCursor"] = next
	}
	return Reply{Result: res}
}

func (s *Server) callReply(req Request) Reply {
	var p struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(req.Params, &p)
	known := false
	for _, t := range s.cfg.Tools {
		var d struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(t, &d) == nil && d.Name == p.Name {
			known = true
		}
	}
	script := s.cfg.Calls[p.Name]
	if !known && len(script) == 0 {
		return ErrorReply(CodeInvalidParams, "unknown tool "+p.Name)
	}
	if len(script) == 0 {
		return TextResult("ok")
	}
	s.mu.Lock()
	i := s.used[p.Name]
	s.used[p.Name]++
	s.mu.Unlock()
	if i >= len(script) {
		i = len(script) - 1
	}
	return script[i]
}

// ServeHTTP serves the endpoint over HTTP.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	req := parse(body)
	req.HTTP, req.HTTPMethod, req.Header = true, r.Method, r.Header.Clone()
	s.record(req)

	if r.Method == http.MethodDelete {
		st := s.cfg.DeleteStatus
		if st == 0 {
			st = http.StatusOK
		}
		w.WriteHeader(st)
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if bad := s.headerMismatch(req); bad != nil {
		out := s.render(*bad, req)
		writeHTTP(w, outcome{status: http.StatusBadRequest, msgs: out})
		return
	}
	out := s.handle(r.Context(), req)
	if out.aborted {
		return
	}
	if out.drop && len(out.msgs) > 0 {
		writeHTTP(w, outcome{status: out.status, header: out.header, msgs: out.msgs, stream: true})
		return
	}
	if out.drop {
		if hj, ok := w.(http.Hijacker); ok {
			if c, _, err := hj.Hijack(); err == nil {
				_ = c.Close()
			}
		}
		return
	}
	writeHTTP(w, out)
}

// render builds the single message for a canned reply.
func (s *Server) render(rep Reply, req Request) [][]byte {
	return [][]byte{mustMarshal(envelope{JSONRPC: "2.0", ID: req.ID, Error: rep.Error})}
}

// headerMismatch is the -32020 reply for a current-revision request whose
// Mcp-Method, MCP-Protocol-Version or (for tools/call) Mcp-Name header is
// missing or disagrees with its body.
func (s *Server) headerMismatch(req Request) *Reply {
	raw, ok := req.meta()[MetaVersion]
	if !ok || req.Method == "" {
		return nil
	}
	var v string
	_ = json.Unmarshal(raw, &v)
	if req.Header.Get("Mcp-Method") != req.Method || req.Header.Get("MCP-Protocol-Version") != v {
		r := ErrorReply(CodeHeaderMismatch, "header mismatch")
		return &r
	}
	if req.Method == "tools/call" {
		var p struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal(req.Params, &p)
		if decodeHeader(req.Header.Get("Mcp-Name")) != p.Name {
			r := ErrorReply(CodeHeaderMismatch, "header mismatch")
			return &r
		}
	}
	return nil
}

// decodeHeader undoes the =?base64?<Base64>?= form of a header value.
func decodeHeader(v string) string {
	if strings.HasPrefix(v, "=?base64?") && strings.HasSuffix(v, "?=") {
		if b, err := base64.StdEncoding.DecodeString(v[len("=?base64?") : len(v)-2]); err == nil {
			return string(b)
		}
	}
	return v
}

func writeHTTP(w http.ResponseWriter, out outcome) {
	for k, v := range out.header {
		w.Header()[k] = v
	}
	if out.silent || len(out.msgs) == 0 {
		w.WriteHeader(out.status)
		return
	}
	if len(out.msgs) == 1 && !out.stream {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(out.status)
		_, _ = w.Write(out.msgs[0])
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(out.status)
	for _, m := range out.msgs {
		_, _ = fmt.Fprintf(w, "event: message\ndata: %s\n\n", m)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}
}

// HTTP starts the server under httptest and closes it when the test ends.
func (s *Server) HTTP(t testing.TB) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(s)
	t.Cleanup(ts.Close)
	return ts
}

// Conn is the client's end of an in-memory stdio pair.
type Conn struct {
	// W is the server's standard input: the client writes lines to it.
	W io.WriteCloser
	// R is the server's standard output: the client reads lines from it.
	R io.Reader
}

// Close ends the connection.
func (c *Conn) Close() error { return c.W.Close() }

// Stdio starts the server on an in-memory pipe pair and stops it when the test
// ends. Messages are one JSON document per line.
func (s *Server) Stdio(t testing.TB) *Conn {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	var wmu sync.Mutex
	write := func(b []byte) {
		wmu.Lock()
		defer wmu.Unlock()
		_, _ = outW.Write(append(append([]byte(nil), b...), '\n'))
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() { _ = outW.Close() }()
		br := bufio.NewReader(inR)
		var wg sync.WaitGroup
		defer wg.Wait()
		for {
			line, err := br.ReadBytes('\n')
			if len(strings.TrimSpace(string(line))) > 0 {
				req := parse(line)
				s.record(req)
				if req.Method == "notifications/cancelled" {
					s.cancel(req)
					continue
				}
				ctx, cancel := context.WithCancel(context.Background())
				entry := &cancel
				if req.ID != nil {
					s.mu.Lock()
					s.cancels[string(req.ID)] = entry
					s.mu.Unlock()
				}
				wg.Add(1)
				go func() {
					defer wg.Done()
					defer cancel()
					defer s.forget(req.ID, entry)
					out := s.handle(ctx, req)
					if !out.silent {
						for _, m := range out.msgs {
							write(m)
						}
					}
					if out.drop {
						_ = inR.Close()
						_ = outW.Close()
					}
				}()
			}
			if err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() {
		_ = inW.Close()
		_ = outR.Close()
		<-done
	})
	return &Conn{W: inW, R: outR}
}

// cancel ends the handling of the request a notifications/cancelled names.
func (s *Server) cancel(req Request) {
	var p struct {
		RequestID json.RawMessage `json:"requestId"`
	}
	_ = json.Unmarshal(req.Params, &p)
	s.mu.Lock()
	c := s.cancels[string(p.RequestID)]
	s.mu.Unlock()
	if c != nil {
		(*c)()
	}
}

// forget removes the cancel entry of a finished request, unless a later
// request with the same id has replaced it.
func (s *Server) forget(id json.RawMessage, entry *context.CancelFunc) {
	if id == nil {
		return
	}
	s.mu.Lock()
	if s.cancels[string(id)] == entry {
		delete(s.cancels, string(id))
	}
	s.mu.Unlock()
}
