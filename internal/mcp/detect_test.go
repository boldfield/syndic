package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

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

func toAnswer(r wire.Response, status int) Answer {
	a := Answer{Status: status, Result: r.Result}
	if r.Error != nil {
		a.Error = &AnswerError{Code: r.Error.Code, Message: r.Error.Message, Data: r.Error.Data}
	}
	return a
}

func httpDetect(t *testing.T, cfg mcptest.Config, protocol string) (Decision, *mcptest.Server) {
	t.Helper()
	srv := mcptest.New(cfg)
	ts := srv.HTTP(t)
	return detectURL(ts.URL, protocol), srv
}

func detectURL(url, protocol string) Decision {
	c := &client.CurrentHTTP{URL: url, Transport: http.DefaultTransport, Info: client.DefaultClientInfo()}
	return DetectHTTP(context.Background(), protocol, func(ctx context.Context) (Answer, error) {
		rep, err := c.Send(ctx, 1, "server/discover", json.RawMessage(`{}`))
		var se *client.StatusError
		if errors.As(err, &se) {
			return Answer{Status: se.Status, Err: se.Err}, nil
		}
		if err != nil {
			return Answer{}, err
		}
		return toAnswer(rep.Response, rep.Status), nil
	})
}

func stdioDetect(t *testing.T, cfg mcptest.Config, timeout time.Duration) (Decision, *mcptest.Server) {
	t.Helper()
	srv := mcptest.New(cfg)
	return detectStdio(t, srv, ProtocolAuto, timeout), srv
}

func detectStdio(t *testing.T, srv *mcptest.Server, protocol string, timeout time.Duration) Decision {
	c := srv.Stdio(t)
	s := &wire.Stream{Messages: wire.NewLineReader(c.R, wire.Limits{})}
	return DetectStdio(protocol, timeout, func() (Answer, error) {
		r, err := client.SendCurrentStdio(c.W, s, 1, "server/discover", json.RawMessage(`{}`), client.DefaultClientInfo())
		if err != nil {
			return Answer{}, err
		}
		return toAnswer(r, 0), nil
	})
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
	d, srv := httpDetect(t, mcptest.Config{}, ProtocolAuto)
	if d.Outcome != OutcomeCurrent || d.Revision != mcptest.Current {
		t.Fatalf("%+v", d)
	}
	onlyDiscover(t, srv)
}

func TestDetectHTTPVersionErrors(t *testing.T) {
	cases := []struct {
		name   string
		status int
		list   []string
		want   Outcome
	}{
		{"200 lists legacy", 200, []string{mcptest.Legacy}, OutcomeLegacy},
		{"400 lists legacy", 400, []string{mcptest.Legacy}, OutcomeLegacy},
		{"404 lists legacy", 404, []string{mcptest.Legacy}, OutcomeLegacy},
		{"400 lists neither", 400, []string{"2024-11-05"}, OutcomeUnoffered},
		{"400 lists nothing", 400, nil, OutcomeUnoffered},
		{"405 lists neither", 405, []string{"2024-11-05"}, OutcomeUnoffered},
		{"500 lists legacy", 500, []string{mcptest.Legacy}, OutcomeLegacy},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d, srv := httpDetect(t, mcptest.Config{Discover: unsupported(c.status, c.list...)}, ProtocolAuto)
			if d.Outcome != c.want {
				t.Fatalf("%+v", d)
			}
			if c.want == OutcomeUnoffered && (d.Reason != ReasonNoCommonRevision || d.Err != nil) {
				t.Errorf("reason %q err %v", d.Reason, d.Err)
			}
			onlyDiscover(t, srv)
		})
	}
}

func TestDetectHTTPCurrentErrorWithoutList(t *testing.T) {
	for _, st := range []int{400, 404, 405} {
		rep := &mcptest.Reply{Status: st, Error: &mcptest.RPCError{Code: mcptest.CodeMethodNotFound, Message: "x"}}
		d, _ := httpDetect(t, mcptest.Config{Discover: rep}, ProtocolAuto)
		if d.Outcome != OutcomeUnoffered || d.Reason != ReasonNoCommonRevision {
			t.Errorf("%d: %+v", st, d)
		}
	}
	rep := &mcptest.Reply{Status: 400, Error: &mcptest.RPCError{Code: mcptest.CodeInvalidParams, Message: "x"}}
	if d, _ := httpDetect(t, mcptest.Config{Discover: rep}, ProtocolAuto); d.Outcome != OutcomeUnoffered {
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
			d, srv := httpDetect(t, mcptest.Config{Discover: &r}, ProtocolAuto)
			if d.Outcome != OutcomeLegacy {
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
		d, srv := httpDetect(t, mcptest.Config{Discover: rep}, ProtocolAuto)
		if d.Outcome != OutcomeUnoffered || d.Err == nil {
			t.Errorf("%s: %+v", name, d)
		}
		onlyDiscover(t, srv)
	}

	srv := mcptest.New(mcptest.Config{})
	ts := srv.HTTP(t)
	url := ts.URL
	ts.Close()
	if d := detectURL(url, ProtocolAuto); d.Outcome != OutcomeUnoffered || d.Err == nil {
		t.Errorf("refused: %+v", d)
	}
}

func TestDetectDiscoveryListings(t *testing.T) {
	cases := []struct {
		list []string
		want Outcome
	}{
		{[]string{mcptest.Legacy, mcptest.Current}, OutcomeCurrent},
		{[]string{mcptest.Legacy}, OutcomeLegacy},
		{[]string{"2024-11-05"}, OutcomeUnoffered},
		{[]string{}, OutcomeUnoffered},
	}
	for _, c := range cases {
		rep := &mcptest.Reply{Result: map[string]any{"supportedVersions": c.list}}
		if d, _ := httpDetect(t, mcptest.Config{Discover: rep}, ProtocolAuto); d.Outcome != c.want {
			t.Errorf("%v http: %+v", c.list, d)
		}
		if d, _ := stdioDetect(t, mcptest.Config{Discover: rep}, time.Second); d.Outcome != c.want {
			t.Errorf("%v stdio: %+v", c.list, d)
		}
	}
}

func TestDetectStdioCurrentAndVersionErrors(t *testing.T) {
	d, srv := stdioDetect(t, mcptest.Config{}, time.Second)
	if d.Outcome != OutcomeCurrent {
		t.Fatalf("%+v", d)
	}
	onlyDiscover(t, srv)
	if d, _ := stdioDetect(t, mcptest.Config{Discover: unsupported(0, mcptest.Legacy)}, time.Second); d.Outcome != OutcomeLegacy {
		t.Errorf("legacy list: %+v", d)
	}
	d, srv = stdioDetect(t, mcptest.Config{Discover: unsupported(0, "2024-11-05")}, time.Second)
	if d.Outcome != OutcomeUnoffered || d.Reason != ReasonNoCommonRevision {
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
		if d.Outcome != OutcomeLegacy || d.Revision != mcptest.Legacy {
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
	if d, _ := httpDetect(t, mcptest.Config{}, "2024-11-05"); d.Outcome != OutcomeUnoffered {
		t.Errorf("unknown protocol: %+v", d)
	}
}
