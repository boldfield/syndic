package sandbox

import "testing"

func TestSupportRule(t *testing.T) {
	all := Caps(F, N1, N2, P, D)
	for _, list := range []bool{false, true} {
		if ok, missing := Supported(all, list); !ok || missing != 0 {
			t.Errorf("all capabilities, network list %v: supported %v, missing %s", list, ok, missing)
		}
	}
	for _, c := range []Capability{F, N1, P, D} {
		for _, list := range []bool{false, true} {
			ok, missing := Supported(all.Without(c), list)
			if ok || missing != Caps(c) {
				t.Errorf("without %s, network list %v: supported %v, missing %s", CapSet(c), list, ok, missing)
			}
		}
	}
	if ok, _ := Supported(all.Without(N2), false); !ok {
		t.Errorf("N2 is required with no network list")
	}
	if ok, missing := Supported(all.Without(N2), true); ok || missing != Caps(N2) {
		t.Errorf("without N2 under a network list: supported %v, missing %s", ok, missing)
	}
	if ok, missing := Supported(0, false); ok || missing != Caps(F, N1, P, D) {
		t.Errorf("empty set: supported %v, missing %s", ok, missing)
	}
}

func TestCapSetNames(t *testing.T) {
	if got := Caps(P, N2, F, D, N1).String(); got != "D,F,N1,N2,P" {
		t.Errorf("names %q", got)
	}
	if got := CapSet(0).Names(); got == nil || len(got) != 0 {
		t.Errorf("empty set names %#v", got)
	}
	if !Caps(F, P).Has(F) || Caps(F, P).Has(F, D) {
		t.Errorf("Has")
	}
}
