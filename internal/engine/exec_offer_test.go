package engine_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/boldfield/syndic/internal/engine"
	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/sandbox"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/transport/fake"
)

// Tests of whether bash is offered (v7 delta, sections 2, 3 and 6): the
// backend, shell, self-test, the participant's and the referee's tool_calls,
// and the run's gate.

func execRoster(extra string) string {
	return refereeRoster(`
[presets.exec]
mode = "solo"
author = "author"
required_reviewers = []
tools = ["read", "exec"]
` + extra)
}

// fakeShell is an executable file standing in for the shell.
func fakeShell(t *testing.T, name string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return p
}

var allCaps = sandbox.Caps(sandbox.F, sandbox.N1, sandbox.N2, sandbox.P, sandbox.D)

func execOpts(t *testing.T, b sandbox.Backend, shell string, extra ...enginetest.Option) []enginetest.Option {
	t.Helper()
	return append(append(refereeOpts(), enginetest.WithExec(engine.ExecOptions{
		Backend:  b,
		Shells:   []string{shell},
		SelfTest: sandbox.SelfTestConfig{Dir: t.TempDir()},
	})), extra...)
}

func passingBackend(caps sandbox.CapSet) *sandbox.Fake {
	return sandbox.NewFake(caps, sandbox.FakeSelfTestScripts("", false)...)
}

func TestExecOfferedWhenEverythingHolds(t *testing.T) {
	be := passingBackend(allCaps)
	h := enginetest.New(t, execRoster(""), nil, execOpts(t, be, fakeShell(t, "bash"))...)
	if r := h.Engine.ExecOffReason(); r != "" {
		t.Fatalf("exec off at start: %q", r)
	}
	if r := h.Engine.ExecOfferReason("author"); r != "" {
		t.Fatalf("exec not offered to the author: %q", r)
	}
}

func TestExecRefusedWithoutParticipantToolCalls(t *testing.T) {
	h := enginetest.New(t, execRoster(""), nil,
		execOpts(t, passingBackend(allCaps), fakeShell(t, "bash"), enginetest.WithToolCalls("author", transport.TriNo))...)
	if r := h.Engine.ExecOfferReason("author"); r != "no_tool_calls:author" {
		t.Fatalf("reason %q", r)
	}
}

func TestExecUnofferedWhenRefereeLacksToolCallsButWebIs(t *testing.T) {
	h := enginetest.New(t, execRoster(""), nil,
		execOpts(t, passingBackend(allCaps), fakeShell(t, "bash"), enginetest.WithModelToolCalls("local", "refmodel", transport.TriNo))...)
	if r := h.Engine.ExecOfferReason("author"); r != "referee_no_tool_calls:local" {
		t.Fatalf("reason %q", r)
	}
	if r := h.Engine.WebOfferReason("local"); r != "" {
		t.Fatalf("web off: %q", r)
	}
}

func TestExecUnofferedWithNeitherShell(t *testing.T) {
	be := passingBackend(allCaps)
	h := enginetest.New(t, execRoster(""), nil, execOpts(t, be, filepath.Join(t.TempDir(), "absent"))...)
	if r := h.Engine.ExecOffReason(); r != "no_shell" {
		t.Fatalf("reason %q", r)
	}
	if n := len(be.Starts()); n != 0 {
		t.Fatalf("%d commands started", n)
	}
}

func TestExecUnsupportedBackendIsUnofferedWithNoWeakerSet(t *testing.T) {
	be := passingBackend(allCaps)
	be.SetCapabilities(sandbox.Caps(sandbox.F, sandbox.N1, sandbox.D), "kernel 4.9 is below the stated minimum")
	h := enginetest.New(t, execRoster(""), nil, execOpts(t, be, fakeShell(t, "bash"))...)
	r := h.Engine.ExecOffReason()
	if !strings.HasPrefix(r, "sandbox_unsupported:fake:") || !strings.Contains(r, "below the stated minimum") || !strings.Contains(r, "missing P") {
		t.Fatalf("reason %q", r)
	}
	if n := len(be.Starts()); n != 0 {
		t.Fatalf("%d commands started under a weaker set", n)
	}
}

func TestExecUnofferedWithoutBackend(t *testing.T) {
	h := enginetest.New(t, execRoster(""), nil, refereeOpts()...)
	if r := h.Engine.ExecOffReason(); r != "no_sandbox_backend" {
		t.Fatalf("reason %q", r)
	}
}

func TestExecPresetWithNetworkListRefusedOnBackendWithoutN2(t *testing.T) {
	be := passingBackend(sandbox.Caps(sandbox.F, sandbox.N1, sandbox.P, sandbox.D))
	roster := execRoster(`network_allow = ["example.com"]` + "\n")
	h := enginetest.New(t, roster, nil, append(execOpts(t, be, fakeShell(t, "bash")), enginetest.NoStart())...)
	err := h.Engine.Start()
	var re *engine.RosterError
	if !asRosterError(err, &re) || !strings.Contains(err.Error(), `"fake"`) || !strings.Contains(err.Error(), "N2") {
		t.Fatalf("Start err = %v, want a roster error naming the backend and N2", err)
	}
}

func TestExecGateClosedUnoffersExecForTheRun(t *testing.T) {
	h := enginetest.New(t, execRoster(""), nil, execOpts(t, passingBackend(allCaps), fakeShell(t, "bash"))...)
	h.Engine.ExecGate().Close("call c1 unverified")
	if r := h.Engine.ExecOffReason(); r != "containment_unverified:call c1 unverified" {
		t.Fatalf("reason %q", r)
	}
	if r := h.Engine.ExecOfferReason("author"); !strings.HasPrefix(r, "containment_unverified") {
		t.Fatalf("reason %q", r)
	}
}

func TestExecStartSelfTestFailureNamesTheTest(t *testing.T) {
	be := sandbox.NewFake(allCaps, sandbox.FakeSelfTestScripts(sandbox.CheckLoopback, false)...)
	h := enginetest.New(t, execRoster(""), nil, execOpts(t, be, fakeShell(t, "bash"))...)
	if r := h.Engine.ExecOffReason(); r != "self_test_failed:loopback_denied" {
		t.Fatalf("reason %q", r)
	}
}

func TestExecSelfTestRunsAgainBeforeATurnsFirstCommand(t *testing.T) {
	be := passingBackend(allCaps)
	be.Push(sandbox.FakeSelfTestScripts(sandbox.CheckDetach, false)...)
	var turnReason string
	var h *enginetest.Harness
	opts := execOpts(t, be, fakeShell(t, "bash"), enginetest.WithBeforeToolEffect(func(turnID, _, _ string) {
		if turnReason == "" {
			turnReason = h.Engine.ExecBeforeCommand(context.Background(), turnID)
		}
	}))
	h = enginetest.New(t, execRoster(""), map[string][]fake.Step{
		"author": {toolStepWith(call("c1", "list_dir", `{"path":"."}`)), enginetest.Reply("done")},
	}, opts...)
	before := len(be.Starts())
	reply, err := h.Engine.Submit(toolReq("s1", "exec", workspaceDir(t)))
	if err != nil {
		t.Fatal(err)
	}
	wantEnd(t, h, reply.TurnID, engine.StateSoloComplete, "solo_ok")
	if turnReason != "self_test_failed:detached_helper_contained" {
		t.Fatalf("turn reason %q", turnReason)
	}
	if len(be.Starts()) <= before {
		t.Fatal("the turn's self-test ran no command")
	}
	if r := h.Engine.ExecOffReason(); r != "" {
		t.Fatalf("a turn's failure unoffered exec for the run: %q", r)
	}
}

func asRosterError(err error, target **engine.RosterError) bool { return errors.As(err, target) }
