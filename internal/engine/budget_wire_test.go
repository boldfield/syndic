package engine_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/budget"
	"github.com/boldfield/syndic/internal/config"
	"github.com/boldfield/syndic/internal/tools"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/transport/openai"
)

// The counting rule covers what the Chat Completions adapter actually sends:
// a request with every definition, an assistant message with two calls and
// the two tool messages answering them, captured from the wire.
func TestBudgetCoversRenderedToolRequest(t *testing.T) {
	bodies := make(chan []byte, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies <- b
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"model":"m","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	defer srv.Close()

	defs := tools.All()
	msgs := []transport.Message{
		{Role: "system", Content: "You are the author."},
		{Role: "user", Content: "Summarize the README & list <its> sections."},
		{Role: "assistant", ToolCalls: []transport.ToolCall{
			{ID: "call_1", Type: "function", Function: transport.ToolCallFunction{Name: tools.NameReadFile, Arguments: `{"path":"README.md","offset":0}`}},
			{ID: "call_2", Type: "function", Function: transport.ToolCallFunction{Name: tools.NameSearchFiles, Arguments: `{"pattern":"^## <h2> & more"}`}},
		}},
		{Role: "tool", ToolCallID: "call_1", Content: "<<<a1b2>>> untrusted content follows\n# README\n<<<a1b2>>>"},
		{Role: "tool", ToolCallID: "call_2", Content: "<<<c3d4>>> untrusted content follows\nREADME.md:3:## Usage\n<<<c3d4>>>"},
		{Role: "assistant", Content: "Reading done."},
	}

	adapter := openai.New(config.EndpointProfile{Name: "local", BaseURL: srv.URL, MaxTokensField: "max_tokens"}, nil)
	if _, err := adapter.Complete(context.Background(), transport.CompleteRequest{
		Envelope:        transport.AttemptEnvelope{AttemptID: "a1"},
		Model:           "m",
		Messages:        msgs,
		Tools:           defs,
		MaxOutputTokens: 4096,
		Deadline:        time.Now().Add(10 * time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	body := <-bodies

	var sent struct {
		Messages []struct {
			Role      string          `json:"role"`
			ToolCalls json.RawMessage `json:"tool_calls"`
		} `json:"messages"`
		Tools json.RawMessage `json:"tools"`
	}
	if err := json.Unmarshal(body, &sent); err != nil {
		t.Fatal(err)
	}
	if len(sent.Messages) != len(msgs) || len(sent.Tools) == 0 {
		t.Fatalf("body = %s", body)
	}

	if counted := budget.PromptBytes(nil, defs); counted < len(sent.Tools) {
		t.Errorf("definitions counted %d bytes, the body's tools value is %d", counted, len(sent.Tools))
	}
	for i, m := range msgs {
		if m.Role != "assistant" {
			continue
		}
		calls := budget.PromptBytes([]transport.Message{{Role: "assistant", ToolCalls: m.ToolCalls}}, nil)
		if calls < len(sent.Messages[i].ToolCalls) {
			t.Errorf("message %d: calls counted %d bytes, the body's tool_calls value is %d", i, calls, len(sent.Messages[i].ToolCalls))
		}
	}
	o := budget.Overheads{MessageTokens: 64, RequestTokens: 256}
	if adm := budget.RequestAdmissionTokens(msgs, defs, 1, o); adm < len(body) {
		t.Errorf("admission = %d tokens, the body is %d bytes", adm, len(body))
	}
}
