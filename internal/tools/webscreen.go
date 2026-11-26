package tools

import (
	"encoding/base64"
	"encoding/hex"
	"math"
	"net/netip"
	"net/url"
	"strings"
	"sync"

	"golang.org/x/net/idna"
)

// Pre-send screening constants (design v5 delta, section 6, "Pre-send
// checks"). They are design constants; the acceptance tests fix them.
const (
	// canaryRun is the length of a byte run shared with the turn's file-derived
	// corpus at which a URL is refused.
	canaryRun = 16
	// maxURLBytes is the longest URL accepted.
	maxURLBytes = 2048
	// maxHostBytes is the longest hostname accepted.
	maxHostBytes = 253
	// maxLabelBytes is the longest hostname label accepted.
	maxLabelBytes = 63
	// entropyMinBytes is the length a label, segment or value must exceed for
	// the entropy rule to apply.
	entropyMinBytes = 48
	// entropyMaxBits is the Shannon entropy, in bits per byte, a value over
	// entropyMinBytes must not exceed.
	entropyMaxBits = 4.5
	// maxFetchesPerHost is the number of fetches to one host allowed per turn.
	maxFetchesPerHost = 10
	// DefaultMaxFetchesPerTurn is tools.max_fetches_per_turn's default.
	DefaultMaxFetchesPerTurn = 20
)

// Screening rules, reported in a refusal so it can be journaled.
const (
	RuleCanary    = "canary"
	RuleShape     = "shape"
	RuleHostCount = "host_count"
	RuleTurnCount = "turn_count"
)

// Route names where a file-derived byte sequence came from. The canary treats
// every route alike; the distinction lets callers state which exposure they are
// recording.
type Route int

const (
	// RouteSnapshotRead is the content of a snapshot file a participant read.
	RouteSnapshotRead Route = iota
	// RouteStagedWrite is the content of a staged write.
	RouteStagedWrite
	// RouteBase is the base content of a staged path.
	RouteBase
	// RouteDiff is a diff given to a reviewer as evidence.
	RouteDiff
	// RouteHistory is the answer or change field of the history document. The
	// history document's input fields are the user's own words and must not be
	// passed in.
	RouteHistory
)

// Verdict is the outcome of screening one URL. A refusal names the rule and a
// reason that never repeats any matched bytes.
type Verdict struct {
	Allowed bool
	Rule    string
	Reason  string
}

// URLScreener applies the deterministic pre-send checks to the URLs of one
// turn. One screener is shared by every role of a turn, so counts and the
// corpus span roles. It performs no name resolution and is safe for concurrent
// use.
type URLScreener struct {
	mu       sync.Mutex
	maxTurn  int
	corpus   map[[canaryRun]byte]struct{}
	perHost  map[string]int
	totalNum int
}

// NewURLScreener returns a screener whose turn-wide fetch cap is maxPerTurn
// (tools.max_fetches_per_turn); a value below one selects the default.
func NewURLScreener(maxPerTurn int) *URLScreener {
	if maxPerTurn < 1 {
		maxPerTurn = DefaultMaxFetchesPerTurn
	}
	return &URLScreener{
		maxTurn: maxPerTurn,
		corpus:  make(map[[canaryRun]byte]struct{}),
		perHost: make(map[string]int),
	}
}

// AddBytes records file-derived bytes a participant has been exposed to this
// turn. Each call is its own sequence: a run that would span the end of one
// call and the start of the next is not a match. Only the sixteen-byte windows
// of the sequence are retained.
func (s *URLScreener) AddBytes(_ Route, b []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := 0; i+canaryRun <= len(b); i++ {
		s.corpus[[canaryRun]byte(b[i:i+canaryRun])] = struct{}{}
	}
}

// Screen applies the canary, the shape rules and the fetch counts to rawURL.
// Only a URL that passes every rule is counted against the per-host and
// per-turn caps; a refused URL consumes no budget.
func (s *URLScreener) Screen(rawURL string) Verdict {
	s.mu.Lock()
	defer s.mu.Unlock()

	u, err := url.Parse(rawURL)
	if err != nil {
		return refuse(RuleShape, "URL does not parse")
	}
	if s.hasCanary(rawURL, u) {
		return refuse(RuleCanary, "URL carries bytes from a file read this turn")
	}
	host, v := shapeCheck(rawURL, u)
	if v != nil {
		return *v
	}
	if s.perHost[host] >= maxFetchesPerHost {
		return refuse(RuleHostCount, "per-host fetch limit reached for this turn")
	}
	if s.totalNum >= s.maxTurn {
		return refuse(RuleTurnCount, "fetch limit reached for this turn")
	}
	s.perHost[host]++
	s.totalNum++
	return Verdict{Allowed: true}
}

func refuse(rule, reason string) Verdict {
	return Verdict{Rule: rule, Reason: reason}
}

// canonicalHost returns the lowercase punycode hostname without a trailing dot
// or port. The count is keyed on the hostname alone, so a different port of the
// same name shares its budget.
func canonicalHost(u *url.URL) (string, bool) {
	h := u.Hostname()
	if h == "" {
		return "", true
	}
	if ip, err := netip.ParseAddr(h); err == nil {
		return ip.Unmap().WithZone("").String(), true
	}
	a, err := idna.Lookup.ToASCII(h)
	if err != nil {
		return "", false
	}
	// Strip the root dot only after IDNA, which maps U+3002 and friends to ".".
	return strings.TrimSuffix(strings.ToLower(a), "."), true
}

// shapeCheck applies the length and entropy rules and returns the canonical
// host for counting.
func shapeCheck(rawURL string, u *url.URL) (string, *Verdict) {
	if len(rawURL) > maxURLBytes {
		v := refuse(RuleShape, "URL longer than 2048 bytes")
		return "", &v
	}
	host, ok := canonicalHost(u)
	if !ok {
		v := refuse(RuleShape, "hostname is not a valid name")
		return "", &v
	}
	if len(host) > maxHostBytes {
		v := refuse(RuleShape, "hostname longer than 253 bytes")
		return "", &v
	}
	if u.User != nil {
		parts := []string{u.User.Username()}
		if p, ok := u.User.Password(); ok {
			parts = append(parts, p)
		}
		for _, p := range parts {
			if highEntropyAny(p) {
				v := refuse(RuleShape, "userinfo is long and high-entropy")
				return "", &v
			}
		}
	}
	for _, l := range strings.Split(host, ".") {
		if len(l) > maxLabelBytes {
			v := refuse(RuleShape, "hostname label longer than 63 bytes")
			return "", &v
		}
		if highEntropy(l) {
			v := refuse(RuleShape, "hostname label is long and high-entropy")
			return "", &v
		}
	}
	for _, seg := range splitAny(u.EscapedPath(), "/;") {
		if highEntropy(percentDecode(seg)) {
			v := refuse(RuleShape, "path segment is long and high-entropy")
			return "", &v
		}
	}
	for _, kv := range splitAny(u.RawQuery, "&;") {
		k, val, _ := strings.Cut(kv, "=")
		if highEntropyAny(k) || highEntropyAny(val) {
			v := refuse(RuleShape, "query component is long and high-entropy")
			return "", &v
		}
	}
	return host, nil
}

// splitAny splits s at every byte of seps.
func splitAny(s, seps string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return strings.ContainsRune(seps, r) })
}

// highEntropyAny applies highEntropy to the percent and form readings of v.
func highEntropyAny(v string) bool {
	return highEntropy(percentDecode(v)) || highEntropy(formDecode(v))
}

// highEntropy reports whether v is longer than entropyMinBytes and has Shannon
// entropy above entropyMaxBits bits per byte.
func highEntropy(v string) bool {
	if len(v) <= entropyMinBytes {
		return false
	}
	var counts [256]int
	for i := 0; i < len(v); i++ {
		counts[v[i]]++
	}
	n := float64(len(v))
	var h float64
	for _, c := range counts {
		if c == 0 {
			continue
		}
		p := float64(c) / n
		h -= p * math.Log2(p)
	}
	return h > entropyMaxBits
}

// percentDecode decodes every valid %XX escape and keeps an invalid one
// literal, as lenient receivers do. A "+" stays a plus.
func percentDecode(s string) string {
	if !strings.Contains(s, "%") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) && unhex(s[i+1]) >= 0 && unhex(s[i+2]) >= 0 {
			b.WriteByte(byte(unhex(s[i+1])<<4 | unhex(s[i+2])))
			i += 2
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// formDecode is percentDecode with "+" read as a space, the
// application/x-www-form-urlencoded reading of a query.
func formDecode(s string) string {
	return percentDecode(strings.ReplaceAll(s, "+", " "))
}

func unhex(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	}
	return -1
}

// hasCanary reports whether the URL, whole or by component and after bounded
// decoding, carries a sixteen-byte run from the corpus.
func (s *URLScreener) hasCanary(rawURL string, u *url.URL) bool {
	if len(s.corpus) == 0 {
		return false
	}
	// Whole-URL views: raw, percent-decoded, and with the query form-decoded.
	// Form decoding ("+" as space) applies to the query only.
	formURL := rawURL
	if i := strings.IndexByte(rawURL, '?'); i >= 0 {
		q := rawURL[i+1:]
		frag := ""
		if j := strings.IndexByte(q, '#'); j >= 0 {
			q, frag = q[:j], q[j:]
		}
		formURL = percentDecode(rawURL[:i+1]) + formDecode(q) + percentDecode(frag)
	}
	for _, v := range []string{rawURL, percentDecode(rawURL), formURL} {
		if s.has(v) {
			return true
		}
	}
	type comp struct {
		v          string
		form, fold bool // form-decode as a query; case-fold as a hostname label
	}
	var comps []comp
	if u.User != nil {
		comps = append(comps, comp{v: u.User.Username()})
		if p, ok := u.User.Password(); ok {
			comps = append(comps, comp{v: p})
		}
	}
	for _, l := range strings.Split(strings.TrimSuffix(u.Hostname(), "."), ".") {
		comps = append(comps, comp{v: l, fold: true})
	}
	for _, seg := range splitAny(u.EscapedPath(), "/;") {
		comps = append(comps, comp{v: seg})
	}
	for _, kv := range splitAny(u.RawQuery, "&;") {
		k, v, _ := strings.Cut(kv, "=")
		comps = append(comps, comp{v: kv, form: true}, comp{v: k, form: true}, comp{v: v, form: true})
	}
	for _, c := range comps {
		if c.v == "" {
			continue
		}
		views := []string{c.v, percentDecode(c.v)}
		if c.form {
			views = append(views, formDecode(c.v))
		}
		if c.fold {
			if lc := strings.ToLower(c.v); lc != c.v {
				views = append(views, lc)
			}
		}
		for _, v := range views {
			if s.hasDecoded(v) {
				return true
			}
		}
	}
	return false
}

// hasDecoded checks v, every first-level decoding of it and every decoding of
// those; a third level is not decoded.
func (s *URLScreener) hasDecoded(v string) bool {
	if s.has(v) {
		return true
	}
	for _, d1 := range decodings(v) {
		if s.has(d1) {
			return true
		}
		for _, d2 := range decodings(d1) {
			if s.has(d2) {
				return true
			}
		}
	}
	return false
}

// has reports whether any sixteen-byte window of v is in the corpus.
func (s *URLScreener) has(v string) bool {
	for i := 0; i+canaryRun <= len(v); i++ {
		if _, ok := s.corpus[[canaryRun]byte([]byte(v[i:i+canaryRun]))]; ok {
			return true
		}
	}
	return false
}

// decodings returns every base64 (padded and unpadded, standard and URL
// alphabets) and hex decoding of v that succeeds.
func decodings(v string) []string {
	if v == "" {
		return nil
	}
	var out []string
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding, base64.URLEncoding, base64.RawStdEncoding, base64.RawURLEncoding,
	} {
		if b, err := enc.DecodeString(v); err == nil && len(b) > 0 {
			out = append(out, string(b))
		}
	}
	h := v
	if len(h)%2 == 1 {
		h = h[:len(h)-1]
	}
	if b, err := hex.DecodeString(h); err == nil && len(b) > 0 {
		out = append(out, string(b))
	}
	return out
}
