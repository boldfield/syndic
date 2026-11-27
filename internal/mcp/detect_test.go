package mcp_test

import (
	"context"
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
		rep := &mcptest.Reply{Result: map[string]any{"supportedVersions": c.list}}
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
			rep := &mcptest.Reply{Status: st, Result: map[string]any{"supportedVersions": []string{list}}}
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
	params, err := client.InitializeParams(info)
	if err != nil {
		t.Fatal(err)
	}
	if err := wire.WriteLine(c.W, wire.NewRequest(2, "initialize", params)); err != nil {
		t.Fatal(err)
	}
	r, err := s.Await(2)
	if err != nil || r.Error != nil {
		t.Fatalf("initialize: %v %+v", err, r)
	}
	if err := client.CheckInitializeResult(r); err != nil {
		t.Fatal(err)
	}
	if m := srv.Methods(); len(m) != 2 || m[0] != "server/discover" || m[1] != "initialize" {
		t.Errorf("sent %v", m)
	}
}

func TestDetectHTTPCurrentAnswerOnOtherStatus(t *testing.T) {
	for _, st := range []int{409, 418, 429} {
		d, srv := httpDetect(t, mcptest.Config{Discover: unsupported(st, mcptest.Legacy)}, mcp.ProtocolAuto)
		if d.Outcome != mcp.OutcomeLegacy {
			t.Errorf("%d error: %+v", st, d)
		}
		onlyDiscover(t, srv)
		rep := &mcptest.Reply{Status: st, Result: map[string]any{"supportedVersions": []string{mcptest.Current}}}
		d, srv = httpDetect(t, mcptest.Config{Discover: rep}, mcp.ProtocolAuto)
		if d.Outcome != mcp.OutcomeCurrent {
			t.Errorf("%d result: %+v", st, d)
		}
		onlyDiscover(t, srv)
	}
}
