package mcptest

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func currentReq(id int, method string, params map[string]any) []byte {
	if params == nil {
		params = map[string]any{}
	}
	params["_meta"] = map[string]any{
		MetaVersion:      Current,
		MetaCapabilities: map[string]any{},
		MetaClientInfo:   map[string]any{"name": "t", "version": "1"},
	}
	return mustMarshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
}

func post(t *testing.T, url string, body []byte, hdr map[string]string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

func currentHeaders(method string) map[string]string {
	return map[string]string{
		"Content-Type": "application/json", "Mcp-Method": method, "MCP-Protocol-Version": Current,
	}
}

func TestHTTPCurrentDiscoverListCall(t *testing.T) {
	s := New(Config{Tools: []json.RawMessage{NewTool("a", "A")}, Calls: map[string][]Reply{"a": {TextResult("hi")}}})
	ts := s.HTTP(t)

	_, b := post(t, ts.URL, currentReq(1, "server/discover", nil), currentHeaders("server/discover"))
	if !strings.Contains(string(b), `"supportedVersions":["2026-07-28"]`) {
		t.Fatalf("discover: %s", b)
	}
	_, b = post(t, ts.URL, currentReq(2, "tools/list", nil), currentHeaders("tools/list"))
	if !strings.Contains(string(b), `"name":"a"`) {
		t.Fatalf("list: %s", b)
	}
	h := currentHeaders("tools/call")
	h["Mcp-Name"] = "a"
	_, b = post(t, ts.URL, currentReq(3, "tools/call", map[string]any{"name": "a"}), h)
	if !strings.Contains(string(b), `"text":"hi"`) {
		t.Fatalf("call: %s", b)
	}
	if s.Count("tools/call") != 1 || s.Requests()[2].Header.Get("Mcp-Name") != "a" {
		t.Fatalf("record: %v", s.Methods())
	}
}

func TestHTTPHeaderMismatch(t *testing.T) {
	s := New(Config{})
	ts := s.HTTP(t)
	resp, b := post(t, ts.URL, currentReq(1, "tools/list", nil), currentHeaders("tools/call"))
	if resp.StatusCode != 400 || !strings.Contains(string(b), "-32020") {
		t.Fatalf("%d %s", resp.StatusCode, b)
	}
}

func TestHTTPUnsupportedVersionAndLegacyOnlyDiscover(t *testing.T) {
	s := New(Config{Revisions: []string{Legacy}})
	ts := s.HTTP(t)
	resp, b := post(t, ts.URL, currentReq(1, "server/discover", nil), currentHeaders("server/discover"))
	if resp.StatusCode != 404 || !strings.Contains(string(b), "-32601") {
		t.Fatalf("%d %s", resp.StatusCode, b)
	}
	resp, b = post(t, ts.URL, currentReq(2, "tools/list", nil), currentHeaders("tools/list"))
	if resp.StatusCode != 400 || !strings.Contains(string(b), "-32022") || !strings.Contains(string(b), `"supported":["2025-11-25"]`) {
		t.Fatalf("%d %s", resp.StatusCode, b)
	}
}

func TestHTTPLegacySessionAndDelete(t *testing.T) {
	s := New(Config{Revisions: []string{Legacy}, SessionID: "sess-1", DeleteStatus: 405})
	ts := s.HTTP(t)
	init := mustMarshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": Legacy}})
	resp, b := post(t, ts.URL, init, nil)
	if resp.Header.Get("mcp-session-id") != "sess-1" || !strings.Contains(string(b), Legacy) {
		t.Fatalf("%v %s", resp.Header, b)
	}
	resp, _ = post(t, ts.URL, []byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`), nil)
	if resp.StatusCode != 202 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	req, _ := http.NewRequest(http.MethodDelete, ts.URL, nil)
	req.Header.Set("Mcp-Session-Id", "sess-1")
	dr, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = dr.Body.Close()
	if dr.StatusCode != 405 || s.Count("DELETE") != 1 {
		t.Fatalf("delete %d %v", dr.StatusCode, s.Methods())
	}
	if got := s.Requests()[2].Header.Get("Mcp-Session-Id"); got != "sess-1" {
		t.Fatalf("session %q", got)
	}
}

func TestHTTPScripts(t *testing.T) {
	tools := []json.RawMessage{NewTool("a", "A")}
	cases := []struct {
		name  string
		reply Reply
		check func(t *testing.T, resp *http.Response, body string)
	}{
		{"error", ErrorReply(-32000, "boom"), func(t *testing.T, _ *http.Response, b string) {
			if !strings.Contains(b, `"error":{"code":-32000`) {
				t.Fatal(b)
			}
		}},
		{"input_required", InputRequired(), func(t *testing.T, _ *http.Response, b string) {
			if !strings.Contains(b, `"resultType":"input_required"`) {
				t.Fatal(b)
			}
		}},
		{"task", Task(), func(t *testing.T, _ *http.Response, b string) {
			if !strings.Contains(b, `"resultType":"task"`) {
				t.Fatal(b)
			}
		}},
		{"malformed", Reply{Body: []byte("{not json")}, func(t *testing.T, _ *http.Response, b string) {
			if b != "{not json" {
				t.Fatal(b)
			}
		}},
		{"status", Reply{Status: 503, Body: []byte{}}, func(t *testing.T, r *http.Response, _ string) {
			if r.StatusCode != 503 {
				t.Fatal(r.StatusCode)
			}
		}},
		{"oversize", Reply{Oversize: 5000}, func(t *testing.T, _ *http.Response, b string) {
			if len(b) < 5000 {
				t.Fatal(len(b))
			}
		}},
		{"notifications", Reply{Notifications: 3}, func(t *testing.T, r *http.Response, b string) {
			if r.Header.Get("Content-Type") != "text/event-stream" || strings.Count(b, "notifications/message") != 3 || !strings.Contains(b, `"result"`) {
				t.Fatal(b)
			}
		}},
		{"server request", Reply{ServerRequests: 1}, func(t *testing.T, _ *http.Response, b string) {
			if !strings.Contains(b, "sampling/createMessage") {
				t.Fatal(b)
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := New(Config{Tools: tools, Calls: map[string][]Reply{"a": {c.reply}}})
			ts := s.HTTP(t)
			resp, b := post(t, ts.URL, currentReq(1, "tools/call", map[string]any{"name": "a"}), currentHeaders("tools/call"))
			c.check(t, resp, string(b))
		})
	}
}

func TestHTTPDropAndAbort(t *testing.T) {
	s := New(Config{
		Tools: []json.RawMessage{NewTool("a", "A"), NewTool("slow", "S")},
		Calls: map[string][]Reply{"a": {{Drop: true}}, "slow": {{Delay: 5 * time.Second}}},
	})
	ts := s.HTTP(t)
	req, _ := http.NewRequest(http.MethodPost, ts.URL, bytes.NewReader(currentReq(1, "tools/call", map[string]any{"name": "a"})))
	for k, v := range currentHeaders("tools/call") {
		req.Header.Set(k, v)
	}
	if resp, err := http.DefaultClient.Do(req); err == nil {
		_ = resp.Body.Close()
		t.Fatal("expected a dropped connection")
	}
	ctx, cancel := contextTimeout(50 * time.Millisecond)
	defer cancel()
	req, _ = http.NewRequestWithContext(ctx, http.MethodPost, ts.URL, bytes.NewReader(currentReq(2, "tools/call", map[string]any{"name": "slow"})))
	for k, v := range currentHeaders("tools/call") {
		req.Header.Set(k, v)
	}
	if resp, err := http.DefaultClient.Do(req); err == nil {
		_ = resp.Body.Close()
		t.Fatal("expected a timeout")
	}
	deadline := time.Now().Add(2 * time.Second)
	for s.Aborted() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if s.Aborted() != 1 {
		t.Fatalf("aborted %d", s.Aborted())
	}
}

func TestPagingAndScriptOrder(t *testing.T) {
	s := New(Config{
		PageSize: 2,
		Tools:    []json.RawMessage{NewTool("a", ""), NewTool("b", ""), NewTool("c", "")},
		Calls:    map[string][]Reply{"a": {TextResult("one"), TextResult("two")}},
	})
	ts := s.HTTP(t)
	_, b := post(t, ts.URL, currentReq(1, "tools/list", nil), currentHeaders("tools/list"))
	if !strings.Contains(string(b), `"nextCursor":"page-2"`) {
		t.Fatal(string(b))
	}
	_, b = post(t, ts.URL, currentReq(2, "tools/list", map[string]any{"cursor": "page-2"}), currentHeaders("tools/list"))
	if strings.Contains(string(b), "nextCursor") || !strings.Contains(string(b), `"name":"c"`) {
		t.Fatal(string(b))
	}
	var got []string
	for i := 0; i < 3; i++ {
		_, b = post(t, ts.URL, currentReq(3+i, "tools/call", map[string]any{"name": "a"}), currentHeaders("tools/call"))
		got = append(got, string(b))
	}
	if !strings.Contains(got[0], "one") || !strings.Contains(got[1], "two") || !strings.Contains(got[2], "two") {
		t.Fatal(got)
	}
	_, b = post(t, ts.URL, currentReq(9, "tools/call", map[string]any{"name": "zzz"}), currentHeaders("tools/call"))
	if !strings.Contains(string(b), "-32602") {
		t.Fatal(string(b))
	}
}

func TestOverrides(t *testing.T) {
	d := ErrorReply(CodeUnsupportedVersion, "no")
	d.Status = 400
	s := New(Config{Discover: &d, Statuses: map[string]int{"tools/list": 500}})
	ts := s.HTTP(t)
	resp, _ := post(t, ts.URL, currentReq(1, "server/discover", nil), currentHeaders("server/discover"))
	if resp.StatusCode != 400 {
		t.Fatal(resp.StatusCode)
	}
	resp, _ = post(t, ts.URL, currentReq(2, "tools/list", nil), currentHeaders("tools/list"))
	if resp.StatusCode != 500 {
		t.Fatal(resp.StatusCode)
	}
}

type stdioClient struct {
	c  *Conn
	br *bufio.Reader
}

func (c *stdioClient) send(t *testing.T, b []byte) {
	t.Helper()
	if _, err := c.c.W.Write(append(append([]byte(nil), b...), '\n')); err != nil {
		t.Fatal(err)
	}
}

func (c *stdioClient) line(t *testing.T) string {
	t.Helper()
	l, err := c.br.ReadString('\n')
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return strings.TrimSuffix(l, "\n")
}

func newStdio(t *testing.T, cfg Config) (*Server, *stdioClient) {
	s := New(cfg)
	c := s.Stdio(t)
	return s, &stdioClient{c: c, br: bufio.NewReader(c.R)}
}

func TestStdioBothRevisions(t *testing.T) {
	s, c := newStdio(t, Config{Revisions: []string{Current, Legacy}, Tools: []json.RawMessage{NewTool("a", "")}})
	c.send(t, currentReq(1, "server/discover", nil))
	if l := c.line(t); !strings.Contains(l, `"supportedVersions":["2026-07-28","2025-11-25"]`) {
		t.Fatal(l)
	}
	c.send(t, mustMarshal(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "initialize", "params": map[string]any{}}))
	if l := c.line(t); !strings.Contains(l, `"protocolVersion":"2025-11-25"`) {
		t.Fatal(l)
	}
	c.send(t, []byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`))
	c.send(t, currentReq(3, "tools/list", nil))
	if l := c.line(t); !strings.Contains(l, `"name":"a"`) {
		t.Fatal(l)
	}
	if s.Called("tools/call") || s.Count("notifications/initialized") != 1 {
		t.Fatalf("%v", s.Methods())
	}
}

func TestStdioLegacyOnlyDiscoverIsUnknownMethod(t *testing.T) {
	_, c := newStdio(t, Config{Revisions: []string{Legacy}})
	c.send(t, currentReq(1, "server/discover", nil))
	if l := c.line(t); !strings.Contains(l, "-32601") {
		t.Fatal(l)
	}
}

func TestStdioSilentDropAndNotifications(t *testing.T) {
	d := Reply{Silent: true}
	_, c := newStdio(t, Config{
		Discover: &d,
		Tools:    []json.RawMessage{NewTool("n", ""), NewTool("d", "")},
		Calls:    map[string][]Reply{"n": {{Notifications: 2, ServerRequests: 1}}, "d": {{Drop: true}}},
	})
	c.send(t, currentReq(1, "server/discover", nil))
	c.send(t, currentReq(2, "tools/call", map[string]any{"name": "n"}))
	var lines []string
	for i := 0; i < 4; i++ {
		lines = append(lines, c.line(t))
	}
	if !strings.Contains(lines[0], "notifications/message") || !strings.Contains(lines[2], "sampling/createMessage") || !strings.Contains(lines[3], `"id":2`) {
		t.Fatal(lines)
	}
	c.send(t, currentReq(3, "tools/call", map[string]any{"name": "d"}))
	if _, err := c.br.ReadString('\n'); err == nil {
		t.Fatal("expected the pipe to close")
	}
}

func TestStdioMalformedOversizeAndCancel(t *testing.T) {
	s, c := newStdio(t, Config{
		Tools: []json.RawMessage{NewTool("m", ""), NewTool("o", ""), NewTool("slow", "")},
		Calls: map[string][]Reply{"m": {{Body: []byte("{oops")}}, "o": {{Oversize: 10000}}, "slow": {{Delay: 5 * time.Second}}},
	})
	c.send(t, currentReq(1, "tools/call", map[string]any{"name": "m"}))
	if l := c.line(t); l != "{oops" {
		t.Fatal(l)
	}
	c.send(t, currentReq(2, "tools/call", map[string]any{"name": "o"}))
	if l := c.line(t); len(l) < 10000 || strings.Contains(l, "\n") {
		t.Fatal(len(l))
	}
	c.send(t, currentReq(3, "tools/call", map[string]any{"name": "slow"}))
	c.send(t, mustMarshal(map[string]any{"jsonrpc": "2.0", "method": "notifications/cancelled", "params": map[string]any{"requestId": 3}}))
	c.send(t, currentReq(4, "tools/list", nil))
	if l := c.line(t); !strings.Contains(l, `"id":4`) {
		t.Fatal(l)
	}
	if s.Count("notifications/cancelled") != 1 {
		t.Fatalf("%v", s.Methods())
	}
}

func TestNestedJSON(t *testing.T) {
	if got := NestedJSON(3); string(got) != "[[[]]]" {
		t.Fatal(string(got))
	}
}

func contextTimeout(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}
