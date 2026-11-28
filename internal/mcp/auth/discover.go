// Package auth is the login of a remote MCP server (v9 delta, section 7,
// "Login"). This file is its first three steps: find the authorization server
// of a resource and read its metadata, safely.
package auth

// Every request here carries no credential, follows no redirect and keeps no
// cookie. Every connection is made by a remote.Transport, which resolves the
// name, applies v5's address rule and connects to the address that passed.
// The issuer's origin and the origin of every endpoint its metadata names is
// a destination of its own: https and passing the address rule, or listed in
// auth_origins. Naming an origin never excepts it.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/boldfield/syndic/internal/mcp/remote"
	"github.com/boldfield/syndic/internal/tools"
)

const (
	// discoverTimeout bounds one discovery request.
	discoverTimeout = 30 * time.Second
	// maxDocumentBytes bounds a metadata document.
	maxDocumentBytes = 1 << 20
)

// Config is what the roster states for the server being logged in to.
type Config struct {
	// URL is the server's configured url.
	URL string
	// AddressAllow and AuthOrigins are the server's settings of the same name.
	AddressAllow []string
	AuthOrigins  []string
}

// ServerMetadata is the part of an authorization server's metadata (RFC 8414)
// that login uses.
type ServerMetadata struct {
	Issuer                string   `json:"issuer"`
	AuthorizationEndpoint string   `json:"authorization_endpoint"`
	TokenEndpoint         string   `json:"token_endpoint"`
	RegistrationEndpoint  string   `json:"registration_endpoint"`
	CodeChallengeMethods  []string `json:"code_challenge_methods_supported"`
	// ISSSupported says whether the authorization response carries iss
	// (RFC 9207).
	ISSSupported bool `json:"authorization_response_iss_parameter_supported"`
	// ClientIDMetadataDocument says client ID metadata documents are accepted.
	ClientIDMetadataDocument bool `json:"client_id_metadata_document_supported"`
}

// Discovery is the outcome of steps 1 to 3.
type Discovery struct {
	// Resource is the server's URL, which the protected-resource metadata named.
	Resource string
	// Issuer is the authorization server's issuer.
	Issuer string
	// Scopes are the scopes the protected-resource metadata names.
	Scopes []string
	// Metadata is the issuer's metadata.
	Metadata ServerMetadata
}

type resourceMetadata struct {
	Resource             string   `json:"resource"`
	AuthorizationServers []string `json:"authorization_servers"`
	ScopesSupported      []string `json:"scopes_supported"`
}

// Discoverer runs discovery for one server.
type Discoverer struct {
	server   *url.URL
	listed   map[string]bool
	client   *http.Client
	resolver tools.Resolver
}

// NewDiscoverer validates the roster's settings. The server's URL must be a
// usable address under the connection rules, as remote.NewTransport applies
// them.
func NewDiscoverer(cfg Config) (*Discoverer, error) {
	tr, err := remote.NewTransport(remote.Config{URL: cfg.URL, AddressAllow: cfg.AddressAllow, AuthOrigins: cfg.AuthOrigins})
	if err != nil {
		return nil, err
	}
	return newDiscoverer(cfg, tr)
}

func newDiscoverer(cfg Config, rt http.RoundTripper) (*Discoverer, error) {
	u, err := url.Parse(cfg.URL)
	if err != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, fmt.Errorf("login: %q is not a usable server URL", cfg.URL)
	}
	d := &Discoverer{
		server:   u,
		listed:   map[string]bool{},
		resolver: net.DefaultResolver,
		client: &http.Client{
			Transport: rt,
			Timeout:   discoverTimeout,
			// A redirect is never followed; the 3xx is the answer.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
	for _, o := range cfg.AuthOrigins {
		ou, err := url.Parse(o)
		if err != nil || ou.Hostname() == "" {
			return nil, fmt.Errorf("login: auth_origins %q is not an origin", o)
		}
		d.listed[originOf(ou)] = true
	}
	return d, nil
}

// originOf is scheme://host[:port], lowercased, with a default port dropped.
func originOf(u *url.URL) string {
	scheme := strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if port == "" || (scheme == "https" && port == "443") || (scheme == "http" && port == "80") {
		port = ""
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port != "" {
		host += ":" + port
	}
	return scheme + "://" + host
}

// Discover runs steps 1 to 3 of login.
func (d *Discoverer) Discover(ctx context.Context) (*Discovery, error) {
	prm, err := d.protectedResource(ctx)
	if err != nil {
		return nil, err
	}
	if prm.Resource != d.server.String() {
		return nil, fmt.Errorf("login: protected-resource metadata names resource %q, not the server's URL", prm.Resource)
	}
	if len(prm.AuthorizationServers) == 0 {
		return nil, errors.New("login: protected-resource metadata names no authorization server")
	}
	issuer := prm.AuthorizationServers[0]
	meta, err := d.issuerMetadata(ctx, issuer)
	if err != nil {
		return nil, err
	}
	return &Discovery{Resource: prm.Resource, Issuer: issuer, Scopes: prm.ScopesSupported, Metadata: *meta}, nil
}

// protectedResource finds and reads the server's protected-resource metadata
// (RFC 9728), from the server's own origin.
func (d *Discoverer) protectedResource(ctx context.Context) (*resourceMetadata, error) {
	var candidates []string
	if h, err := d.challengeMetadata(ctx); err != nil {
		return nil, err
	} else if h != "" {
		candidates = append(candidates, h)
	}
	root := originOf(d.server)
	if p := d.server.EscapedPath(); p != "" && p != "/" {
		candidates = append(candidates, root+"/.well-known/oauth-protected-resource"+p)
	}
	candidates = append(candidates, root+"/.well-known/oauth-protected-resource")
	var prm resourceMetadata
	if err := d.firstDocument(ctx, candidates, &prm); err != nil {
		return nil, fmt.Errorf("login: protected-resource metadata: %w", err)
	}
	return &prm, nil
}

var resourceMetadataParam = regexp.MustCompile(`(?i)resource_metadata="([^"]*)"`)

// challengeMetadata probes the server's URL with no credential and returns
// the resource_metadata of a 401 challenge, if it has one. A document on any
// other origin than the server's is refused: the metadata is the server's own.
func (d *Discoverer) challengeMetadata(ctx context.Context) (string, error) {
	resp, err := d.get(ctx, d.server.String())
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxDocumentBytes))
	if resp.StatusCode != http.StatusUnauthorized {
		return "", nil
	}
	for _, h := range resp.Header.Values("WWW-Authenticate") {
		m := resourceMetadataParam.FindStringSubmatch(h)
		if m == nil {
			continue
		}
		u, err := url.Parse(m[1])
		if err != nil || originOf(u) != originOf(d.server) {
			return "", fmt.Errorf("login: challenge names resource metadata %q outside the server's origin", m[1])
		}
		return u.String(), nil
	}
	return "", nil
}

// issuerMetadata reads the issuer's metadata (RFC 8414), after the issuer's
// origin and then each endpoint's origin has passed the destination rule.
func (d *Discoverer) issuerMetadata(ctx context.Context, issuer string) (*ServerMetadata, error) {
	iu, err := url.Parse(issuer)
	if err != nil || iu.Host == "" || iu.RawQuery != "" || iu.Fragment != "" || iu.User != nil {
		return nil, fmt.Errorf("login: issuer %q is not a usable issuer identifier", issuer)
	}
	if err := d.checkOrigin(ctx, "issuer", iu); err != nil {
		return nil, err
	}
	root := originOf(iu)
	path := strings.TrimRight(iu.EscapedPath(), "/")
	var candidates []string
	if path != "" {
		candidates = []string{
			root + "/.well-known/oauth-authorization-server" + path,
			root + "/.well-known/openid-configuration" + path,
			root + path + "/.well-known/openid-configuration",
		}
	} else {
		candidates = []string{
			root + "/.well-known/oauth-authorization-server",
			root + "/.well-known/openid-configuration",
		}
	}
	var meta ServerMetadata
	if err := d.firstDocument(ctx, candidates, &meta); err != nil {
		return nil, fmt.Errorf("login: authorization-server metadata: %w", err)
	}
	if meta.Issuer != issuer {
		return nil, fmt.Errorf("login: authorization-server metadata names issuer %q, not %q", meta.Issuer, issuer)
	}
	s256 := false
	for _, m := range meta.CodeChallengeMethods {
		s256 = s256 || m == "S256"
	}
	if !s256 {
		return nil, errors.New("login: authorization server does not offer the S256 challenge method")
	}
	for _, e := range []struct{ name, raw string }{
		{"authorization_endpoint", meta.AuthorizationEndpoint},
		{"token_endpoint", meta.TokenEndpoint},
		{"registration_endpoint", meta.RegistrationEndpoint},
	} {
		if e.raw == "" {
			if e.name == "registration_endpoint" {
				continue
			}
			return nil, fmt.Errorf("login: authorization-server metadata names no %s", e.name)
		}
		eu, err := url.Parse(e.raw)
		if err != nil || eu.Host == "" || eu.User != nil {
			return nil, fmt.Errorf("login: %s %q is not a usable URL", e.name, e.raw)
		}
		if err := d.checkOrigin(ctx, e.name, eu); err != nil {
			return nil, err
		}
	}
	return &meta, nil
}

// checkOrigin applies the destination rule to u's origin: listed in
// auth_origins, or https and passing v5's address rule. It makes no
// connection to the origin; a name is resolved only to be checked.
func (d *Discoverer) checkOrigin(ctx context.Context, what string, u *url.URL) error {
	if d.listed[originOf(u)] {
		return nil
	}
	refuse := func(reason string) error {
		return &tools.RefusalError{Rule: tools.RuleAddress, Reason: fmt.Sprintf("%s origin %s: %s and it is not listed in auth_origins", what, originOf(u), reason)}
	}
	if strings.ToLower(u.Scheme) != "https" {
		return refuse("it is not https")
	}
	host := u.Hostname()
	var addrs []netip.Addr
	if ip, err := netip.ParseAddr(host); err == nil {
		addrs = []netip.Addr{ip}
	} else {
		if strings.EqualFold(host, "localhost") {
			return refuse("it is loopback")
		}
		addrs, err = d.resolver.LookupNetIP(ctx, "ip", strings.ToLower(host))
		if err != nil {
			return fmt.Errorf("login: resolve %s: %w", host, err)
		}
		if len(addrs) == 0 {
			return refuse("it did not resolve")
		}
	}
	for _, a := range addrs {
		if r := tools.ValidateAddress(a); r != nil {
			return refuse(r.Reason)
		}
	}
	return nil
}

// errNotFound marks a candidate location that has no document.
var errNotFound = errors.New("no document")

// firstDocument reads the first candidate that answers 200 into v. A location
// that answers anything else is skipped; any other failure ends the search.
func (d *Discoverer) firstDocument(ctx context.Context, candidates []string, v any) error {
	for _, c := range candidates {
		err := d.document(ctx, c, v)
		if errors.Is(err, errNotFound) {
			continue
		}
		return err
	}
	return errNotFound
}

func (d *Discoverer) document(ctx context.Context, target string, v any) error {
	resp, err := d.get(ctx, target)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxDocumentBytes))
		return errNotFound
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxDocumentBytes+1))
	if err != nil {
		return err
	}
	if len(body) > maxDocumentBytes {
		return fmt.Errorf("%s: document is too large", target)
	}
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("%s: not a metadata document: %w", target, err)
	}
	return nil
}

// get sends one GET carrying nothing but what http adds by itself: no
// Authorization, no cookie.
func (d *Discoverer) get(ctx context.Context, target string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	return d.client.Do(req)
}
