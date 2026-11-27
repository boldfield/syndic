package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/boldfield/syndic/internal/mcp/mcptest"
	"github.com/boldfield/syndic/internal/mcp/wire"
)

func metaOf(t *testing.T, req mcptest.Request) map[string]json.RawMessage {
	t.Helper()
	var p struct {
		Meta map[string]json.RawMessage `json:"_meta"`
	}
	if err := json.Unmarshal(req.Params, &p); err != nil {
		t.Fatal(err)
	}
	return p.Meta
}

func checkMeta(t *testing.T, req mcptest.Request, info ClientInfo) {
	t.Helper()
	m := metaOf(t, req)
	if string(m[MetaProtocolVersion]) != `"2026-07-28"` {
		t.Errorf("version = %s", m[MetaProtocolVersion])
	}
	if string(m[MetaClientCapabilities]) != `{}` {
		t.Errorf("capabilities = %s", m[MetaClientCapabilities])
	}
	var got ClientInfo
	if err := json.Unmarshal(m[MetaClientInfo], &got); err != nil || got != info {
		t.Errorf("clientInfo = %s, want %+v", m[MetaClientInfo], info)
	}
}

func TestCurrentOverHTTPCarriesMetaAndHeaders(t *testing.T) {
	srv := mcptest.New(mcptest.Config{Tools: []json.RawMessage{mcptest.NewTool("héllo", "d")}})
	ts := srv.HTTP(t)
	info := ClientInfo{Name: "syndic", Version: "1.2.3"}
	c := &CurrentHTTP{URL: ts.URL, Transport: http.DefaultTransport, Info: info}

	rep, err := c.Send(context.Background(), 1, "tools/call", json.RawMessage(`{"name":"héllo","arguments":{"a":1}}`))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Response.Error != nil || rep.Status != 200 {
		t.Fatalf("reply = %+v", rep)
	}
	reqs := srv.Requests()
	if len(reqs) != 1 {
		t.Fatalf("%d requests", len(reqs))
	}
	r := reqs[0]
	checkMeta(t, r, info)
	h := r.Header
	if r.HTTPMethod != http.MethodPost {
		t.Errorf("method %s", r.HTTPMethod)
	}
	for k, want := range map[string]string{
		"Accept":               "application/json, text/event-stream",
		"Content-Type":         "application/json",
		"MCP-Protocol-Version": "2026-07-28",
		"Mcp-Method":           "tools/call",
		"Mcp-Name":             "=?base64?aMOpbGxv?=",
	} {
		if got := h.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if h.Get("Mcp-Session-Id") != "" {
		t.Error("session header sent")
	}
}

func TestCurrentHTTPListHasNoName(t *testing.T) {
	srv := mcptest.New(mcptest.Config{})
	ts := srv.HTTP(t)
	c := &CurrentHTTP{URL: ts.URL, Transport: http.DefaultTransport, Info: DefaultClientInfo()}
	if _, err := c.Send(context.Background(), 1, "tools/list", nil); err != nil {
		t.Fatal(err)
	}
	r := srv.Requests()[0]
	if r.Header.Get("Mcp-Name") != "" || r.Header.Get("Mcp-Method") != "tools/list" {
		t.Errorf("headers %v", r.Header)
	}
	checkMeta(t, r, DefaultClientInfo())
}

func TestCurrentHTTPFollowsNoRedirectAndRetriesNothing(t *testing.T) {
	hits := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		http.Redirect(w, r, "/elsewhere", http.StatusTemporaryRedirect)
	}))
	defer ts.Close()
	c := &CurrentHTTP{URL: ts.URL, Transport: http.DefaultTransport, Info: DefaultClientInfo()}
	_, err := c.Send(context.Background(), 1, "tools/list", nil)
	var se *StatusError
	if !errors.As(err, &se) || se.Status != http.StatusTemporaryRedirect {
		t.Fatalf("err = %v", err)
	}
	if hits != 1 {
		t.Errorf("%d hits", hits)
	}
}

func TestCurrentHTTPErrorBodyAndEmpty404(t *testing.T) {
	srv := mcptest.New(mcptest.Config{Statuses: map[string]int{"tools/list": 404}})
	ts := srv.HTTP(t)
	c := &CurrentHTTP{URL: ts.URL, Transport: http.DefaultTransport, Info: DefaultClientInfo()}
	rep, err := c.Send(context.Background(), 1, "tools/list", nil)
	var se *StatusError
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if rep.Status != http.StatusNotFound || len(rep.Response.Result) == 0 {
		t.Fatalf("reply = %+v", rep)
	}

	empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer empty.Close()
	c.URL = empty.URL
	_, err = c.Send(context.Background(), 1, "server/discover", nil)
	if !errors.As(err, &se) || se.Status != 404 {
		t.Fatalf("err = %v", err)
	}
}

func TestCurrentOverStdioCarriesMeta(t *testing.T) {
	srv := mcptest.New(mcptest.Config{})
	conn := srv.Stdio(t)
	s := &wire.Stream{Messages: wire.NewLineReader(conn.R, wire.Limits{}), Closer: conn}
	info := ClientInfo{Name: "syndic", Version: "9"}
	resp, err := SendCurrentStdio(conn.W, s, 7, "server/discover", nil, info)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Error != nil {
		t.Fatalf("error %+v", resp.Error)
	}
	checkMeta(t, srv.Requests()[0], info)
}

func TestCurrentParamsRefusesMeta(t *testing.T) {
	if _, err := CurrentParams(json.RawMessage(`{"_meta":{}}`), ClientInfo{}); err == nil {
		t.Error("accepted _meta")
	}
	if _, err := CurrentParams(json.RawMessage(`[1]`), ClientInfo{}); err == nil {
		t.Error("accepted an array")
	}
}

func TestEncodeHeaderValue(t *testing.T) {
	for in, want := range map[string]string{
		"tools/call":      "tools/call",
		"":                "",
		" lead":           "=?base64?IGxlYWQ=?=",
		"a\nb":            "=?base64?YQpi?=",
		"=?base64?QQ==?=": "=?base64?PT9iYXNlNjQ/UVE9PT89?=",
	} {
		if got := EncodeHeaderValue(in); got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
}

func TestCurrentHTTPNeedsTransport(t *testing.T) {
	hits := 0
	ts := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits++ }))
	defer ts.Close()
	c := &CurrentHTTP{URL: ts.URL, Info: DefaultClientInfo()}
	if _, err := c.Send(context.Background(), 1, "tools/list", nil); err == nil {
		t.Fatal("no error without a transport")
	}
	if hits != 0 {
		t.Errorf("%d hits", hits)
	}
}
