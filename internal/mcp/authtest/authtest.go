// Package authtest provides a scriptable fake OAuth authorization server and
// protected resource for tests (v9 delta, sections 7 and 11). One Server serves
// protected-resource metadata (RFC 9728), authorization-server metadata
// (RFC 8414), an authorization endpoint that redirects to the client's loopback
// listener, a token endpoint with refresh and rotation, a registration
// endpoint (RFC 7591), and a protected resource at ResourceURL.
//
// Every request is recorded with its headers and form values so a test can
// assert on what a client sent, and on what it did not. It contacts nothing
// outside the test binary and needs no credential.
package authtest

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// Paths the server serves.
const (
	ResourcePath     = "/mcp"
	AuthorizePath    = "/authorize"
	TokenPath        = "/token"
	RegistrationPath = "/register"
)

// StaticClientID is the client ID the hand-registered route accepts.
const StaticClientID = "static-client"

// Config is the per-test script. The zero Config is a well-behaved server with
// no issuer path, S256 offered, `iss` promised and sent, and every client
// identity route accepted. Use New for those defaults.
type Config struct {
	// Resource is the "resource" the protected-resource metadata names.
	// Empty means ResourceURL().
	Resource string
	// ExtraResources are further protected-resource paths (each starting with
	// "/") served alongside ResourcePath. A token is good only at the resource
	// it was issued for.
	ExtraResources []string
	// Missing lists request paths that answer 404, e.g. a well-known location,
	// so a client has to fall back to the next one.
	Missing []string
	// Issuer is the issuer the protected-resource metadata lists and the
	// authorization-server metadata names. Empty means the server's URL plus
	// IssuerPath. PRMIssuer, when set, overrides only the one the
	// protected-resource metadata lists.
	Issuer    string
	PRMIssuer string
	// IssuerPath is appended to the server's URL for the default issuer.
	IssuerPath string
	// ChallengeMetadata makes a 401 carry resource_metadata.
	ChallengeMetadata bool
	// NoS256 omits S256 from code_challenge_methods_supported.
	NoS256 bool
	// PromiseISS sets authorization_response_iss_parameter_supported.
	PromiseISS bool
	// SendISS adds iss to the authorization response; ISS overrides its value.
	SendISS bool
	ISS     string
	// Client-identity routes accepted.
	AcceptMetadataDocument bool // client_id metadata documents advertised and accepted
	AcceptRegistration     bool // registration_endpoint advertised and accepting
	AcceptStatic           bool // StaticClientID accepted
	// RegistrationSecret makes registration return a client secret.
	RegistrationSecret bool
	// AccessTTL and RefreshTTL; zero means one hour and unlimited.
	AccessTTL  time.Duration
	RefreshTTL time.Duration
	// NoRotate keeps the refresh token on refresh instead of issuing a new one.
	NoRotate bool
	// RejectRefresh answers every refresh with invalid_grant.
	RejectRefresh bool
	// MissingScope, when set, makes the resource answer 403 insufficient_scope
	// naming it.
	MissingScope string
	// Scope is returned from the token endpoint.
	Scope string
	// Endpoint bases: an origin (or any URL prefix) that replaces the server's
	// own in the metadata. Empty means the server itself. Use Other for a
	// second live origin.
	AuthorizationBase string
	TokenBase         string
	RegistrationBase  string
}

// Request is one recorded request.
type Request struct {
	Method string
	Path   string
	Host   string
	Origin string // "main" or "other": which listener received it
	Header http.Header
	Form   url.Values // query values and, for form posts, body values
	Body   string
	Time   time.Time
}

type grant struct {
	clientID, redirect, challenge, resource string
	used                                    bool
}

type token struct {
	expires  time.Time
	resource string
}

// Server is the fake. Fields are guarded by mu.
type Server struct {
	// URL is the main listener's URL, no trailing slash.
	URL string

	main  *httptest.Server
	other *httptest.Server

	mu       sync.Mutex
	cfg      Config
	requests []Request
	codes    map[string]*grant
	access   map[string]token
	refresh  map[string]*grant // refresh token -> grant context
	rtExpiry map[string]time.Time
	clients  map[string]string // registered client ID -> secret ("" for none)
	// Now is the clock; tests may replace it before use.
	Now func() time.Time
}

// New starts a server and registers its cleanup with t.
func New(t testing.TB, mut ...func(*Config)) *Server {
	t.Helper()
	s := &Server{
		cfg: Config{
			PromiseISS: true, SendISS: true,
			AcceptMetadataDocument: true, AcceptRegistration: true, AcceptStatic: true,
		},
		codes: map[string]*grant{}, access: map[string]token{},
		refresh: map[string]*grant{}, rtExpiry: map[string]time.Time{},
		clients: map[string]string{},
		Now:     time.Now,
	}
	for _, m := range mut {
		m(&s.cfg)
	}
	s.main = httptest.NewServer(s.handler("main"))
	s.URL = s.main.URL
	t.Cleanup(func() {
		s.main.Close()
		s.mu.Lock()
		o := s.other
		s.mu.Unlock()
		if o != nil {
			o.Close()
		}
	})
	return s
}

// Set changes the script; it takes effect for the next request.
func (s *Server) Set(f func(*Config)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f(&s.cfg)
}

// Other starts, once, a second listener serving the same handlers and returns
// its URL. Put an endpoint there with the *Base fields of Config.
func (s *Server) Other(t testing.TB) string {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.other == nil {
		s.other = httptest.NewServer(s.handler("other"))
	}
	return s.other.URL
}

// ResourceURL is the protected resource's URL.
func (s *Server) ResourceURL() string { return s.URL + ResourcePath }

// IssuerURL is the issuer the metadata names under the current script.
func (s *Server) IssuerURL() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.issuerLocked()
}

func (s *Server) issuerLocked() string {
	if s.cfg.Issuer != "" {
		return s.cfg.Issuer
	}
	return s.URL + s.cfg.IssuerPath
}

// Requests returns a copy of every request received, in order.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.requests...)
}

// RequestsTo returns the recorded requests whose path equals path.
func (s *Server) RequestsTo(path string) []Request {
	var out []Request
	for _, r := range s.Requests() {
		if r.Path == path {
			out = append(out, r)
		}
	}
	return out
}

// Count is the number of recorded requests to path.
func (s *Server) Count(path string) int { return len(s.RequestsTo(path)) }

// Tokens returns the access and refresh tokens issued so far, for tests that
// need to assert a value is scrubbed or never sent.
func (s *Server) Tokens() (access, refresh []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k := range s.access {
		access = append(access, k)
	}
	for k := range s.refresh {
		refresh = append(refresh, k)
	}
	return
}

// Expire makes every issued access token expired on the server side.
func (s *Server) Expire() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, v := range s.access {
		v.expires = s.Now().Add(-time.Second)
		s.access[k] = v
	}
}

// Approve performs the authorization request a browser would, without
// following the redirect, and returns the redirect target (or the error
// response). The test then requests that URL from the client's listener.
func (s *Server) Approve(authURL string) (location string, status int, err error) {
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Get(authURL)
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.Header.Get("Location"), resp.StatusCode, nil
}

func (s *Server) handler(origin string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		r.Body = io.NopCloser(strings.NewReader(string(body)))
		_ = r.ParseForm()
		form := url.Values{}
		for k, v := range r.Form {
			form[k] = append([]string(nil), v...)
		}
		s.mu.Lock()
		s.requests = append(s.requests, Request{
			Method: r.Method, Path: r.URL.Path, Host: r.Host, Origin: origin,
			Header: r.Header.Clone(), Form: form, Body: string(body), Time: s.Now(),
		})
		s.mu.Unlock()
		s.route(w, r, form, body)
	})
}

func (s *Server) route(w http.ResponseWriter, r *http.Request, form url.Values, body []byte) {
	p := r.URL.Path
	s.mu.Lock()
	missing := slices.Contains(s.cfg.Missing, p)
	s.mu.Unlock()
	if missing {
		http.NotFound(w, r)
		return
	}
	switch {
	case s.isResourcePath(p):
		s.serveResource(w, r)
	case strings.HasPrefix(p, "/.well-known/oauth-protected-resource"):
		s.servePRM(w, strings.TrimPrefix(p, "/.well-known/oauth-protected-resource"))
	case strings.HasPrefix(p, "/.well-known/oauth-authorization-server"),
		strings.HasPrefix(p, "/.well-known/openid-configuration"),
		strings.HasSuffix(p, "/.well-known/openid-configuration"):
		s.serveASMeta(w)
	case p == AuthorizePath:
		s.serveAuthorize(w, r, form)
	case p == TokenPath:
		s.serveToken(w, r, form)
	case p == RegistrationPath:
		s.serveRegister(w, r, body)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) isResourcePath(p string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return p == ResourcePath || slices.Contains(s.cfg.ExtraResources, p)
}

// resourceForLocked is the resource identifier of the protected resource at path.
func (s *Server) resourceForLocked(path string) string {
	if path == ResourcePath && s.cfg.Resource != "" {
		return s.cfg.Resource
	}
	return s.URL + path
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func oauthErr(w http.ResponseWriter, status int, code, desc string) {
	writeJSON(w, status, map[string]string{"error": code, "error_description": desc})
}

func (s *Server) serveResource(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h := r.Header.Get("Authorization")
	tok, ok := strings.CutPrefix(h, "Bearer ")
	t, known := s.access[tok]
	if !ok || !known || !s.Now().Before(t.expires) || t.resource != s.resourceForLocked(r.URL.Path) {
		ch := `Bearer realm="mcp"`
		if s.cfg.ChallengeMetadata {
			ch += `, resource_metadata="` + s.URL + `/.well-known/oauth-protected-resource` + r.URL.Path + `"`
		}
		w.Header().Set("WWW-Authenticate", ch)
		oauthErr(w, http.StatusUnauthorized, "invalid_token", "missing, unknown or expired token")
		return
	}
	if sc := s.cfg.MissingScope; sc != "" {
		w.Header().Set("WWW-Authenticate", `Bearer error="insufficient_scope", scope="`+sc+`"`)
		oauthErr(w, http.StatusForbidden, "insufficient_scope", "scope required: "+sc)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) servePRM(w http.ResponseWriter, path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if path == "" || (path != ResourcePath && !slices.Contains(s.cfg.ExtraResources, path)) {
		path = ResourcePath
	}
	res := s.resourceForLocked(path)
	iss := s.cfg.PRMIssuer
	if iss == "" {
		iss = s.issuerLocked()
	}
	doc := map[string]any{"resource": res, "authorization_servers": []string{iss}}
	if s.cfg.Scope != "" {
		doc["scopes_supported"] = strings.Fields(s.cfg.Scope)
	}
	writeJSON(w, http.StatusOK, doc)
}

func baseOr(base, def string) string {
	if base != "" {
		return strings.TrimRight(base, "/")
	}
	return def
}

func (s *Server) serveASMeta(w http.ResponseWriter) {
	s.mu.Lock()
	defer s.mu.Unlock()
	methods := []string{"plain"}
	if !s.cfg.NoS256 {
		methods = []string{"S256", "plain"}
	}
	doc := map[string]any{
		"issuer":                                         s.issuerLocked(),
		"authorization_endpoint":                         baseOr(s.cfg.AuthorizationBase, s.URL) + AuthorizePath,
		"token_endpoint":                                 baseOr(s.cfg.TokenBase, s.URL) + TokenPath,
		"response_types_supported":                       []string{"code"},
		"grant_types_supported":                          []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":               methods,
		"token_endpoint_auth_methods_supported":          []string{"none", "client_secret_basic"},
		"authorization_response_iss_parameter_supported": s.cfg.PromiseISS,
	}
	if s.cfg.AcceptRegistration {
		doc["registration_endpoint"] = baseOr(s.cfg.RegistrationBase, s.URL) + RegistrationPath
	}
	if s.cfg.AcceptMetadataDocument {
		doc["client_id_metadata_document_supported"] = true
	}
	writeJSON(w, http.StatusOK, doc)
}

func (s *Server) newID(prefix string) string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return prefix + "-" + hex.EncodeToString(b)
}

func isLoopbackRedirect(u string) bool {
	p, err := url.Parse(u)
	if err != nil || p.Scheme != "http" {
		return false
	}
	h := p.Hostname()
	return h == "127.0.0.1" || h == "::1" || h == "localhost"
}

// clientKnown reports whether the client ID is accepted by an enabled route.
func (s *Server) clientKnownLocked(id string) bool {
	switch {
	case s.isRegisteredLocked(id):
		return s.cfg.AcceptRegistration
	case id == StaticClientID:
		return s.cfg.AcceptStatic
	case strings.HasPrefix(id, "https://"):
		return s.cfg.AcceptMetadataDocument
	}
	return false
}

func (s *Server) isRegisteredLocked(id string) bool {
	_, ok := s.clients[id]
	return ok
}

// clientAuthLocked checks the client on a token request: the identity from
// client_secret_basic or the form, the secret when the client has one, and
// that it is the client the grant was issued to.
func (s *Server) clientAuthLocked(r *http.Request, form url.Values, g *grant) bool {
	id, secret := form.Get("client_id"), form.Get("client_secret")
	if u, p, ok := r.BasicAuth(); ok {
		if ue, err := url.QueryUnescape(u); err == nil {
			u = ue
		}
		if pe, err := url.QueryUnescape(p); err == nil {
			p = pe
		}
		id, secret = u, p
	}
	if id != g.clientID {
		return false
	}
	want := s.clients[id]
	return want == "" || subtle.ConstantTimeCompare([]byte(secret), []byte(want)) == 1
}

func (s *Server) serveAuthorize(w http.ResponseWriter, r *http.Request, form url.Values) {
	s.mu.Lock()
	defer s.mu.Unlock()
	redirect := form.Get("redirect_uri")
	if !s.clientKnownLocked(form.Get("client_id")) {
		oauthErr(w, http.StatusBadRequest, "invalid_client", "unknown client")
		return
	}
	if !isLoopbackRedirect(redirect) {
		oauthErr(w, http.StatusBadRequest, "invalid_request", "redirect_uri must be loopback")
		return
	}
	u, _ := url.Parse(redirect)
	q := u.Query()
	state := form.Get("state")
	fail := func(code string) {
		q.Set("error", code)
		if state != "" {
			q.Set("state", state)
		}
		u.RawQuery = q.Encode()
		http.Redirect(w, r, u.String(), http.StatusFound)
	}
	if form.Get("response_type") != "code" {
		fail("unsupported_response_type")
		return
	}
	if form.Get("code_challenge") == "" || form.Get("code_challenge_method") != "S256" || s.cfg.NoS256 {
		fail("invalid_request")
		return
	}
	if form.Get("resource") == "" {
		fail("invalid_target")
		return
	}
	code := s.newID("code")
	s.codes[code] = &grant{
		clientID: form.Get("client_id"), redirect: redirect,
		challenge: form.Get("code_challenge"), resource: form.Get("resource"),
	}
	q.Set("code", code)
	if state != "" {
		q.Set("state", state)
	}
	if s.cfg.SendISS {
		iss := s.cfg.ISS
		if iss == "" {
			iss = s.issuerLocked()
		}
		q.Set("iss", iss)
	}
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

func (s *Server) issueLocked(w http.ResponseWriter, g *grant, oldRefresh string) {
	ttl := s.cfg.AccessTTL
	if ttl == 0 {
		ttl = time.Hour
	}
	at := s.newID("at")
	s.access[at] = token{expires: s.Now().Add(ttl), resource: g.resource}
	rt := oldRefresh
	if rt == "" || !s.cfg.NoRotate {
		if oldRefresh != "" {
			delete(s.refresh, oldRefresh)
			delete(s.rtExpiry, oldRefresh)
		}
		rt = s.newID("rt")
		s.refresh[rt] = g
		if s.cfg.RefreshTTL > 0 {
			s.rtExpiry[rt] = s.Now().Add(s.cfg.RefreshTTL)
		}
	}
	out := map[string]any{
		"access_token": at, "token_type": "Bearer",
		"expires_in": int(ttl.Seconds()), "refresh_token": rt,
	}
	if s.cfg.Scope != "" {
		out["scope"] = s.cfg.Scope
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) serveToken(w http.ResponseWriter, r *http.Request, form url.Values) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.Method != http.MethodPost {
		oauthErr(w, http.StatusMethodNotAllowed, "invalid_request", "POST only")
		return
	}
	switch form.Get("grant_type") {
	case "authorization_code":
		g, ok := s.codes[form.Get("code")]
		if !ok || g.used {
			oauthErr(w, http.StatusBadRequest, "invalid_grant", "unknown or used code")
			return
		}
		g.used = true
		sum := sha256.Sum256([]byte(form.Get("code_verifier")))
		want := base64.RawURLEncoding.EncodeToString(sum[:])
		if subtle.ConstantTimeCompare([]byte(want), []byte(g.challenge)) != 1 {
			oauthErr(w, http.StatusBadRequest, "invalid_grant", "PKCE verification failed")
			return
		}
		if form.Get("redirect_uri") != g.redirect || !s.clientAuthLocked(r, form, g) {
			oauthErr(w, http.StatusBadRequest, "invalid_grant", "redirect_uri or client mismatch")
			return
		}
		if form.Get("resource") != g.resource {
			oauthErr(w, http.StatusBadRequest, "invalid_target", "resource missing or different")
			return
		}
		s.issueLocked(w, g, "")
	case "refresh_token":
		rt := form.Get("refresh_token")
		g, ok := s.refresh[rt]
		if s.cfg.RejectRefresh || !ok {
			oauthErr(w, http.StatusBadRequest, "invalid_grant", "refresh rejected")
			return
		}
		if exp, has := s.rtExpiry[rt]; has && !s.Now().Before(exp) {
			oauthErr(w, http.StatusBadRequest, "invalid_grant", "refresh token expired")
			return
		}
		if !s.clientAuthLocked(r, form, g) {
			oauthErr(w, http.StatusBadRequest, "invalid_client", "client mismatch")
			return
		}
		if form.Get("resource") != g.resource {
			oauthErr(w, http.StatusBadRequest, "invalid_target", "resource missing or different")
			return
		}
		s.issueLocked(w, g, rt)
	default:
		oauthErr(w, http.StatusBadRequest, "unsupported_grant_type", "")
	}
}

func (s *Server) serveRegister(w http.ResponseWriter, r *http.Request, body []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.Method != http.MethodPost || !s.cfg.AcceptRegistration {
		oauthErr(w, http.StatusBadRequest, "invalid_request", "registration not accepted")
		return
	}
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		oauthErr(w, http.StatusBadRequest, "invalid_client_metadata", "not JSON")
		return
	}
	if req["application_type"] != "native" {
		oauthErr(w, http.StatusBadRequest, "invalid_client_metadata", `application_type must be "native"`)
		return
	}
	id := s.newID("client")
	secret := ""
	if s.cfg.RegistrationSecret {
		secret = s.newID("secret")
	}
	s.clients[id] = secret
	out := map[string]any{"client_id": id, "redirect_uris": req["redirect_uris"], "application_type": "native"}
	if s.cfg.RegistrationSecret {
		out["client_secret"] = secret
	}
	writeJSON(w, http.StatusCreated, out)
}
