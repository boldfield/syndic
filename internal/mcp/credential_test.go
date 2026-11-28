package mcp

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/boldfield/syndic/internal/transport"
)

func TestCredentialSentToConfiguredServerOnly(t *testing.T) {
	c, err := ParseStaticCredential("https://mcp.example.com/rpc", "tok-secret-1")
	if err != nil {
		t.Fatal(err)
	}
	h := c.HeaderFor("https://MCP.example.com:443/other")
	if h["Authorization"][0] != "Bearer tok-secret-1" {
		t.Fatalf("configured origin got %v", h)
	}
	for _, other := range []string{
		"https://evil.example.com/rpc", "http://mcp.example.com/rpc",
		"https://mcp.example.com:8443/rpc", "https://auth.example.com/", "::bad", "https://u:p@mcp.example.com/",
	} {
		if h := c.HeaderFor(other); h != nil {
			t.Errorf("%s got credential %v", other, h)
		}
	}
	h["Authorization"][0] = "y"
	if c.HeaderFor("https://mcp.example.com/")["Authorization"][0] == "y" {
		t.Error("returned header aliases the credential")
	}
}

func TestCredentialHeaderSet(t *testing.T) {
	c, err := ParseStaticCredential("https://m.example.com", `{"X-Api-Key":"k-123","X-Team":"t-9"}`)
	if err != nil {
		t.Fatal(err)
	}
	h := c.HeaderFor("https://m.example.com/")
	if h["X-Api-Key"][0] != "k-123" || h["X-Team"][0] != "t-9" {
		t.Fatalf("header set %v", h)
	}
	for _, bad := range []string{"", "{}", `{"a":1}`, "tok\nx", `{"A B":"v"}`} {
		if _, err := ParseStaticCredential("https://m.example.com", bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if _, err := ParseStaticCredential("ftp://m", "t"); err == nil {
		t.Error("bad url accepted")
	}
}

func TestScrubListCoversCredentialsAndEnv(t *testing.T) {
	c, _ := ParseStaticCredential("https://m.example.com", "tok\"en-777")
	l := NewScrubList()
	l.AddCredential(c)
	l.AddEnv(map[string]string{"DB_PASS": "hunter2-env"})
	l.Add("issued-token-abc", "")

	for name, in := range map[string]string{
		"result":  `{"content":"your token is tok"en-777"}`,
		"jsonesc": `{"content":"tok\"en-777"}`,
		"error":   "401 for Bearer tok\"en-777",
		"stderr":  "connecting with hunter2-env\n",
		"issued":  "issued-token-abc",
	} {
		out, n := l.Scrub(in)
		if n == 0 {
			t.Errorf("%s: nothing scrubbed from %q", name, in)
		}
		for _, v := range []string{"en-777", "hunter2-env", "issued-token-abc"} {
			if strings.Contains(out, v) {
				t.Errorf("%s: %q survives in %q", name, v, out)
			}
		}
		if !strings.Contains(out, transport.ScrubMarker) {
			t.Errorf("%s: no marker in %q", name, out)
		}
	}
	if out, n := l.Scrub("nothing here"); n != 0 || out != "nothing here" {
		t.Error("clean text changed")
	}
	b, n := l.ScrubBytes([]byte("x hunter2-env y"))
	if n != 1 || strings.Contains(string(b), "hunter2") {
		t.Errorf("bytes: %q", b)
	}
}

func TestHeaderSetBareTokenScrubbed(t *testing.T) {
	c, err := ParseStaticCredential("https://m.example.com/mcp", `{"Authorization":"Bearer xyz-secret-42"}`)
	if err != nil {
		t.Fatal(err)
	}
	l := NewScrubList()
	l.AddCredential(c)
	for _, in := range []string{
		`{"content":"saw xyz-secret-42"}`,
		`invalid token xyz-secret-42`,
		"stderr: xyz-secret-42\n",
	} {
		if out, n := l.Scrub(in); n == 0 || strings.Contains(out, "xyz-secret-42") {
			t.Fatalf("not scrubbed: %q -> %q", in, out)
		}
	}
}

// A credential formatted into a log line or encoded into a journal row or
// event carries no part of its value.
func TestCredentialValueNotFormatted(t *testing.T) {
	c, err := ParseStaticCredential("https://m.example.com/mcp", `{"Authorization":"Bearer fmt-secret-5"}`)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(struct{ C *Credential }{c})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{fmt.Sprint(c), fmt.Sprintf("%v %+v %#v %s", c, c, c, c), string(b)} {
		if strings.Contains(s, "fmt-secret-5") {
			t.Fatalf("credential value in %q", s)
		}
	}
}
