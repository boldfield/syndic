package history

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

func effectTurn(state string, effs ...Effect) Turn {
	return Turn{ID: "t1", State: "approved", Input: []byte("q"), Answer: []byte("a"), Label: "reviewed",
		EffectsState: state, Effects: effs}
}

func eff(id, state, result string) Effect {
	return Effect{ID: id, Server: "srv", Tool: "tool", Arguments: []byte(`{"k":"v"}`), State: state, Result: []byte(result)}
}

func v2(t *testing.T, turns ...Turn) *Document {
	t.Helper()
	d := NewV2()
	for _, tr := range turns {
		if err := d.AddTurn(tr); err != nil {
			t.Fatal(err)
		}
	}
	return d
}

func TestV2PreambleAndRoundTrip(t *testing.T) {
	if !strings.HasPrefix(NewV2().String(), "syndic-history/v2\n") {
		t.Fatal("not v2")
	}
	d := v2(t, effectTurn("stopped", eff("e1", EffectDone, "ok"), eff("e2", EffectFailed, "boom"),
		eff("e3", EffectUncertain, ""), eff("e4", EffectNotRun, "")))
	p, err := Parse(d.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if string(p.Bytes()) != string(d.Bytes()) || p.Hash() != d.Hash() {
		t.Fatal("round trip differs")
	}
	got := p.Included()[0]
	if got.EffectsState != "stopped" || len(got.Effects) != 4 || string(got.Effects[1].Result) != "boom" || got.Effects[2].State != EffectUncertain {
		t.Fatalf("fields: %+v", got)
	}
	want := "effects 7\nstopped\neffect "
	if !strings.Contains(d.String(), want) {
		t.Fatalf("missing %q in %q", want, d.String())
	}
}

func TestEffectResultCarriedForDoneAndFailed(t *testing.T) {
	s := v2(t, effectTurn("closed", eff("e1", EffectDone, "RES1"), eff("e2", EffectFailed, "RES2"))).String()
	if !strings.Contains(s, "result 4\nRES1\n") || !strings.Contains(s, "result 4\nRES2\n") {
		t.Fatal(s)
	}
}

func TestEffectOverBoundIsRef(t *testing.T) {
	big := strings.Repeat("x", MaxHistoryResultBytes+1)
	atBound := strings.Repeat("y", MaxHistoryResultBytes)
	d := v2(t, effectTurn("applied", eff("e1", EffectDone, big), eff("e2", EffectDone, atBound)))
	s := d.String()
	sum := sha256.Sum256([]byte(big))
	ref := "4097 " + hex.EncodeToString(sum[:])
	if !strings.Contains(s, "result-ref 69\n"+ref+"\n") || strings.Contains(s, big) {
		t.Fatalf("no ref: %.300q", s)
	}
	if !strings.Contains(s, atBound) {
		t.Fatal("bound-size result must be carried")
	}
	p, err := Parse(d.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if p.Included()[0].Effects[0].ResultRef != ref || p.Hash() != d.Hash() {
		t.Fatal("ref not parsed back")
	}
}

func TestEffectUncertainAndNotRunCarryNoResult(t *testing.T) {
	if err := NewV2().AddTurn(effectTurn("stopped", eff("e1", EffectUncertain, "late"))); err == nil {
		t.Fatal("uncertain with result accepted")
	}
	if err := NewV2().AddTurn(effectTurn("appliable", eff("e1", EffectNotRun, "x"))); err == nil {
		t.Fatal("not run with result accepted")
	}
	s := v2(t, effectTurn("stopped", eff("e1", EffectUncertain, ""))).String()
	if strings.Contains(s, "result") {
		t.Fatal(s)
	}
}

func TestHistoryEffectStateMapping(t *testing.T) {
	for in, want := range map[string]string{"pending": "not run", "skipped": "not run", "done": "done", "failed": "failed", "uncertain": "uncertain"} {
		got, err := HistoryEffectState(in)
		if err != nil || got != want {
			t.Fatalf("%s: %q %v", in, got, err)
		}
	}
	if _, err := HistoryEffectState("dispatching"); err == nil {
		t.Fatal("dispatching mapped")
	}
}

func TestSentEffectOfDiscardedSetKeepsState(t *testing.T) {
	s := v2(t, effectTurn("discarded", eff("e1", EffectDone, "ok"), eff("e2", EffectNotRun, ""))).String()
	if !strings.Contains(s, "effects 9\ndiscarded\n") || !strings.Contains(s, "state 4\ndone\n") || !strings.Contains(s, "state 7\nnot run\n") {
		t.Fatal(s)
	}
}

func TestEffectStateChangesHashAndTrimDropsFields(t *testing.T) {
	a := v2(t, effectTurn("stopped", eff("e1", EffectUncertain, "")))
	b := v2(t, effectTurn("stopped", eff("e1", EffectNotRun, "")))
	if a.Hash() == b.Hash() {
		t.Fatal("hash unchanged")
	}
	d := v2(t, effectTurn("applied", eff("e1", EffectDone, "ok")))
	if err := d.AddTurn(Turn{ID: "t2", State: "approved", Input: []byte("q"), Answer: []byte("a"), Label: "reviewed"}); err != nil {
		t.Fatal(err)
	}
	if err := d.SetContextFromTurn("t2"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(d.String(), "effect") {
		t.Fatal(d.String())
	}
}

func TestV1DocumentStillLoadsAndVerifies(t *testing.T) {
	for _, g := range goldens {
		p, err := Parse([]byte(g.bytes))
		if err != nil {
			t.Fatalf("%s: %v", g.name, err)
		}
		if string(p.Bytes()) != g.bytes || p.Hash() != g.hash {
			t.Fatalf("%s: v1 not preserved", g.name)
		}
	}
	if err := New().AddTurn(effectTurn("applied", eff("e1", EffectDone, "ok"))); err == nil {
		t.Fatal("v1 accepted effects")
	}
	if _, err := Parse([]byte("syndic-history/v1\nturn 1\nt\nstate 8\napproved\ninput 0\n\nanswer 0\n\nlabel 8\nreviewed\neffects 7\napplied\n")); err == nil {
		t.Fatal("v1 with effects parsed")
	}
}

func TestParseRejectsBadEffects(t *testing.T) {
	good := v2(t, effectTurn("applied", eff("e1", EffectDone, "ok"))).String()
	for name, bad := range map[string]string{
		"bad state":    strings.Replace(good, "applied", "weird", 1),
		"bad effstate": strings.Replace(good, "state 4\ndone", "state 4\ndonX", 1),
		"short ref":    strings.Replace(good, "result 2\nok", "result-ref 3\n1 a", 1),
	} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}
