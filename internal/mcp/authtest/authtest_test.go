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
		"grant_type": {"refresh_token"}, "refresh_token": {tok["refresh_token"].(string)}, "client_id": {StaticClientID}, "resource": {s.ResourceURL()},
	})
	if code != 200 || tok2["refresh_token"] == tok["refresh_token"] {
		t.Fatalf("no rotation: %d %v", code, tok2)
	}
	if code, _ := post(t, s.URL+TokenPath, url.Values{
		"grant_type": {"refresh_token"}, "refresh_token": {tok["refresh_token"].(string)}, "client_id": {StaticClientID}, "resource": {s.ResourceURL()},
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

const cb = "http://127.0.0.1:9/cb"

// authorize runs the authorization request and returns the redirect target.
func authorize(t *testing.T, s *Server, clientID, resource string) (*url.URL, int) {
	t.Helper()
	sum := sha256.Sum256([]byte(verifier))
	q := url.Values{
		"response_type": {"code"}, "client_id": {clientID}, "state": {"st"}, "redirect_uri": {cb},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "code_challenge_method": {"S256"},
		"resource": {resource},
	}
	loc, status, err := s.Approve(s.URL + AuthorizePath + "?" + q.Encode())
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(loc)
	return u, status
}

// exchange logs in and returns the token endpoint status and body. basic, when
// non-empty, is sent as the Basic credentials "id:secret".
func exchange(t *testing.T, s *Server, clientID, resource string, extra url.Values, basic [2]string) (int, map[string]any) {
	t.Helper()
	u, st := authorize(t, s, clientID, resource)
	if st != http.StatusFound {
		t.Fatalf("authorize %d", st)
	}
	f := url.Values{
		"grant_type": {"authorization_code"}, "code": {u.Query().Get("code")}, "code_verifier": {verifier},
		"redirect_uri": {cb}, "resource": {resource},
	}
	if basic[0] == "" {
		f.Set("client_id", clientID)
	}
	for k, v := range extra {
		f[k] = v
	}
	return tokenPost(t, s, f, basic)
}

func tokenPost(t *testing.T, s *Server, f url.Values, basic [2]string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest("POST", s.URL+TokenPath, strings.NewReader(f.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if basic[0] != "" {
		req.SetBasicAuth(basic[0], basic[1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var m map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&m)
	return resp.StatusCode, m
}

func register(t *testing.T, s *Server) map[string]any {
	t.Helper()
	resp, err := http.Post(s.URL+RegistrationPath, "application/json", strings.NewReader(`{"application_type":"native","redirect_uris":["`+cb+`"]}`))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var m map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&m)
	if resp.StatusCode != 201 {
		t.Fatalf("register %d %v", resp.StatusCode, m)
	}
	return m
}

func bearer(t *testing.T, s *Server, tok, path string) int {
	t.Helper()
	resp, _ := get(t, s.URL+path, map[string]string{"Authorization": "Bearer " + tok})
	return resp.StatusCode
}

func TestTokenIsPerResource(t *testing.T) {
	s := New(t, func(c *Config) { c.ExtraResources = []string{"/other"} })
	_, a := exchange(t, s, StaticClientID, s.URL+"/mcp", nil, [2]string{})
	_, b := exchange(t, s, StaticClientID, s.URL+"/other", nil, [2]string{})
	ta, tb := a["access_token"].(string), b["access_token"].(string)
	if bearer(t, s, ta, "/mcp") != 200 || bearer(t, s, tb, "/other") != 200 {
		t.Fatal("token refused at its own resource")
	}
	if bearer(t, s, ta, "/other") != 401 || bearer(t, s, tb, "/mcp") != 401 {
		t.Fatal("token accepted at another resource")
	}
	_, prm := get(t, s.URL+"/.well-known/oauth-protected-resource/other", nil)
	if prm["resource"] != s.URL+"/other" {
		t.Fatalf("%v", prm)
	}
}

func TestClientSecretChecked(t *testing.T) {
	s := New(t, func(c *Config) { c.RegistrationSecret = true })
	reg := register(t, s)
	id, secret := reg["client_id"].(string), reg["client_secret"].(string)
	res := s.ResourceURL()
	if code, _ := exchange(t, s, id, res, url.Values{"client_id": {id}}, [2]string{}); code != 400 {
		t.Fatalf("no secret accepted: %d", code)
	}
	if code, _ := exchange(t, s, id, res, nil, [2]string{id, "wrong"}); code != 400 {
		t.Fatalf("wrong secret accepted: %d", code)
	}
	if code, _ := exchange(t, s, id, res, nil, [2]string{StaticClientID, secret}); code != 400 {
		t.Fatalf("other client accepted: %d", code)
	}
	if code, _ := exchange(t, s, id, res, url.Values{"client_id": {id}, "client_secret": {"nope"}}, [2]string{}); code != 400 {
		t.Fatalf("wrong post secret accepted: %d", code)
	}
	code, tok := exchange(t, s, id, res, url.Values{"client_id": {id}, "client_secret": {secret}}, [2]string{})
	if code != 200 {
		t.Fatalf("client_secret_post refused: %d", code)
	}
	if code, _ := exchange(t, s, id, res, nil, [2]string{id, secret}); code != 200 {
		t.Fatalf("client_secret_basic refused: %d", code)
	}
	// Refresh is bound to the client.
	rf := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {tok["refresh_token"].(string)}, "resource": {res}}
	if code, _ := tokenPost(t, s, rf, [2]string{id, "wrong"}); code != 400 {
		t.Fatalf("refresh with wrong secret: %d", code)
	}
	if code, _ := tokenPost(t, s, rf, [2]string{StaticClientID, ""}); code != 400 {
		t.Fatalf("refresh by another client: %d", code)
	}
	if code, _ := tokenPost(t, s, rf, [2]string{id, secret}); code != 200 {
		t.Fatalf("refresh refused: %d", code)
	}
	// A second server (issuer) does not know this registration.
	s2 := New(t)
	if _, st := authorize(t, s2, id, s2.ResourceURL()); st != 400 {
		t.Fatalf("registration used at a second issuer: %d", st)
	}
}

func TestIdentityRoutes(t *testing.T) {
	const doc = "https://client.example/metadata.json"
	s := New(t)
	reg := register(t, s)["client_id"].(string)
	ids := map[string]string{"static": StaticClientID, "registered": reg, "document": doc}
	for only := range ids {
		s.Set(func(c *Config) {
			c.AcceptStatic = only == "static"
			c.AcceptRegistration = only == "registered"
			c.AcceptMetadataDocument = only == "document"
		})
		for name, id := range ids {
			_, st := authorize(t, s, id, s.ResourceURL())
			if want := name == only; (st == http.StatusFound) != want {
				t.Fatalf("only %s: %s (%s) got %d", only, name, id, st)
			}
		}
	}
	// A full login with a dynamically registered client and a metadata-document one.
	s.Set(func(c *Config) { c.AcceptStatic, c.AcceptRegistration, c.AcceptMetadataDocument = true, true, true })
	for _, id := range []string{reg, doc} {
		if code, _ := exchange(t, s, id, s.ResourceURL(), url.Values{"client_id": {id}}, [2]string{}); code != 200 {
			t.Fatalf("login as %s: %d", id, code)
		}
	}
	_, as := get(t, s.URL+"/.well-known/oauth-authorization-server", nil)
	if as["registration_endpoint"] == nil || as["client_id_metadata_document_supported"] != true {
		t.Fatalf("%v", as)
	}
	s.Set(func(c *Config) { c.AcceptRegistration, c.AcceptMetadataDocument = false, false })
	_, as = get(t, s.URL+"/.well-known/oauth-authorization-server", nil)
	if as["registration_endpoint"] != nil || as["client_id_metadata_document_supported"] != nil {
		t.Fatalf("%v", as)
	}
}

func TestRefreshBehaviours(t *testing.T) {
	s := New(t)
	res := s.ResourceURL()
	refresh := func(rt string) (int, map[string]any) {
		return tokenPost(t, s, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {rt}, "client_id": {StaticClientID}, "resource": {res}}, [2]string{})
	}
	_, tok := exchange(t, s, StaticClientID, res, url.Values{"client_id": {StaticClientID}}, [2]string{})
	rt := tok["refresh_token"].(string)

	s.Set(func(c *Config) { c.RejectRefresh = true })
	if code, _ := refresh(rt); code != 400 {
		t.Fatalf("refresh not rejected: %d", code)
	}
	s.Set(func(c *Config) { c.RejectRefresh = false; c.NoRotate = true })
	code, t2 := refresh(rt)
	if code != 200 || t2["refresh_token"] != rt {
		t.Fatalf("NoRotate: %d %v", code, t2)
	}

	now := time.Now()
	s.Now = func() time.Time { return now }
	s.Set(func(c *Config) { c.NoRotate = false; c.RefreshTTL = time.Minute; c.AccessTTL = 30 * time.Second })
	_, t3 := exchange(t, s, StaticClientID, res, url.Values{"client_id": {StaticClientID}}, [2]string{})
	if t3["expires_in"] != float64(30) {
		t.Fatalf("expires_in %v", t3["expires_in"])
	}
	at := t3["access_token"].(string)
	if bearer(t, s, at, "/mcp") != 200 {
		t.Fatal("fresh token refused")
	}
	now = now.Add(45 * time.Second)
	if bearer(t, s, at, "/mcp") != 401 {
		t.Fatal("token past AccessTTL accepted")
	}
	now = now.Add(time.Minute)
	if code, _ := refresh(t3["refresh_token"].(string)); code != 400 {
		t.Fatalf("expired refresh token accepted: %d", code)
	}
}

func TestChallengeAndISSAndDiscovery(t *testing.T) {
	s := New(t, func(c *Config) { c.ChallengeMetadata = true; c.ISS = "https://bogus.example"; c.IssuerPath = "/t" })
	resp, _ := get(t, s.ResourceURL(), nil)
	if h := resp.Header.Get("WWW-Authenticate"); !strings.Contains(h, `resource_metadata="`+s.URL+`/.well-known/oauth-protected-resource/mcp"`) {
		t.Fatalf("challenge %q", h)
	}
	s.Set(func(c *Config) { c.ChallengeMetadata = false })
	resp, _ = get(t, s.ResourceURL(), nil)
	if strings.Contains(resp.Header.Get("WWW-Authenticate"), "resource_metadata") {
		t.Fatal("resource_metadata present")
	}
	u, _ := authorize(t, s, StaticClientID, s.ResourceURL())
	if u.Query().Get("iss") != "https://bogus.example" {
		t.Fatalf("iss %v", u)
	}
	// Missing locations answer 404 so a client must fall back.
	s.Set(func(c *Config) {
		c.Missing = []string{"/.well-known/oauth-protected-resource/mcp", "/.well-known/oauth-authorization-server/t"}
	})
	if r, _ := get(t, s.URL+"/.well-known/oauth-protected-resource/mcp", nil); r.StatusCode != 404 {
		t.Fatalf("%d", r.StatusCode)
	}
	if r, _ := get(t, s.URL+"/.well-known/oauth-protected-resource", nil); r.StatusCode != 200 {
		t.Fatalf("%d", r.StatusCode)
	}
	if r, _ := get(t, s.URL+"/.well-known/oauth-authorization-server/t", nil); r.StatusCode != 404 {
		t.Fatalf("%d", r.StatusCode)
	}
	if r, m := get(t, s.URL+"/.well-known/openid-configuration/t", nil); r.StatusCode != 200 || m["issuer"] != s.URL+"/t" {
		t.Fatalf("%d %v", r.StatusCode, m)
	}
}
