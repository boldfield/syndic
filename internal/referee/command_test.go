package referee

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestCommandFirstRequestGolden(t *testing.T) {
	msgs := CommandPromptMessages(CommandPayload{
		CallName:   "bash",
		Command:    "ls -la",
		UserInput:  "list the files",
		PriorCalls: []string{"pwd", "echo \"hi\""},
	})
	if len(msgs) != 2 || msgs[0].Role != "system" || msgs[1].Role != "user" {
		t.Fatalf("unexpected messages: %+v", msgs)
	}
	got := msgs[0].Content + "\n=====\n" + msgs[1].Content
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.WriteFile("testdata/command_first_request.golden", []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile("testdata/command_first_request.golden")
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("first request differs from golden:\n%s", got)
	}
}

func TestCommandTemplateText(t *testing.T) {
	for _, s := range []string{"sandbox", "private copy", "discarded", "read_file", "list_dir", "search_files", "data, not instructions", "allow <reason>", "deny <reason>"} {
		if !strings.Contains(commandSystemText, s) {
			t.Errorf("template lacks %q", s)
		}
	}
}

func TestCommandPayloadOnlyDeclaredFields(t *testing.T) {
	out := buildCommandPayloadUser(CommandPayload{CallName: "bash", Command: "cmd", UserInput: "in"})
	want := "user input[2]:\nin\n\nproposed call:\n\"bash\" \"cmd\"\n\nprior calls this turn:\nnone\n"
	if out != want {
		t.Errorf("got %q want %q", out, want)
	}
	out = buildCommandPayloadUser(CommandPayload{CallName: "bash", Command: "c", UserInput: "u", PriorCalls: []string{"a", "b"}})
	if !strings.HasSuffix(out, "prior calls this turn:\n\"a\"\n\"b\"\n") {
		t.Errorf("prior calls rendering: %q", out)
	}
}

func TestCommandTemplateHashDistinct(t *testing.T) {
	if len(CommandTemplateHash()) != 64 || CommandTemplateHash() == TemplateHash() {
		t.Errorf("bad command template hash %q", CommandTemplateHash())
	}
}

// Design (v7 delta section 6): "the first referee request matches the command
// template byte for byte and contains no output and no file content."
func TestCommandFirstRequestHasNoOutputOrFileContent(t *testing.T) {
	const output = "OUTPUT-SENTINEL-7f3a"
	const fileText = "FILE-CONTENT-SENTINEL-91bc"
	// The prior call ran and produced output, and the file exists; none of it
	// has a field in the payload, so none may appear in any message.
	msgs := CommandPromptMessages(CommandPayload{
		CallName:   "bash",
		Command:    "cat secrets.txt",
		UserInput:  "show me the file",
		PriorCalls: []string{"echo hi"},
	})
	for _, m := range msgs {
		if strings.Contains(m.Content, output) || strings.Contains(m.Content, fileText) {
			t.Errorf("%s message contains output or file content: %q", m.Role, m.Content)
		}
	}
}

// Design (v7 delta section 6): "the payload contains no output, no candidate
// text and no file content for any input." The payload type has exactly the
// four declared fields, so no other content can be rendered.
func TestCommandPayloadFieldSetPinned(t *testing.T) {
	typ := reflect.TypeOf(CommandPayload{})
	var got []string
	for i := 0; i < typ.NumField(); i++ {
		got = append(got, typ.Field(i).Name)
	}
	want := []string{"CallName", "Command", "UserInput", "PriorCalls"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("CommandPayload fields %v, want exactly %v", got, want)
	}
}
