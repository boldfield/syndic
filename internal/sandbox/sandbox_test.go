package sandbox

import (
	"strings"
	"testing"
)

func TestSupportedRule(t *testing.T) {
	all := []Capability{F, N1, N2, P, D}
	for _, drop := range []Capability{F, N1, P, D} {
		var caps []Capability
		for _, c := range all {
			if c != drop {
				caps = append(caps, c)
			}
		}
		for _, list := range []bool{false, true} {
			if Supported(NewCapSet(caps...), list) {
				t.Errorf("without %s, list=%v: supported", drop, list)
			}
		}
	}
	noN2 := NewCapSet(F, N1, P, D)
	if !Supported(noN2, false) {
		t.Error("N2 must not be needed without a network list")
	}
	if Supported(noN2, true) {
		t.Error("N2 must be needed with a network list")
	}
	if got := Missing(noN2, true); len(got) != 1 || got[0] != N2 {
		t.Errorf("Missing = %v", got)
	}
	if !Supported(NewCapSet(all...), true) {
		t.Error("full set unsupported")
	}
}

func TestEnvCombinations(t *testing.T) {
	caller := map[string]string{"TOKEN": "t", "OTHER": "o", "BASH_ENV": "x", "ENV": "y", "SECRET": "s", "PATH": "/evil", "HTTP_PROXY": "http://evil"}
	proxy := &ProxyListener{Network: "unix", Address: "/p", URL: "http://u:p@127.0.0.1:1"}
	for _, withProxy := range []bool{false, true} {
		for _, pass := range [][]string{nil, {"TOKEN"}, {"TOKEN", "MISSING", "BASH_ENV", "ENV", "PATH", "HTTP_PROXY"}} {
			var pl *ProxyListener
			want := map[string]string{"PATH": "/a:/b", "HOME": "/tmp/h", "LANG": "C.UTF-8", "TERM": "dumb"}
			if withProxy {
				pl = proxy
				for _, k := range ProxyVars {
					want[k] = proxy.URL
				}
			}
			if len(pass) > 0 {
				want["TOKEN"] = "t"
			}
			got := BuildEnv("/a:/b", "/tmp/h", pl, pass, caller)
			if len(got) != len(want) {
				t.Fatalf("proxy=%v pass=%v: got %v", withProxy, pass, got)
			}
			for _, kv := range got {
				k, v, _ := strings.Cut(kv, "=")
				if want[k] != v {
					t.Errorf("proxy=%v pass=%v: %s=%q", withProxy, pass, k, v)
				}
				if k == "BASH_ENV" || k == "ENV" {
					t.Errorf("%s present", k)
				}
			}
		}
	}
}
