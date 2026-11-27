package sandbox_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/boldfield/syndic/internal/sandbox"
)

func selfTestConfig(t *testing.T, networkList bool) sandbox.SelfTestConfig {
	t.Helper()
	helper := filepath.Join(t.TempDir(), "helper")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := sandbox.SelfTestConfig{
		Argv:        func(c string) []string { return []string{"/bin/sh", "-c", c} },
		Path:        "/usr/bin:/bin",
		Dir:         t.TempDir(),
		Helper:      helper,
		NetworkList: networkList,
	}
	if networkList {
		cfg.Network = func() (sandbox.NetworkFixture, error) {
			return sandbox.NetworkFixture{
				Listener:  sandbox.Listener{Network: "tcp", Address: "127.0.0.1:1"},
				ProxyURL:  "http://u:p@127.0.0.1:1",
				OtherAddr: "127.0.0.1:2",
			}, nil
		}
	}
	return cfg
}

func TestSelfTestConformingPasses(t *testing.T) {
	for _, networkList := range []bool{false, true} {
		fake := sandbox.NewFake(sandbox.Caps(sandbox.F, sandbox.N1, sandbox.N2, sandbox.P, sandbox.D),
			sandbox.FakeSelfTestScripts("", networkList)...)
		res := sandbox.RunAtStart(context.Background(), fake, selfTestConfig(t, networkList))
		if err := res.Err(); err != nil {
			t.Fatalf("networkList=%v: %v", networkList, err)
		}
		if res.Scope != "run" {
			t.Errorf("scope %q, want run", res.Scope)
		}
		for _, c := range sandbox.AllSelfTestChecks() {
			r, ok := res.Result(c)
			if !ok {
				t.Fatalf("no result for %s", c)
			}
			wantRan := c != sandbox.CheckProxy || networkList
			if r.Ran != wantRan {
				t.Errorf("%s: ran=%v, want %v", c, r.Ran, wantRan)
			}
		}
	}
}

func TestSelfTestEachCheckFailsInTurn(t *testing.T) {
	for _, failing := range sandbox.AllSelfTestChecks() {
		t.Run(string(failing), func(t *testing.T) {
			fake := sandbox.NewFake(sandbox.Caps(sandbox.F, sandbox.N1, sandbox.N2, sandbox.P, sandbox.D),
				sandbox.FakeSelfTestScripts(failing, true)...)
			res := sandbox.RunBeforeTurn(context.Background(), fake, selfTestConfig(t, true))
			if res.Scope != "turn" {
				t.Errorf("scope %q, want turn", res.Scope)
			}
			if res.Passed() {
				t.Fatal("self-test passed with a failing check")
			}
			failed := res.Failed()
			if len(failed) != 1 || failed[0].Check != failing {
				t.Fatalf("failed %+v, want only %s", failed, failing)
			}
			if err := res.Err(); err == nil || !strings.Contains(err.Error(), string(failing)) {
				t.Errorf("error %v does not name %s", err, failing)
			}
		})
	}
}

func TestSelfTestHelperProbes(t *testing.T) {
	var out strings.Builder
	if code := sandbox.SelfTestHelper([]string{"read-denied", filepath.Join(t.TempDir(), "absent")}, &out); code != 0 || !strings.Contains(out.String(), "selftest ok") {
		t.Errorf("read of absent file: code %d, output %q", code, out.String())
	}
	f := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(f, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if code := sandbox.SelfTestHelper([]string{"read-denied", f}, &out); code == 0 {
		t.Error("read of a readable file reported denied")
	}
	if code := sandbox.SelfTestHelper([]string{"bogus"}, &out); code != 2 {
		t.Errorf("unknown mode: code %d, want 2", code)
	}
}
