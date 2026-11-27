package sandbox

import (
	"reflect"
	"strings"
	"testing"
)

func serverCfg(state, tmp, ws string) ServerConfig {
	return ServerConfig{
		System: SystemLocations{
			Path:          []string{"/usr/bin", "/bin"},
			ReadOnlyPaths: []string{"/usr", "/lib"},
			Devices:       append([]string{}, DeviceNodes...),
		},
		StateDir:     state,
		TempDir:      tmp,
		WorkspaceDir: ws,
		EnvNames:     []string{"API_KEY"},
	}
}

func mustServer(t *testing.T, c ServerConfig) ServerPolicy {
	t.Helper()
	p, err := NewServerPolicy(c)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func serverHash(t *testing.T, c ServerConfig) string {
	t.Helper()
	h, err := mustServer(t, c).Hash()
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestServerPolicyLocationsAndCapabilities(t *testing.T) {
	c := serverCfg("/s", "/t", "")
	p := mustServer(t, c)
	if !reflect.DeepEqual(p.ReadWrite, []string{"/s", "/t"}) || len(p.ReadOnly) != 0 {
		t.Errorf("rw %v ro %v", p.ReadWrite, p.ReadOnly)
	}
	if got := p.Capabilities.String(); got != "D,F,N1,P" {
		t.Errorf("caps %s", got)
	}
	c.WorkspaceDir = "/w"
	c.NetworkAllow = []string{"example.com"}
	p = mustServer(t, c)
	if !reflect.DeepEqual(p.ReadOnly, []string{"/w"}) || !reflect.DeepEqual(p.ReadWrite, []string{"/s", "/t"}) {
		t.Errorf("rw %v ro %v", p.ReadWrite, p.ReadOnly)
	}
	if got := p.Capabilities.String(); got != "D,F,N2,P" {
		t.Errorf("caps %s", got)
	}
	if !reflect.DeepEqual(p.System, c.System) {
		t.Errorf("system %v", p.System)
	}
}

func TestServerPolicyEnv(t *testing.T) {
	p := mustServer(t, serverCfg("/s", "/t", ""))
	got := p.Env("/usr/bin:/bin", "", map[string]string{"API_KEY": "k", "OTHER": "x"})
	want := []string{"API_KEY=k", "HOME=/s", "LANG=C.UTF-8", "PATH=/usr/bin:/bin", "TERM=dumb", "TMPDIR=/t"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("env %v, want %v", got, want)
	}
}

func TestServerPolicyEnvNames(t *testing.T) {
	c := serverCfg("/s", "/t", "")
	got := mustServer(t, c).EnvNames
	want := []string{"API_KEY", "HOME", "LANG", "PATH", "TERM", "TMPDIR"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("env names %v, want %v", got, want)
	}
	c.NetworkAllow = []string{"example.com"}
	got = mustServer(t, c).EnvNames
	want = []string{"API_KEY", "HOME", "HTTPS_PROXY", "HTTP_PROXY", "LANG", "PATH", "TERM", "TMPDIR", "http_proxy", "https_proxy"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("env names %v, want %v", got, want)
	}
}

func TestServerPolicyRefusesReservedName(t *testing.T) {
	for _, n := range []string{"HOME", "PATH", "HTTP_PROXY", "BASH_ENV", "TMPDIR"} {
		c := serverCfg("/s", "/t", "")
		c.EnvNames = []string{"OK", n}
		if _, err := NewServerPolicy(c); err == nil || !strings.Contains(err.Error(), n) {
			t.Errorf("%s: err %v", n, err)
		}
	}
}

func TestServerPolicyHash(t *testing.T) {
	base := serverHash(t, serverCfg("/s1", "/t1", "/w1"))
	if h := serverHash(t, serverCfg("/s2", "/t2", "/w2")); h != base {
		t.Errorf("hash differs across instances")
	}
	mods := map[string]func(*ServerConfig){
		"path":      func(c *ServerConfig) { c.System.Path = []string{"/bin"} },
		"read-only": func(c *ServerConfig) { c.System.ReadOnlyPaths = []string{"/usr"} },
		"devices":   func(c *ServerConfig) { c.System.Devices = []string{"/dev/null"} },
		"workspace": func(c *ServerConfig) { c.WorkspaceDir = "" },
		"network":   func(c *ServerConfig) { c.NetworkAllow = []string{"example.com"} },
		"env":       func(c *ServerConfig) { c.EnvNames = []string{"API_KEY", "MORE"} },
	}
	for name, mod := range mods {
		c := serverCfg("/s1", "/t1", "/w1")
		mod(&c)
		if serverHash(t, c) == base {
			t.Errorf("%s does not change the hash", name)
		}
	}
	p := mustServer(t, serverCfg("/s1", "/t1", "/w1"))
	b, _ := p.CanonicalJSON()
	if strings.Contains(string(b), "/s1") || strings.Contains(string(b), "/w1") || strings.Contains(string(b), "/t1") {
		t.Errorf("instance path in %s", b)
	}
	if !strings.Contains(string(b), `"roles":["state","tmp","workspace"]`) {
		t.Errorf("roles missing in %s", b)
	}
}

func TestFakeRecordsServerPipes(t *testing.T) {
	f := NewFake(Caps(F, P, D, N1), FakeScript{})
	if _, err := f.Start(t.Context(), Spec{ServerPipes: true}); err != nil {
		t.Fatal(err)
	}
	if s := f.Starts(); len(s) != 1 || !s[0].ServerPipes {
		t.Errorf("starts %+v", s)
	}
}
