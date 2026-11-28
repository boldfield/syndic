package auth

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/boldfield/syndic/internal/mcp/authtest"
	"github.com/boldfield/syndic/internal/secretstore"
)

func only(meta, reg, static bool) func(*authtest.Config) {
	return func(c *authtest.Config) {
		c.AcceptMetadataDocument, c.AcceptRegistration, c.AcceptStatic = meta, reg, static
	}
}

// loginClient logs in to s and returns the client_id the authorization
// request named, and the secret store.
func loginClient(t *testing.T, s *authtest.Server, store *secretstore.Store, configured string) (string, error) {
	t.Helper()
	d, err := NewDiscoverer(Config{URL: s.ResourceURL(), AuthOrigins: []string{s.URL}})
	if err != nil {
		t.Fatal(err)
	}
	var id string
	_, err = d.Login(context.Background(), LoginOptions{
		ClientID: configured, Store: store,
		Announce: func(u string) {
			pu, _ := url.Parse(u)
			id = pu.Query().Get("client_id")
			hit(t, s, u, nil)
		},
	})
	return id, err
}

func TestClientIdentityRoutes(t *testing.T) {
	cases := []struct {
		name       string
		accept     func(*authtest.Config)
		wantID     func(string) bool
		registered bool
	}{
		{"metadata document", only(true, false, false), func(id string) bool { return id == ClientMetadataURL }, false},
		{"registration", only(false, true, false), func(id string) bool { return strings.HasPrefix(id, "client-") }, true},
		{"configured client_id", only(false, false, true), func(id string) bool { return id == authtest.StaticClientID }, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := authtest.New(t, c.accept)
			store, err := secretstore.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			id, err := loginClient(t, s, store, authtest.StaticClientID)
			if err != nil {
				t.Fatalf("login: %v", err)
			}
			if !c.wantID(id) {
				t.Errorf("client_id = %q", id)
			}
			if got := s.Count(authtest.RegistrationPath) == 1; got != c.registered {
				t.Errorf("registration requests = %d", s.Count(authtest.RegistrationPath))
			}
			if _, _, err := store.Get(LoginKey(s.IssuerURL(), s.ResourceURL(), id)); err != nil {
				t.Errorf("tokens not stored under the client: %v", err)
			}
		})
	}
}

func TestClientIdentityNoRoute(t *testing.T) {
	s := authtest.New(t, only(false, false, true))
	store, _ := secretstore.Open(t.TempDir())
	if _, err := loginClient(t, s, store, ""); err == nil {
		t.Error("login without any route succeeded")
	}
}

func TestRegistrationReusedWithSecret(t *testing.T) {
	s := authtest.New(t, only(false, true, false), func(c *authtest.Config) { c.RegistrationSecret = true })
	store, _ := secretstore.Open(t.TempDir())
	a, err := loginClient(t, s, store, "")
	if err != nil {
		t.Fatal(err)
	}
	b, err := loginClient(t, s, store, "")
	if err != nil {
		t.Fatal(err)
	}
	if a != b || s.Count(authtest.RegistrationPath) != 1 {
		t.Errorf("registration not reused: %q %q, %d requests", a, b, s.Count(authtest.RegistrationPath))
	}
	raw, _, err := store.Get(RegistrationKey(s.IssuerURL()))
	if err != nil {
		t.Fatal(err)
	}
	var reg registration
	if err := json.Unmarshal(raw, &reg); err != nil || reg.ClientSecret == "" {
		t.Errorf("secret not stored: %v", err)
	}
}

func TestRegistrationNotUsedWithSecondIssuer(t *testing.T) {
	store, _ := secretstore.Open(t.TempDir())
	opts := only(false, true, false)
	one := authtest.New(t, opts)
	two := authtest.New(t, opts)
	a, err := loginClient(t, one, store, "")
	if err != nil {
		t.Fatal(err)
	}
	b, err := loginClient(t, two, store, "")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Error("the first issuer's registration was used with the second")
	}
	if two.Count(authtest.RegistrationPath) != 1 {
		t.Errorf("second issuer saw %d registrations", two.Count(authtest.RegistrationPath))
	}
	for _, r := range two.Requests() {
		if r.Form.Get("client_id") == a {
			t.Error("second issuer was sent the first issuer's client_id")
		}
	}
}

func TestClientMetadataFile(t *testing.T) {
	raw, err := os.ReadFile("../../../docs/oauth/client-metadata.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		ClientID     string   `json:"client_id"`
		ClientName   string   `json:"client_name"`
		RedirectURIs []string `json:"redirect_uris"`
		GrantTypes   []string `json:"grant_types"`
		ResponseType []string `json:"response_types"`
		AuthMethod   string   `json:"token_endpoint_auth_method"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.ClientID != ClientMetadataURL {
		t.Errorf("client_id = %q", doc.ClientID)
	}
	if doc.ClientName == "" || len(doc.GrantTypes) == 0 || len(doc.ResponseType) == 0 || doc.AuthMethod != "none" {
		t.Errorf("incomplete document: %+v", doc)
	}
	if len(doc.RedirectURIs) == 0 {
		t.Error("no redirect_uris")
	}
	for _, u := range doc.RedirectURIs {
		pu, err := url.Parse(u)
		if err != nil || pu.Scheme != "http" || (pu.Hostname() != "127.0.0.1" && pu.Hostname() != "::1") {
			t.Errorf("redirect %q is not loopback", u)
		}
	}
}
