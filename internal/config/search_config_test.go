package config

import (
	"strings"
	"testing"
)

// [tools.web.search] loads with its provider, host and secret reference, and
// the example validates.
func TestLoadSearchConfig(t *testing.T) {
	roster, err := Load("testdata/tools-config.toml")
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	sc := roster.Tools.Web.Search
	if sc == nil {
		t.Fatal("Search config is nil")
	}
	if sc.Provider != "example-search" || sc.Host != "search.anthropic.com" {
		t.Errorf("Search = %+v, want provider example-search on search.anthropic.com", sc)
	}
	if sc.Auth == nil || sc.Auth.Source != "env" || sc.Auth.Name != "SEARCH_API_KEY" {
		t.Errorf("Search.Auth = %+v, want env:SEARCH_API_KEY", sc.Auth)
	}
	if err := roster.Validate(); err != nil {
		t.Errorf("Validate failed: %v", err)
	}
}

// A roster without the table has no search provider, and web_search is then
// not offered; nothing else about [tools.web] changes.
func TestSearchConfigAbsentByDefault(t *testing.T) {
	roster, err := Load("testdata/example.toml")
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if roster.Tools != nil && roster.Tools.Web != nil && roster.Tools.Web.Search != nil {
		t.Fatalf("example roster configures a search provider: %+v", roster.Tools.Web.Search)
	}
}

// The table needs a provider name, one plain hostname and a secret reference;
// each shape of a bad value is refused at load with an error naming the key.
func TestValidateSearchConfig(t *testing.T) {
	valid := func() *Roster {
		roster, err := Load("testdata/tools-config.toml")
		if err != nil {
			t.Fatalf("Load failed: %v", err)
		}
		return roster
	}
	cases := []struct {
		name string
		edit func(*SearchConfig)
		want string
	}{
		{"no provider", func(sc *SearchConfig) { sc.Provider = "" }, "tools.web.search.provider is required"},
		{"no host", func(sc *SearchConfig) { sc.Host = "" }, "tools.web.search.host: required"},
		{"wildcard host", func(sc *SearchConfig) { sc.Host = "*.search.test" }, "one exact hostname"},
		{"scheme in host", func(sc *SearchConfig) { sc.Host = "https://search.test" }, "without scheme"},
		{"userinfo in host", func(sc *SearchConfig) { sc.Host = "key@search.test" }, "without scheme or credentials"},
		{"port in host", func(sc *SearchConfig) { sc.Host = "search.test:8443" }, "must not include port"},
		{"path in host", func(sc *SearchConfig) { sc.Host = "search.test/api" }, "not a path"},
		{"ipv4 host", func(sc *SearchConfig) { sc.Host = "203.0.113.9" }, "IP literals are not allowed"},
		{"ipv6 host", func(sc *SearchConfig) { sc.Host = "[2001:db8::1]" }, "IPv6 literals are not allowed"},
		{"trailing dot", func(sc *SearchConfig) { sc.Host = "search.test." }, "trailing dot"},
		{"no auth", func(sc *SearchConfig) { sc.Auth = nil }, "tools.web.search.auth is required"},
		{"bad auth source", func(sc *SearchConfig) { sc.Auth = &SecretRef{Source: "vault", Name: "k"} }, "auth.source must be env or file"},
		{"no auth name", func(sc *SearchConfig) { sc.Auth = &SecretRef{Source: "file"} }, "tools.web.search.auth.name is required"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			roster := valid()
			c.edit(roster.Tools.Web.Search)
			err := roster.Validate()
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("Validate() = %v, want an error containing %q", err, c.want)
			}
		})
	}
	// A file secret is an acceptable reference, and a trailing dot is not a
	// canonical hostname.
	roster := valid()
	roster.Tools.Web.Search.Auth = &SecretRef{Source: "file", Name: "search-key"}
	roster.Tools.Web.Search.Host = "search.example.org"
	if err := roster.Validate(); err != nil {
		t.Fatalf("Validate() = %v for a file secret", err)
	}
}

// Whether the named provider exists is a property of the build, not of the
// roster: an unknown name validates and leaves web_search unoffered.
func TestSearchConfigProviderNameIsNotValidatedAgainstABuild(t *testing.T) {
	roster, err := Load("testdata/tools-config.toml")
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	roster.Tools.Web.Search.Provider = "some-vendor-nobody-built"
	if err := roster.Validate(); err != nil {
		t.Fatalf("Validate() = %v", err)
	}
}
