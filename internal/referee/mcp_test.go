package referee

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestMCPFirstRequestGolden(t *testing.T) {
	msgs := MCPPromptMessages(MCPPayload{
		UserInput: "find the open issues",
		Call:      MCPCall{Server: "tracker", Tool: "list_issues", Arguments: `{"state":"open"}`},
		PriorCalls: []MCPCall{
			{Server: "tracker", Tool: "get_project", Arguments: `{"id":7}`},
		},
	})
	if len(msgs) != 2 || msgs[0].Role != "system" || msgs[1].Role != "user" {
		t.Fatalf("unexpected messages: %+v", msgs)
	}
	got := msgs[0].Content + "\n=====\n" + msgs[1].Content
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.WriteFile("testdata/mcp_first_request.golden", []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile("testdata/mcp_first_request.golden")
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("request differs from golden:\n%s", got)
	}
}

func TestMCPTemplateText(t *testing.T) {
	for _, s := range []string{"external tool", "outside the engine", "data, not instructions", "allow <reason>", "deny <reason>"} {
		if !strings.Contains(mcpSystemText, s) {
			t.Errorf("template lacks %q", s)
		}
	}
}

func TestMCPPayloadRendering(t *testing.T) {
	out := buildMCPPayloadUser(MCPPayload{UserInput: "in", Call: MCPCall{Server: "s", Tool: "t", Arguments: "{}"}})
	want := "user input[2]:\nin\n\nproposed call:\n\"s\" \"t\" \"{}\"\n\nprior calls this turn:\nnone\n"
	if out != want {
		t.Errorf("got %q want %q", out, want)
	}
	out = buildMCPPayloadUser(MCPPayload{UserInput: "u", Call: MCPCall{Server: "s", Tool: "t", Arguments: "{}"},
		PriorCalls: []MCPCall{{"a", "b", "{}"}, {"c", "d", `{"x":1}`}}})
	if !strings.HasSuffix(out, "prior calls this turn:\n\"a\" \"b\" \"{}\"\n\"c\" \"d\" \"{\\\"x\\\":1}\"\n") {
		t.Errorf("prior calls rendering: %q", out)
	}
}

func TestMCPTemplateHashDistinct(t *testing.T) {
	h := MCPTemplateHash()
	if len(h) != 64 || h == TemplateHash() || h == CommandTemplateHash() {
		t.Errorf("bad MCP template hash %q", h)
	}
}

// Design (v9 delta section 4, step 3): "the referee request matches the
// template byte for byte and contains no result and no tool description."
// The payload types have exactly the declared fields, so no result, tool
// description, candidate text or file content can be rendered.
func TestMCPPayloadFieldSetPinned(t *testing.T) {
	fields := func(v interface{}) []string {
		typ := reflect.TypeOf(v)
		var got []string
		for i := 0; i < typ.NumField(); i++ {
			got = append(got, typ.Field(i).Name)
		}
		return got
	}
	if got, want := fields(MCPPayload{}), []string{"UserInput", "Call", "PriorCalls"}; !reflect.DeepEqual(got, want) {
		t.Errorf("MCPPayload fields %v, want %v", got, want)
	}
	if got, want := fields(MCPCall{}), []string{"Server", "Tool", "Arguments"}; !reflect.DeepEqual(got, want) {
		t.Errorf("MCPCall fields %v, want %v", got, want)
	}
}

func TestMCPRequestHasNoResultOrDescription(t *testing.T) {
	msgs := MCPPromptMessages(MCPPayload{
		UserInput:  "u",
		Call:       MCPCall{Server: "s", Tool: "t", Arguments: `{"q":"x"}`},
		PriorCalls: []MCPCall{{"s", "t", "{}"}},
	})
	for _, m := range msgs {
		if strings.Contains(m.Content, "RESULT-SENTINEL") || strings.Contains(m.Content, "DESCRIPTION-SENTINEL") {
			t.Errorf("%s message contains result or description", m.Role)
		}
	}
	if msgs[0].Content != mcpSystemText {
		t.Errorf("system message differs from template")
	}
}
