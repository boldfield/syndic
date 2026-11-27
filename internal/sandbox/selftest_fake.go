package sandbox

import (
	"fmt"
	"os"
	"syscall"
)

// FakeSelfTestScripts returns the fake backend's scripts for one self-test
// run, in the order the checks start commands: one per check, and two for
// the proxy check when networkList is set. With fail empty every command
// behaves as a conforming backend would make it; with a check named, that
// check's first command misbehaves as a non-conforming backend would, so a
// test can make each check fail in turn. For CheckDetach the failing script
// reports a process that still exists.
func FakeSelfTestScripts(fail SelfTestCheck, networkList bool) []FakeScript {
	ok := func(out string) FakeScript {
		return FakeScript{Chunks: []FakeChunk{{Data: []byte(out + "\n")}}}
	}
	bad := FakeScript{Chunks: []FakeChunk{{Data: []byte("misbehaved\n")}}, Exit: ExitStatus{Code: 1}}
	pick := func(c SelfTestCheck, good FakeScript) FakeScript {
		if c == fail {
			if c == CheckTrue {
				return FakeScript{Exit: ExitStatus{Code: 1}}
			}
			return bad
		}
		return good
	}
	detached := ok(fmt.Sprintf("%s %d", selfTestOK, unusedPID()))
	if fail == CheckDetach {
		detached = ok(fmt.Sprintf("%s %d", selfTestOK, os.Getpid()))
	}
	scripts := []FakeScript{
		pick(CheckTrue, FakeScript{}),
		pick(CheckReadState, ok(selfTestOK)),
		pick(CheckWriteOutside, ok(selfTestOK)),
		pick(CheckLoopback, ok(selfTestOK)),
		detached,
	}
	if networkList {
		scripts = append(scripts, pick(CheckProxy, ok(selfTestOK)), ok(selfTestOK))
	}
	return scripts
}

// unusedPID returns a process id no process holds.
func unusedPID() int {
	for pid := 4_000_000; pid > 1_000_000; pid-- {
		if err := syscall.Kill(pid, 0); err == syscall.ESRCH {
			return pid
		}
	}
	return 4_000_000
}
