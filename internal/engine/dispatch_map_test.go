package engine

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/boldfield/syndic/internal/transport"
)

// transportStatusConstants returns the value of every TransportStatus*
// constant declared in internal/transport.
func transportStatusConstants(t *testing.T) map[string]string {
	t.Helper()
	fset := token.NewFileSet()
	paths, err := filepath.Glob("../transport/*.go")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		{
			for _, decl := range f.Decls {
				gd, ok := decl.(*ast.GenDecl)
				if !ok || gd.Tok != token.CONST {
					continue
				}
				for _, spec := range gd.Specs {
					vs := spec.(*ast.ValueSpec)
					for i, name := range vs.Names {
						if !strings.HasPrefix(name.Name, "TransportStatus") || i >= len(vs.Values) {
							continue
						}
						lit, ok := vs.Values[i].(*ast.BasicLit)
						if !ok || lit.Kind != token.STRING {
							continue
						}
						v, err := strconv.Unquote(lit.Value)
						if err != nil {
							t.Fatal(err)
						}
						out[name.Name] = v
					}
				}
			}
		}
	}
	return out
}

func TestClassifyDispatch(t *testing.T) {
	type want struct {
		nilFailure bool
		attempt    transport.CompletionStatus
		status     string
		retry      bool
		hold       bool
		state      string
	}
	errBoom := errors.New("boom")
	cases := []struct {
		name  string
		res   transport.CompleteResult
		goErr error
		want  want
	}{
		{"ok", transport.CompleteResult{TransportStatus: transport.TransportStatusOK, Status: transport.StatusComplete}, nil, want{nilFailure: true}},
		{"ok with truncated", transport.CompleteResult{TransportStatus: transport.TransportStatusOK, Status: transport.StatusTruncated}, nil, want{nilFailure: true}},
		{"unreachable", transport.CompleteResult{TransportStatus: transport.TransportStatusUnreachable}, nil, want{attempt: transport.StatusError, status: "unreachable", retry: true, state: "unreachable"}},
		{"reset", transport.CompleteResult{TransportStatus: transport.TransportStatusReset}, nil, want{attempt: transport.StatusInterrupted, status: "reset", hold: true}},
		{"timeout", transport.CompleteResult{TransportStatus: transport.TransportStatusTimeout}, nil, want{attempt: transport.StatusInterrupted, status: "timeout", hold: true}},
		{"unauthorized", transport.CompleteResult{TransportStatus: transport.TransportStatusUnauthorized}, nil, want{attempt: transport.StatusError, status: "unauthorized", state: "unauthorized"}},
		{"model_unavailable", transport.CompleteResult{TransportStatus: transport.TransportStatusModelUnavailable}, nil, want{attempt: transport.StatusError, status: "model_unavailable", state: "model_unavailable"}},
		{"cancelled is a reset", transport.CompleteResult{TransportStatus: transport.TransportStatusCancelled}, nil, want{attempt: transport.StatusInterrupted, status: "reset", hold: true}},
		{"malformed_body", transport.CompleteResult{TransportStatus: transport.TransportStatusMalformedBody}, nil, want{attempt: transport.StatusError, status: "malformed_body"}},
		{"body_too_large", transport.CompleteResult{TransportStatus: transport.TransportStatusBodyTooLarge}, nil, want{attempt: transport.StatusError, status: "body_too_large"}},
		{"http_503", transport.CompleteResult{TransportStatus: "http_503"}, nil, want{attempt: transport.StatusError, status: "http_503"}},
		{"http_404", transport.CompleteResult{TransportStatus: "http_404"}, nil, want{attempt: transport.StatusError, status: "http_404"}},
		{"not sent", transport.CompleteResult{}, fmt.Errorf("resolve: %w", transport.ErrNotSent), want{attempt: transport.StatusError, status: "not_sent:resolve: request not sent"}},
		{"go error", transport.CompleteResult{}, errBoom, want{attempt: transport.StatusInterrupted, status: "adapter_error:boom", hold: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := classifyDispatch(tc.res, tc.goErr)
			if tc.want.nilFailure {
				if f != nil {
					t.Fatalf("failure = %+v, want none", f)
				}
				return
			}
			if f == nil {
				t.Fatal("no failure")
			}
			got := want{attempt: f.attemptStatus, status: f.transportStatus, retry: f.retry, hold: f.hold, state: f.state}
			if got != tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestEveryTransportStatusHasAMapping fails when a transport status constant
// is added without deciding what it does to an attempt.
func TestEveryTransportStatusHasAMapping(t *testing.T) {
	expected := map[string]string{
		"TransportStatusOK":               "ok",
		"TransportStatusUnauthorized":     "failure",
		"TransportStatusModelUnavailable": "failure",
		"TransportStatusUnreachable":      "failure",
		"TransportStatusTimeout":          "failure",
		"TransportStatusReset":            "failure",
		"TransportStatusCancelled":        "failure",
		"TransportStatusMalformedBody":    "failure",
		"TransportStatusBodyTooLarge":     "failure",
	}
	consts := transportStatusConstants(t)
	for name, value := range consts {
		kind, ok := expected[name]
		if !ok {
			t.Errorf("%s (%q) has no expected mapping; add it to the table and to this test", name, value)
			continue
		}
		f := classifyDispatch(transport.CompleteResult{TransportStatus: value}, nil)
		switch kind {
		case "ok":
			if f != nil {
				t.Errorf("%s: failure %+v, want none", name, f)
			}
		default:
			if f == nil || f.attemptStatus == "" || f.transportStatus == "" {
				t.Errorf("%s: failure %+v, want a complete one", name, f)
			}
		}
	}
	for name := range expected {
		if _, ok := consts[name]; !ok {
			t.Errorf("%s no longer exists in internal/transport", name)
		}
	}
}
