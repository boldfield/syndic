package auth

// Steps 4 to 6 of login (v9 delta, section 7): the authorization-code flow
// with PKCE S256 and the resource parameter, and the storage of the tokens.
// No token, code, secret or authorization header is written to any error or
// to the operator's terminal; the only value shown is the authorization URL,
// which carries none of them.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/boldfield/syndic/internal/secretstore"
)

// LoginKind is the first part of the secret store key of a login's tokens.
const LoginKind = "mcp-login"

// maxTokenBytes bounds a token response.
const maxTokenBytes = 1 << 20

// LoginOptions is what Login needs beyond discovery.
type LoginOptions struct {
	// ClientID is mcp.<server>.client_id, the client the operator registered.
	ClientID string
	// Scopes is mcp.<server>.scopes. When empty, the scopes the
	// protected-resource metadata names are asked for.
	Scopes []string
	// Store receives the tokens.
	Store *secretstore.Store
	// Announce is called with the authorization URL once the loopback
	// listener is open. The operator opens the URL.
	Announce func(authURL string)
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

// Tokens is the record stored for a login.
type Tokens struct {
	Issuer       string    `json:"issuer"`
	Resource     string    `json:"resource"`
	ClientID     string    `json:"client_id"`
	AccessToken  string    `json:"access_token"`
	TokenType    string    `json:"token_type"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	Scope        string    `json:"scope,omitempty"`
	Expiry       time.Time `json:"expiry,omitzero"`
}

// LoginKey is the secret store key of the tokens of one issuer, one resource
// and one client together.
func LoginKey(issuer, resource, clientID string) []string {
	return []string{LoginKind, issuer, resource, clientID}
}

// Login runs steps 1 to 6 for the server and returns the new generation of
// the stored login. Nothing is stored unless every check has passed.
func (d *Discoverer) Login(ctx context.Context, opts LoginOptions) (string, error) {
	if opts.ClientID == "" {
		return "", errors.New("login: no client identity: set mcp.<server>.client_id")
	}
	if opts.Store == nil {
		return "", errors.New("login: no secret store")
	}
	disc, err := d.Discover(ctx)
	if err != nil {
		return "", err
	}
	verifier, err := randomString(32)
	if err != nil {
		return "", err
	}
	state, err := randomString(32)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("login: listen on loopback: %w", err)
	}
	redirect := "http://" + ln.Addr().String() + "/callback"

	scopes := opts.Scopes
	if len(scopes) == 0 {
		scopes = disc.Scopes
	}
	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", opts.ClientID)
	q.Set("redirect_uri", redirect)
	q.Set("state", state)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("resource", disc.Resource)
	if len(scopes) > 0 {
		q.Set("scope", strings.Join(scopes, " "))
	}
	authURL, err := withQuery(disc.Metadata.AuthorizationEndpoint, q)
	if err != nil {
		_ = ln.Close()
		return "", err
	}

	cb := newCallback(ln, state)
	if opts.Announce != nil {
		opts.Announce(authURL)
	}
	params, err := cb.wait(ctx)
	if err != nil {
		return "", err
	}
	code, err := checkResponse(params, disc)
	if err != nil {
		return "", err
	}

	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	tok, err := d.exchange(ctx, disc, opts.ClientID, code, verifier, redirect, now)
	if err != nil {
		return "", err
	}
	rec, err := json.Marshal(tok)
	if err != nil {
		return "", err
	}
	return opts.Store.Put(LoginKey(disc.Issuer, disc.Resource, opts.ClientID), rec, true)
}

// checkResponse applies step 5 to the authorization response (the state has
// been checked by the listener): iss, then the error and the code.
func checkResponse(p url.Values, disc *Discovery) (string, error) {
	if _, has := p["iss"]; has {
		if p.Get("iss") != disc.Issuer {
			return "", errors.New("login: authorization response names a different issuer")
		}
	} else if disc.Metadata.ISSSupported {
		return "", errors.New("login: authorization response carries no iss, which the server promised")
	}
	if e := p.Get("error"); e != "" {
		return "", fmt.Errorf("login: authorization refused: %s", oneLine(e))
	}
	code := p.Get("code")
	if code == "" {
		return "", errors.New("login: authorization response carries no code")
	}
	return code, nil
}

func (d *Discoverer) exchange(ctx context.Context, disc *Discovery, clientID, code, verifier, redirect string, now func() time.Time) (*Tokens, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", redirect)
	form.Set("client_id", clientID)
	form.Set("code_verifier", verifier)
	form.Set("resource", disc.Resource)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, disc.Metadata.TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	client, done := d.clientFor(req.URL)
	defer done()
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("login: token request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxTokenBytes+1))
	if err != nil {
		return nil, fmt.Errorf("login: token response: %w", err)
	}
	if len(body) > maxTokenBytes {
		return nil, errors.New("login: token response is too large")
	}
	var tr struct {
		AccessToken  string `json:"access_token"`
		TokenType    string `json:"token_type"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
		Scope        string `json:"scope"`
		Error        string `json:"error"`
	}
	if err := json.Unmarshal(body, &tr); err != nil {
		return nil, fmt.Errorf("login: token endpoint answered %d with no usable document", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK || tr.Error != "" {
		return nil, fmt.Errorf("login: token endpoint answered %d: %s", resp.StatusCode, oneLine(tr.Error))
	}
	if tr.AccessToken == "" || !strings.EqualFold(tr.TokenType, "bearer") {
		return nil, errors.New("login: token response carries no bearer access token")
	}
	t := &Tokens{
		Issuer: disc.Issuer, Resource: disc.Resource, ClientID: clientID,
		AccessToken: tr.AccessToken, TokenType: "Bearer", RefreshToken: tr.RefreshToken, Scope: tr.Scope,
	}
	if tr.ExpiresIn > 0 {
		t.Expiry = now().Add(time.Duration(tr.ExpiresIn) * time.Second)
	}
	return t, nil
}

// callback is the loopback listener of one login. It accepts one request,
// whatever it is, and then closes; the state it carries is compared once.
type callback struct {
	ln     net.Listener
	srv    *http.Server
	result chan result
	once   sync.Once
}

type result struct {
	params url.Values
	err    error
}

func newCallback(ln net.Listener, state string) *callback {
	c := &callback{ln: ln, result: make(chan result, 1)}
	c.srv = &http.Server{
		ReadHeaderTimeout: 10 * time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			first := false
			c.once.Do(func() { first = true })
			if !first {
				http.NotFound(w, r)
				return
			}
			// Close the listener at once: this is the one request.
			_ = ln.Close()
			p := r.URL.Query()
			var res result
			switch {
			case r.Method != http.MethodGet || r.URL.Path != "/callback":
				res.err = errors.New("login: the listener's one request was not the authorization response")
			case subtle.ConstantTimeCompare([]byte(p.Get("state")), []byte(state)) != 1:
				res.err = errors.New("login: authorization response carries the wrong state")
			default:
				res.params = p
			}
			if res.err != nil {
				http.Error(w, "login refused", http.StatusBadRequest)
			} else {
				_, _ = io.WriteString(w, "Login received. You may close this window.\n")
			}
			c.result <- res
		}),
	}
	go func() { _ = c.srv.Serve(ln) }()
	return c
}

// wait returns the one response, or the context's error. The listener is
// closed on return.
func (c *callback) wait(ctx context.Context) (url.Values, error) {
	defer func() {
		_ = c.ln.Close()
		sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = c.srv.Shutdown(sctx)
	}()
	select {
	case r := <-c.result:
		return r.params, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func randomString(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("login: random: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func withQuery(endpoint string, q url.Values) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", fmt.Errorf("login: authorization_endpoint: %w", err)
	}
	existing := u.Query()
	for k, v := range q {
		existing[k] = v
	}
	u.RawQuery = existing.Encode()
	return u.String(), nil
}

// oneLine keeps a server-supplied error word short and printable.
func oneLine(s string) string {
	if len(s) > 80 {
		s = s[:80]
	}
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return '?'
		}
		return r
	}, s)
}
