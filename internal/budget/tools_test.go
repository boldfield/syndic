package budget_test

import (
	"encoding/json"
	"math"
	"slices"
	"testing"

	"github.com/boldfield/syndic/internal/budget"
	"github.com/boldfield/syndic/internal/tools"
	"github.com/boldfield/syndic/internal/transport"
)

// wireTools is the tools array in the shape the Chat Completions adapter
// sends, encoded independently of the package under test.
func wireTools(t *testing.T, defs []transport.Tool) int {
	t.Helper()
	if len(defs) == 0 {
		return 0
	}
	type function struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		Parameters  map[string]any `json:"parameters"`
	}
	type tool struct {
		Function function `json:"function"`
		Type     string   `json:"type"`
	}
	shaped := make([]tool, len(defs))
	for i, d := range defs {
		shaped[i] = tool{Type: d.Type, Function: function{d.Function.Name, d.Function.Description, d.Function.Parameters}}
	}
	b, err := json.Marshal(shaped)
	if err != nil {
		t.Fatal(err)
	}
	return len(b)
}

// wireCalls is a tool_calls array in the shape the adapter sends.
func wireCalls(t *testing.T, calls []transport.ToolCall) int {
	t.Helper()
	if len(calls) == 0 {
		return 0
	}
	type function struct {
		Arguments string `json:"arguments"`
		Name      string `json:"name"`
	}
	type call struct {
		Function function `json:"function"`
		ID       string   `json:"id"`
		Type     string   `json:"type"`
	}
	shaped := make([]call, len(calls))
	for i, c := range calls {
		shaped[i] = call{ID: c.ID, Type: c.Type, Function: function{c.Function.Arguments, c.Function.Name}}
	}
	b, err := json.Marshal(shaped)
	if err != nil {
		t.Fatal(err)
	}
	return len(b)
}

// toolRequest is a request with all nine definitions, one assistant message
// with two tool calls and two tool messages answering them.
func toolRequest() ([]transport.Message, []transport.Tool) {
	msgs := []transport.Message{
		{Role: "system", Content: "You are the author."},
		{Role: "user", Content: "Summarize the README."},
		{Role: "assistant", Content: "Reading.", ToolCalls: []transport.ToolCall{
			{ID: "call_1", Type: "function", Function: transport.ToolCallFunction{Name: tools.NameReadFile, Arguments: `{"path":"README.md"}`}},
			{ID: "call_22", Type: "function", Function: transport.ToolCallFunction{Name: tools.NameSearchFiles, Arguments: `{"pattern":"<a & b>"}`}},
		}},
		{Role: "tool", ToolCallID: "call_1", Content: "<<<result>>> untrusted: # README\n<<<end>>>"},
		{Role: "tool", ToolCallID: "call_22", Content: "<<<result>>> untrusted: README.md:1:a & b\n<<<end>>>"},
	}
	return msgs, tools.All()
}

func TestPromptBytesCountsEachPartOnce(t *testing.T) {
	msgs, defs := toolRequest()
	if len(defs) != 9 {
		t.Fatalf("definitions = %d, want 9", len(defs))
	}
	want := wireTools(t, defs)
	for _, m := range msgs {
		want += len(m.Content)
		switch m.Role {
		case "assistant":
			want += wireCalls(t, m.ToolCalls)
		case "tool":
			want += len(m.ToolCallID)
		}
	}
	full := budget.PromptBytes(msgs, defs)
	if full != want {
		t.Fatalf("PromptBytes = %d, want %d", full, want)
	}

	t.Run("definition", func(t *testing.T) {
		for i := range defs {
			fewer := slices.Delete(slices.Clone(defs), i, i+1)
			part := wireTools(t, defs) - wireTools(t, fewer)
			// One element of the array and its separating comma.
			if one := wireTools(t, defs[i:i+1]) - 2 + 1; part != one {
				t.Fatalf("definition %d: part = %d, want %d", i, part, one)
			}
			if got := full - budget.PromptBytes(msgs, fewer); got != part {
				t.Errorf("removing definition %d lowered by %d, want %d", i, got, part)
			}
		}
	})

	t.Run("call", func(t *testing.T) {
		calls := msgs[2].ToolCalls
		for i := range calls {
			fewer := slices.Clone(msgs)
			fewer[2].ToolCalls = slices.Delete(slices.Clone(calls), i, i+1)
			part := wireCalls(t, calls) - wireCalls(t, fewer[2].ToolCalls)
			if one := wireCalls(t, calls[i:i+1]) - 2 + 1; part != one {
				t.Fatalf("call %d: part = %d, want %d", i, part, one)
			}
			if got := full - budget.PromptBytes(fewer, defs); got != part {
				t.Errorf("removing call %d lowered by %d, want %d", i, got, part)
			}
		}
	})

	t.Run("tool message", func(t *testing.T) {
		for _, i := range []int{3, 4} {
			fewer := slices.Delete(slices.Clone(msgs), i, i+1)
			part := len(msgs[i].Content) + len(msgs[i].ToolCallID)
			if got := full - budget.PromptBytes(fewer, defs); got != part {
				t.Errorf("removing tool message %d lowered by %d, want %d", i, got, part)
			}
		}
	})
}

func TestPromptBytesNoTools(t *testing.T) {
	msgs := []transport.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "日本語"},
		{Role: "assistant", Content: "reply"},
	}
	if got := budget.PromptBytes(msgs, nil); got != 3+9+5 {
		t.Errorf("PromptBytes = %d, want %d", got, 3+9+5)
	}
	if got := budget.PromptBytes(nil, nil); got != 0 {
		t.Errorf("PromptBytes(empty) = %d, want 0", got)
	}
}

func TestRequestTokensApplyTheRule(t *testing.T) {
	msgs, defs := toolRequest()
	o := budget.Overheads{MessageTokens: 8, RequestTokens: 16}
	n := len(msgs)

	// At one token per byte, every counted byte is one token, each message
	// adds its overhead, and the definitions add none.
	if got, want := budget.RequestAdmissionTokens(msgs, defs, 1, o), budget.PromptBytes(msgs, defs)+n*8+16; got != want {
		t.Errorf("RequestAdmissionTokens = %d, want %d", got, want)
	}
	if got, want := budget.RequestEstimateTokens(msgs, defs, 1, o), budget.PromptBytes(msgs, defs)+n*8+16; got != want {
		t.Errorf("RequestEstimateTokens = %d, want %d", got, want)
	}

	// The definitions are converted once per request, at the same rate.
	defBytes := budget.PromptBytes(nil, defs)
	adm := budget.RequestAdmissionTokens(msgs, defs, 0.3, o) - budget.RequestAdmissionTokens(msgs, nil, 0.3, o)
	if want := int(math.Ceil(float64(defBytes) * 0.3)); adm != want {
		t.Errorf("definitions' admission = %d, want %d", adm, want)
	}
	est := budget.RequestEstimateTokens(msgs, defs, 4, o) - budget.RequestEstimateTokens(msgs, nil, 4, o)
	if want := (defBytes + 3) / 4; est != want {
		t.Errorf("definitions' estimate = %d, want %d", est, want)
	}
	if got := budget.RequestAdmissionTokens(nil, defs, 1, o); got != defBytes+16 {
		t.Errorf("definitions alone = %d, want %d", got, defBytes+16)
	}
}

func TestRequestTokensSaturate(t *testing.T) {
	_, defs := toolRequest()
	if got := budget.RequestAdmissionTokens(nil, defs, 1e300, budget.Overheads{}); got != math.MaxInt {
		t.Errorf("RequestAdmissionTokens = %d, want MaxInt", got)
	}
	if got := budget.RequestEstimateTokens(nil, defs, 1e-300, budget.Overheads{}); got != math.MaxInt {
		t.Errorf("RequestEstimateTokens = %d, want MaxInt", got)
	}
	call := []transport.Message{{Role: "assistant", ToolCalls: []transport.ToolCall{{ID: "c", Type: "function"}}}}
	if got := budget.RequestAdmissionTokens(call, nil, 1e300, budget.Overheads{}); got != math.MaxInt {
		t.Errorf("RequestAdmissionTokens(call) = %d, want MaxInt", got)
	}
	// A definition the adapter could not encode never fits.
	bad := []transport.Tool{{Type: "function", Function: transport.ToolFunction{Name: "x", Parameters: map[string]any{"v": math.NaN()}}}}
	if got := budget.PromptBytes(nil, bad); got != math.MaxInt {
		t.Errorf("PromptBytes(unencodable) = %d, want MaxInt", got)
	}
	if got := budget.RequestAdmissionTokens(nil, bad, 1, budget.Overheads{RequestTokens: 16}); got != math.MaxInt {
		t.Errorf("RequestAdmissionTokens(unencodable) = %d, want MaxInt", got)
	}
}

func TestRequestTokensPanic(t *testing.T) {
	_, defs := toolRequest()
	for _, rate := range []float64{0, -1, math.NaN(), math.Inf(1)} {
		for name, f := range map[string]func(){
			"admission": func() { budget.RequestAdmissionTokens(nil, defs, rate, budget.Overheads{}) },
			"estimate":  func() { budget.RequestEstimateTokens(nil, defs, rate, budget.Overheads{}) },
		} {
			func() {
				defer func() {
					if recover() == nil {
						t.Errorf("%s did not panic at rate %v", name, rate)
					}
				}()
				f()
			}()
		}
	}
}
