package tools

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"net/url"
	"strings"

	"github.com/boldfield/syndic/internal/config"
)

// Rules reported by the scheme, userinfo and allowlist checks (design v5 delta,
// section 6, "Pre-send checks" 4 and 5). Every one runs on the URL string and
// before any name resolution.
const (
	RuleScheme    = "scheme"
	RuleUserinfo  = "userinfo"
	RuleAllowlist = "allowlist"
)

// RefusalError is a refusal by a pre-send rule. Rule is one of the Rule
// constants.
type RefusalError struct {
	Rule   string
	Reason string
}

func (e *RefusalError) Error() string { return e.Rule + ": " + e.Reason }

func refusal(rule, reason string) *RefusalError {
	return &RefusalError{Rule: rule, Reason: reason}
}

// checkScheme applies the scheme rule to the URL string: only http and https
// are accepted, and it runs before anything else.
func checkScheme(rawURL string) *RefusalError {
	i := strings.Index(rawURL, "://")
	if i < 0 {
		return refusal(RuleScheme, "URL scheme is not http or https")
	}
	switch strings.ToLower(rawURL[:i]) {
	case "http", "https":
		return nil
	}
	return refusal(RuleScheme, "URL scheme is not http or https")
}

// checkCredentials refuses a URL that carries userinfo or no host: a request
// has no credentials, and Go's client would turn userinfo into an
// Authorization header.
func checkCredentials(u *url.URL) *RefusalError {
	if u.User != nil {
		return refusal(RuleUserinfo, "URL carries credentials")
	}
	if u.Hostname() == "" {
		return refusal(RuleShape, "URL has no hostname")
	}
	return nil
}

// HostPolicy is the tools.web.allowed_hosts allowlist. Matching is on the
// canonical hostname (lowercase punycode, no port, no trailing dot) and is
// purely textual: no name is ever resolved. A nil *HostPolicy means no
// allowlist is set.
type HostPolicy struct {
	exact  map[string]struct{}
	wild   []string // base domains of "*.base" entries
	deep   []string // base domains of "**.base" entries
	hasAny bool
}

// NewHostPolicy parses allowlist patterns. An empty list yields a nil policy
// (no allowlist). A malformed entry is an error, so a configured allowlist
// never widens to allow everything.
func NewHostPolicy(entries []string) (*HostPolicy, error) {
	if len(entries) == 0 {
		return nil, nil
	}
	p := &HostPolicy{exact: make(map[string]struct{}), hasAny: true}
	for _, e := range entries {
		kind, base, err := parseHostEntry(e)
		if err != nil {
			return nil, err
		}
		switch kind {
		case "**":
			p.deep = append(p.deep, base)
		case "*":
			p.wild = append(p.wild, base)
		default:
			p.exact[base] = struct{}{}
		}
	}
	return p, nil
}

// HostPolicyFromConfig builds the allowlist from tools.web.allowed_hosts. It
// returns a nil policy when w is nil or lists no hosts.
func HostPolicyFromConfig(w *config.WebConfig) (*HostPolicy, error) {
	if w == nil {
		return nil, nil
	}
	return NewHostPolicy(w.AllowedHosts)
}

// parseHostEntry returns the wildcard kind ("", "*" or "**") and the canonical
// base domain of one entry, with the same shape rules as the config validator.
func parseHostEntry(e string) (kind, base string, err error) {
	bad := func(why string) (string, string, error) {
		return "", "", fmt.Errorf("allowed_hosts entry %q: %s", e, why)
	}
	rest := e
	switch {
	case strings.HasPrefix(e, "**."):
		kind, rest = "**", e[3:]
	case strings.HasPrefix(e, "*."):
		kind, rest = "*", e[2:]
	}
	switch {
	case e == "":
		return bad("empty")
	case strings.ContainsAny(rest, "*/:[]@ \t"):
		return bad("must be a hostname with at most one leading wildcard and no port, path or IP literal")
	case kind != "" && !strings.Contains(strings.TrimSuffix(rest, "."), "."):
		return bad("wildcard entry needs a base domain with at least one dot")
	}
	canon, err := hostProfile.ToASCII(rest)
	if err != nil {
		return bad(err.Error())
	}
	canon = strings.TrimSuffix(strings.ToLower(canon), ".")
	if canon == "" || strings.HasPrefix(canon, ".") || strings.Contains(canon, "..") {
		return bad("empty hostname label")
	}
	if looksLikeIP(canon) {
		return bad("IP literals are not allowed")
	}
	return kind, canon, nil
}

// looksLikeIP reports whether name is an IP literal in any form a client might
// parse: a canonical address, or a final label that is all digits or 0x hex.
func looksLikeIP(name string) bool {
	if _, err := netip.ParseAddr(name); err == nil {
		return true
	}
	last := name[strings.LastIndexByte(name, '.')+1:]
	if last == "" {
		return false
	}
	digits := strings.TrimPrefix(strings.ToLower(last), "0x")
	if len(digits) == 0 {
		return strings.EqualFold(last, "0x")
	}
	hex := len(digits) != len(last)
	for i := 0; i < len(digits); i++ {
		c := digits[i]
		if c >= '0' && c <= '9' || hex && c >= 'a' && c <= 'f' {
			continue
		}
		return false
	}
	return true
}

// Match reports whether the canonical hostname host is allowed. host may be in
// any case or unicode form; it is canonicalized first. An exact entry matches
// itself only, "*.d" matches subdomains of d and not d, and "**.d" matches
// both. There is no suffix matching.
func (p *HostPolicy) Match(host string) bool {
	if p == nil {
		return true
	}
	h, err := hostProfile.ToASCII(host)
	if err != nil {
		h = host
	}
	h = strings.TrimSuffix(strings.ToLower(h), ".")
	if h == "" {
		return false
	}
	if _, ok := p.exact[h]; ok {
		return true
	}
	for _, b := range p.deep {
		if h == b || strings.HasSuffix(h, "."+b) {
			return true
		}
	}
	for _, b := range p.wild {
		if strings.HasSuffix(h, "."+b) {
			return true
		}
	}
	return false
}

// checkHost applies the allowlist to u, which has already passed the scheme
// and credentials rules. With no allowlist it allows everything; with one it
// refuses every IP literal, then any host not matched.
func (p *HostPolicy) checkHost(u *url.URL) *RefusalError {
	if p == nil {
		return nil
	}
	h := u.Hostname()
	if looksLikeIP(strings.TrimSuffix(h, ".")) || strings.Contains(h, ":") {
		return refusal(RuleAllowlist, "IP literals are refused when an allowlist is set")
	}
	canon, _ := canonicalHost(u)
	if !p.Match(canon) {
		return refusal(RuleAllowlist, "hostname is not on the allowlist")
	}
	return nil
}

// SetHostPolicy installs the allowlist the screener applies; nil removes it.
func (s *URLScreener) SetHostPolicy(p *HostPolicy) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.policy = p
}

// NewGETRequest builds the only request a fetch sends: GET, no body, no
// credentials and no cookies. It re-applies the scheme and credentials rules.
func NewGETRequest(ctx context.Context, rawURL string) (*http.Request, error) {
	if r := checkScheme(rawURL); r != nil {
		return nil, r
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, refusal(RuleShape, "URL does not parse")
	}
	if r := checkCredentials(u); r != nil {
		return nil, r
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, refusal(RuleShape, "URL does not parse")
	}
	return req, nil
}

// Resolver resolves a hostname. *net.Resolver satisfies it.
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// Fetcher sends screened GET requests. A URL is resolved only after the
// screener has allowed it. The lookup is a pre-flight query on the canonical
// (punycode) host and its result is not used for dialing: the transport
// resolves the name again, so this type does not pin the connection to the
// address that was looked up (design v5 delta, section 6). The address rule
// and connection pinning belong to a later task and this Fetcher is not
// compliant with them.
type Fetcher struct {
	Screener *URLScreener
	Resolver Resolver
	// Transport carries the request; nil selects http.DefaultTransport.
	Transport http.RoundTripper
}

// Fetch screens rawURL, looks up its canonical host, and sends a GET with no body,
// cookie jar, credentials or redirect following. A refusal is a *RefusalError.
func (f *Fetcher) Fetch(ctx context.Context, rawURL string) (*http.Response, error) {
	if f.Screener == nil || f.Resolver == nil {
		return nil, errors.New("fetcher needs a screener and a resolver")
	}
	if v := f.Screener.Screen(rawURL); !v.Allowed {
		return nil, refusal(v.Rule, v.Reason)
	}
	req, err := NewGETRequest(ctx, rawURL)
	if err != nil {
		return nil, err
	}
	host, _ := canonicalHost(req.URL)
	if _, err := f.Resolver.LookupNetIP(ctx, "ip", host); err != nil {
		return nil, fmt.Errorf("resolve %s: %w", host, err)
	}
	c := &http.Client{
		Transport:     f.Transport,
		Jar:           nil,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return c.Do(req)
}
