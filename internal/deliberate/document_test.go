package deliberate

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func bs(ss ...string) [][]byte {
	var out [][]byte
	for _, s := range ss {
		out = append(out, []byte(s))
	}
	return out
}

func mustDoc(t *testing.T, p, c [][]byte, d []byte) []byte {
	t.Helper()
	doc, err := Document(p, c, d)
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

func TestRoundTrip(t *testing.T) {
	p := bs("line one\nline two\n", "critique 3\nnot a header", "")
	c := bs("", "proposal 2\nx", "a\n\nb")
	d := []byte("disposition 5\nfake")
	gp, gc, gd, err := Parse(mustDoc(t, p, c, d))
	if err != nil {
		t.Fatal(err)
	}
	if len(gp) != len(p) || len(gc) != len(c) {
		t.Fatalf("counts %d/%d", len(gp), len(gc))
	}
	for i := range p {
		if !bytes.Equal(gp[i], p[i]) || !bytes.Equal(gc[i], c[i]) {
			t.Fatalf("mismatch at %d", i)
		}
	}
	if !bytes.Equal(gd, d) {
		t.Fatal("disposition mismatch")
	}
}

func TestDocumentCounts(t *testing.T) {
	if _, err := Document(bs("a"), bs("b"), nil); err == nil {
		t.Fatal("one proposal accepted")
	}
	if _, err := Document(bs("a", "b"), bs("c"), nil); err == nil {
		t.Fatal("count mismatch accepted")
	}
}

func TestHashChangesWithEachByte(t *testing.T) {
	p, c, d := bs("aa", "bb"), bs("cc", "dd"), []byte("ee")
	base := Hash(mustDoc(t, p, c, d))
	cases := map[string]string{
		"proposal":    Hash(mustDoc(t, bs("aa", "bX"), c, d)),
		"critique":    Hash(mustDoc(t, p, bs("cc", "dX"), d)),
		"disposition": Hash(mustDoc(t, p, c, []byte("eX"))),
	}
	for name, h := range cases {
		if h == base {
			t.Errorf("%s change left hash unchanged", name)
		}
	}
	if len(base) != 64 || base != strings.ToLower(base) {
		t.Errorf("hash %q not lower-case hex sha-256", base)
	}
}

func TestNeighbourMoveChangesBytes(t *testing.T) {
	a := mustDoc(t, bs("ab", "c"), bs("d", "e"), []byte("f"))
	b := mustDoc(t, bs("a", "bc"), bs("d", "e"), []byte("f"))
	e := mustDoc(t, bs("a", "b"), bs("cd", "e"), []byte("f"))
	g := mustDoc(t, bs("a", "b"), bs("c", "d"), []byte("ef"))
	h := mustDoc(t, bs("a", "b"), bs("c", "de"), []byte("f"))
	docs := [][]byte{a, b, e, g, h}
	for i := range docs {
		for j := i + 1; j < len(docs); j++ {
			if bytes.Equal(docs[i], docs[j]) {
				t.Errorf("documents %d and %d are equal", i, j)
			}
		}
	}
}

func TestParseRefuses(t *testing.T) {
	good := string(mustDoc(t, bs("a", "b"), bs("c", "d"), []byte("e")))
	cases := map[string]string{
		"wrong header":      strings.Replace(good, "v1", "v2", 1),
		"proposal after":    header + "critique 1\nc\nproposal 1\na\nproposal 1\nb\ncritique 1\nd\ndisposition 1\ne\n",
		"disposition early": header + "proposal 1\na\nproposal 1\nb\ndisposition 1\ne\ncritique 1\nc\ncritique 1\nd\n",
		"count differs":     header + "proposal 1\na\nproposal 1\nb\ncritique 1\nc\ndisposition 1\ne\n",
		"length past end":   header + "proposal 99\na\n",
		"bytes after":       good + "x",
		"field after":       good + "proposal 1\na\n",
		"missing newline":   strings.Replace(good, "proposal 1\na\n", "proposal 1\naX", 1),
		"no disposition":    header + "proposal 1\na\nproposal 1\nb\ncritique 1\nc\ncritique 1\nd\n",
		"unknown field":     header + "proposal 1\na\nproposal 1\nb\nother 1\nc\n",
		"leading zero":      strings.Replace(good, "proposal 1", "proposal 01", 1),
		"only one proposal": header + "proposal 1\na\ncritique 1\nc\ndisposition 1\ne\n",
		"empty":             "",
	}
	for name, doc := range cases {
		if _, _, _, err := Parse([]byte(doc)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, _, _, err := Parse([]byte(good)); err != nil {
		t.Fatalf("good document refused: %v", err)
	}
}

func TestGolden(t *testing.T) {
	doc := mustDoc(t, bs("first\nproposal", ""), bs("critique\n", "second critique"), []byte("integrate"))
	want, err := os.ReadFile("testdata/golden.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(doc, want) {
		t.Fatalf("document differs from golden:\n%q\nvs\n%q", doc, want)
	}
	wantHash, err := os.ReadFile("testdata/golden.sha256")
	if err != nil {
		t.Fatal(err)
	}
	if got := Hash(doc); got != strings.TrimSpace(string(wantHash)) {
		t.Fatalf("hash %s, golden %s", got, wantHash)
	}
}
