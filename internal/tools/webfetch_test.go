package tools

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
)

var okAddrs = map[string][]string{"ok.test": {"93.184.216.34"}}

// redirectServer redirects /N to /N+1 until /last, which serves body.
func redirectServer(t *testing.T, last int, hits *atomic.Int32, body string) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		var n int
		_, _ = fmt.Sscanf(r.URL.Path, "/%d", &n)
		if n < last {
			http.Redirect(w, r, fmt.Sprintf("/%d", n+1), http.StatusFound)
			return
		}
		_, _ = fmt.Fprint(w, body)
	}))
	t.Cleanup(ts.Close)
	return ts
}

func TestWebFetchThreeRedirectsFollowedExactlyOnceEach(t *testing.T) {
	var hits atomic.Int32
	ts := redirectServer(t, 3, &hits, "final")
	g := newPinRig(t, ts.Listener.Addr().String(), okAddrs)
	wf := NewWebFetchTools(g.f, 0)
	res, err := wf.Fetch(context.Background(), "http://ok.test/0")
	if err != nil || res.ErrorCode != "" || string(res.Content) != "final" {
		t.Fatalf("res %+v err %v", res, err)
	}
	if hits.Load() != 4 || len(g.ref.calls) != 4 || len(g.res.hosts) != 4 || len(g.dials) != 4 {
		t.Fatalf("hits %d referee %d lookups %d dials %d, want 4 each", hits.Load(), len(g.ref.calls), len(g.res.hosts), len(g.dials))
	}
	g.f.screener.mu.Lock()
	defer g.f.screener.mu.Unlock()
	if g.f.screener.perHost["ok.test"] != 4 || g.f.screener.totalNum != 4 {
		t.Fatalf("counters host %d total %d, want 4", g.f.screener.perHost["ok.test"], g.f.screener.totalNum)
	}
}

func TestWebFetchFourthRedirectRefused(t *testing.T) {
	var hits atomic.Int32
	ts := redirectServer(t, 10, &hits, "never")
	g := newPinRig(t, ts.Listener.Addr().String(), okAddrs)
	res, err := NewWebFetchTools(g.f, 0).Fetch(context.Background(), "http://ok.test/0")
	if err != nil || res.ErrorCode != ErrCodeRedirectLimit {
		t.Fatalf("res %+v err %v", res, err)
	}
	if hits.Load() != 4 || len(g.dials) != 4 || len(g.ref.calls) != 4 {
		t.Fatalf("hits %d dials %d referee %d, want 4", hits.Load(), len(g.dials), len(g.ref.calls))
	}
}

func TestWebFetchCrossHostRedirectReturnedAsData(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://other.test/x", http.StatusFound)
	}))
	defer ts.Close()
	g := newPinRig(t, ts.Listener.Addr().String(), okAddrs)
	res, err := NewWebFetchTools(g.f, 0).Fetch(context.Background(), "http://ok.test/")
	if err != nil || res.ErrorCode != "" || !strings.Contains(string(res.Content), "different host") || !strings.Contains(string(res.Content), "http://other.test/x") {
		t.Fatalf("res %q %q err %v", res.Content, res.ErrorCode, err)
	}
	if len(g.ref.calls) != 1 || len(g.res.hosts) != 1 || len(g.dials) != 1 {
		t.Fatalf("referee %d lookups %d dials %d, want 1", len(g.ref.calls), len(g.res.hosts), len(g.dials))
	}
}

func TestWebFetchCrossHostTargetOverLimitIsError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://other.test/"+strings.Repeat("a", 200), http.StatusFound)
	}))
	defer ts.Close()
	g := newPinRig(t, ts.Listener.Addr().String(), okAddrs)
	res, _ := NewWebFetchTools(g.f, 50).Fetch(context.Background(), "http://ok.test/")
	if res.ErrorCode != ErrCodeTooLarge || !strings.Contains(string(res.Content), "more than 50 bytes") {
		t.Fatalf("res %q %q", res.Content, res.ErrorCode)
	}
}

func TestWebFetchOversizeBodyIsErrorWithSize(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, strings.Repeat("x", 500))
	}))
	defer ts.Close()
	g := newPinRig(t, ts.Listener.Addr().String(), okAddrs)
	res, _ := NewWebFetchTools(g.f, 100).Fetch(context.Background(), "http://ok.test/")
	if res.ErrorCode != ErrCodeTooLarge || string(res.Content) != "result too large: more than 100 bytes" {
		t.Fatalf("res %q %q", res.Content, res.ErrorCode)
	}
	// A body exactly at the limit passes.
	res, _ = NewWebFetchTools(g.f, 500).Fetch(context.Background(), "http://ok.test/")
	if res.ErrorCode != "" || len(res.Content) != 500 {
		t.Fatalf("at-limit res %q %q", res.Content, res.ErrorCode)
	}
}

func TestWebFetchNon2xxIsError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "gone", http.StatusNotFound)
	}))
	defer ts.Close()
	g := newPinRig(t, ts.Listener.Addr().String(), okAddrs)
	res, _ := NewWebFetchTools(g.f, 0).Fetch(context.Background(), "http://ok.test/")
	if res.ErrorCode != ErrCodeHTTPStatus || !strings.Contains(string(res.Content), "404") {
		t.Fatalf("res %q %q", res.Content, res.ErrorCode)
	}
}

// A same-host target that fails a URL rule is refused before the referee, the
// resolver or any connection sees it.
func TestWebFetchSameHostHopShapeRefusedBeforeReferee(t *testing.T) {
	var hits atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Redirect(w, r, "/"+strings.Repeat("a", 2100), http.StatusFound)
	}))
	defer ts.Close()
	g := newPinRig(t, ts.Listener.Addr().String(), okAddrs)
	res, _ := NewWebFetchTools(g.f, 0).Fetch(context.Background(), "http://ok.test/")
	if res.ErrorCode != ErrCodeRefused || !strings.Contains(string(res.Content), RuleShape) {
		t.Fatalf("res %q %q", res.Content, res.ErrorCode)
	}
	if hits.Load() != 1 || len(g.ref.calls) != 1 || len(g.res.hosts) != 1 || len(g.dials) != 1 {
		t.Fatalf("hits %d referee %d lookups %d dials %d, want 1 each", hits.Load(), len(g.ref.calls), len(g.res.hosts), len(g.dials))
	}
}

type denySecond struct{ calls []string }

func (d *denySecond) Allow(_ context.Context, u string) (bool, string, error) {
	d.calls = append(d.calls, u)
	return len(d.calls) < 2, "", nil
}

// A same-host target the referee denies is neither resolved nor requested.
func TestWebFetchSameHostHopRefereeDeniedBeforeResolve(t *testing.T) {
	var hits atomic.Int32
	ts := redirectServer(t, 5, &hits, "x")
	g := newPinRig(t, ts.Listener.Addr().String(), okAddrs)
	ref := &denySecond{}
	g.f.referee = ref
	res, _ := NewWebFetchTools(g.f, 0).Fetch(context.Background(), "http://ok.test/0")
	if res.ErrorCode != ErrCodeRefused || !strings.Contains(string(res.Content), RuleReferee) {
		t.Fatalf("res %q %q", res.Content, res.ErrorCode)
	}
	if len(ref.calls) != 2 || hits.Load() != 1 || len(g.res.hosts) != 1 || len(g.dials) != 1 {
		t.Fatalf("referee %d hits %d lookups %d dials %d", len(ref.calls), hits.Load(), len(g.res.hosts), len(g.dials))
	}
}

type flipResolver struct {
	n     int
	hosts []string
}

func (r *flipResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	r.n++
	r.hosts = append(r.hosts, host)
	if r.n == 1 {
		return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
	}
	return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
}

// A same-host target that now resolves to a forbidden address is refused
// after exactly one lookup and without a connection.
func TestWebFetchSameHostHopAddressRefusedWithoutDial(t *testing.T) {
	var hits atomic.Int32
	ts := redirectServer(t, 5, &hits, "x")
	g := newPinRig(t, ts.Listener.Addr().String(), okAddrs)
	fr := &flipResolver{}
	g.f.resolver = fr
	res, _ := NewWebFetchTools(g.f, 0).Fetch(context.Background(), "http://ok.test/0")
	if res.ErrorCode != ErrCodeRefused || !strings.Contains(string(res.Content), RuleAddress) {
		t.Fatalf("res %q %q", res.Content, res.ErrorCode)
	}
	if fr.n != 2 || len(g.ref.calls) != 2 || len(g.dials) != 1 || hits.Load() != 1 {
		t.Fatalf("lookups %d referee %d dials %d hits %d", fr.n, len(g.ref.calls), len(g.dials), hits.Load())
	}
}

// A non-2xx body exactly at the limit is too_large once the status prefix is
// counted, never an http_error result above the bound.
func TestWebFetchNon2xxExactLimitIsTooLarge(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprint(w, strings.Repeat("x", 100))
	}))
	defer ts.Close()
	g := newPinRig(t, ts.Listener.Addr().String(), okAddrs)
	res, _ := NewWebFetchTools(g.f, 100).Fetch(context.Background(), "http://ok.test/")
	if res.ErrorCode != ErrCodeTooLarge || string(res.Content) != "result too large: more than 100 bytes" {
		t.Fatalf("res %q %q", res.Content, res.ErrorCode)
	}
	res, _ = NewWebFetchTools(g.f, 200).Fetch(context.Background(), "http://ok.test/")
	if res.ErrorCode != ErrCodeHTTPStatus || len(res.Content) > 200 {
		t.Fatalf("under-limit res %q %q", res.Content, res.ErrorCode)
	}
}
