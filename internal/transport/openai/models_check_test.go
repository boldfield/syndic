package openai

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/boldfield/syndic/internal/config"
	"github.com/boldfield/syndic/internal/transport"
)

func checkServer(t *testing.T, h http.HandlerFunc) *Adapter {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return New(config.EndpointProfile{Name: "test", BaseURL: srv.URL, Auth: &config.SecretRef{Source: "env", Name: "test-secret"}}, &mockResolver{secret: "test"})
}

func TestCheckModelsListsModels(t *testing.T) {
	a := checkServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"a"},{"id":"b"}]}`))
	})
	got, err := a.CheckModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !got.OK || !got.Usable || !got.Listed || len(got.Models) != 2 || got.Status != 200 || got.FailureClass != transport.FailureClassNone {
		t.Errorf("got %+v", got)
	}
}

func TestCheckModelsNotListed(t *testing.T) {
	for name, body := range map[string]string{
		"more":      `{"data":[{"id":"a"}],"has_more":true}`,
		"next":      `{"data":[{"id":"a"}],"next_page_token":"x"}`,
		"cut":       `{"data":[{"id":"` + strings.Repeat("a", 1<<20) + `"}]}`,
		"malformed": `not json`,
		"null data": `{"data":null}`,
	} {
		t.Run(name, func(t *testing.T) {
			a := checkServer(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) })
			got, err := a.CheckModels(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if got.Listed || got.Models != nil || !got.OK {
				t.Errorf("got %+v", got)
			}
		})
	}
}

func TestCheckModelsUsable(t *testing.T) {
	for _, code := range []int{404, 405, 501} {
		a := checkServer(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(code) })
		got, _ := a.CheckModels(context.Background())
		if got.Usable || got.OK || got.Status != code {
			t.Errorf("%d: got %+v", code, got)
		}
		if got.FailureClass == transport.FailureClassNone {
			t.Errorf("%d: no class", code)
		}
	}
	a := checkServer(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) })
	if got, _ := a.CheckModels(context.Background()); !got.Usable {
		t.Errorf("500 should stay usable: %+v", got)
	}
}

// A health check carries the same class, wait and no-retry mark as a
// completion, and as a probe step, that gets the same response.
func TestCheckModelsClassMatchesCompletionAndProbe(t *testing.T) {
	cases := map[string]struct {
		status  int
		headers map[string]string
		body    string
	}{
		"throttled": {429, map[string]string{"Retry-After": "7"}, ""},
		"unavail":   {503, map[string]string{"Retry-After": "3", "x-should-retry": "false"}, ""},
		"quota":     {402, nil, ""},
		"auth":      {401, map[string]string{"x-should-retry": "false"}, ""},
		"fault":     {500, nil, ""},
		"gateway":   {504, nil, ""},
		"request":   {400, nil, ""},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			var served atomic.Int32
			a := checkServer(t, func(w http.ResponseWriter, r *http.Request) {
				served.Add(1)
				for k, v := range c.headers {
					w.Header().Set(k, v)
				}
				w.WriteHeader(c.status)
				_, _ = w.Write([]byte(c.body))
			})
			check, err := a.CheckModels(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			comp, _ := a.Complete(context.Background(), transport.CompleteRequest{
				Model:           "m",
				Messages:        []transport.Message{{Role: "user", Content: "hi"}},
				MaxOutputTokens: 16,
			})
			var probe transport.ProbeResponse
			_, _ = a.ProbeWithOptions(context.Background(), "test", "m", transport.ProbeOptions{
				AfterResponse: func(r transport.ProbeResponse) bool { probe = r; return false },
			})
			if check.FailureClass == transport.FailureClassNone {
				t.Fatalf("no class: %+v", check)
			}
			if check.FailureClass != comp.FailureClass || check.ServerWait != comp.ServerWait || check.NoRetry != comp.NoRetry {
				t.Errorf("check %+v differs from completion %q %+v %v", check, comp.FailureClass, comp.ServerWait, comp.NoRetry)
			}
			if check.FailureClass != probe.FailureClass || check.ServerWait != probe.ServerWait || check.NoRetry != probe.NoRetry {
				t.Errorf("check %+v differs from probe %+v", check, probe)
			}
		})
	}
}

func TestCheckModelsSendsGetOnlyToModelsRoute(t *testing.T) {
	var method, path, auth string
	var n atomic.Int32
	a := checkServer(t, func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		method, path, auth = r.Method, r.URL.Path, r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"data":[]}`))
	})
	if _, err := a.CheckModels(context.Background()); err != nil {
		t.Fatal(err)
	}
	if method != "GET" || path != "/models" || n.Load() != 1 {
		t.Errorf("%s %s x%d", method, path, n.Load())
	}
	if auth != "Bearer test" {
		t.Errorf("Authorization = %q", auth)
	}
}

func TestCheckModelsDoesNotFollowRedirect(t *testing.T) {
	var n atomic.Int32
	a := checkServer(t, func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		http.Redirect(w, r, "/elsewhere", http.StatusTemporaryRedirect)
	})
	got, err := a.CheckModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n.Load() != 1 || got.Status != http.StatusTemporaryRedirect || got.OK {
		t.Errorf("requests=%d got %+v", n.Load(), got)
	}
}

func TestCheckModelsReadsAtMostTheBound(t *testing.T) {
	var read int64
	a := checkServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(make([]byte, 2<<20))
	})
	a.client.Transport = countingTransport{base: http.DefaultTransport, n: &read}
	got, err := a.CheckModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if read > modelsCheckBodyLimit || got.Listed {
		t.Errorf("read %d, got %+v", read, got)
	}
}

type countingTransport struct {
	base http.RoundTripper
	n    *int64
}

func (c countingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := c.base.RoundTrip(r)
	if resp != nil {
		resp.Body = &countingBody{ReadCloser: resp.Body, n: c.n}
	}
	return resp, err
}

type countingBody struct {
	io.ReadCloser
	n *int64
}

func (b *countingBody) Read(p []byte) (int, error) {
	k, err := b.ReadCloser.Read(p)
	*b.n += int64(k)
	return k, err
}

func TestCheckModelsNoResponseIsUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	a := New(config.EndpointProfile{Name: "test", BaseURL: url}, &mockResolver{secret: "test"})
	got, err := a.CheckModels(context.Background())
	if err == nil || got.FailureClass != transport.FailureClassUnreachable {
		t.Errorf("err %v got %+v", err, got)
	}
}
