package authtest

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func get(t *testing.T, u string, h map[string]string) (*http.Response, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest("GET", u, nil)
	for k, v := range h {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var m map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&m)
	return resp, m
}

func post(t *testing.T, u string, v url.Values) (int, map[string]any) {
	t.Helper()
	resp, err := http.PostForm(u, v)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var m map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&m)
	return resp.StatusCode, m
}

const verifier = "verifier-verifier-verifier-verifier-1234567"

func login(t *testing.T, s *Server, clientID string) map[string]any {
	t.Helper()
	sum := sha256.Sum256([]byte(verifier))
	q := url.Values{
		"response_type": {"code"}, "client_id": {clientID}, "state": {"st"},
		"redirect_uri":   {"http://127.0.0.1:9/cb"},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "code_challenge_method": {"S256"},
		"resource": {s.ResourceURL()},
	}
	loc, status, err := s.Approve(s.URL + AuthorizePath + "?" + q.Encode())
	if err != nil || status != http.StatusFound {
		t.Fatalf("approve: %v %d", err, status)
	}
	u, _ := url.Parse(loc)
	if u.Query().Get("state") != "st" || u.Query().Get("iss") != s.IssuerURL() {
		t.Fatalf("bad redirect %s", loc)
	}
	code, tok := post(t, s.URL+TokenPath, url.Values{
		"grant_type": {"authorization_code"}, "code": {u.Query().Get("code")},
		"code_verifier": {verifier}, "client_id": {clientID},
		"redirect_uri": {"http://127.0.0.1:9/cb"}, "resource": {s.ResourceURL()},
	})
	if code != 200 {
		t.Fatalf("token: %d %v", code, tok)
	}
	return tok
}

func TestFlowAndRotation(t *testing.T) {
	s := New(t)
	resp, _ := get(t, s.ResourceURL(), nil)
	if resp.StatusCode != 401 {
		t.Fatalf("want 401, got %d", resp.StatusCode)
	}
	_, as := get(t, s.URL+"/.well-known/oauth-authorization-server", nil)
	if as["issuer"] != s.URL || as["authorization_response_iss_parameter_supported"] != true {
		t.Fatalf("metadata %v", as)
	}
	tok := login(t, s, StaticClientID)
	at := tok["access_token"].(string)
	if resp, _ := get(t, s.ResourceURL(), map[string]string{"Authorization": "Bearer " + at}); resp.StatusCode != 200 {
		t.Fatalf("resource %d", resp.StatusCode)
	}
	code, tok2 := post(t, s.URL+TokenPath, url.Values{
		"grant_type": {"refresh_token"}, "refresh_token": {tok["refresh_token"].(string)}, "resource": {s.ResourceURL()},
	})
	if code != 200 || tok2["refresh_token"] == tok["refresh_token"] {
		t.Fatalf("no rotation: %d %v", code, tok2)
	}
	if code, _ := post(t, s.URL+TokenPath, url.Values{
		"grant_type": {"refresh_token"}, "refresh_token": {tok["refresh_token"].(string)}, "resource": {s.ResourceURL()},
	}); code != 400 {
		t.Fatalf("old refresh token still works: %d", code)
	}
	if n := s.Count(TokenPath); n != 3 {
		t.Fatalf("recorded %d token requests", n)
	}
	if got := s.RequestsTo(TokenPath)[0].Form.Get("resource"); got != s.ResourceURL() {
		t.Fatalf("resource not recorded: %q", got)
	}
}

func TestScripts(t *testing.T) {
	s := New(t, func(c *Config) {
		c.NoS256 = true
		c.PromiseISS, c.SendISS = false, false
		c.AcceptStatic, c.AcceptMetadataDocument = false, false
		c.AccessTTL = time.Minute
		c.IssuerPath = "/tenant"
		c.Resource = "https://elsewhere.example/mcp"
	})
	_, as := get(t, s.URL+"/.well-known/oauth-authorization-server/tenant", nil)
	if as["issuer"] != s.URL+"/tenant" || as["authorization_response_iss_parameter_supported"] != false {
		t.Fatalf("%v", as)
	}
	if m := as["code_challenge_methods_supported"].([]any); len(m) != 1 || m[0] == "S256" {
		t.Fatalf("S256 offered: %v", m)
	}
	if as["client_id_metadata_document_supported"] != nil {
		t.Fatal("cimd advertised")
	}
	_, prm := get(t, s.URL+"/.well-known/oauth-protected-resource/mcp", nil)
	if prm["resource"] != "https://elsewhere.example/mcp" {
		t.Fatalf("%v", prm)
	}
	// Registration demands native.
	resp, err := http.Post(s.URL+RegistrationPath, "application/json", strings.NewReader(`{"redirect_uris":["http://127.0.0.1/cb"]}`))
	if err != nil || resp.StatusCode != 400 {
		t.Fatalf("%v %v", err, resp)
	}
	resp, _ = http.Post(s.URL+RegistrationPath, "application/json", strings.NewReader(`{"application_type":"native"}`))
	if resp.StatusCode != 201 {
		t.Fatalf("register %d", resp.StatusCode)
	}
}

func TestOtherOriginAndFailures(t *testing.T) {
	s := New(t)
	other := s.Other(t)
	s.Set(func(c *Config) { c.TokenBase = other; c.MissingScope = "tools:write"; c.RejectRefresh = true })
	_, as := get(t, s.URL+"/.well-known/oauth-authorization-server", nil)
	if as["token_endpoint"] != other+TokenPath {
		t.Fatalf("%v", as)
	}
	tok := login2(t, s, other)
	resp, _ := get(t, s.ResourceURL(), map[string]string{"Authorization": "Bearer " + tok})
	if resp.StatusCode != 403 || !strings.Contains(resp.Header.Get("WWW-Authenticate"), "tools:write") {
		t.Fatalf("%d %v", resp.StatusCode, resp.Header)
	}
	s.Expire()
	if resp, _ := get(t, s.ResourceURL(), map[string]string{"Authorization": "Bearer " + tok}); resp.StatusCode != 401 {
		t.Fatalf("expired token accepted: %d", resp.StatusCode)
	}
	if reqs := s.RequestsTo(TokenPath); len(reqs) != 1 || reqs[0].Origin != "other" {
		t.Fatalf("token request origin: %+v", reqs)
	}
}

// login2 is login with the token request sent to the other origin.
func login2(t *testing.T, s *Server, tokenBase string) string {
	t.Helper()
	sum := sha256.Sum256([]byte(verifier))
	q := url.Values{
		"response_type": {"code"}, "client_id": {StaticClientID}, "state": {"st"},
		"redirect_uri":   {"http://127.0.0.1:9/cb"},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "code_challenge_method": {"S256"},
		"resource": {s.ResourceURL()},
	}
	loc, _, err := s.Approve(s.URL + AuthorizePath + "?" + q.Encode())
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(loc)
	_, tok := post(t, tokenBase+TokenPath, url.Values{
		"grant_type": {"authorization_code"}, "code": {u.Query().Get("code")},
		"code_verifier": {verifier}, "client_id": {StaticClientID},
		"redirect_uri": {"http://127.0.0.1:9/cb"}, "resource": {s.ResourceURL()},
	})
	return tok["access_token"].(string)
}
