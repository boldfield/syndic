package jcs

import (
	"bufio"
	"bytes"
	"errors"
	"math"
	"os"
	"strconv"
	"strings"
	"testing"
)

func canon(t *testing.T, in string) string {
	t.Helper()
	out, err := Canonicalize([]byte(in))
	if err != nil {
		t.Fatalf("Canonicalize(%q): %v", in, err)
	}
	return string(out)
}

func readFile(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestRFCVectors(t *testing.T) {
	for _, n := range []string{"rfc_3_2_2", "rfc_3_2_3"} {
		got := canon(t, string(readFile(t, n+"_input.json")))
		if want := string(readFile(t, n+"_expected.json")); got != want {
			t.Errorf("%s:\n got %s\nwant %s", n, got, want)
		}
	}
}

func TestRFCNumbers(t *testing.T) {
	f, err := os.Open("testdata/rfc_numbers.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "#") || line == "" {
			continue
		}
		fields := strings.Fields(line)
		bits, err := strconv.ParseUint(fields[0], 16, 64)
		if err != nil {
			t.Fatal(err)
		}
		v := math.Float64frombits(bits)
		in := strconv.FormatFloat(v, 'e', -1, 64)
		if got := canon(t, in); got != fields[1] {
			t.Errorf("%s (%s): got %s, want %s", fields[0], in, got, fields[1])
		}
	}
}

func TestOrderAndWhitespaceInsensitive(t *testing.T) {
	a := `{"b":[1,2,{"y":1,"x":2}],"a":"s"}`
	b := " {\n\t\"a\" : \"s\" ,\r\n \"b\":[ 1 , 2 , {\"x\":2,\"y\":1} ] } "
	if !Equal([]byte(a), []byte(b)) {
		t.Error("texts differing in order and white space not equal")
	}
	if got := canon(t, b); got != `{"a":"s","b":[1,2,{"x":2,"y":1}]}` {
		t.Errorf("got %s", got)
	}
	if Equal([]byte(a), []byte(`{"a":"s"}`)) || Equal([]byte(a), []byte(`{`)) {
		t.Error("unequal texts reported equal")
	}
}

func TestDuplicates(t *testing.T) {
	for _, in := range []string{
		`{"a":1,"a":2}`,
		`{"a":1,"\u0061":2}`,
		`[1,[{"x":1,"y":{"k":0,"k":1}}]]`,
		`[{"a":1,"a":1}]`,
	} {
		if _, err := Canonicalize([]byte(in)); !errors.Is(err, ErrDuplicateMember) {
			t.Errorf("%s: got %v", in, err)
		}
	}
	// Same name in different objects is fine.
	canon(t, `{"a":{"a":1},"b":{"a":2}}`)
}

func TestUTF16Order(t *testing.T) {
	// U+FF5E sorts before U+10000 by UTF-8 byte but after it by UTF-16 unit.
	in := "{\"\uff5e\":1,\"\U00010000\":2}"
	want := "{\"\U00010000\":2,\"\uff5e\":1}"
	if got := canon(t, in); got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestNumbers(t *testing.T) {
	cases := map[string]string{
		"1.0": "1", "1e0": "1", "100": "100", "1e21": "1e+21", "1e20": "100000000000000000000",
		"0.000001": "0.000001", "1e-7": "1e-7", "-0": "0", "-0.0e5": "0", "0": "0",
		"1.7976931348623157e308":  "1.7976931348623157e+308",
		"-1.7976931348623157e308": "-1.7976931348623157e+308",
		"5e-324":                  "5e-324", "4.9406564584124654e-324": "5e-324",
		"9007199254740993": "9007199254740992", "18446744073709551616": "18446744073709552000",
		"123456789012345678901234567890": "1.2345678901234568e+29",
		"1.5":                            "1.5", "-12.25E+2": "-1225",
	}
	for in, want := range cases {
		if got := canon(t, in); got != want {
			t.Errorf("%s: got %s want %s", in, got, want)
		}
	}
	for _, in := range []string{"1e309", "-1e999", "1e-400"} {
		if _, err := Canonicalize([]byte(in)); !errors.Is(err, ErrNumber) {
			t.Errorf("%s: got %v", in, err)
		}
	}
}

func TestStrings(t *testing.T) {
	got := canon(t, `"\u0000\u001f\b\t\n\f\r\"\\\/\u007f\u00e9\ud83d\ude00"`)
	want := "\"\\u0000\\u001f\\b\\t\\n\\f\\r\\\"\\\\/\x7f\u00e9\U0001F600\""
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestRefusals(t *testing.T) {
	cases := []struct {
		in   string
		want error
	}{
		{``, ErrSyntax}, {`  `, ErrSyntax}, {`1 2`, ErrSyntax}, {`{"a":1}}`, ErrSyntax},
		{`[1,]`, ErrSyntax}, {`{"a":1,}`, ErrSyntax}, {`{a:1}`, ErrSyntax}, {`{"a" 1}`, ErrSyntax},
		{`[1`, ErrSyntax}, {`01`, ErrSyntax}, {`1.`, ErrSyntax}, {`.5`, ErrSyntax}, {`+1`, ErrSyntax},
		{`1e`, ErrSyntax}, {`tru`, ErrSyntax}, {`NaN`, ErrSyntax}, {`"a`, ErrSyntax},
		{"\"a\nb\"", ErrSyntax}, {`"\x"`, ErrSyntax}, {`"\u12"`, ErrSyntax}, {"\ufeff1", ErrSyntax},
		{"\"\xff\"", ErrUTF8}, {"\"\xc0\xaf\"", ErrUTF8},
		{`"\ud800"`, ErrSurrogate}, {`"\udc00"`, ErrSurrogate}, {`"\ud800\u0041"`, ErrSurrogate},
		{`"\ud800x"`, ErrSurrogate}, {`{"\udfff":1}`, ErrSurrogate},
	}
	for _, c := range cases {
		if _, err := Canonicalize([]byte(c.in)); !errors.Is(err, c.want) {
			t.Errorf("%q: got %v want %v", c.in, err, c.want)
		}
	}
}

func TestDepth(t *testing.T) {
	nest := func(n int) string { return strings.Repeat("[", n) + strings.Repeat("]", n) }
	if _, err := CanonicalizeLimit([]byte(nest(5)), 5); err != nil {
		t.Errorf("at limit: %v", err)
	}
	if _, err := CanonicalizeLimit([]byte(nest(6)), 5); !errors.Is(err, ErrDepth) {
		t.Errorf("one deeper: %v", err)
	}
	obj := strings.Repeat(`{"a":`, 5) + "1" + strings.Repeat("}", 5)
	if _, err := CanonicalizeLimit([]byte(obj), 5); err != nil {
		t.Errorf("objects at limit: %v", err)
	}
	if _, err := CanonicalizeLimit([]byte(obj), 4); !errors.Is(err, ErrDepth) {
		t.Errorf("objects one deeper: %v", err)
	}
	if _, err := Canonicalize([]byte(nest(DefaultMaxDepth))); err != nil {
		t.Errorf("default limit: %v", err)
	}
	if _, err := Canonicalize([]byte(nest(DefaultMaxDepth + 1))); !errors.Is(err, ErrDepth) {
		t.Errorf("default limit+1: %v", err)
	}
	// Very deep input is refused, not recursed into.
	if _, err := Canonicalize([]byte(strings.Repeat("[", 1<<20))); !errors.Is(err, ErrDepth) {
		t.Errorf("huge: %v", err)
	}
}

func FuzzCanonicalize(f *testing.F) {
	for _, s := range []string{`{"b":1,"a":[1e21,-0,"\u00e9"]}`, `[]`, `"x"`, `{"a":{"b":null}}`, `1.5e-7`, `{"a":1,"a":2}`} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, in []byte) {
		out, err := Canonicalize(in)
		if err != nil {
			return
		}
		again, err := Canonicalize(out)
		if err != nil {
			t.Fatalf("canonical output %q refused: %v", out, err)
		}
		if !bytes.Equal(out, again) {
			t.Fatalf("not idempotent: %q -> %q", out, again)
		}
	})
}
