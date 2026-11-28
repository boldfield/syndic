package engine

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/boldfield/syndic/internal/config"
	"github.com/boldfield/syndic/internal/wire"
)

// A request without attachments hashes to the bytes it had before the member
// existed, so a stored submission_id still replays.
func TestSubmissionHashWithoutAttachmentsKeepsLegacyBytes(t *testing.T) {
	preset, conv := "solo", "conv-1"
	req := wire.SubmitRequest{SubmissionID: "s", Input: "question", Preset: &preset, ConversationID: &conv, Workspace: "/ws"}
	const legacy = "06a3b386e744a8921909f8015c1cef167d512d7a8a85a51eafa3403de4b03474"
	got, err := submissionHash(req)
	if err != nil || got != legacy {
		t.Fatalf("hash = %q, %v; want the pre-attachment hash %s", got, err, legacy)
	}
	req.Attachments = []string{"a.txt"}
	if with, _ := submissionHash(req); with == legacy {
		t.Fatal("the attachment list is not covered by the request hash")
	}
}

// A web MCP grant is web for the attachment rule: a preset whose only tool is
// a remote MCP tool is refused with route attachment when web with file
// content is off, and accepted when it is on.
func TestAttachmentEgressRefusalCountsWebMCPGrant(t *testing.T) {
	roster := func(allow string) *config.Roster {
		body := `
[tools]
allow_web_with_read = ` + allow + `

[[endpoints]]
name = "local"
base_url = "http://localhost:11434/v1"
declared_location = "local"
declared_execution = "local"
declared_billing = "none"
network_dependency = "none"
dialect = "chat_completions_v1"
models = ["m"]
tokens_per_byte_max = 1.0

[[participants]]
id = "author"
model = "m"
route = "api"
endpoint = "local"

[mcp.remote]
transport = "http"
url = "https://a.example/mcp"
[mcp.remote.tools]
search = "read"

[presets.mcponly]
mode = "solo"
author = "author"
required_reviewers = []
tools = ["mcp:remote"]
`
		p := filepath.Join(t.TempDir(), "r.toml")
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		r, err := config.Load(p)
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		return r
	}
	off := roster("false")
	eff, err := off.Resolve("mcponly", "")
	if err != nil {
		t.Fatal(err)
	}
	we := attachmentEgressRefusal(off, eff)
	if we == nil || we.Code != wire.ErrorCodeEgress || we.Route == nil || *we.Route != "attachment" ||
		we.Participant == nil || *we.Participant != "author" {
		t.Fatalf("refusal = %+v, want egress/attachment naming author", we)
	}
	on := roster("true")
	if eff, err = on.Resolve("mcponly", ""); err != nil {
		t.Fatal(err)
	}
	if we := attachmentEgressRefusal(on, eff); we != nil {
		t.Fatalf("refused with web and file content allowed: %+v", we)
	}
}
