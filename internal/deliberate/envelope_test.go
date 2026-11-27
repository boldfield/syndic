package deliberate

import (
	"bytes"
	"testing"
)

const (
	am = AnswerMarker
	dm = DispositionMarker
)

func TestParseEnvelopeWellFormed(t *testing.T) {
	cases := map[string]struct{ in, ans, disp string }{
		"plain":            {am + "\nA\n" + dm + "\nD", "A", "D"},
		"multiline":        {am + "\nA1\n\nA2\n" + dm + "\nD1\nD2", "A1\n\nA2", "D1\nD2"},
		"leading empties":  {"\n\n" + am + "\nA\n" + dm + "\nD", "A", "D"},
		"trailing newline": {am + "\nA\n" + dm + "\nD\n", "A", "D\n"},
		"answer newline":   {am + "\nA\n\n" + dm + "\nD", "A\n", "D"},
		"near markers":     {am + "\n" + am + " \n " + dm + "\n" + dm + "\nD", am + " \n " + dm, "D"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			a, d, err := ParseEnvelope([]byte(c.in))
			if err != nil {
				t.Fatal(err)
			}
			if string(a) != c.ans || string(d) != c.disp {
				t.Fatalf("got %q, %q; want %q, %q", a, d, c.ans, c.disp)
			}
		})
	}
}

func TestParseEnvelopeMalformed(t *testing.T) {
	cases := map[string]struct {
		in   string
		want Reason
	}{
		"text before":             {"hello\n" + am + "\nA\n" + dm + "\nD", ReasonTextBeforeAnswer},
		"cr before":               {"\r\n" + am + "\nA\n" + dm + "\nD", ReasonTextBeforeAnswer},
		"spaces before":           {" \n" + am + "\nA\n" + dm + "\nD", ReasonTextBeforeAnswer},
		"empty input":             {"", ReasonAnswerMarkerMissing},
		"answer missing":          {"A\n" + dm + "\nD", ReasonAnswerMarkerMissing},
		"disposition missing":     {am + "\nA\n", ReasonDispositionMissing},
		"answer repeated":         {am + "\nA\n" + am + "\n" + dm + "\nD", ReasonMarkerRepeated},
		"disposition repeated":    {am + "\nA\n" + dm + "\nD\n" + dm + "\nE", ReasonMarkerRepeated},
		"answer contains marker":  {am + "\nA\n" + dm + "\nB\n" + dm + "\nD", ReasonMarkerRepeated},
		"out of order":            {dm + "\nD\n" + am + "\nA", ReasonMarkersOutOfOrder},
		"empty answer":            {am + "\n" + dm + "\nD", ReasonEmptyAnswer},
		"blank answer":            {am + "\n \t\n" + dm + "\nD", ReasonEmptyAnswer},
		"empty disposition":       {am + "\nA\n" + dm, ReasonEmptyDisposition},
		"empty disposition nl":    {am + "\nA\n" + dm + "\n", ReasonEmptyDisposition},
		"blank disposition":       {am + "\nA\n" + dm + "\n  \n\n", ReasonEmptyDisposition},
		"crlf answer marker":      {am + "\r\nA\r\n" + dm + "\r\nD", ReasonAnswerMarkerMissing},
		"crlf disposition marker": {am + "\nA\n" + dm + "\r\nD", ReasonDispositionMissing},
		"trailing space answer":   {am + " \nA\n" + dm + "\nD", ReasonAnswerMarkerMissing},
		"trailing space disp":     {am + "\nA\n" + dm + " \nD", ReasonDispositionMissing},
		"text before near marker": {am + " \n" + am + "\nA\n" + dm + "\nD", ReasonTextBeforeAnswer},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, _, err := ParseEnvelope([]byte(c.in))
			got, ok := ReasonOf(err)
			if !ok || got != c.want {
				t.Fatalf("got %v, want reason %s", err, c.want)
			}
		})
	}
}

func TestReasonsDistinct(t *testing.T) {
	all := []Reason{ReasonTextBeforeAnswer, ReasonAnswerMarkerMissing, ReasonDispositionMissing,
		ReasonMarkerRepeated, ReasonMarkersOutOfOrder, ReasonEmptyAnswer, ReasonEmptyDisposition}
	seen := map[Reason]bool{}
	for _, r := range all {
		if seen[r] {
			t.Fatalf("duplicate reason %s", r)
		}
		seen[r] = true
	}
}

func hasMarkerLine(b []byte) bool {
	for _, l := range bytes.Split(b, []byte{'\n'}) {
		if string(l) == am || string(l) == dm {
			return true
		}
	}
	return false
}

func FuzzEnvelope(f *testing.F) {
	f.Add([]byte(am+"\nA\n"+dm+"\nD"), []byte("A"), []byte("D"))
	f.Add([]byte(""), []byte("x\r\n"), []byte(" y "))
	f.Fuzz(func(t *testing.T, raw, answer, disposition []byte) {
		_, _, _ = ParseEnvelope(raw) // must not panic
		if hasMarkerLine(answer) || hasMarkerLine(disposition) ||
			len(bytes.TrimSpace(answer)) == 0 || len(bytes.TrimSpace(disposition)) == 0 {
			return
		}
		a, d, err := ParseEnvelope(RenderEnvelope(answer, disposition))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(a, answer) || !bytes.Equal(d, disposition) {
			t.Fatalf("round trip: got %q, %q; want %q, %q", a, d, answer, disposition)
		}
	})
}
