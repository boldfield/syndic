package history

import (
	"bytes"
	"errors"
	"math/rand"
	"strconv"
	"strings"
	"testing"
)

func build(t *testing.T, turns ...Turn) *Document {
	t.Helper()
	d := New()
	for _, tr := range turns {
		if err := d.AddTurn(tr); err != nil {
			t.Fatalf("AddTurn(%q): %v", tr.ID, err)
		}
	}
	return d
}

var goldens = []struct {
	name  string
	turns []Turn
	bytes string
	hash  string
}{
	{
		name:  "empty",
		turns: nil,
		bytes: "syndic-history/v1\n",
		hash:  "032325920ed3077e386903e2fc66a91810cce8b5dd8a53463eec518b799f4ee7",
	},
	{
		name:  "simple",
		turns: []Turn{{ID: "t1", State: "approved", Input: []byte("hi"), Answer: []byte("yo"), Label: "reviewed"}},
		bytes: "syndic-history/v1\nturn 2\nt1\nstate 8\napproved\ninput 2\nhi\nanswer 2\nyo\nlabel 8\nreviewed\n",
		hash:  "0c3a0fe89df98f389fe8e886505e64d4fcfb257633ede8dc6bf1380597d0f6a8",
	},
	{
		name:  "newlines",
		turns: []Turn{{ID: "t1", State: "approved", Input: []byte("a\n\nb\n"), Answer: []byte("\nanswer 1\nx\n"), Label: "reviewed"}},
		bytes: "syndic-history/v1\nturn 2\nt1\nstate 8\napproved\ninput 5\na\n\nb\n\nanswer 12\n\nanswer 1\nx\n\nlabel 8\nreviewed\n",
		hash:  "9b10f839c7e21c7d8324cb40eaee4941668edd29d1e8f8822156bcd856ebb0cb",
	},
	{
		name:  "multibyte",
		turns: []Turn{{ID: "t1", State: "approved", Input: []byte("héllo 世界"), Answer: []byte("🙂"), Label: "human override"}},
		bytes: "syndic-history/v1\nturn 2\nt1\nstate 8\napproved\ninput 13\nhéllo 世界\nanswer 4\n🙂\nlabel 14\nhuman override\n",
		hash:  "066c4af0ae96bca91486348c5c1792576ca00989ab2843fece168b223edbbc2d",
	},
	{
		name:  "missing",
		turns: []Turn{{ID: "t1", State: "cancelled", Input: []byte("q"), Answer: []byte("secret"), Label: "reviewed"}},
		bytes: "syndic-history/v1\nturn 2\nt1\nstate 9\ncancelled\ninput 1\nq\nanswer 30\nno approved answer (cancelled)\nlabel 8\nreviewed\n",
		hash:  "1d176c8eb029b81a8018405deed764ef8ad360065edd5fca9b0eee1e6c2e75ba",
	},
	{
		name: "changes",
		turns: []Turn{{ID: "t1", State: "overridden", Input: []byte("q"), Answer: []byte("a"), Label: "human override",
			ChangesState: "discarded",
			Changes:      []Change{{"a.go", "applied"}, {"b.go", "not applied"}, {"c d.go", "conflict"}}}},
		bytes: "syndic-history/v1\nturn 2\nt1\nstate 10\noverridden\ninput 1\nq\nanswer 1\na\nlabel 14\nhuman override\nchanges 9\ndiscarded\nchange 12\napplied a.go\nchange 16\nnot applied b.go\nchange 15\nconflict c d.go\n",
		hash:  "942fae7a6cc06984a70b537bf4beeb082b1b799b9dd7c95f1c6c8e379d040da6",
	},
	{
		name: "multi",
		turns: []Turn{
			{ID: "t1", State: "approved", Input: []byte("one"), Answer: []byte("1"), Label: "reviewed"},
			{ID: "t2", State: "incomplete", Input: []byte("two"), Label: "unreviewed (solo)"},
			{ID: "t3", State: "solo_complete", Input: []byte("três"), Answer: []byte("3"), Label: "unreviewed (solo)"},
		},
		bytes: "syndic-history/v1\n" +
			"turn 2\nt1\nstate 8\napproved\ninput 3\none\nanswer 1\n1\nlabel 8\nreviewed\n" +
			"turn 2\nt2\nstate 10\nincomplete\ninput 3\ntwo\nanswer 31\nno approved answer (incomplete)\nlabel 17\nunreviewed (solo)\n" +
			"turn 2\nt3\nstate 13\nsolo_complete\ninput 5\ntrês\nanswer 1\n3\nlabel 17\nunreviewed (solo)\n",
		hash: "3bdc79da23384b07be23836aa58c12691a3af40a96a4148e4ca01bfffc3b5ecf",
	},
}

func TestGoldenBytesAndHash(t *testing.T) {
	for _, g := range goldens {
		t.Run(g.name, func(t *testing.T) {
			d := build(t, g.turns...)
			if got := d.String(); got != g.bytes {
				t.Fatalf("bytes:\n got %q\nwant %q", got, g.bytes)
			}
			if got := d.Hash(); got != g.hash {
				t.Fatalf("hash = %s, want %s", got, g.hash)
			}
		})
	}
}

func TestParseRoundtripIsByteExact(t *testing.T) {
	for _, g := range goldens {
		t.Run(g.name, func(t *testing.T) {
			p, err := Parse([]byte(g.bytes))
			if err != nil {
				t.Fatal(err)
			}
			if got := p.String(); got != g.bytes {
				t.Fatalf("roundtrip:\n got %q\nwant %q", got, g.bytes)
			}
		})
	}
	d := build(t, goldens[2].turns...)
	p, _ := Parse(d.Bytes())
	tr := p.Included()[0]
	if !bytes.Equal(tr.Input, []byte("a\n\nb\n")) || !bytes.Equal(tr.Answer, []byte("\nanswer 1\nx\n")) {
		t.Fatalf("content not preserved: %q %q", tr.Input, tr.Answer)
	}
}

func TestApplyAnnotationsChangeHash(t *testing.T) {
	base := Turn{ID: "t", State: "approved", Input: []byte("q"), Answer: []byte("a"), Label: "reviewed"}
	variants := []Turn{base}
	for _, mut := range []func(*Turn){
		func(x *Turn) { x.ChangesState = "appliable" },
		func(x *Turn) { x.ChangesState = "applied" },
		func(x *Turn) { x.ChangesState = "applied"; x.Changes = []Change{{"f", "applied"}} },
		func(x *Turn) { x.ChangesState = "applied"; x.Changes = []Change{{"f", "not applied"}} },
		func(x *Turn) { x.ChangesState = "applied"; x.Changes = []Change{{"f", "conflict"}} },
		func(x *Turn) { x.Label = "human override" },
	} {
		v := base
		mut(&v)
		variants = append(variants, v)
	}
	seen := map[string]int{}
	for i, v := range variants {
		h := build(t, v).Hash()
		if j, ok := seen[h]; ok {
			t.Fatalf("variants %d and %d share hash", j, i)
		}
		seen[h] = i
	}
}

func TestMissingAnswerIgnoresSuppliedAnswer(t *testing.T) {
	for _, st := range []string{"incomplete", "cancelled"} {
		d := build(t, Turn{ID: "t", State: st, Input: []byte("q"), Answer: []byte("secret"), Label: "reviewed"})
		if bytes.Contains(d.Bytes(), []byte("secret")) {
			t.Fatalf("%s: unapproved answer leaked", st)
		}
		if !bytes.Contains(d.Bytes(), []byte("no approved answer ("+st+")")) {
			t.Fatalf("%s: marker missing", st)
		}
	}
}

func TestValidation(t *testing.T) {
	ok := Turn{ID: "t", State: "approved", Label: "reviewed"}
	cases := map[string]func(*Turn){
		"empty id":        func(x *Turn) { x.ID = "" },
		"bad state":       func(x *Turn) { x.State = "weird" },
		"bad label":       func(x *Turn) { x.Label = "x" },
		"bad changes":     func(x *Turn) { x.ChangesState = "bogus\nstate" },
		"changes missing": func(x *Turn) { x.Changes = []Change{{"a", "applied"}} },
		"bad outcome":     func(x *Turn) { x.ChangesState = "applied"; x.Changes = []Change{{"a", "done"}} },
		"empty path":      func(x *Turn) { x.ChangesState = "applied"; x.Changes = []Change{{"", "applied"}} },
	}
	for name, mut := range cases {
		v := ok
		mut(&v)
		if err := New().AddTurn(v); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	d := build(t, ok)
	if err := d.AddTurn(ok); err == nil {
		t.Error("duplicate id accepted")
	}
	build(t, Turn{ID: "t", State: "approved", Label: "reviewed", ChangesState: "applied",
		Changes: []Change{{"a not", "not applied"}, {"a applied", "applied"}}})
	p, err := Parse([]byte("syndic-history/v1\nturn 1\nt\nstate 8\napproved\ninput 0\n\nanswer 0\n\nlabel 8\nreviewed\nchanges 7\napplied\nchange 16\nnot applied a.go\n"))
	if err != nil || p.Included()[0].Changes[0] != (Change{"a.go", "not applied"}) {
		t.Fatalf("parse change: %v %+v", err, p)
	}
}

const noChangeTurn = "syndic-history/v1\nturn 1\nt\nstate 8\napproved\ninput 0\n\nanswer 0\n\nlabel 8\nreviewed\nchanges 7\napplied\n"

func changeTurn(cs ...Change) Turn {
	return Turn{ID: "t", State: "approved", Label: "reviewed", ChangesState: "applied", Changes: cs}
}

func turnsEqual(a, b []Turn) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		x, y := a[i], b[i]
		if x.ID != y.ID || x.State != y.State || !bytes.Equal(x.Input, y.Input) || !bytes.Equal(x.Answer, y.Answer) ||
			x.Label != y.Label || x.ChangesState != y.ChangesState || len(x.Changes) != len(y.Changes) {
			return false
		}
		for j := range x.Changes {
			if x.Changes[j] != y.Changes[j] {
				return false
			}
		}
	}
	return true
}

var outcomes = []string{OutcomeApplied, OutcomeNotApplied, OutcomeConflict}

func TestChangeWrittenOutcomeFirst(t *testing.T) {
	d := build(t, changeTurn(Change{"a\nb", "applied"}, Change{"c\r\n", "conflict"}, Change{"é 世", "not applied"}))
	want := noChangeTurn +
		"change 11\napplied a\nb\n" +
		"change 12\nconflict c\r\n\n" +
		"change 18\nnot applied é 世\n"
	if string(d.Bytes()) != want {
		t.Fatalf("bytes:\n%q\nwant:\n%q", d.Bytes(), want)
	}
}

func TestFormerlyCollidingChangesDiffer(t *testing.T) {
	a := build(t, changeTurn(Change{"a not", "applied"}))
	b := build(t, changeTurn(Change{"a", "not applied"}))
	if bytes.Equal(a.Bytes(), b.Bytes()) || a.Hash() == b.Hash() {
		t.Fatal("distinct changes share bytes or hash")
	}
	for _, d := range []*Document{a, b} {
		p, err := Parse(d.Bytes())
		if err != nil {
			t.Fatal(err)
		}
		if !turnsEqual(p.Included(), d.Included()) {
			t.Fatalf("read back %+v, want %+v", p.Included(), d.Included())
		}
	}
}

func TestChangePathsRoundTripByteExact(t *testing.T) {
	paths := []string{
		"a b", "a\nb", "a\r\nb", "héllo/世界/🙂",
		"applied x", "not applied x", "conflict x",
		"x not", "x applied", "\xff\xfe\x80 bad", "\xc3",
	}
	for _, p := range paths {
		for _, o := range outcomes {
			d := build(t, changeTurn(Change{p, o}))
			got, err := Parse(d.Bytes())
			if err != nil {
				t.Fatalf("%q/%s: %v", p, o, err)
			}
			if c := got.Included()[0].Changes[0]; c != (Change{p, o}) {
				t.Fatalf("read back %+v, want %q/%s", c, p, o)
			}
			if !bytes.Equal(got.Bytes(), d.Bytes()) {
				t.Fatalf("%q/%s: bytes differ", p, o)
			}
		}
	}
}

func TestChangeRoundTripProperty(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	alphabet := []byte("ab \r\n\x00\xff\xc3\xa9")
	words := []string{"applied ", "not applied ", "conflict ", "applied", " not"}
	for i := 0; i < 300; i++ {
		tr := changeTurn()
		n := rng.Intn(4) + 1
		for j := 0; j < n; j++ {
			var p []byte
			if rng.Intn(3) == 0 {
				p = append(p, words[rng.Intn(len(words))]...)
			}
			for k := rng.Intn(6); k >= 0; k-- {
				p = append(p, alphabet[rng.Intn(len(alphabet))])
			}
			tr.Changes = append(tr.Changes, Change{string(p), outcomes[rng.Intn(3)]})
		}
		d := build(t, tr)
		got, err := Parse(d.Bytes())
		if err != nil {
			t.Fatalf("iteration %d: %v", i, err)
		}
		if !bytes.Equal(got.Bytes(), d.Bytes()) || !turnsEqual(got.Included(), d.Included()) {
			t.Fatalf("iteration %d: round trip differs for %+v", i, tr.Changes)
		}
	}
}

func FuzzChangeRoundTrip(f *testing.F) {
	f.Add("a not", 0)
	f.Add("not applied x", 1)
	f.Add("\xff\r\n", 2)
	f.Fuzz(func(t *testing.T, path string, o int) {
		if path == "" {
			return
		}
		if o < 0 {
			o = -o
		}
		d := build(t, changeTurn(Change{path, outcomes[o%3]}))
		got, err := Parse(d.Bytes())
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got.Bytes(), d.Bytes()) || !turnsEqual(got.Included(), d.Included()) {
			t.Fatal("round trip differs")
		}
	})
}

func TestParseRejectsBadChangePayloads(t *testing.T) {
	for name, payload := range map[string]string{
		"no outcome":    "a.go",
		"empty path":    "applied ",
		"empty path np": "not applied ",
		"empty payload": "",
		"path first":    "a applied",
		"path first np": "a not applied",
		"no space":      "appliedx",
		"outcome only":  "conflict",
	} {
		doc := noChangeTurn + "change " + strconv.Itoa(len(payload)) + "\n" + payload + "\n"
		if _, err := Parse([]byte(doc)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestOutcomeAndChangesStateChangeHash(t *testing.T) {
	seen := map[string]string{}
	for _, st := range []string{"appliable", "applied", "partial", "conflict", "discarded"} {
		for _, o := range outcomes {
			tr := changeTurn(Change{"f", o})
			tr.ChangesState = st
			h := build(t, tr).Hash()
			if prev, ok := seen[h]; ok {
				t.Fatalf("%s/%s collides with %s", st, o, prev)
			}
			seen[h] = st + "/" + o
		}
	}
}

func TestParseRejectsNonCanonical(t *testing.T) {
	const hdr = "syndic-history/v1\n"
	rest := "state 8\napproved\ninput 0\n\nanswer 0\n\nlabel 8\nreviewed\n"
	bad := map[string]string{
		"no preamble":      "turn 1\nt\n" + rest,
		"plus sign":        hdr + "turn +1\nt\n" + rest,
		"minus sign":       hdr + "turn -1\nt\n" + rest,
		"leading zero":     hdr + "turn 01\nt\n" + rest,
		"double space":     hdr + "turn  1\nt\n" + rest,
		"trailing garbage": hdr + "turn 1x\nt\n" + rest,
		"trailing token":   hdr + "turn 1 x\nt\n" + rest,
		"empty name":       hdr + " 1\nt\n" + rest,
		"unknown name":     hdr + "bogus 1\nt\n" + rest,
		"empty length":     hdr + "turn \nt\n" + rest,
		"no space":         hdr + "turn1\nt\n" + rest,
		"huge length":      hdr + "turn 99999999999999999999\nt\n" + rest,
		"short content":    hdr + "turn 5\nt\n" + rest,
		"no trailing nl":   hdr + "turn 1\ntXstate 8\napproved\n",
		"truncated":        hdr + "turn 1\nt\nstate 8\napproved\n",
		"wrong order":      hdr + "state 8\napproved\nturn 1\nt\n",
		"bad state":        hdr + "turn 1\nt\nstate 1\nx\ninput 0\n\nanswer 0\n\nlabel 8\nreviewed\n",
		"missing line":     hdr + "turn 1\nt\nstate 9\ncancelled\ninput 0\n\nanswer 0\n\nlabel 8\nreviewed\n",
		"bad change":       hdr + "turn 1\nt\n" + rest + "changes 7\napplied\nchange 3\na.b\n",
		"change no state":  hdr + "turn 1\nt\n" + rest + "change 9\napplied a.b\n",
		"dup turn":         hdr + "turn 1\nt\n" + rest + "turn 1\nt\n" + rest,
	}
	for name, doc := range bad {
		if _, err := Parse([]byte(doc)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := Parse([]byte(hdr + "turn 1\nt\n" + rest)); err != nil {
		t.Fatalf("control rejected: %v", err)
	}
}

func TestTrimSelection(t *testing.T) {
	mk := func(id string) Turn {
		return Turn{ID: id, State: "approved", Input: []byte(id), Answer: []byte(id), Label: "reviewed"}
	}
	d := build(t, mk("a"), mk("b"), mk("c"))
	if n := len(d.Included()); n != 3 {
		t.Fatalf("absent trim included %d", n)
	}
	if err := d.SetContextFromTurn("b"); err != nil {
		t.Fatal(err)
	}
	inc := d.Included()
	if len(inc) != 2 || inc[0].ID != "b" || inc[1].ID != "c" {
		t.Fatalf("included = %+v", inc)
	}
	want := build(t2(t), mk("b"), mk("c"))
	if d.String() != want.String() || d.Hash() != want.Hash() {
		t.Fatal("trimmed document differs from one built from the included turns")
	}
	if len(d.RenderForAuthor()) != 4 {
		t.Fatal("author rendering must follow the trim")
	}
	if err := d.SetContextFromTurn("nope"); err == nil {
		t.Fatal("unknown turn accepted")
	}
	if d.String() != want.String() {
		t.Fatal("failed trim changed the document")
	}
}

func t2(t *testing.T) *testing.T { return t }

func TestRenderForAuthorExact(t *testing.T) {
	d := build(t,
		Turn{ID: "t1", State: "approved", Input: []byte("q\nx"), Answer: []byte("a\nchanges 1\nz"), Label: "reviewed",
			ChangesState: "applied", Changes: []Change{{"f.go", "applied"}}},
		Turn{ID: "t2", State: "cancelled", Input: []byte("q2"), Label: "reviewed"},
	)
	want := []Message{
		{"user", "turn 2\nt1\nstate 8\napproved\ninput 3\nq\nx\nlabel 8\nreviewed\nchanges 7\napplied\nchange 12\napplied f.go\n"},
		{"assistant", "a\nchanges 1\nz"},
		{"user", "turn 2\nt2\nstate 9\ncancelled\ninput 2\nq2\nlabel 8\nreviewed\n"},
		{"assistant", "no approved answer (cancelled)"},
	}
	got := d.RenderForAuthor()
	if len(got) != len(want) {
		t.Fatalf("got %d messages", len(got))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("message %d:\n got %+v\nwant %+v", i, got[i], want[i])
		}
	}
}

// Documents with different hashes must render differently, including the
// pair a lossy rendering would conflate.
func TestRenderForAuthorInjective(t *testing.T) {
	a := build(t, Turn{ID: "t", State: "approved", Input: []byte("q"), Answer: []byte("x\nchanges:\napplied f.go"), Label: "reviewed"})
	b := build(t, Turn{ID: "t", State: "approved", Input: []byte("q"), Answer: []byte("x"), Label: "reviewed",
		ChangesState: "applied", Changes: []Change{{"f.go", "applied"}}})
	c := build(t, Turn{ID: "u", State: "approved", Input: []byte("q"), Answer: []byte("x"), Label: "reviewed"})
	d := build(t, Turn{ID: "t", State: "approved", Input: []byte("q"), Answer: []byte("x"), Label: "human override"})
	docs := []*Document{a, b, c, d}
	seen := map[string]int{}
	for i, x := range docs {
		var sb strings.Builder
		for _, m := range x.RenderForAuthor() {
			sb.WriteString(m.Role + "\x00" + m.Content + "\x01")
		}
		if j, ok := seen[sb.String()]; ok {
			t.Fatalf("documents %d and %d render identically", j, i)
		}
		seen[sb.String()] = i
	}
}

// Documents that differ only in their change lines must render differently
// for both the author and the reviewer.
func TestRenderChangeLinesInjective(t *testing.T) {
	mk := func(c ...Change) *Document {
		return build(t, Turn{ID: "t", State: "approved", Input: []byte("q"), Answer: []byte("x"), Label: "reviewed",
			ChangesState: "applied", Changes: c})
	}
	docs := []*Document{
		mk(Change{"a not", "applied"}),
		mk(Change{"a", "not applied"}),
		mk(Change{"a", "applied"}),
		mk(Change{"a", "conflict"}),
		mk(Change{"b", "applied"}),
		mk(Change{"a", "applied"}, Change{"b", "applied"}),
	}
	entropy := func() *bytes.Reader { return bytes.NewReader(bytes.Repeat([]byte{0xab}, 16)) }
	authors := map[string]int{}
	reviewers := map[string]int{}
	hashes := map[string]int{}
	for i, x := range docs {
		if j, ok := hashes[x.Hash()]; ok {
			t.Fatalf("documents %d and %d share a hash", j, i)
		}
		hashes[x.Hash()] = i
		var sb strings.Builder
		for _, m := range x.RenderForAuthor() {
			sb.WriteString(m.Role + "\x00" + m.Content + "\x01")
		}
		if j, ok := authors[sb.String()]; ok {
			t.Fatalf("documents %d and %d render identically for the author", j, i)
		}
		authors[sb.String()] = i
		r, err := x.RenderForReviewer(entropy())
		if err != nil {
			t.Fatal(err)
		}
		if j, ok := reviewers[r]; ok {
			t.Fatalf("documents %d and %d render identically for the reviewer", j, i)
		}
		reviewers[r] = i
	}
}

func TestRenderForReviewer(t *testing.T) {
	d := build(t, goldens[2].turns...)
	out, err := d.RenderForReviewer(bytes.NewReader(bytes.Repeat([]byte{0xab}, 16)))
	if err != nil {
		t.Fatal(err)
	}
	delim := strings.Repeat("ab", 16)
	want := "===== " + delim + " BEGIN HISTORY =====\n" + d.String() + "===== " + delim + " END HISTORY =====\n"
	if out != want {
		t.Fatalf("got %q want %q", out, want)
	}

	// A document containing the delimiter is refused.
	hostile := build(t, Turn{ID: "t", State: "approved", Input: []byte("x" + delim), Answer: []byte("y"), Label: "reviewed"})
	if _, err := hostile.RenderForReviewer(bytes.NewReader(bytes.Repeat([]byte{0xab}, 16))); !errors.Is(err, ErrDelimiterCollision) {
		t.Fatalf("err = %v, want ErrDelimiterCollision", err)
	}
	if _, err := d.RenderForReviewer(bytes.NewReader(nil)); err == nil {
		t.Fatal("short entropy accepted")
	}
}
