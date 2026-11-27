package referee

import (
	"os"
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
