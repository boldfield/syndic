package mcp_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/mcp"
	"github.com/boldfield/syndic/internal/mcp/client"
	"github.com/boldfield/syndic/internal/mcp/mcptest"
	"github.com/boldfield/syndic/internal/mcp/wire"
)

func unsupported(status int, supported ...string) *mcptest.Reply {
	return &mcptest.Reply{Status: status, Error: &mcptest.RPCError{
		Code: mcptest.CodeUnsupportedVersion, Message: "unsupported",
		Data: map[string]any{"supported": supported, "requested": mcptest.Current},
	}}
}

// discovery is a discovery result listing versions, whole in shape.
func discovery(versions ...string) map[string]any {
	if versions == nil {
		versions = []string{}
	}
	return map[string]any{
		"supportedVersions": versions,
		"capabilities":      map[string]any{},
		"resultType":        "complete",
		"ttlMs":             0,
		"cacheScope":        "none",
	}
}

func httpDetect(t *testing.T, cfg mcptest.Config, protocol string) (mcp.Decision, *mcptest.Server) {
	t.Helper()
	srv := mcptest.New(cfg)
	ts := srv.HTTP(t)
	return detectURL(ts.URL, protocol), srv
}

func detectURL(url, protocol string) mcp.Decision {
	c := &client.CurrentHTTP{URL: url, Transport: http.DefaultTransport, Info: client.DefaultClientInfo()}
	return client.DiscoverHTTP(context.Background(), protocol, c)
}

func stdioDetect(t *testing.T, cfg mcptest.Config, timeout time.Duration) (mcp.Decision, *mcptest.Server) {
	t.Helper()
	srv := mcptest.New(cfg)
	return detectStdio(t, srv, mcp.ProtocolAuto, timeout), srv
}

func detectStdio(t *testing.T, srv *mcptest.Server, protocol string, timeout time.Duration) mcp.Decision {
	c := srv.Stdio(t)
	s := &wire.Stream{Messages: wire.NewLineReader(c.R, wire.Limits{})}
	return client.DiscoverStdio(protocol, timeout, c.W, s, client.DefaultClientInfo())
}

func onlyDiscover(t *testing.T, srv *mcptest.Server) {
	t.Helper()
	for _, m := range srv.Methods() {
		if m != "server/discover" {
			t.Errorf("sent %q", m)
		}
	}
}

func TestDetectHTTPCurrentFromDiscovery(t *testing.T) {
	d, srv := httpDetect(t, mcptest.Config{}, mcp.ProtocolAuto)
	if d.Outcome != mcp.OutcomeCurrent || d.Revision != mcptest.Current {
		t.Fatalf("%+v", d)
	}
	onlyDiscover(t, srv)
}

func TestDetectHTTPVersionErrors(t *testing.T) {
	cases := []struct {
		name   string
		status int
		list   []string
		want   mcp.Outcome
	}{
		{"200 lists legacy", 200, []string{mcptest.Legacy}, mcp.OutcomeLegacy},
		{"400 lists legacy", 400, []string{mcptest.Legacy}, mcp.OutcomeLegacy},
		{"404 lists legacy", 404, []string{mcptest.Legacy}, mcp.OutcomeLegacy},
		{"400 lists neither", 400, []string{"2024-11-05"}, mcp.OutcomeUnoffered},
		{"400 lists nothing", 400, nil, mcp.OutcomeUnoffered},
		{"405 lists neither", 405, []string{"2024-11-05"}, mcp.OutcomeUnoffered},
		{"401 lists legacy", 401, []string{mcptest.Legacy}, mcp.OutcomeUnoffered},
		{"403 lists legacy", 403, []string{mcptest.Legacy}, mcp.OutcomeUnoffered},
		{"500 lists legacy", 500, []string{mcptest.Legacy}, mcp.OutcomeUnoffered},
		{"503 lists legacy", 503, []string{mcptest.Legacy}, mcp.OutcomeUnoffered},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d, srv := httpDetect(t, mcptest.Config{Discover: unsupported(c.status, c.list...)}, mcp.ProtocolAuto)
			if d.Outcome != c.want {
				t.Fatalf("%+v", d)
			}
			if c.want == mcp.OutcomeUnoffered && c.status < 500 && c.status != 401 && c.status != 403 && (d.Reason != mcp.ReasonNoCommonRevision || d.Err != nil) {
				t.Errorf("reason %q err %v", d.Reason, d.Err)
			}
			onlyDiscover(t, srv)
		})
	}
}

func TestDetectHTTPCurrentErrorWithoutList(t *testing.T) {
	for _, st := range []int{400, 404, 405} {
		rep := &mcptest.Reply{Status: st, Error: &mcptest.RPCError{Code: mcptest.CodeMethodNotFound, Message: "x"}}
		d, _ := httpDetect(t, mcptest.Config{Discover: rep}, mcp.ProtocolAuto)
		if d.Outcome != mcp.OutcomeUnoffered || d.Reason != mcp.ReasonNoCommonRevision {
			t.Errorf("%d: %+v", st, d)
		}
	}
	rep := &mcptest.Reply{Status: 400, Error: &mcptest.RPCError{Code: mcptest.CodeInvalidParams, Message: "x"}}
	if d, _ := httpDetect(t, mcptest.Config{Discover: rep}, mcp.ProtocolAuto); d.Outcome != mcp.OutcomeUnoffered {
		t.Errorf("%+v", d)
	}
}

func TestDetectHTTPStatusAloneShowsLegacy(t *testing.T) {
	bodies := map[string]*mcptest.Reply{
		"empty":     {Silent: true},
		"text":      {Body: []byte("nope")},
		"other rpc": {Error: &mcptest.RPCError{Code: -32000, Message: "x"}},
	}
	for _, st := range []int{400, 404, 405} {
		for name, rep := range bodies {
			r := *rep
			r.Status = st
			d, srv := httpDetect(t, mcptest.Config{Discover: &r}, mcp.ProtocolAuto)
			if d.Outcome != mcp.OutcomeLegacy {
				t.Errorf("%d %s: %+v", st, name, d)
			}
			onlyDiscover(t, srv)
		}
	}
}

func TestDetectHTTPFailuresAreNotEvidence(t *testing.T) {
	cases := map[string]*mcptest.Reply{
		"401":       {Status: 401, Silent: true},
		"403":       {Status: 403, Body: []byte("no")},
		"500":       {Status: 500, Silent: true},
		"500 rpc":   {Status: 500, Error: &mcptest.RPCError{Code: -32603, Message: "x"}},
		"malformed": {Body: []byte("{not json")},
		"empty 200": {Silent: true},
		"no list":   {Result: map[string]any{"capabilities": map[string]any{}}},
	}
	for name, rep := range cases {
		d, srv := httpDetect(t, mcptest.Config{Discover: rep}, mcp.ProtocolAuto)
		if d.Outcome != mcp.OutcomeUnoffered || d.Err == nil {
			t.Errorf("%s: %+v", name, d)
		}
		onlyDiscover(t, srv)
	}

	srv := mcptest.New(mcptest.Config{})
	ts := srv.HTTP(t)
	url := ts.URL
	ts.Close()
	if d := detectURL(url, mcp.ProtocolAuto); d.Outcome != mcp.OutcomeUnoffered || d.Err == nil {
		t.Errorf("refused: %+v", d)
	}
}

func TestDetectDiscoveryListings(t *testing.T) {
	cases := []struct {
		list []string
		want mcp.Outcome
	}{
		{[]string{mcptest.Legacy, mcptest.Current}, mcp.OutcomeCurrent},
		{[]string{mcptest.Legacy}, mcp.OutcomeLegacy},
		{[]string{"2024-11-05"}, mcp.OutcomeUnoffered},
		{[]string{}, mcp.OutcomeUnoffered},
	}
	for _, c := range cases {
		rep := &mcptest.Reply{Result: discovery(c.list...)}
		if d, _ := httpDetect(t, mcptest.Config{Discover: rep}, mcp.ProtocolAuto); d.Outcome != c.want {
			t.Errorf("%v http: %+v", c.list, d)
		}
		if d, _ := stdioDetect(t, mcptest.Config{Discover: rep}, time.Second); d.Outcome != c.want {
			t.Errorf("%v stdio: %+v", c.list, d)
		}
	}
}

func TestDetectStdioCurrentAndVersionErrors(t *testing.T) {
	d, srv := stdioDetect(t, mcptest.Config{}, time.Second)
	if d.Outcome != mcp.OutcomeCurrent {
		t.Fatalf("%+v", d)
	}
	onlyDiscover(t, srv)
	if d, _ := stdioDetect(t, mcptest.Config{Discover: unsupported(0, mcptest.Legacy)}, time.Second); d.Outcome != mcp.OutcomeLegacy {
		t.Errorf("legacy list: %+v", d)
	}
	d, srv = stdioDetect(t, mcptest.Config{Discover: unsupported(0, "2024-11-05")}, time.Second)
	if d.Outcome != mcp.OutcomeUnoffered || d.Reason != mcp.ReasonNoCommonRevision {
		t.Errorf("neither: %+v", d)
	}
	onlyDiscover(t, srv)
}

func TestDetectStdioOtherAnswersShowLegacy(t *testing.T) {
	cases := map[string]mcptest.Config{
		"unknown method": {Revisions: []string{mcptest.Legacy}},
		"other error":    {Discover: &mcptest.Reply{Error: &mcptest.RPCError{Code: -32000, Message: "x"}}},
		"silent":         {Discover: &mcptest.Reply{Silent: true}},
	}
	for name, cfg := range cases {
		d, srv := stdioDetect(t, cfg, 200*time.Millisecond)
		if d.Outcome != mcp.OutcomeLegacy || d.Revision != mcptest.Legacy {
			t.Errorf("%s: %+v", name, d)
		}
		onlyDiscover(t, srv)
	}
}

func TestNamedRevisionDetectsNothing(t *testing.T) {
	for _, p := range []string{mcptest.Current, mcptest.Legacy} {
		d, srv := httpDetect(t, mcptest.Config{}, p)
		if d.Revision != p || d.Detected {
			t.Errorf("%s: %+v", p, d)
		}
		if len(srv.Requests()) != 0 {
			t.Errorf("%s: sent %v", p, srv.Methods())
		}
		srv = mcptest.New(mcptest.Config{})
		d = detectStdio(t, srv, p, time.Second)
		if d.Revision != p || len(srv.Requests()) != 0 {
			t.Errorf("%s stdio: %+v %v", p, d, srv.Methods())
		}
	}
	if d, _ := httpDetect(t, mcptest.Config{}, "2024-11-05"); d.Outcome != mcp.OutcomeUnoffered {
		t.Errorf("unknown protocol: %+v", d)
	}
}

func TestDetectHTTPDiscoveryResultOnFailingStatus(t *testing.T) {
	for _, st := range []int{400, 404, 405} {
		for list, want := range map[string]mcp.Outcome{mcptest.Current: mcp.OutcomeCurrent, mcptest.Legacy: mcp.OutcomeLegacy, "2024-11-05": mcp.OutcomeUnoffered} {
			rep := &mcptest.Reply{Status: st, Result: discovery(list)}
			d, srv := httpDetect(t, mcptest.Config{Discover: rep}, mcp.ProtocolAuto)
			if d.Outcome != want {
				t.Errorf("%d %s: %+v", st, list, d)
			}
			onlyDiscover(t, srv)
		}
	}
}

func TestDiscoverStdioSilentThenInitialize(t *testing.T) {
	srv := mcptest.New(mcptest.Config{Revisions: []string{mcptest.Legacy}, Discover: &mcptest.Reply{Silent: true}})
	c := srv.Stdio(t)
	s := &wire.Stream{Messages: wire.NewLineReader(c.R, wire.Limits{})}
	info := client.DefaultClientInfo()
	d := client.DiscoverStdio(mcp.ProtocolAuto, 100*time.Millisecond, c.W, s, info)
	if d.Outcome != mcp.OutcomeLegacy {
		t.Fatalf("%+v", d)
	}
	if _, err := client.OpenLegacyStdio(c.W, s, info); err != nil {
		t.Fatal(err)
	}
	// initialize was answered, so the server has recorded it and discovery;
	// the one-way initialized may still be in flight.
	if m := srv.Methods(); len(m) < 2 || m[0] != "server/discover" || m[1] != "initialize" {
		t.Errorf("sent %v", m)
	}
	for deadline := time.Now().Add(5 * time.Second); srv.Count("notifications/initialized") == 0; {
		if time.Now().After(deadline) {
			t.Fatalf("initialized not sent: %v", srv.Methods())
		}
		time.Sleep(5 * time.Millisecond)
	}
	if m := srv.Methods(); len(m) != 3 {
		t.Errorf("sent %v", m)
	}
}

// A discovery answer that comes after the wait is not taken as the answer to
// the legacy initialize that follows.
func TestDiscoverStdioLateAnswerThenInitialize(t *testing.T) {
	srv := mcptest.New(mcptest.Config{
		Revisions: []string{mcptest.Legacy},
		Discover:  &mcptest.Reply{Delay: 150 * time.Millisecond, Error: &mcptest.RPCError{Code: -32601, Message: "unknown method"}},
		Initialize: &mcptest.Reply{Delay: 400 * time.Millisecond, Result: map[string]any{
			"protocolVersion": mcptest.Legacy,
			"capabilities":    map[string]any{},
			"serverInfo":      map[string]any{"name": "fake", "version": "0"},
		}},
	})
	c := srv.Stdio(t)
	s := &wire.Stream{Messages: wire.NewLineReader(c.R, wire.Limits{})}
	info := client.DefaultClientInfo()
	d := client.DiscoverStdio(mcp.ProtocolAuto, 100*time.Millisecond, c.W, s, info)
	if d.Outcome != mcp.OutcomeLegacy {
		t.Fatalf("%+v", d)
	}
	if _, err := client.OpenLegacyStdio(c.W, s, info); err != nil {
		t.Fatal(err)
	}
}

// chattyReader yields one notification after a pause and then nothing.
type chattyReader struct {
	after time.Duration
	sent  bool
	stop  chan struct{}
}

func (r *chattyReader) Next() ([]byte, error) {
	if !r.sent {
		r.sent = true
		time.Sleep(r.after)
		return []byte(`{"jsonrpc":"2.0","method":"notifications/message","params":{}}`), nil
	}
	<-r.stop
	return nil, io.EOF
}

func TestDiscoverStdioTimeoutBoundsWholeExchange(t *testing.T) {
	r := &chattyReader{after: 75 * time.Millisecond, stop: make(chan struct{})}
	t.Cleanup(func() { close(r.stop) })
	s := &wire.Stream{Messages: r}
	start := time.Now()
	d := client.DiscoverStdio(mcp.ProtocolAuto, 100*time.Millisecond, io.Discard, s, client.DefaultClientInfo())
	if d.Outcome != mcp.OutcomeLegacy {
		t.Fatalf("%+v", d)
	}
	if el := time.Since(start); el > 160*time.Millisecond {
		t.Errorf("waited %v", el)
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestDiscoverStdioSendFailureIsUnoffered(t *testing.T) {
	r := &chattyReader{stop: make(chan struct{})}
	t.Cleanup(func() { close(r.stop) })
	s := &wire.Stream{Messages: r}
	d := client.DiscoverStdio(mcp.ProtocolAuto, time.Second, failWriter{}, s, client.DefaultClientInfo())
	var se *mcp.SendError
	if d.Outcome != mcp.OutcomeUnoffered || !errors.As(d.Err, &se) {
		t.Fatalf("%+v", d)
	}
}

// A 400, 404 or 405 whose body is a result but not a discovery result shows a
// legacy server by the status; the same on a success is a malformed success.
func TestDetectHTTPOtherResultOnLegacyStatus(t *testing.T) {
	for _, body := range []map[string]any{{"hello": 1}, {"capabilities": map[string]any{}}} {
		for _, st := range []int{400, 404, 405} {
			d, srv := httpDetect(t, mcptest.Config{Discover: &mcptest.Reply{Status: st, Result: body}}, mcp.ProtocolAuto)
			if d.Outcome != mcp.OutcomeLegacy {
				t.Errorf("%d %v: %+v", st, body, d)
			}
			onlyDiscover(t, srv)
		}
		d, _ := httpDetect(t, mcptest.Config{Discover: &mcptest.Reply{Result: body}}, mcp.ProtocolAuto)
		if d.Outcome != mcp.OutcomeUnoffered || d.Err == nil {
			t.Errorf("200 %v: %+v", body, d)
		}
	}
}

func TestDetectHTTPCurrentAnswerOnOtherStatus(t *testing.T) {
	for _, st := range []int{302, 307, 409, 418, 429} {
		d, srv := httpDetect(t, mcptest.Config{Discover: unsupported(st, mcptest.Legacy)}, mcp.ProtocolAuto)
		if d.Outcome != mcp.OutcomeLegacy {
			t.Errorf("%d error: %+v", st, d)
		}
		onlyDiscover(t, srv)
		rep := &mcptest.Reply{Status: st, Result: discovery(mcptest.Current)}
		d, srv = httpDetect(t, mcptest.Config{Discover: rep}, mcp.ProtocolAuto)
		if d.Outcome != mcp.OutcomeCurrent {
			t.Errorf("%d result: %+v", st, d)
		}
		onlyDiscover(t, srv)
	}
}

func TestDetectHTTPNothingToJudgeIsNotEvidence(t *testing.T) {
	cases := map[string]*mcptest.Reply{
		"302 empty":   {Status: 302, Silent: true},
		"307 text":    {Status: 307, Body: []byte("moved")},
		"418 empty":   {Status: 418, Silent: true},
		"200 other":   {Error: &mcptest.RPCError{Code: -32603, Message: "x"}},
		"409 other":   {Status: 409, Error: &mcptest.RPCError{Code: -32000, Message: "x"}},
		"302 no list": {Status: 302, Result: map[string]any{"capabilities": map[string]any{}}},
	}
	for name, rep := range cases {
		d, srv := httpDetect(t, mcptest.Config{Discover: rep}, mcp.ProtocolAuto)
		if d.Outcome != mcp.OutcomeUnoffered || d.Err == nil {
			t.Errorf("%s: %+v", name, d)
		}
		onlyDiscover(t, srv)
	}
}

// A success that is not a discovery result in its whole shape, or whose
// result is not complete, is a malformed success: unoffered, no initialize.
func TestDetectMalformedDiscoverySuccess(t *testing.T) {
	without := func(k string) map[string]any {
		r := discovery(mcptest.Current)
		delete(r, k)
		return r
	}
	with := func(k string, v any) map[string]any {
		r := discovery(mcptest.Current)
		r[k] = v
		return r
	}
	cases := map[string]map[string]any{
		"no capabilities":     without("capabilities"),
		"no resultType":       without("resultType"),
		"no ttlMs":            without("ttlMs"),
		"no cacheScope":       without("cacheScope"),
		"capabilities list":   with("capabilities", []any{}),
		"input_required":      with("resultType", "input_required"),
		"task":                with("resultType", "task"),
		"ttlMs text":          with("ttlMs", "0"),
		"negative ttlMs":      with("ttlMs", -1),
		"cacheScope number":   with("cacheScope", 1),
		"versions not string": with("supportedVersions", []any{1}),
	}
	for name, body := range cases {
		d, srv := httpDetect(t, mcptest.Config{Discover: &mcptest.Reply{Result: body}}, mcp.ProtocolAuto)
		if d.Outcome != mcp.OutcomeUnoffered || d.Err == nil {
			t.Errorf("%s http: %+v", name, d)
		}
		onlyDiscover(t, srv)
		if d, _ := stdioDetect(t, mcptest.Config{Discover: &mcptest.Reply{Result: body}}, time.Second); d.Outcome != mcp.OutcomeUnoffered || d.Err == nil {
			t.Errorf("%s stdio: %+v", name, d)
		}
		// On a legacy status the same body is not a discovery result, and
		// the status decides.
		d, _ = httpDetect(t, mcptest.Config{Discover: &mcptest.Reply{Status: 400, Result: body}}, mcp.ProtocolAuto)
		if d.Outcome != mcp.OutcomeLegacy {
			t.Errorf("%s 400: %+v", name, d)
		}
	}
}
