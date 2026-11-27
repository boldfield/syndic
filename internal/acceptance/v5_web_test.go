package acceptance

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/tools"
)

// presendPara is the "Pre-send checks" paragraph of the v5 delta's section 8
// (the revision 5 test list). The paragraph's tests are named TestPresend_*.
const presendPara = "Presend checks"

func init() {
	cover(presendPara, "the canary refuses a URL carrying 16 bytes from a file read this turn, from a staged write, from a staged path's base content, from a reviewer diff and from the history document, each percent-encoded, base64-encoded, hex-encoded, base64-of-hex and hex-of-base64, in a query value, in a path segment and in a hostname label", "TestPresend_CanaryEncodingsSourcesPositions")
	cover(presendPara, "a 15-byte match is not refused", "TestPresend_CanaryFifteenBytes")
	cover(presendPara, "a URL over 2048 bytes is refused", "TestPresend_URLLength")
	cover(presendPara, "a hostname label over 63 bytes and a hostname over 253 bytes are refused", "TestPresend_HostnameLengths")
	cover(presendPara, "a value encoded three levels deep is not decoded", "TestPresend_CanaryThreeLevelsNotDecoded")
	cover(presendPara, "a 49-byte value at entropy 4.6 is refused and a 48-byte value is not, in a hostname label as in a query value", "TestPresend_EntropyBoundary")
	cover(presendPara, "the eleventh fetch to one host is refused", "TestPresend_PerHostCount")
	cover(presendPara, "the twenty-first fetch in a turn is refused", "TestPresend_TurnCount")
	cover(presendPara, "no name resolution occurs for a URL the checks or the referee refuse, verified with a resolver stub that records queries", "TestPresend_NoResolutionForRefused")
	cover(presendPara, "resolution occurs once, after the referee's allow", "TestPresend_ResolutionOnceAfterReferee")
	cover(presendPara, "the connection is pinned to the validated address", "TestPresend_ConnectionPinned")
	cover(presendPara, "a same-host redirect target is checked by every rule and the referee before it is resolved or requested", "TestPresend_SameHostRedirectRechecked")
	cover(presendPara, "the fourth hop is refused", "TestPresend_FourthHopRefused")
	cover(presendPara, "a cross-host redirect is returned as a result and not followed", "TestPresend_CrossHostRedirectNotFollowed")
	cover(presendPara, "`*.example.com` does not match the apex and `**.example.com` does", "TestPresend_AllowlistWildcards")
	cover(presendPara, "an IP literal is refused when an allowlist is set", "TestPresend_AllowlistIPLiteral")
	cover(presendPara, "a `web_search` query is checked by the canary and refereed, and the provider host is not resolved for a refused query", "TestPresend_SearchCheckedRefereedUnresolved")
	cover(presendPara, "every refusal is journaled with the rule and the URL", "TestPresend_RefusalsJournaled")
}

// presendTrace is the ordered record of every effect of a fetch or a search:
// the referee's questions, the resolver's lookups, the dials and the requests
// a server received. Tests compare it with the order the design fixes.
type presendTrace struct {
	mu sync.Mutex
	ev []string
}

func (tr *presendTrace) add(format string, a ...any) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	tr.ev = append(tr.ev, fmt.Sprintf(format, a...))
}

func (tr *presendTrace) events() []string {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	return append([]string(nil), tr.ev...)
}

// count returns the number of events with the prefix.
func (tr *presendTrace) count(prefix string) int {
	n := 0
	for _, e := range tr.events() {
		if strings.HasPrefix(e, prefix) {
			n++
		}
	}
	return n
}

func (tr *presendTrace) want(t *testing.T, want ...string) {
	t.Helper()
	if got := tr.events(); !reflect.DeepEqual(got, want) {
		t.Fatalf("events\n got %q\nwant %q", got, want)
	}
}

// presendResolver is a resolver stub that records every query in the trace.
type presendResolver struct {
	tr    *presendTrace
	addrs map[string][]string
}

func (r *presendResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	r.tr.add("lookup %s", host)
	var out []netip.Addr
	for _, s := range r.addrs[host] {
		out = append(out, netip.MustParseAddr(s))
	}
	return out, nil
}

// presendReferee is a fake referee that records every question in the trace
// and denies the URLs deny names.
type presendReferee struct {
	tr   *presendTrace
	deny func(u string) bool
}

func (r *presendReferee) Allow(_ context.Context, u string) (bool, string, error) {
	r.tr.add("referee %s", u)
	return r.deny == nil || !r.deny(u), "fake", nil
}

// fetchRig is a PinnedFetcher under WebFetchTools whose resolver, referee and
// dialer are fakes, and whose only connection target is a local httptest
// listener: nothing leaves the machine.
type fetchRig struct {
	tr       *presendTrace
	screener *tools.URLScreener
	ref      *presendReferee
	fetcher  *tools.PinnedFetcher
	web      *tools.WebFetchTools
	hosts    []string // the Host header of every request the server received
	mu       sync.Mutex
}

var presendAddrs = map[string][]string{
	"pub.test":   {"93.184.216.34"},
	"other.test": {"8.8.8.8"},
	"priv.test":  {"93.184.216.34", "10.0.0.5"},
}

// newFetchRig serves handler on a local listener and returns a rig whose dial
// seam records the address it was asked for and connects to the listener.
func newFetchRig(t *testing.T, screener *tools.URLScreener, handler http.HandlerFunc) *fetchRig {
	t.Helper()
	g := &fetchRig{tr: &presendTrace{}, screener: screener}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.tr.add("serve %s", r.URL.RequestURI())
		g.mu.Lock()
		g.hosts = append(g.hosts, r.Host)
		g.mu.Unlock()
		if handler != nil {
			handler(w, r)
		}
	}))
	t.Cleanup(ts.Close)
	g.ref = &presendReferee{tr: g.tr}
	res := &presendResolver{tr: g.tr, addrs: presendAddrs}
	f, err := tools.NewPinnedFetcher(screener, g.ref, res)
	if err != nil {
		t.Fatal(err)
	}
	target := ts.Listener.Addr().String()
	g.fetcher = f.WithNetwork(&tools.SearchNetwork{
		Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			g.tr.add("dial %s", addr)
			var d net.Dialer
			return d.DialContext(ctx, network, target)
		},
	})
	g.web = tools.NewWebFetchTools(g.fetcher, 0)
	return g
}

func (g *fetchRig) fetch(t *testing.T, rawURL string) tools.Result {
	t.Helper()
	res, err := g.web.Fetch(context.Background(), rawURL)
	if err != nil {
		t.Fatalf("fetch %s: %v", rawURL, err)
	}
	return res
}

func presendRefused(t *testing.T, res tools.Result, rule string) {
	t.Helper()
	if res.ErrorCode != tools.ErrCodeRefused || !strings.Contains(string(res.Content), rule+":") {
		t.Fatalf("result %q (%q), want a refusal by rule %q", res.Content, res.ErrorCode, rule)
	}
}

func presendWantRule(t *testing.T, v tools.Verdict, rule string) {
	t.Helper()
	if v.Allowed || v.Rule != rule {
		t.Fatalf("verdict %+v, want refused by %q", v, rule)
	}
}

func presendWantAllowed(t *testing.T, v tools.Verdict) {
	t.Helper()
	if !v.Allowed {
		t.Fatalf("verdict %+v, want allowed", v)
	}
}

// shannon is the Shannon entropy of v in bits per byte.
func shannon(v string) float64 {
	var counts [256]int
	for i := 0; i < len(v); i++ {
		counts[v[i]]++
	}
	var h float64
	for _, c := range counts {
		if c == 0 {
			continue
		}
		p := float64(c) / float64(len(v))
		h -= p * math.Log2(p)
	}
	return h
}

// entropyValue returns a lowercase value of length n whose entropy is about
// 4.6 bits per byte: twenty-four letters, each used twice, and for n = 49 one
// of them a third time.
func entropyValue(t *testing.T, n int) string {
	t.Helper()
	const letters = "abcdefghijklmnopqrstuvwx"
	var rev []byte
	for i := len(letters) - 1; i >= 0; i-- {
		rev = append(rev, letters[i])
	}
	v := (letters + string(rev) + "a")[:n]
	if h := shannon(v); h < 4.55 || h > 4.65 {
		t.Fatalf("value of %d bytes has entropy %.3f, want about 4.6", n, h)
	}
	return v
}

// The five routes by which file-derived bytes reach the corpus, each with a
// distinct 16-byte secret inside surrounding content.
var presendSources = []struct {
	name   string
	route  tools.Route
	secret string
}{
	{"snapshot-read", tools.RouteSnapshotRead, "snapshotread0001"},
	{"staged-write", tools.RouteStagedWrite, "stagedwrite00002"},
	{"staged-base", tools.RouteBase, "stagedbase000003"},
	{"reviewer-diff", tools.RouteDiff, "reviewerdiff0004"},
	{"history", tools.RouteHistory, "historydoc000005"},
}

func pct(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		fmt.Fprintf(&b, "%%%02X", s[i])
	}
	return b.String()
}

func b64(s string) string  { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
func hexs(s string) string { return hex.EncodeToString([]byte(s)) }

var presendEncodings = []struct {
	name string
	enc  func(string) string
}{
	{"plain", func(s string) string { return s }},
	{"percent", pct},
	{"base64", b64},
	{"hex", hexs},
	{"base64-of-hex", func(s string) string { return b64(hexs(s)) }},
	{"hex-of-base64", func(s string) string { return hexs(b64(s)) }},
}

// presendPositions place one encoded value into a URL.
var presendPositions = []struct {
	name string
	url  func(v string) string
}{
	{"query", func(v string) string { return "http://pub.test/search?q=" + v }},
	{"path", func(v string) string { return "http://pub.test/docs/" + v + "/page" }},
	{"hostname", func(v string) string { return "http://" + v + ".pub.test/" }},
}

func presendScreenerWith(route tools.Route, secret string) *tools.URLScreener {
	s := tools.NewURLScreener(0)
	s.AddBytes(route, []byte("some surrounding content before "+secret+" and after it"))
	return s
}

func TestPresend_CanaryEncodingsSourcesPositions(t *testing.T) {
	for _, src := range presendSources {
		for _, enc := range presendEncodings {
			for _, pos := range presendPositions {
				t.Run(src.name+"/"+enc.name+"/"+pos.name, func(t *testing.T) {
					s := presendScreenerWith(src.route, src.secret)
					v := s.Screen(pos.url(enc.enc(src.secret)))
					if enc.name == "percent" && pos.name == "hostname" {
						// A percent escape is not a valid hostname byte: the
						// URL does not parse, which is refused before the
						// canary could look at it. Either way it is refused.
						if v.Allowed {
							t.Fatalf("verdict %+v, want refused", v)
						}
						return
					}
					presendWantRule(t, v, tools.RuleCanary)
				})
			}
		}
	}
}

// A control for the matrix: the same positions with a value that is not in the
// corpus are allowed, so the refusals above are the canary's.
func TestPresend_CanaryFifteenBytes(t *testing.T) {
	src := presendSources[0]
	cases := map[string]func(string) string{
		"plain":  func(s string) string { return s },
		"base64": b64,
		"hex":    hexs,
	}
	for name, enc := range cases {
		for _, pos := range presendPositions {
			t.Run(name+"/"+pos.name, func(t *testing.T) {
				s := presendScreenerWith(src.route, src.secret)
				presendWantAllowed(t, s.Screen(pos.url(enc(src.secret[:15]))))
				presendWantAllowed(t, s.Screen(pos.url(enc(src.secret[1:]))))
				// The sixteenth byte makes the same value a match.
				presendWantRule(t, s.Screen(pos.url(enc(src.secret))), tools.RuleCanary)
			})
		}
	}
}

func TestPresend_CanaryThreeLevelsNotDecoded(t *testing.T) {
	src := presendSources[0]
	two := func(s string) string { return hexs(b64(hexs(s))) } // three levels
	for _, pos := range presendPositions[:2] {                 // a third level is 86 bytes: too long for a label
		t.Run(pos.name, func(t *testing.T) {
			s := presendScreenerWith(src.route, src.secret)
			presendWantAllowed(t, s.Screen(pos.url(two(src.secret))))
			// Two levels of the same secret are decoded.
			presendWantRule(t, s.Screen(pos.url(hexs(b64(src.secret)))), tools.RuleCanary)
			presendWantRule(t, s.Screen(pos.url(b64(hexs(src.secret)))), tools.RuleCanary)
		})
	}
}

func TestPresend_URLLength(t *testing.T) {
	const prefix = "http://pub.test/?q="
	for _, c := range []struct {
		n    int
		rule string
	}{{2048, ""}, {2049, tools.RuleShape}} {
		u := prefix + strings.Repeat("a", c.n-len(prefix))
		if len(u) != c.n {
			t.Fatalf("built %d bytes, want %d", len(u), c.n)
		}
		v := tools.NewURLScreener(0).Screen(u)
		if c.rule == "" {
			presendWantAllowed(t, v)
		} else {
			presendWantRule(t, v, c.rule)
		}
	}
}

func TestPresend_HostnameLengths(t *testing.T) {
	label := func(n int) string { return strings.Repeat("a", n) }
	host := func(labels ...string) string { return strings.Join(labels, ".") }
	for _, c := range []struct {
		name string
		host string
		want string
	}{
		{"label-63", host(label(63), "test"), ""},
		{"label-64", host(label(64), "test"), tools.RuleShape},
		{"host-253", host(label(63), label(63), label(63), label(61)), ""},
		{"host-254", host(label(63), label(63), label(63), label(62)), tools.RuleShape},
	} {
		t.Run(c.name, func(t *testing.T) {
			v := tools.NewURLScreener(0).Screen("http://" + c.host + "/")
			if c.want == "" {
				presendWantAllowed(t, v)
			} else {
				presendWantRule(t, v, c.want)
			}
		})
	}
	if n := len(host(label(63), label(63), label(63), label(61))); n != 253 {
		t.Fatalf("host length %d, want 253", n)
	}
}

func TestPresend_EntropyBoundary(t *testing.T) {
	v48, v49 := entropyValue(t, 48), entropyValue(t, 49)
	lowEntropy49 := strings.Repeat("abcdefghijklmnopqrst", 3)[:49]
	if h := shannon(lowEntropy49); h > 4.5 {
		t.Fatalf("control value has entropy %.3f, want at most 4.5", h)
	}
	for _, pos := range presendPositions {
		t.Run(pos.name, func(t *testing.T) {
			presendWantRule(t, tools.NewURLScreener(0).Screen(pos.url(v49)), tools.RuleShape)
			presendWantAllowed(t, tools.NewURLScreener(0).Screen(pos.url(v48)))
			presendWantAllowed(t, tools.NewURLScreener(0).Screen(pos.url(lowEntropy49)))
		})
	}
}

func TestPresend_PerHostCount(t *testing.T) {
	s := tools.NewURLScreener(0)
	for i := 1; i <= 10; i++ {
		presendWantAllowed(t, s.Screen(fmt.Sprintf("http://pub.test/%d", i)))
	}
	presendWantRule(t, s.Screen("http://pub.test/11"), tools.RuleHostCount)
	// The cap is per host.
	presendWantAllowed(t, s.Screen("http://other.test/1"))
}

func TestPresend_TurnCount(t *testing.T) {
	s := tools.NewURLScreener(0) // the default, 20, counted across hosts
	for i := 0; i < 20; i++ {
		presendWantAllowed(t, s.Screen(fmt.Sprintf("http://host%d.test/", i%4)))
	}
	presendWantRule(t, s.Screen("http://host9.test/"), tools.RuleTurnCount)

	// tools.max_fetches_per_turn sets the bound, and a refusal consumes none.
	s = tools.NewURLScreener(2)
	presendWantAllowed(t, s.Screen("http://a.test/"))
	presendWantRule(t, s.Screen("ftp://a.test/"), tools.RuleScheme)
	presendWantAllowed(t, s.Screen("http://b.test/"))
	presendWantRule(t, s.Screen("http://c.test/"), tools.RuleTurnCount)
}

func TestPresend_NoResolutionForRefused(t *testing.T) {
	policy, err := tools.NewHostPolicy([]string{"pub.test"})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name    string
		url     string
		setup   func(s *tools.URLScreener)
		deny    func(string) bool
		rule    string
		referee int // questions the referee is asked
	}{
		{name: "scheme", url: "ftp://pub.test/", rule: tools.RuleScheme},
		{name: "canary", url: "http://pub.test/?q=snapshotread0001", rule: tools.RuleCanary,
			setup: func(s *tools.URLScreener) { s.AddBytes(tools.RouteSnapshotRead, []byte("xx snapshotread0001 xx")) }},
		{name: "shape", url: "http://pub.test/?q=" + strings.Repeat("a", 2100), rule: tools.RuleShape},
		{name: "allowlist", url: "http://other.test/", rule: tools.RuleAllowlist,
			setup: func(s *tools.URLScreener) { s.SetHostPolicy(policy) }},
		{name: "host-count", url: "http://pub.test/x", rule: tools.RuleHostCount,
			setup: func(s *tools.URLScreener) {
				for i := 0; i < 10; i++ {
					s.Screen("http://pub.test/")
				}
			}},
		{name: "turn-count", url: "http://pub.test/x", rule: tools.RuleTurnCount,
			setup: func(s *tools.URLScreener) { s.Screen("http://other.test/") }},
		{name: "referee", url: "http://pub.test/x", rule: tools.RuleReferee, referee: 1,
			deny: func(string) bool { return true }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := tools.NewURLScreener(1)
			if c.name != "turn-count" {
				s = tools.NewURLScreener(0)
			}
			if c.setup != nil {
				c.setup(s)
			}
			g := newFetchRig(t, s, nil)
			g.ref.deny = c.deny
			res := g.fetch(t, c.url)
			presendRefused(t, res, c.rule)
			if g.tr.count("lookup") != 0 || g.tr.count("dial") != 0 || g.tr.count("serve") != 0 {
				t.Fatalf("a refused URL reached the network: %q", g.tr.events())
			}
			if n := g.tr.count("referee"); n != c.referee {
				t.Fatalf("referee asked %d times, want %d: %q", n, c.referee, g.tr.events())
			}
		})
	}
}

func TestPresend_ResolutionOnceAfterReferee(t *testing.T) {
	g := newFetchRig(t, tools.NewURLScreener(0), func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("page"))
	})
	res := g.fetch(t, "http://pub.test/doc")
	if res.ErrorCode != "" || string(res.Content) != "page" {
		t.Fatalf("result %q (%q)", res.Content, res.ErrorCode)
	}
	g.tr.want(t,
		"referee http://pub.test/doc",
		"lookup pub.test",
		"dial 93.184.216.34:80",
		"serve /doc",
	)
}

func TestPresend_ConnectionPinned(t *testing.T) {
	g := newFetchRig(t, tools.NewURLScreener(0), func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("page"))
	})
	if res := g.fetch(t, "http://pub.test/"); string(res.Content) != "page" {
		t.Fatalf("result %q (%q)", res.Content, res.ErrorCode)
	}
	// One lookup, one connection, to the address that passed the address rule,
	// and the request still names the host.
	if g.tr.count("lookup") != 1 || g.tr.count("dial") != 1 || g.tr.events()[2] != "dial 93.184.216.34:80" {
		t.Fatalf("events %q", g.tr.events())
	}
	g.mu.Lock()
	hosts := g.hosts
	g.mu.Unlock()
	if len(hosts) != 1 || hosts[0] != "pub.test" {
		t.Fatalf("Host headers %q, want pub.test", hosts)
	}

	// A resolution with any forbidden address is refused with no connection.
	g2 := newFetchRig(t, tools.NewURLScreener(0), nil)
	presendRefused(t, g2.fetch(t, "http://priv.test/"), tools.RuleAddress)
	if g2.tr.count("dial") != 0 || g2.tr.count("serve") != 0 {
		t.Fatalf("a refused address was dialed: %q", g2.tr.events())
	}
	// So is an IP literal in a private range, which is not looked up at all.
	g3 := newFetchRig(t, tools.NewURLScreener(0), nil)
	presendRefused(t, g3.fetch(t, "http://10.0.0.5/"), tools.RuleAddress)
	if g3.tr.count("lookup") != 0 || g3.tr.count("dial") != 0 {
		t.Fatalf("events %q", g3.tr.events())
	}
}

func TestPresend_SameHostRedirectRechecked(t *testing.T) {
	redirect := func(to string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/a" {
				http.Redirect(w, r, to, http.StatusFound)
				return
			}
			_, _ = w.Write([]byte("final"))
		}
	}
	t.Run("followed-with-every-step-per-hop", func(t *testing.T) {
		g := newFetchRig(t, tools.NewURLScreener(0), redirect("/b"))
		res := g.fetch(t, "http://pub.test/a")
		if res.ErrorCode != "" || string(res.Content) != "final" {
			t.Fatalf("result %q (%q)", res.Content, res.ErrorCode)
		}
		g.tr.want(t,
			"referee http://pub.test/a", "lookup pub.test", "dial 93.184.216.34:80", "serve /a",
			"referee http://pub.test/b", "lookup pub.test", "dial 93.184.216.34:80", "serve /b",
		)
	})
	t.Run("rules-refuse-the-target-before-the-referee", func(t *testing.T) {
		for name, c := range map[string]struct {
			to   string
			rule string
			seed func(s *tools.URLScreener)
		}{
			"canary": {"/?q=snapshotread0001", tools.RuleCanary, func(s *tools.URLScreener) {
				s.AddBytes(tools.RouteSnapshotRead, []byte("xx snapshotread0001 xx"))
			}},
			"shape":    {"/" + strings.Repeat("a", 2100), tools.RuleShape, nil},
			"host-cap": {"/b", tools.RuleHostCount, func(s *tools.URLScreener) { exhaustHost(s, 9) }},
			"turn-cap": {"/b", tools.RuleTurnCount, nil},
		} {
			t.Run(name, func(t *testing.T) {
				s := tools.NewURLScreener(0)
				if name == "turn-cap" {
					s = tools.NewURLScreener(1)
				}
				if c.seed != nil {
					c.seed(s)
				}
				g := newFetchRig(t, s, redirect(c.to))
				presendRefused(t, g.fetch(t, "http://pub.test/a"), c.rule)
				// Only the first hop was refereed, resolved, dialed and served.
				if len(g.tr.events()) != 4 {
					t.Fatalf("events %q, want the first hop only", g.tr.events())
				}
			})
		}
	})
	t.Run("referee-denies-the-target", func(t *testing.T) {
		g := newFetchRig(t, tools.NewURLScreener(0), redirect("/b"))
		g.ref.deny = func(u string) bool { return strings.HasSuffix(u, "/b") }
		presendRefused(t, g.fetch(t, "http://pub.test/a"), tools.RuleReferee)
		g.tr.want(t,
			"referee http://pub.test/a", "lookup pub.test", "dial 93.184.216.34:80", "serve /a",
			"referee http://pub.test/b",
		)
	})
}

// exhaustHost counts n fetches against pub.test.
func exhaustHost(s *tools.URLScreener, n int) {
	for i := 0; i < n; i++ {
		s.Screen("http://pub.test/")
	}
}

func TestPresend_FourthHopRefused(t *testing.T) {
	step := func(w http.ResponseWriter, r *http.Request) {
		var n int
		_, _ = fmt.Sscanf(r.URL.Path, "/%d", &n)
		http.Redirect(w, r, fmt.Sprintf("/%d", n+1), http.StatusFound)
	}
	g := newFetchRig(t, tools.NewURLScreener(0), step)
	res := g.fetch(t, "http://pub.test/0")
	if res.ErrorCode != tools.ErrCodeRedirectLimit {
		t.Fatalf("result %q (%q), want %s", res.Content, res.ErrorCode, tools.ErrCodeRedirectLimit)
	}
	var want []string
	for i := 0; i <= 3; i++ { // the fetch and three followed hops
		want = append(want,
			fmt.Sprintf("referee http://pub.test/%d", i), "lookup pub.test", "dial 93.184.216.34:80", fmt.Sprintf("serve /%d", i))
	}
	g.tr.want(t, want...) // the fourth target is never refereed, resolved or requested

	// Three hops is within the bound.
	g = newFetchRig(t, tools.NewURLScreener(0), func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/3" {
			_, _ = w.Write([]byte("end"))
			return
		}
		step(w, r)
	})
	if res := g.fetch(t, "http://pub.test/0"); string(res.Content) != "end" || res.ErrorCode != "" {
		t.Fatalf("result %q (%q)", res.Content, res.ErrorCode)
	}
}

func TestPresend_CrossHostRedirectNotFollowed(t *testing.T) {
	g := newFetchRig(t, tools.NewURLScreener(0), func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://other.test/next?x=1", http.StatusFound)
	})
	res := g.fetch(t, "http://pub.test/")
	if res.ErrorCode != "" || !strings.Contains(string(res.Content), "http://other.test/next?x=1") {
		t.Fatalf("result %q (%q), want the target returned as data", res.Content, res.ErrorCode)
	}
	// Only the original host was refereed, resolved, dialed and requested.
	g.tr.want(t, "referee http://pub.test/", "lookup pub.test", "dial 93.184.216.34:80", "serve /")
}

func TestPresend_AllowlistWildcards(t *testing.T) {
	cases := []struct {
		entry string
		allow []string
		deny  []string
	}{
		{"*.example.com", []string{"a.example.com", "A.Example.COM", "b.a.example.com"}, []string{"example.com", "badexample.com", "example.com.evil.test", "example.org"}},
		{"**.example.com", []string{"example.com", "a.example.com", "b.a.example.com"}, []string{"badexample.com", "example.com.evil.test"}},
		{"docs.example.com", []string{"docs.example.com"}, []string{"example.com", "x.docs.example.com", "mdocs.example.com"}},
	}
	for _, c := range cases {
		t.Run(c.entry, func(t *testing.T) {
			policy, err := tools.NewHostPolicy([]string{c.entry})
			if err != nil {
				t.Fatal(err)
			}
			for _, h := range c.allow {
				s := tools.NewURLScreener(0)
				s.SetHostPolicy(policy)
				presendWantAllowed(t, s.Screen("https://"+h+"/"))
				// The default port is stripped before matching.
				presendWantAllowed(t, s.Screen("https://"+h+":443/"))
			}
			for _, h := range c.deny {
				s := tools.NewURLScreener(0)
				s.SetHostPolicy(policy)
				presendWantRule(t, s.Screen("https://"+h+"/"), tools.RuleAllowlist)
			}
		})
	}
}

func TestPresend_AllowlistIPLiteral(t *testing.T) {
	policy, err := tools.NewHostPolicy([]string{"**.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{"http://93.184.216.34/", "http://[2606:2800:220:1::1]/", "http://1.1.1.1:8080/x"} {
		s := tools.NewURLScreener(0)
		s.SetHostPolicy(policy)
		presendWantRule(t, s.Screen(u), tools.RuleAllowlist)

		g := newFetchRig(t, tools.NewURLScreener(0), nil)
		g.screener.SetHostPolicy(policy)
		presendRefused(t, g.fetch(t, u), tools.RuleAllowlist)
		g.tr.want(t) // neither refereed nor resolved nor dialed
	}
	// With no allowlist an IP literal passes the screener (the address rule
	// judges it later).
	presendWantAllowed(t, tools.NewURLScreener(0).Screen("http://93.184.216.34/"))
}

// presendProvider is the fake search provider: GET https://<host>/search?q=...
// with the credential in a header.
type presendProvider struct{}

func (presendProvider) Name() string { return "fake" }

func (presendProvider) Request(ctx context.Context, host, query, credential string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+host+"/search?q="+url.QueryEscape(query), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Search-Key", credential)
	return req, nil
}

const (
	presendSearchHost = "search.test"
	presendSearchCred = "search-credential-value"
)

// searchRig is a journal with one accepted call per search, and a
// WebSearchTools over a PinnedSearcher whose resolver, referee, dialer and
// provider server are local fakes.
type searchRig struct {
	tr    *presendTrace
	ref   *presendReferee
	creds atomic.Int32
	j     *journal.Store
	att   string
	calls int
	web   *tools.WebSearchTools
}

func newSearchRig(t *testing.T, screener *tools.URLScreener) *searchRig {
	t.Helper()
	g := &searchRig{tr: &presendTrace{}}
	g.ref = &presendReferee{tr: g.tr}
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.tr.add("serve %s", r.URL.Path)
		_, _ = w.Write([]byte(`[{"title":"result"}]`))
	}))
	t.Cleanup(ts.Close)
	target := ts.Listener.Addr().String()
	n := &tools.SearchNetwork{
		Resolver: &presendResolver{tr: g.tr, addrs: map[string][]string{presendSearchHost: {"93.184.216.34"}}},
		Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			g.tr.add("dial %s", addr)
			var d net.Dialer
			return d.DialContext(ctx, network, target)
		},
		TLSConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // the local test server's certificate is self-signed
	}
	cred := func() (string, error) {
		g.creds.Add(1)
		return presendSearchCred, nil
	}
	searcher, err := tools.NewPinnedSearcher(screener, g.ref, presendProvider{}, presendSearchHost, cred, n)
	if err != nil {
		t.Fatal(err)
	}

	db, err := journal.Open(filepath.Join(t.TempDir(), "j.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	g.j = journal.NewStore(db)
	err = g.j.Tx(func(tx *journal.Tx) error {
		run, _, err := tx.CreateRun([]byte(`{}`))
		if err != nil {
			return err
		}
		turn, err := tx.CreateTurn(run, "review", nil)
		if err != nil {
			return err
		}
		g.att, err = tx.BeginAttempt(turn, "author", "author", 1, nil, "")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	g.web = tools.NewWebSearchTools(g.j, searcher, 0)
	return g
}

// search journals an accepted web_search call and executes it, returning the
// result and the journaled call.
func (g *searchRig) search(t *testing.T, query string) (tools.Result, journal.ToolCall) {
	t.Helper()
	raw, err := json.Marshal(map[string]string{"query": query})
	if err != nil {
		t.Fatal(err)
	}
	var id string
	if err := g.j.Tx(func(tx *journal.Tx) error {
		c, err := tx.RecordToolCall(g.att, g.calls, fmt.Sprintf("p%d", g.calls), tools.NameWebSearch, tools.GroupWeb, raw)
		id = c.ID
		return err
	}); err != nil {
		t.Fatal(err)
	}
	g.calls++
	res, err := g.web.Search(context.Background(), id, raw, func() error { return nil })
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	var tc journal.ToolCall
	if err := g.j.Tx(func(tx *journal.Tx) (err error) { tc, err = tx.GetToolCall(id); return err }); err != nil {
		t.Fatal(err)
	}
	return res, tc
}

func TestPresend_SearchCheckedRefereedUnresolved(t *testing.T) {
	t.Run("allowed-query-is-refereed-then-resolved-once-and-pinned", func(t *testing.T) {
		g := newSearchRig(t, tools.NewURLScreener(0))
		res, _ := g.search(t, "go generics tutorial")
		if res.ErrorCode != "" || !strings.Contains(string(res.Content), "result") {
			t.Fatalf("result %q (%q)", res.Content, res.ErrorCode)
		}
		g.tr.want(t, "referee go generics tutorial", "lookup search.test", "dial 93.184.216.34:443", "serve /search")
		if g.creds.Load() != 1 {
			t.Fatalf("credential read %d times, want once, after the allow", g.creds.Load())
		}
	})
	t.Run("canary-refuses-the-query-unresolved", func(t *testing.T) {
		for _, enc := range presendEncodings {
			if enc.name == "percent" {
				continue // a query is text: the provider URL escapes it, so "%73" in it is literal
			}
			t.Run(enc.name, func(t *testing.T) {
				s := tools.NewURLScreener(0)
				s.AddBytes(tools.RouteSnapshotRead, []byte("xx snapshotread0001 xx"))
				g := newSearchRig(t, s)
				res, _ := g.search(t, enc.enc("snapshotread0001"))
				presendRefused(t, res, tools.RuleCanary)
				g.tr.want(t) // no referee question, no lookup, no dial
				if g.creds.Load() != 0 {
					t.Fatal("the credential was read for a refused query")
				}
			})
		}
	})
	t.Run("shape-refuses-the-query-unresolved", func(t *testing.T) {
		g := newSearchRig(t, tools.NewURLScreener(0))
		res, _ := g.search(t, entropyValue(t, 49))
		presendRefused(t, res, tools.RuleShape)
		g.tr.want(t)
		res, _ = g.search(t, entropyValue(t, 48))
		if res.ErrorCode != "" {
			t.Fatalf("result %q (%q)", res.Content, res.ErrorCode)
		}
	})
	t.Run("referee-deny-leaves-the-provider-unresolved", func(t *testing.T) {
		g := newSearchRig(t, tools.NewURLScreener(0))
		g.ref.deny = func(string) bool { return true }
		res, _ := g.search(t, "an unwanted query")
		presendRefused(t, res, tools.RuleReferee)
		g.tr.want(t, "referee an unwanted query")
		if g.creds.Load() != 0 {
			t.Fatal("the credential was read for a refused query")
		}
	})
	t.Run("fetch-counts-apply-to-queries", func(t *testing.T) {
		g := newSearchRig(t, tools.NewURLScreener(1))
		if res, _ := g.search(t, "one"); res.ErrorCode != "" {
			t.Fatalf("result %q (%q)", res.Content, res.ErrorCode)
		}
		res, _ := g.search(t, "two")
		presendRefused(t, res, tools.RuleTurnCount)
		if g.tr.count("lookup") != 1 {
			t.Fatalf("events %q, want one lookup", g.tr.events())
		}
	})
	t.Run("provider-host-must-pass-the-checks-to-be-offered", func(t *testing.T) {
		policy, err := tools.NewHostPolicy([]string{"*.example.com"})
		if err != nil {
			t.Fatal(err)
		}
		if r := tools.SearchHostAllowed("api.example.com", policy); r != nil {
			t.Fatalf("allowlisted provider host refused: %v", r)
		}
		var r *tools.RefusalError
		if err := tools.SearchHostAllowed(presendSearchHost, policy); !errors.As(err, &r) || r.Rule != tools.RuleAllowlist {
			t.Fatalf("host off the allowlist: %v", err)
		}
		if r := tools.SearchHostAllowed(presendSearchHost, nil); r != nil {
			t.Fatalf("no allowlist: %v", r)
		}
	})
}

func TestPresend_RefusalsJournaled(t *testing.T) {
	policy, err := tools.NewHostPolicy([]string{"*.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	// Refusals of a search are journaled against the accepted call: the call's
	// arguments hold the query, and the error result names the rule.
	queries := []struct {
		name  string
		query string
		setup func(s *tools.URLScreener)
		deny  bool
		rule  string
	}{
		{"canary", "secret snapshotread0001", func(s *tools.URLScreener) {
			s.AddBytes(tools.RouteSnapshotRead, []byte("xx snapshotread0001 xx"))
		}, false, tools.RuleCanary},
		{"shape", entropyValue(t, 49), nil, false, tools.RuleShape},
		{"referee", "denied query", nil, true, tools.RuleReferee},
		{"turn-count", "second query", func(s *tools.URLScreener) { s.Screen("http://x.test/") }, false, tools.RuleTurnCount},
	}
	for _, c := range queries {
		t.Run("search/"+c.name, func(t *testing.T) {
			s := tools.NewURLScreener(1)
			if c.name != "turn-count" {
				s = tools.NewURLScreener(0)
			}
			if c.setup != nil {
				c.setup(s)
			}
			g := newSearchRig(t, s)
			if c.deny {
				g.ref.deny = func(string) bool { return true }
			}
			res, tc := g.search(t, c.query)
			presendRefused(t, res, c.rule)
			if tc.Status != journal.ToolCallError || tc.Result == nil || tc.Result.ErrorCode != tools.ErrCodeRefused {
				t.Fatalf("journaled call %+v result %+v, want an error result", tc, tc.Result)
			}
			body, err := g.j.Content(tc.Result.ResultSHA256)
			if err != nil || !strings.Contains(string(body), c.rule+":") || string(body) != string(res.Content) {
				t.Fatalf("journaled result %q (err %v), want the refusal naming %q", body, err, c.rule)
			}
			args, err := g.j.Content(tc.ArgsSHA256)
			if err != nil || !strings.Contains(string(args), c.query) {
				t.Fatalf("journaled arguments %q (err %v), want the query %q", args, err, c.query)
			}
		})
	}

	// A refused fetch names its rule in the error the model receives, and the
	// refusal carries the rule the journal records.
	for name, c := range map[string]struct {
		url  string
		rule string
		seed func(s *tools.URLScreener)
	}{
		"scheme":    {"ftp://pub.test/x", tools.RuleScheme, nil},
		"allowlist": {"http://pub.test/x", tools.RuleAllowlist, func(s *tools.URLScreener) { s.SetHostPolicy(policy) }},
		"shape":     {"http://pub.test/" + strings.Repeat("a", 2100), tools.RuleShape, nil},
	} {
		t.Run("fetch/"+name, func(t *testing.T) {
			s := tools.NewURLScreener(0)
			if c.seed != nil {
				c.seed(s)
			}
			g := newFetchRig(t, s, nil)
			presendRefused(t, g.fetch(t, c.url), c.rule)
			_, err := g.fetcher.Fetch(context.Background(), c.url)
			var r *tools.RefusalError
			if !errors.As(err, &r) || r.Rule != c.rule {
				t.Fatalf("fetch error %v, want a refusal by %q", err, c.rule)
			}
		})
	}
}
