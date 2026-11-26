package sandbox

import (
	"reflect"
	"sort"
	"strings"
	"testing"
)

func envNames(env []string) []string {
	var names []string
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		names = append(names, name)
	}
	return names
}

func TestEnvCombinations(t *testing.T) {
	source := map[string]string{
		"GOPATH":      "/go",
		"GOFLAGS":     "",
		"SECRET":      "not passed",
		"BASH_ENV":    "/evil",
		"ENV":         "/evil",
		"PATH":        "/engine/path",
		"HOME":        "/engine/home",
		"HTTP_PROXY":  "http://engine",
		"https_proxy": "http://engine",
	}
	const proxy = "http://syndic:cred@127.0.0.1:9"
	cases := []struct {
		name        string
		proxyURL    string
		passthrough []string
		want        []string
	}{
		{"bare", "", nil, []string{"HOME=/tmp/call", "LANG=C.UTF-8", "PATH=/usr/bin:/bin", "TERM=dumb"}},
		{"proxy", proxy, nil, []string{
			"HOME=/tmp/call", "HTTPS_PROXY=" + proxy, "HTTP_PROXY=" + proxy, "LANG=C.UTF-8",
			"PATH=/usr/bin:/bin", "TERM=dumb", "http_proxy=" + proxy, "https_proxy=" + proxy,
		}},
		{"passthrough", "", []string{"GOPATH", "GOFLAGS", "MISSING"}, []string{
			"GOFLAGS=", "GOPATH=/go", "HOME=/tmp/call", "LANG=C.UTF-8", "PATH=/usr/bin:/bin", "TERM=dumb",
		}},
		{"proxy and passthrough", proxy, []string{"GOPATH"}, []string{
			"GOPATH=/go", "HOME=/tmp/call", "HTTPS_PROXY=" + proxy, "HTTP_PROXY=" + proxy, "LANG=C.UTF-8",
			"PATH=/usr/bin:/bin", "TERM=dumb", "http_proxy=" + proxy, "https_proxy=" + proxy,
		}},
		{"reserved names dropped", "", []string{"BASH_ENV", "ENV", "PATH", "HOME", "LANG", "TERM", "HTTP_PROXY", "https_proxy", "GOPATH"}, []string{
			"GOPATH=/go", "HOME=/tmp/call", "LANG=C.UTF-8", "PATH=/usr/bin:/bin", "TERM=dumb",
		}},
		{"reserved names dropped under proxy", proxy, []string{"HTTP_PROXY", "https_proxy", "BASH_ENV"}, []string{
			"HOME=/tmp/call", "HTTPS_PROXY=" + proxy, "HTTP_PROXY=" + proxy, "LANG=C.UTF-8",
			"PATH=/usr/bin:/bin", "TERM=dumb", "http_proxy=" + proxy, "https_proxy=" + proxy,
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Env(EnvConfig{
				Path:        "/usr/bin:/bin",
				TempDir:     "/tmp/call",
				ProxyURL:    c.proxyURL,
				Passthrough: c.passthrough,
				Source:      source,
			})
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("got  %q\nwant %q", got, c.want)
			}
			names := envNames(got)
			if !sort.StringsAreSorted(names) {
				t.Errorf("not sorted: %q", names)
			}
			for _, n := range names {
				if n == "BASH_ENV" || n == "ENV" {
					t.Errorf("%s present", n)
				}
			}
		})
	}
}

func TestEnvIgnoresProcessEnvironment(t *testing.T) {
	t.Setenv("SYNDIC_ENV_TEST_LEAK", "leak")
	t.Setenv("BASH_ENV", "/evil")
	for _, kv := range Env(EnvConfig{Path: "/bin", TempDir: "/t", Passthrough: []string{"SYNDIC_ENV_TEST_LEAK"}}) {
		if strings.Contains(kv, "leak") || strings.HasPrefix(kv, "BASH_ENV=") {
			t.Errorf("process environment leaked: %q", kv)
		}
	}
}

func TestReservedName(t *testing.T) {
	for _, n := range []string{"PATH", "HOME", "LANG", "TERM", "HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy", "BASH_ENV", "ENV"} {
		if !ReservedName(n) {
			t.Errorf("%s not reserved", n)
		}
	}
	for _, n := range []string{"GOPATH", "Path", "env", "NO_PROXY"} {
		if ReservedName(n) {
			t.Errorf("%s reserved", n)
		}
	}
}
