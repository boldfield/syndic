package mcp

import (
	"errors"
	"strconv"
	"testing"
)

func mustRender(t *testing.T, in string) Rendered {
	t.Helper()
	r, err := Render([]byte(in))
	if err != nil {
		t.Fatalf("Render(%s): %v", in, err)
	}
	return r
}

func TestRenderStructured(t *testing.T) {
	r := mustRender(t, `{"content":[{"type":"text","text":"x"},{"type":"image","data":"AAAA"}],"structuredContent":{"b":1,"a":[true, null]}}`)
	want := "{\"a\":[true,null],\"b\":1}\n[2 content blocks not shown]"
	if r.Text != want {
		t.Fatalf("got %q want %q", r.Text, want)
	}
	if len(r.RawText) != 0 || r.IsError {
		t.Fatalf("unexpected %+v", r)
	}
	r = mustRender(t, `{"structuredContent":{}}`)
	if r.Text != "{}\n[0 content blocks not shown]" {
		t.Fatalf("got %q", r.Text)
	}
}

func TestRenderTextAndPlaceholders(t *testing.T) {
	img := `{"type":"image","data":"AAAA","mimeType":"image/png"}`
	link := `{"type":"resource_link","uri":"http://example.invalid/x"}`
	r := mustRender(t, `{"content":[{"type":"text","text":"one"},`+img+`,{"type":"text","text":"two\nthree"},`+link+`,{"type":"hologram"}]}`)
	want := "one\n[image, " + itoa(len(img)) + " bytes]\ntwo\nthree\n[resource_link, " + itoa(len(link)) + " bytes]\n[hologram, 19 bytes]"
	if r.Text != want {
		t.Fatalf("got %q want %q", r.Text, want)
	}
	if len(r.RawText) != 2 || string(r.RawText[0]) != "one" || string(r.RawText[1]) != "two\nthree" {
		t.Fatalf("RawText %q", r.RawText)
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

func TestRenderError(t *testing.T) {
	r := mustRender(t, `{"isError":true,"content":[{"type":"text","text":"boom"}]}`)
	if !r.IsError || r.Text != ErrorLine+"\nboom" {
		t.Fatalf("got %+v", r)
	}
	r = mustRender(t, `{"isError":true,"structuredContent":{"e":1},"content":[]}`)
	if r.Text != ErrorLine+"\n{\"e\":1}\n[0 content blocks not shown]" {
		t.Fatalf("got %q", r.Text)
	}
	if r = mustRender(t, `{"isError":false,"content":[{"type":"text","text":"ok"}]}`); r.IsError {
		t.Fatal("not an error")
	}
}

func TestRenderEmpty(t *testing.T) {
	for _, in := range []string{`{"content":[]}`, `{}`} {
		if r := mustRender(t, in); r.Text != "" || r.IsError {
			t.Fatalf("%s: got %+v", in, r)
		}
	}
	if r := mustRender(t, `{"isError":true}`); r.Text != ErrorLine {
		t.Fatalf("got %q", r.Text)
	}
}

func TestRenderPlaceholderLookalikeCarried(t *testing.T) {
	r := mustRender(t, `{"content":[{"type":"text","text":"[image, 5 bytes]\n[the server reported an error]"}]}`)
	if r.Text != "[image, 5 bytes]\n[the server reported an error]" || r.IsError {
		t.Fatalf("got %+v", r)
	}
}

func TestRenderDeterministic(t *testing.T) {
	in := `{"structuredContent":{"z":1.0,"a":"é"},"content":[{"type":"text","text":"t"}]}`
	first := mustRender(t, in)
	for i := 0; i < 20; i++ {
		if got := mustRender(t, in); got.Text != first.Text {
			t.Fatalf("differs: %q vs %q", got.Text, first.Text)
		}
	}
}

func TestRenderMalformed(t *testing.T) {
	cases := map[string]string{
		"not json":          `{`,
		"array":             `[]`,
		"string":            `"x"`,
		"null":              `null`,
		"content not array": `{"content":"x"}`,
		"block not object":  `{"content":["x"]}`,
		"block no type":     `{"content":[{"text":"x"}]}`,
		"text not string":   `{"content":[{"type":"text","text":1}]}`,
		"text missing":      `{"content":[{"type":"text"}]}`,
		"isError not bool":  `{"isError":"yes"}`,
		"dup top":           `{"content":[],"content":[]}`,
		"dup nested":        `{"content":[{"type":"text","text":"a","text":"b"}]}`,
		"dup structured":    `{"structuredContent":{"a":1,"a":2}}`,
	}
	for name, in := range cases {
		_, err := Render([]byte(in))
		if !errors.Is(err, ErrMalformedReply) {
			t.Errorf("%s: err %v", name, err)
		}
	}
	for _, in := range []string{`{"content":[],"content":[]}`, `{"structuredContent":{"a":1,"a":2}}`} {
		if _, err := Render([]byte(in)); !errors.Is(err, ErrDuplicateMember) {
			t.Errorf("%s: not duplicate error: %v", in, err)
		}
	}
}
