package tui

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/config"
	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/transport/fake"
	"github.com/boldfield/syndic/internal/wire"
)

func TestProvisionalPromptWithStatus(t *testing.T) {
	a := NewApp(context.Background(), "socket", WithWorkDir(t.TempDir()))
	status := &wire.StatusReply{
		RosterPath: "/etc/roster.toml",
	}
	a.showProvisionalPrompt(status)
	if a.provisional == nil {
		t.Fatal("provisional prompt not set")
	}
	if a.provisional.rosterPath != "/etc/roster.toml" {
		t.Errorf("rosterPath = %q, want /etc/roster.toml", a.provisional.rosterPath)
	}
	view := a.View()
	if !strings.Contains(view, "Provisional deadlines") {
		t.Errorf("View does not contain 'Provisional deadlines'")
	}
	if !strings.Contains(view, "Configured at: /etc/roster.toml") {
		t.Errorf("View does not contain roster path")
	}
	if !strings.Contains(view, "(y/n)>") {
		t.Errorf("View does not contain prompt '(y/n)>'")
	}
}

func TestProvisionalPromptWithoutStatus(t *testing.T) {
	a := NewApp(context.Background(), "socket", WithWorkDir(t.TempDir()))
	a.showProvisionalPrompt(nil)
	if a.provisional == nil {
		t.Fatal("provisional prompt not set")
	}
	view := a.View()
	if !strings.Contains(view, "Provisional deadlines are in effect") {
		t.Errorf("View does not contain 'Provisional deadlines are in effect'")
	}
	if !strings.Contains(view, "(y/n)>") {
		t.Errorf("View does not contain prompt '(y/n)>'")
	}
}

func TestProvisionalFlowWithRealEngine(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping slow test")
	}

	roster := `
[[endpoints]]
name = "fake"
base_url = "http://localhost/v1"
declared_location = "local"
declared_execution = "local"
declared_billing = "none"
network_dependency = "none"
dialect = "chat_completions_v1"
tokens_per_byte_max = 1.0

[[participants]]
id = "author"
model = "claude"
route = "api"
endpoint = "fake"
eligible_roles = ["author"]
pool = "default"

[[participants]]
id = "reviewer"
model = "claude"
route = "api"
endpoint = "fake"
eligible_roles = ["reviewer"]
pool = "default"

[deadlines]
attempt = "300s"
turn = "900s"
delivery = "60s"
provisional = true

[presets.solo]
authors = ["author"]
`

	dir := t.TempDir()
	rosterPath := dir + "/roster.toml"
	if err := os.WriteFile(rosterPath, []byte(roster), 0644); err != nil {
		t.Fatal(err)
	}

	r, err := config.LoadRoster(rosterPath, nil)
	if err != nil {
		t.Fatal(err)
	}

	socket := dir + "/socket"
	srv := enginetest.NewTestServer(socket, r)
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Stop() })

	adapter := fake.NewAdapter()
	srv.SetAdapter(adapter)

	ctx := context.Background()
	a := NewApp(ctx, socket, WithWorkDir(dir))

	conn := make(chan connectedMsg, 1)
	a.gen++
	go func() {
		msg := connectCmd(ctx, a.gen, socket, 0)()
		conn <- msg.(connectedMsg)
	}()

	msg := <-conn
	if msg.err != nil {
		t.Fatalf("connect failed: %v", msg.err)
	}
	a.cl = msg.cl
	a.state.Hello = msg.cl.Hello()
	a.state = MergeStatus(a.state, &msg.status)

	// First submit should fail with provisional error
	adapter.Push(fake.Step{Status: "complete", TransportStatus: "ok"})
	a.lastSubmitInput = "test input"
	submit := a.submitInput(a.lastSubmitInput)
	submitRes := submit().(submitMsg)
	if submitRes.err == nil {
		t.Fatal("expected submit to fail with provisional error")
	}

	var we *wire.Error
	if !strings.Contains(submitRes.refusal, "provisional") {
		t.Fatalf("expected provisional error, got: %s", submitRes.refusal)
	}

	// Should show the provisional prompt
	a.onSubmit(submitRes)
	if a.provisional == nil {
		t.Fatal("provisional prompt should be shown")
	}
	view := a.View()
	if !strings.Contains(view, "Provisional deadlines") {
		t.Error("View should contain provisional deadline message")
	}
	if !strings.Contains(view, "(y/n)>") {
		t.Error("View should contain (y/n) prompt")
	}

	// User says yes - should resend with allow_provisional
	adapter.Push(fake.Step{Status: "complete", TransportStatus: "ok"})
	yes := a.provisionalYes()
	yesRes := yes().(submitMsg)
	if yesRes.err != nil {
		t.Fatalf("second submit failed: %v", yesRes.err)
	}
	if yesRes.turnID == "" {
		t.Fatal("second submit should create a turn")
	}

	// Verify that the input was kept until the engine accepted
	if a.input.Value() != "" {
		t.Errorf("input should have been cleared by onSubmit after success, got %q", a.input.Value())
	}
	a.onSubmit(yesRes)

	// allowProvisional should be set for future submits
	if !a.allowProvisional {
		t.Fatal("allowProvisional should be true after yes")
	}

	// Provisional should be cleared
	if a.provisional != nil {
		t.Fatal("provisional should be cleared after yes")
	}
}

func TestProvisionalFlowNo(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping slow test")
	}

	roster := `
[[endpoints]]
name = "fake"
base_url = "http://localhost/v1"
declared_location = "local"
declared_execution = "local"
declared_billing = "none"
network_dependency = "none"
dialect = "chat_completions_v1"
tokens_per_byte_max = 1.0

[[participants]]
id = "author"
model = "claude"
route = "api"
endpoint = "fake"
eligible_roles = ["author"]
pool = "default"

[deadlines]
attempt = "300s"
turn = "900s"
delivery = "60s"
provisional = true

[presets.solo]
authors = ["author"]
`

	dir := t.TempDir()
	rosterPath := dir + "/roster.toml"
	if err := os.WriteFile(rosterPath, []byte(roster), 0644); err != nil {
		t.Fatal(err)
	}

	r, err := config.LoadRoster(rosterPath, nil)
	if err != nil {
		t.Fatal(err)
	}

	socket := dir + "/socket"
	srv := enginetest.NewTestServer(socket, r)
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Stop() })

	adapter := fake.NewAdapter()
	srv.SetAdapter(adapter)

	ctx := context.Background()
	a := NewApp(ctx, socket, WithWorkDir(dir))

	conn := make(chan connectedMsg, 1)
	a.gen++
	go func() {
		msg := connectCmd(ctx, a.gen, socket, 0)()
		conn <- msg.(connectedMsg)
	}()

	msg := <-conn
	if msg.err != nil {
		t.Fatalf("connect failed: %v", msg.err)
	}
	a.cl = msg.cl
	a.state.Hello = msg.cl.Hello()
	a.state = MergeStatus(a.state, &msg.status)

	// First submit fails with provisional error
	adapter.Push(fake.Step{Status: "complete", TransportStatus: "ok"})
	a.lastSubmitInput = "test input"
	submit := a.submitInput(a.lastSubmitInput)
	submitRes := submit().(submitMsg)
	a.onSubmit(submitRes)

	if a.provisional == nil {
		t.Fatal("provisional prompt should be shown")
	}
	inputBefore := a.input.Value()
	if inputBefore == "" {
		a.input.SetValue("test input")
		inputBefore = a.input.Value()
	}

	// User says no - should keep the input
	no := a.provisionalNo()
	if no != nil {
		t.Fatal("provisionalNo should return nil")
	}

	if a.provisional != nil {
		t.Fatal("provisional should be cleared")
	}

	if a.input.Value() != inputBefore {
		t.Errorf("input should be kept, was %q now %q", inputBefore, a.input.Value())
	}

	if a.refusal != "submit cancelled; input kept" {
		t.Errorf("refusal should indicate cancellation, got %q", a.refusal)
	}

	// A second submit should prompt again
	adapter.Push(fake.Step{Status: "complete", TransportStatus: "ok"})
	submit2 := a.submitInput(a.lastSubmitInput)
	submitRes2 := submit2().(submitMsg)
	a.onSubmit(submitRes2)

	if a.provisional == nil {
		t.Fatal("provisional should prompt again on second submit")
	}
}

func TestProvisionalNotAskedWhenFalse(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping slow test")
	}

	roster := `
[[endpoints]]
name = "fake"
base_url = "http://localhost/v1"
declared_location = "local"
declared_execution = "local"
declared_billing = "none"
network_dependency = "none"
dialect = "chat_completions_v1"
tokens_per_byte_max = 1.0

[[participants]]
id = "author"
model = "claude"
route = "api"
endpoint = "fake"
eligible_roles = ["author"]
pool = "default"

[deadlines]
attempt = "300s"
turn = "900s"
delivery = "60s"
provisional = false

[presets.solo]
authors = ["author"]
`

	dir := t.TempDir()
	rosterPath := dir + "/roster.toml"
	if err := os.WriteFile(rosterPath, []byte(roster), 0644); err != nil {
		t.Fatal(err)
	}

	r, err := config.LoadRoster(rosterPath, nil)
	if err != nil {
		t.Fatal(err)
	}

	socket := dir + "/socket"
	srv := enginetest.NewTestServer(socket, r)
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Stop() })

	adapter := fake.NewAdapter()
	srv.SetAdapter(adapter)

	ctx := context.Background()
	a := NewApp(ctx, socket, WithWorkDir(dir))

	conn := make(chan connectedMsg, 1)
	a.gen++
	go func() {
		msg := connectCmd(ctx, a.gen, socket, 0)()
		conn <- msg.(connectedMsg)
	}()

	msg := <-conn
	if msg.err != nil {
		t.Fatalf("connect failed: %v", msg.err)
	}
	a.cl = msg.cl
	a.state.Hello = msg.cl.Hello()
	a.state = MergeStatus(a.state, &msg.status)

	// Submit should not fail with provisional error
	adapter.Push(fake.Step{Status: "complete", TransportStatus: "ok"})
	submit := a.submitInput("test input")
	submitRes := submit().(submitMsg)

	if submitRes.err != nil {
		var we *wire.Error
		if strings.Contains(submitRes.refusal, "provisional") {
			t.Fatal("should not get provisional error when provisional = false")
		}
	}
}
