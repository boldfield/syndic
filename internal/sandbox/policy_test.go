package sandbox

import "testing"

func basePolicy() Policy {
	return Policy{
		Path:          "/usr/local/bin:/usr/bin:/bin",
		ReadOnlyPaths: []string{"/usr", "/lib"},
		DeviceNodes:   []string{"/dev/null", "/dev/zero", "/dev/random", "/dev/urandom"},
		EnvNames:      []string{"TERM", "PATH", "HOME", "LANG", "TOKEN"},
		Capabilities:  NewCapSet(P, F, D, N1),
		NetworkList:   []string{"*.example.com", "b.example.org"},
	}
}

func TestPolicyBytesAndHashPinned(t *testing.T) {
	const wantJSON = `{"capabilities":["D","F","N1","P"],"device_nodes":["/dev/null","/dev/zero","/dev/random","/dev/urandom"],"env_names":["HOME","LANG","PATH","TERM","TOKEN"],"network_list":["*.example.com","b.example.org"],"path":"/usr/local/bin:/usr/bin:/bin","read_only_paths":["/usr","/lib"]}`
	const wantHash = "ee3e3b9a579cc4756f3bc19e880a3206eaba0075c2b12b289c8169b853458f28"
	p := basePolicy()
	if got := string(p.CanonicalJSON()); got != wantJSON {
		t.Errorf("json:\n%s", got)
	}
	if got := p.Hash(); got != wantHash {
		t.Errorf("hash %s", got)
	}
}

func TestPolicyEncodingProperties(t *testing.T) {
	p := basePolicy()
	// Capability and env-name order do not matter; other list order does.
	q := basePolicy()
	q.Capabilities = NewCapSet(N1, D, F, P)
	q.EnvNames = []string{"TOKEN", "LANG", "HOME", "PATH", "TERM"}
	if p.Hash() != q.Hash() {
		t.Error("capability/env-name order changed the hash")
	}
	if got := string((Policy{}).CanonicalJSON()); got != `{"capabilities":[],"device_nodes":[],"env_names":[],"network_list":[],"path":"","read_only_paths":[]}` {
		t.Errorf("empty policy: %s", got)
	}
}

func TestPolicyHashChangesWithEveryField(t *testing.T) {
	base := basePolicy().Hash()
	muts := map[string]func(*Policy){
		"path":     func(p *Policy) { p.Path += ":/x" },
		"ro":       func(p *Policy) { p.ReadOnlyPaths = []string{"/usr"} },
		"ro order": func(p *Policy) { p.ReadOnlyPaths = []string{"/lib", "/usr"} },
		"dev":      func(p *Policy) { p.DeviceNodes = p.DeviceNodes[:3] },
		"env":      func(p *Policy) { p.EnvNames = append(append([]string{}, p.EnvNames...), "X") },
		"caps":     func(p *Policy) { p.Capabilities = NewCapSet(F, D, N1) },
		"net":      func(p *Policy) { p.NetworkList = nil },
		"net order": func(p *Policy) {
			p.NetworkList = []string{"b.example.org", "*.example.com"}
		},
	}
	for name, mut := range muts {
		p := basePolicy()
		mut(&p)
		if p.Hash() == base {
			t.Errorf("%s did not change the hash", name)
		}
	}
}

func TestPolicyNoHTMLEscaping(t *testing.T) {
	p := basePolicy()
	p.NetworkList = []string{"a&b<c>"}
	if got := string(p.CanonicalJSON()); !contains(got, `"a&b<c>"`) {
		t.Errorf("escaped: %s", got)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
