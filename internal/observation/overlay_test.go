package observation

import (
	"reflect"
	"testing"
)

func TestStagedChildren(t *testing.T) {
	st := map[string]string{"a": "h", "d/x": "h", "d/y/z": "h", "e/q": "h"}
	if got := StagedChildren(st, Root); !reflect.DeepEqual(got, map[string]string{"a": "f", "d": "d", "e": "d"}) {
		t.Fatalf("root: %v", got)
	}
	if got := StagedChildren(st, "d"); !reflect.DeepEqual(got, map[string]string{"x": "f", "y": "d"}) {
		t.Fatalf("d: %v", got)
	}
	if got := StagedChildren(st, "a"); len(got) != 0 {
		t.Fatalf("a: %v", got)
	}
}

func TestHasStagedDescendantAndAncestors(t *testing.T) {
	st := map[string]string{"d/x": "h", "dd": "h"}
	if !HasStagedDescendant(st, "d") || HasStagedDescendant(st, "dd") || HasStagedDescendant(st, "d/x") {
		t.Fatal("descendant detection wrong")
	}
	if got := Ancestors("a/b/c"); !reflect.DeepEqual(got, []string{"a", "a/b"}) {
		t.Fatalf("got %v", got)
	}
	if got := Ancestors("a"); len(got) != 0 {
		t.Fatalf("got %v", got)
	}
}
