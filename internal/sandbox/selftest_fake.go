package sandbox

// FakeSelfTestScripts returns the fake backend's scripts for one self-test
// run, in the order the checks start commands: one per check, and two for
// the proxy check when networkList is set. With fail empty every command
// behaves as a conforming backend would make it; with a check named, that
// check's first command misbehaves as a non-conforming backend would, so a
// test can make each check fail in turn. For CheckDetach the failing script
// is a helper that could not hold its lock.
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
	scripts := []FakeScript{
		pick(CheckTrue, FakeScript{}),
		pick(CheckReadState, ok(selfTestOK)),
		pick(CheckWriteOutside, ok(selfTestOK)),
		pick(CheckLoopback, ok(selfTestOK)),
		pick(CheckDetach, ok(selfTestOK)),
	}
	if networkList {
		scripts = append(scripts, pick(CheckProxy, ok(selfTestOK)), ok(selfTestOK))
	}
	return scripts
}
