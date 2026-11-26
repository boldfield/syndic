package sandbox

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func fixedPolicy() Policy {
	return Policy{
		Path:          "/usr/local/bin:/usr/bin:/bin",
		ReadOnlyPaths: []string{"/usr", "/lib", "/etc/ssl"},
		Devices:       append([]string{}, DeviceNodes...),
		EnvNames:      []string{"TERM", "PATH", "GOPATH", "HOME", "LANG"},
		Capabilities:  Caps(P, N1, F, D),
		NetworkAllow:  []string{"proxy.golang.org", "*.example.com", "a<b>&é"},
	}
}

func TestPolicyCanonicalJSONPinned(t *testing.T) {
	const wantJSON = `{"capabilities":["D","F","N1","P"],"devices":["/dev/null","/dev/zero","/dev/random","/dev/urandom"],"env_names":["GOPATH","HOME","LANG","PATH","TERM"],"network_allow":["proxy.golang.org","*.example.com","a<b>&é"],"path":"/usr/local/bin:/usr/bin:/bin","read_only_paths":["/usr","/lib","/etc/ssl"]}`
	const wantHash = "82b893f9d70711c41d52da52a3075ee52d30a05ac4f9023324ec4ea1a181fcb8"
	b, err := fixedPolicy().CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != wantJSON {
		t.Errorf("canonical JSON\n got %s\nwant %s", b, wantJSON)
	}
	h, err := fixedPolicy().Hash()
	if err != nil {
		t.Fatal(err)
	}
	if h != wantHash {
		t.Errorf("hash %s, want %s", h, wantHash)
	}
	sum := sha256.Sum256(b)
	if h != hex.EncodeToString(sum[:]) {
		t.Errorf("hash is not the SHA-256 of the encoding")
	}
}

func TestPolicyEmptyListsEncodeAsArrays(t *testing.T) {
	b, err := Policy{}.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"capabilities":[],"devices":[],"env_names":[],"network_allow":[],"path":"","read_only_paths":[]}`
	if string(b) != want {
		t.Errorf("got %s", b)
	}
}

func TestPolicyEveryFieldChangesHash(t *testing.T) {
	base, err := fixedPolicy().Hash()
	if err != nil {
		t.Fatal(err)
	}
	mutations := map[string]func(*Policy){
		"path":           func(p *Policy) { p.Path = "/usr/bin:/bin" },
		"read_only":      func(p *Policy) { p.ReadOnlyPaths = append(p.ReadOnlyPaths, "/lib64") },
		"read_only_ord":  func(p *Policy) { p.ReadOnlyPaths[0], p.ReadOnlyPaths[1] = p.ReadOnlyPaths[1], p.ReadOnlyPaths[0] },
		"devices":        func(p *Policy) { p.Devices = p.Devices[:3] },
		"env_names":      func(p *Policy) { p.EnvNames = append(p.EnvNames, "GOCACHE") },
		"capabilities":   func(p *Policy) { p.Capabilities |= CapSet(N2) },
		"network_allow":  func(p *Policy) { p.NetworkAllow = nil },
		"network_allow2": func(p *Policy) { p.NetworkAllow[0] = "proxy.golang.org." },
	}
	for name, mutate := range mutations {
		p := fixedPolicy()
		mutate(&p)
		h, err := p.Hash()
		if err != nil {
			t.Fatal(err)
		}
		if h == base {
			t.Errorf("%s: hash unchanged", name)
		}
	}

	// The capability set and the environment names are sets: their order
	// does not matter.
	p := fixedPolicy()
	p.EnvNames = []string{"LANG", "HOME", "GOPATH", "PATH", "TERM"}
	if h, _ := p.Hash(); h != base {
		t.Errorf("reordering env names changed the hash")
	}
}
