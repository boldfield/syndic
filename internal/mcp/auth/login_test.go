package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/boldfield/syndic/internal/mcp/authtest"
	"github.com/boldfield/syndic/internal/secretstore"
)

// hit performs the authorization the browser would and requests the redirect
// from the client's listener, with edit applied to the redirect's query.
func hit(t *testing.T, s *authtest.Server, authURL string, edit func(url.Values)) {
	t.Helper()
	loc, status, err := s.Approve(authURL)
	if err != nil || loc == "" {
		t.Errorf("approve: %v %d %q", err, status, loc)
		return
	}
	u, _ := url.Parse(loc)
	q := u.Query()
	if edit != nil {
		edit(q)
	}
	u.RawQuery = q.Encode()
	resp, err := http.Get(u.String())
	if err == nil {
		_ = resp.Body.Close()
	}
}

type loginRun struct {
	store *secretstore.Store
	gen   string
	err   error
	url   string
}

func runLogin(t *testing.T, s *authtest.Server, resourcePath string, edit func(url.Values), replay bool) *loginRun {
	t.Helper()
	store, err := secretstore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return runLoginWith(t, s, resourcePath, store, edit, replay)
}

func runLoginWith(t *testing.T, s *authtest.Server, resourcePath string, store *secretstore.Store, edit func(url.Values), replay bool) *loginRun {
	t.Helper()
	d, err := NewDiscoverer(Config{URL: s.URL + resourcePath, AuthOrigins: []string{s.URL}})
	if err != nil {
		t.Fatal(err)
	}
	r := &loginRun{store: store}
	r.gen, r.err = d.Login(context.Background(), LoginOptions{
		ClientID: authtest.StaticClientID,
		Store:    store,
		Announce: func(u string) {
			r.url = u
			hit(t, s, u, edit)
			if replay {
				hit(t, s, u, edit)
			}
		},
	})
	return r
}

func stored(t *testing.T, r *loginRun, s *authtest.Server, path string) (*Tokens, bool) {
	t.Helper()
	iss := s.IssuerURL()
	raw, _, err := r.store.Get(LoginKey(iss, s.URL+path, authtest.StaticClientID))
	if errors.Is(err, secretstore.ErrNotFound) {
		return nil, false
	}
	if err != nil {
		t.Fatal(err)
	}
	var tk Tokens
	if err := json.Unmarshal(raw, &tk); err != nil {
		t.Fatal(err)
	}
	return &tk, true
}

func TestLoginCompletesWithPKCEAndResource(t *testing.T) {
	s := authtest.New(t)
	r := runLogin(t, s, authtest.ResourcePath, nil, false)
	if r.err != nil {
		t.Fatal(r.err)
	}
	if r.gen == "" {
		t.Error("no generation")
	}
	u, _ := url.Parse(r.url)
	q := u.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || q.Get("resource") != s.ResourceURL() || q.Get("state") == "" {
		t.Errorf("bad authorization request %v", q)
	}
	toks := s.RequestsTo(authtest.TokenPath)
	if len(toks) != 1 || toks[0].Form.Get("resource") != s.ResourceURL() || toks[0].Form.Get("code_verifier") == "" {
		t.Errorf("bad token request %+v", toks)
	}
	tk, ok := stored(t, r, s, authtest.ResourcePath)
	if !ok || tk.AccessToken == "" || tk.ClientID != authtest.StaticClientID {
		t.Fatalf("tokens not stored: %+v", tk)
	}
	if strings.Contains(r.url, tk.AccessToken) {
		t.Error("authorization URL carries a token")
	}
}

func TestLoginRefusedStoresNothing(t *testing.T) {
	cases := []struct {
		name string
		cfg  func(*authtest.Config)
		edit func(url.Values)
		play bool
	}{
		{"other resource", func(c *authtest.Config) { c.Resource = "http://127.0.0.1:1/other" }, nil, false},
		{"other issuer", func(c *authtest.Config) { c.PRMIssuer = "http://127.0.0.1:1" }, nil, false},
		{"no S256", func(c *authtest.Config) { c.NoS256 = true }, nil, false},
		{"wrong iss", nil, func(q url.Values) { q.Set("iss", "http://evil.example") }, false},
		{"missing iss when promised", nil, func(q url.Values) { q.Del("iss") }, false},
		{"wrong state", nil, func(q url.Values) { q.Set("state", "nope") }, false},
		{"authorization error", nil, func(q url.Values) { q.Set("error", "access_denied") }, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := authtest.New(t, func(cfg *authtest.Config) {
				if c.cfg != nil {
					c.cfg(cfg)
				}
			})
			r := runLogin(t, s, authtest.ResourcePath, c.edit, c.play)
			if r.err == nil {
				t.Fatal("login succeeded")
			}
			if _, ok := stored(t, r, s, authtest.ResourcePath); ok {
				t.Error("tokens stored")
			}
			if s.Count(authtest.TokenPath) != 0 {
				t.Error("token endpoint was contacted")
			}
		})
	}
}

func TestLoginReplayedStateRefused(t *testing.T) {
	s := authtest.New(t)
	r := runLogin(t, s, authtest.ResourcePath, nil, true)
	if r.err != nil {
		t.Fatal(r.err)
	}
	// The listener took one request; the replay went nowhere.
	if n := s.Count(authtest.TokenPath); n != 1 {
		t.Errorf("token requests = %d, want 1", n)
	}
	// A second listener never exists for the same state: a new login has a new one.
	r2 := runLoginWith(t, s, authtest.ResourcePath, r.store, nil, false)
	if r2.err != nil {
		t.Fatal(r2.err)
	}
	u1, _ := url.Parse(r.url)
	u2, _ := url.Parse(r2.url)
	if u1.Query().Get("state") == u2.Query().Get("state") {
		t.Error("state reused across logins")
	}
	if r2.gen == r.gen {
		t.Error("generation not renewed")
	}
}

func TestLoginMissingISSAcceptedWhenNotPromised(t *testing.T) {
	s := authtest.New(t, func(c *authtest.Config) { c.PromiseISS = false; c.SendISS = false })
	r := runLogin(t, s, authtest.ResourcePath, nil, false)
	if r.err != nil {
		t.Fatal(r.err)
	}
	if _, ok := stored(t, r, s, authtest.ResourcePath); !ok {
		t.Error("tokens not stored")
	}
}

func TestLoginTwoResourcesStoredSeparately(t *testing.T) {
	s := authtest.New(t, func(c *authtest.Config) { c.ExtraResources = []string{"/mcp2"} })
	store, err := secretstore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := runLoginWith(t, s, authtest.ResourcePath, store, nil, false)
	b := runLoginWith(t, s, "/mcp2", store, nil, false)
	if a.err != nil || b.err != nil {
		t.Fatal(a.err, b.err)
	}
	ta, oka := stored(t, a, s, authtest.ResourcePath)
	tb, okb := stored(t, b, s, "/mcp2")
	if !oka || !okb || ta.AccessToken == tb.AccessToken || ta.Resource == tb.Resource {
		t.Fatalf("tokens shared or missing: %+v %+v", ta, tb)
	}
	if a.gen == b.gen {
		t.Error("generations shared")
	}
}

func TestLoginNeedsClientID(t *testing.T) {
	s := authtest.New(t)
	store, _ := secretstore.Open(t.TempDir())
	d, err := NewDiscoverer(Config{URL: s.ResourceURL(), AuthOrigins: []string{s.URL}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Login(context.Background(), LoginOptions{Store: store}); err == nil {
		t.Error("login without a client_id succeeded")
	}
}
