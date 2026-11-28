package sandbox_test

import (
	"bufio"
	"context"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/sandbox"
)

var serverCaps = sandbox.Caps(sandbox.F, sandbox.N1, sandbox.N2, sandbox.P, sandbox.D)

// driveServers plays the helper of each started server: it reads the probe
// line from the instance's standard input and answers on standard output,
// misbehaving for the start whose check is fail. It returns the probe line
// each start received, once stop is closed.
func driveServers(t *testing.T, fake *sandbox.Fake, fail sandbox.SelfTestCheck, networkList bool, stop <-chan struct{}) <-chan []string {
	t.Helper()
	order := []sandbox.SelfTestCheck{sandbox.CheckTrue, sandbox.CheckReadState, sandbox.CheckWriteOutside, sandbox.CheckLoopback, sandbox.CheckDetach}
	if networkList {
		order = append(order, sandbox.CheckProxy, sandbox.CheckProxy)
	}
	order = append(order, sandbox.CheckWriteWorkspace, sandbox.CheckReadOtherState)
	probes := make(chan []string, 1)
	go func() {
		var got []string
		defer func() { probes <- got }()
		for i, c := range order {
			var sp *sandbox.FakeServerProcess
			for sp == nil {
				select {
				case <-stop:
					return
				case <-time.After(time.Millisecond):
				}
				sp = fake.Server(i)
			}
			line, _ := bufio.NewReader(sp.ServerIn()).ReadString('\n')
			fields := strings.Split(strings.TrimSuffix(line, "\n"), "\x1f")
			got = append(got, fields[0])
			reply := "selftest ok\n"
			if c == fail && (c != sandbox.CheckProxy || i == indexOf(order, c)) {
				reply = "misbehaved\n"
				if c == sandbox.CheckDetach {
					// A sleeper that survives holds its lock.
					f, err := os.OpenFile(fields[1], os.O_RDONLY, 0)
					if err == nil {
						defer func() { _ = f.Close() }()
						_ = syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
						reply = "selftest ok\n"
					}
				}
			}
			_, _ = sp.ServerOut().Write([]byte(reply))
		}
	}()
	return probes
}

// serverScripts returns n scripts of a server that stays up long enough to
// answer its probe before it exits.
func serverScripts(n int) []sandbox.FakeScript {
	out := make([]sandbox.FakeScript, n)
	for i := range out {
		out[i] = sandbox.FakeScript{Runtime: 50 * time.Millisecond}
	}
	return out
}

func indexOf(s []sandbox.SelfTestCheck, c sandbox.SelfTestCheck) int {
	for i, x := range s {
		if x == c {
			return i
		}
	}
	return -1
}

func serverConfig(t *testing.T, networkList bool) sandbox.ServerSelfTestConfig {
	cfg := sandbox.ServerSelfTestConfig{SelfTestConfig: selfTestConfig(t, networkList)}
	cfg.CallLimit = 5 * time.Second
	return cfg
}

func TestServerSelfTestConformingPasses(t *testing.T) {
	for _, networkList := range []bool{false, true} {
		fake := sandbox.NewFake(serverCaps, serverScripts(9)...)
		stop := make(chan struct{})
		probes := driveServers(t, fake, "", networkList, stop)
		res := sandbox.RunServersAtStart(context.Background(), fake, serverConfig(t, networkList))
		close(stop)
		got := <-probes
		if err := res.Err(); err != nil {
			t.Fatalf("networkList=%v: %v", networkList, err)
		}
		if res.Scope != "run" {
			t.Errorf("scope %q, want run", res.Scope)
		}
		for _, c := range sandbox.AllServerSelfTestChecks() {
			r, ok := res.Result(c)
			if !ok {
				t.Fatalf("no result for %s", c)
			}
			if want := c != sandbox.CheckProxy || networkList; r.Ran != want {
				t.Errorf("%s: ran=%v, want %v", c, r.Ran, want)
			}
		}
		if len(got) != len(fake.Starts()) || len(got) < 7 {
			t.Fatalf("probes %v for %d starts", got, len(fake.Starts()))
		}
		for i, s := range fake.Starts() {
			if !s.ServerPipes {
				t.Errorf("start %d without ServerPipes", i)
			}
		}
	}
}

func TestServerSelfTestEachCheckFailsInTurn(t *testing.T) {
	for _, failing := range sandbox.AllServerSelfTestChecks() {
		t.Run(string(failing), func(t *testing.T) {
			fake := sandbox.NewFake(serverCaps, serverScripts(9)...)
			stop := make(chan struct{})
			driveServers(t, fake, failing, true, stop)
			res := sandbox.RunServersBeforeTurn(context.Background(), fake, serverConfig(t, true))
			close(stop)
			if res.Scope != "turn" {
				t.Errorf("scope %q, want turn", res.Scope)
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

func TestServerSelfTestPolicyInSpec(t *testing.T) {
	fake := sandbox.NewFake(serverCaps, serverScripts(9)...)
	stop := make(chan struct{})
	driveServers(t, fake, "", false, stop)
	_ = sandbox.RunServersAtStart(context.Background(), fake, serverConfig(t, false))
	close(stop)
	starts := fake.Starts()
	ws := starts[len(starts)-2]
	if len(ws.System.ReadOnlyPaths) != 1 {
		t.Errorf("workspace instance read-only paths %v, want the copy", ws.System.ReadOnlyPaths)
	}
	if n := len(starts[0].System.ReadOnlyPaths); n != 0 {
		t.Errorf("first instance has %d read-only paths", n)
	}
	home := ""
	for _, kv := range starts[0].Env {
		if strings.HasPrefix(kv, "HOME=") {
			home = strings.TrimPrefix(kv, "HOME=")
		}
	}
	if home == "" || home != starts[0].ScratchDir {
		t.Errorf("HOME %q, state dir %q", home, starts[0].ScratchDir)
	}
}

func TestServerSelfTestNoPipesFails(t *testing.T) {
	// A backend whose process has no server pipes cannot run a server.
	fake := sandbox.NewFake(serverCaps, sandbox.FakeScript{})
	cfg := serverConfig(t, false)
	res := sandbox.RunServersAtStart(context.Background(), fake, cfg)
	if res.Passed() {
		t.Fatal("self-test passed")
	}
}

func TestServerSelfTestUnverifiedContainmentClosesGate(t *testing.T) {
	fake := sandbox.NewFake(serverCaps, sandbox.FakeScript{HoldPipe: true, NeverConfirm: true, Runtime: time.Hour})
	cfg := serverConfig(t, false)
	cfg.Gate = &sandbox.Gate{}
	cfg.Sequence = sandbox.Sequence{KillGrace: 10 * time.Millisecond, CleanupDeadline: 10 * time.Millisecond}
	cfg.CallLimit = 50 * time.Millisecond
	res := sandbox.RunServersAtStart(context.Background(), fake, cfg)
	if res.Passed() {
		t.Fatal("self-test passed with unconfirmed containment")
	}
	if _, closed := cfg.Gate.Closed(); !closed {
		t.Error("run gate still open")
	}
	if n := len(fake.Starts()); n != 1 || len(res.Checks) != 1 {
		t.Errorf("%d started, %d checks; want the run to stop after 1", n, len(res.Checks))
	}
}

func TestServerHelperServe(t *testing.T) {
	var out strings.Builder
	if code := sandbox.SelfTestHelper([]string{"ping"}, &out); code != 0 || !strings.Contains(out.String(), "selftest ok") {
		t.Errorf("ping: code %d, output %q", code, out.String())
	}
}
