package mcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/boldfield/syndic/internal/mcp"
	"github.com/boldfield/syndic/internal/mcp/client"
	"github.com/boldfield/syndic/internal/mcp/mcptest"
)

func tools(n int) []json.RawMessage {
	var out []json.RawMessage
	for i := 0; i < n; i++ {
		out = append(out, mcptest.NewTool(fmt.Sprintf("t%d", i), "d"))
	}
	return out
}

// httpFetch lists over the current revision, as the engine does.
func httpFetch(url string) mcp.Fetch {
	c := &client.CurrentHTTP{URL: url, Transport: http.DefaultTransport, Info: client.DefaultClientInfo()}
	var id int64
	return func(cursor string) (json.RawMessage, error) {
		id++
		params := json.RawMessage(`{}`)
		if cursor != "" {
			params, _ = json.Marshal(map[string]string{"cursor": cursor})
		}
		rep, err := c.Send(context.Background(), id, "tools/list", params)
		if err != nil {
			return nil, err
		}
		if rep.Response.Error != nil {
			return nil, rep.Response.Error
		}
		return rep.Response.Result, nil
	}
}

func TestListFollowsCursorsAndSendsNoCall(t *testing.T) {
	srv := mcptest.New(mcptest.Config{Tools: tools(5), PageSize: 2})
	ts := srv.HTTP(t)
	l, err := mcp.ListTools(httpFetch(ts.URL), mcp.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Tools) != 5 || l.Pages != 3 {
		t.Fatalf("tools=%d pages=%d", len(l.Tools), l.Pages)
	}
	for i, tl := range l.Tools {
		if tl.Definition == nil || tl.Name != fmt.Sprintf("t%d", i) || tl.Definition.Pin == "" {
			t.Errorf("tool %d = %+v", i, tl)
		}
	}
	if srv.Called("tools/call") {
		t.Error("tools/call was sent")
	}
	if n := srv.Count("tools/list"); n != 3 {
		t.Errorf("tools/list sent %d times", n)
	}
}

func TestListPerToolReasonAndAnnotations(t *testing.T) {
	raw := []json.RawMessage{
		json.RawMessage(`{"name":"ok","inputSchema":{"type":"object"},"annotations":{"readOnlyHint":true}}`),
		json.RawMessage(`{"name":"bad"}`),
		json.RawMessage(`"nope"`),
	}
	res, _ := json.Marshal(map[string]any{"tools": raw})
	l, err := mcp.ListTools(func(string) (json.RawMessage, error) { return res, nil }, mcp.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if l.Tools[0].Definition == nil || string(l.Tools[0].Annotations()) != `{"readOnlyHint":true}` {
		t.Errorf("tool 0 = %+v", l.Tools[0])
	}
	if ne := l.Tools[1].NotOffered; ne == nil || ne.Reason != mcp.ReasonMissingField {
		t.Errorf("tool 1 = %+v", l.Tools[1])
	}
	if ne := l.Tools[2].NotOffered; ne == nil || ne.Reason != mcp.ReasonMalformed {
		t.Errorf("tool 2 = %+v", l.Tools[2])
	}
}

func TestListPastPageBoundFails(t *testing.T) {
	pages := 0
	fetch := func(string) (json.RawMessage, error) {
		pages++
		return json.RawMessage(`{"tools":[],"nextCursor":"more"}`), nil
	}
	_, err := mcp.ListTools(fetch, mcp.ListOptions{})
	if !errors.Is(err, mcp.ErrListing) {
		t.Fatalf("err = %v", err)
	}
	if pages != 32 {
		t.Errorf("fetched %d pages", pages)
	}
}

func TestListExactlyAtBoundsSucceeds(t *testing.T) {
	ts := mcptest.New(mcptest.Config{Tools: tools(256), PageSize: 8}).HTTP(t)
	l, err := mcp.ListTools(httpFetch(ts.URL), mcp.ListOptions{})
	if err != nil || len(l.Tools) != 256 || l.Pages != 32 {
		t.Fatalf("err=%v listing=%+v", err, l)
	}
}

func TestListPastToolBoundFails(t *testing.T) {
	srv := mcptest.New(mcptest.Config{Tools: tools(257), PageSize: 100})
	ts := srv.HTTP(t)
	_, err := mcp.ListTools(httpFetch(ts.URL), mcp.ListOptions{})
	if !errors.Is(err, mcp.ErrListing) {
		t.Fatalf("err = %v", err)
	}
	if srv.Called("tools/call") {
		t.Error("tools/call was sent")
	}
	if _, err := mcp.ListTools(httpFetch(ts.URL), mcp.ListOptions{MaxTools: 300}); err != nil {
		t.Errorf("raised bound: %v", err)
	}
}

func TestListDuplicateNameFailsWholeListing(t *testing.T) {
	ts := mcptest.New(mcptest.Config{
		Tools:    []json.RawMessage{mcptest.NewTool("a", "x"), mcptest.NewTool("b", "x"), mcptest.NewTool("a", "y")},
		PageSize: 2,
	}).HTTP(t)
	l, err := mcp.ListTools(httpFetch(ts.URL), mcp.ListOptions{})
	if !errors.Is(err, mcp.ErrListing) || l != nil {
		t.Fatalf("listing=%v err=%v", l, err)
	}
}

func TestListMalformedPageAndFetchError(t *testing.T) {
	for _, body := range []string{`{}`, `[]`, `{"tools":[],"nextCursor":5}`} {
		_, err := mcp.ListTools(func(string) (json.RawMessage, error) { return json.RawMessage(body), nil }, mcp.ListOptions{})
		if !errors.Is(err, mcp.ErrListing) {
			t.Errorf("%s: err = %v", body, err)
		}
	}
	boom := errors.New("boom")
	_, err := mcp.ListTools(func(string) (json.RawMessage, error) { return nil, boom }, mcp.ListOptions{})
	if !errors.Is(err, boom) {
		t.Errorf("err = %v", err)
	}
}
