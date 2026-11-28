package main

import (
	"testing"

	"github.com/boldfield/syndic/internal/sandbox"
)

func TestExecOptionsWiresSupervisorAndBackend(t *testing.T) {
	dir := t.TempDir()
	o, err := execOptions(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(o.Supervisor.Command) != 2 || o.Supervisor.Command[1] != sandbox.SupervisorArg {
		t.Errorf("supervisor command %v", o.Supervisor.Command)
	}
	if o.NewBackend == nil {
		t.Error("no backend constructor")
	}
	if o.SelfTest.StateDir != dir || o.SelfTest.Helper == "" {
		t.Errorf("self-test %+v", o.SelfTest)
	}
}
