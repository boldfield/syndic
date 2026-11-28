package mcp

import (
	"testing"

	"github.com/boldfield/syndic/internal/config"
)

func localServer() config.MCPServer {
	return config.MCPServer{
		Transport: config.MCPTransportStdio,
		Command:   "/bin/srv",
		Args:      []string{"--a"},
		Env:       map[string]config.SecretRef{"TOKEN": {Source: "env", Name: "SRV_TOKEN"}},
	}
}

func localInputs() IdentityInputs {
	return IdentityInputs{EnvGenerations: map[string]string{"TOKEN": "g1"}, PolicyHash: "p1"}
}

// withServer fills the table-derived inputs from a loaded server table.
func withServer(s config.MCPServer, in IdentityInputs) IdentityInputs {
	in.Table = s
	in.Local = s.Transport == config.MCPTransportStdio
	in.EnvNames = nil
	for n := range s.Env {
		in.EnvNames = append(in.EnvNames, n)
	}
	in.AuthName = ""
	if s.Auth != nil {
		in.AuthName = s.Auth.Name
	}
	return in
}

func mustID(t *testing.T, s config.MCPServer, in IdentityInputs) string {
	t.Helper()
	id, err := ServerIdentity(withServer(s, in))
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestServerIdentityChanges(t *testing.T) {
	base := mustID(t, localServer(), localInputs())
	if base != mustID(t, localServer(), localInputs()) {
		t.Fatal("not deterministic")
	}
	cases := map[string]func(*config.MCPServer, *IdentityInputs){
		"command": func(s *config.MCPServer, _ *IdentityInputs) { s.Command = "/bin/other" },
		"args":    func(s *config.MCPServer, _ *IdentityInputs) { s.Args = []string{"--b"} },
		"secret ref name": func(s *config.MCPServer, _ *IdentityInputs) {
			s.Env = map[string]config.SecretRef{"TOKEN": {Source: "env", Name: "OTHER"}}
		},
		"sandbox policy": func(_ *config.MCPServer, in *IdentityInputs) { in.PolicyHash = "p2" },
		"generation": func(_ *config.MCPServer, in *IdentityInputs) {
			in.EnvGenerations = map[string]string{"TOKEN": "g2"}
		},
	}
	for name, mod := range cases {
		s, in := localServer(), localInputs()
		mod(&s, &in)
		if mustID(t, s, in) == base {
			t.Errorf("%s did not change identity", name)
		}
	}
}

func remoteServer() config.MCPServer {
	return config.MCPServer{
		Transport: config.MCPTransportHTTP,
		URL:       "https://example.test/mcp",
		Auth:      &config.SecretRef{Source: "file", Name: "login"},
	}
}

func remoteInputs() IdentityInputs {
	return IdentityInputs{Login: &LoginIdentity{Issuer: "https://i", Resource: "https://example.test/mcp", ClientID: "c", Generation: "lg1"}}
}

func TestServerIdentityRemote(t *testing.T) {
	base := mustID(t, remoteServer(), remoteInputs())
	s := remoteServer()
	s.URL = "https://example.test/other"
	if mustID(t, s, remoteInputs()) == base {
		t.Error("url did not change identity")
	}
	in := remoteInputs()
	in.Login.Generation = "lg2"
	if mustID(t, remoteServer(), in) == base {
		t.Error("login generation did not change identity")
	}
	in = remoteInputs()
	in.Login.Issuer = "https://j"
	if mustID(t, remoteServer(), in) == base {
		t.Error("issuer did not change identity")
	}
}

// Token refresh and refresh-token rotation change the stored record, not the
// generation, and identity takes only the generation.
func TestServerIdentityStableAcrossRefresh(t *testing.T) {
	a := mustID(t, remoteServer(), remoteInputs())
	b := mustID(t, remoteServer(), remoteInputs())
	if a != b {
		t.Fatal("identity changed with the same generation")
	}
}

func TestServerIdentityMissingGeneration(t *testing.T) {
	if _, err := ServerIdentity(withServer(localServer(), IdentityInputs{PolicyHash: "p"})); err == nil {
		t.Error("missing env generation accepted")
	}
	if _, err := ServerIdentity(withServer(localServer(), IdentityInputs{EnvGenerations: map[string]string{"TOKEN": "g"}})); err == nil {
		t.Error("missing policy hash accepted")
	}
	if _, err := ServerIdentity(withServer(remoteServer(), IdentityInputs{})); err == nil {
		t.Error("missing auth generation accepted")
	}
}
