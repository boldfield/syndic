package openai

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/config"
	"github.com/boldfield/syndic/internal/transport"
)

const classOKBody = `{"model":"m","choices":[{"message":{"content":"hi"},"finish_reason":"stop"}]}`

func classAdapter(url string, mutate func(*config.EndpointProfile), opts ...AdapterOption) *Adapter {
	p := config.EndpointProfile{Name: "t", BaseURL: url}
	if mutate != nil {
		mutate(&p)
	}
	return NewWithOptions(p, &mockResolver{secret: "s"}, opts...)
}

func classReq() transport.CompleteRequest {
	return transport.CompleteRequest{
		Envelope: transport.AttemptEnvelope{AttemptID: "a1"},
		Model:    "m",
		Messages: []transport.Message{{Role: "user", Content: "hi"}},
	}
}

func serve(t *testing.T, h http.HandlerFunc) *httptest.Server {
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	return s
}

func TestCompleteClassStatusTable(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		body       string
		badGateway string
		want       transport.FailureClass
	}{
		{"429", 429, `{}`, "", transport.FailureClassThrottled},
		{"503", 503, `{}`, "", transport.FailureClassThrottled},
		{"529", 529, `{}`, "", transport.FailureClassThrottled},
		{"402", 402, `{}`, "", transport.FailureClassQuota},
		{"quota marker", 429, `{"error":{"code":"insufficient_quota"}}`, "", transport.FailureClassQuota},
		{"500", 500, `{}`, "", transport.FailureClassServerFault},
		{"507", 507, `{}`, "", transport.FailureClassServerFault},
		{"502", 502, `{}`, "", transport.FailureClassUncertain},
		{"502 server_fault", 502, `{}`, config.BadGatewayServerFault, transport.FailureClassServerFault},
		{"504", 504, `{}`, "", transport.FailureClassUncertain},
		{"408", 408, `{}`, "", transport.FailureClassUncertain},
		{"401", 401, `{}`, "", transport.FailureClassRejectedConfig},
		{"403", 403, `{}`, "", transport.FailureClassRejectedConfig},
		{"404", 404, `{}`, "", transport.FailureClassRejectedConfig},
		{"501", 501, `{}`, "", transport.FailureClassRejectedConfig},
		{"505", 505, `{}`, "", transport.FailureClassRejectedConfig},
		{"400", 400, `{}`, "", transport.FailureClassRejectedRequest},
		{"413", 413, `{}`, "", transport.FailureClassRejectedRequest},
		{"422", 422, `{}`, "", transport.FailureClassRejectedRequest},
		{"418", 418, `{}`, "", transport.FailureClassRejectedRequest},
		{"unparsable 2xx", 200, `not json`, "", transport.FailureClassBadResponse},
		{"ok", 200, classOKBody, "", transport.FailureClassNone},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := serve(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Retry-After", "7")
				w.Header().Set("X-Should-Retry", "false")
				w.WriteHeader(c.status)
				_, _ = w.Write([]byte(c.body))
			})
			a := classAdapter(s.URL, func(p *config.EndpointProfile) { p.BadGateway = c.badGateway })
			res, err := a.Complete(context.Background(), classReq())
			if err != nil {
				t.Fatal(err)
			}
			if res.FailureClass != c.want {
				t.Fatalf("class = %q, want %q", res.FailureClass, c.want)
			}
			if c.want == transport.FailureClassNone {
				if res.ServerWait.Kind != transport.ServerWaitAbsent || res.NoRetry {
					t.Fatalf("transport-ok result carries wait/no-retry: %+v", res)
				}
				return
			}
			if res.ServerWait != (transport.ServerWait{Kind: transport.ServerWaitDuration, Millis: 7000}) || !res.NoRetry {
				t.Fatalf("wait/no-retry = %+v %v", res.ServerWait, res.NoRetry)
			}
		})
	}
}

func TestCompleteClassLocal(t *testing.T) {
	s := serve(t, func(w http.ResponseWriter, r *http.Request) { t.Error("sent") })
	check := func(name string, a *Adapter, req transport.CompleteRequest) {
		t.Run(name, func(t *testing.T) {
			res, err := a.Complete(context.Background(), req)
			if !errors.Is(err, transport.ErrNotSent) {
				t.Fatalf("err = %v", err)
			}
			if res.FailureClass != transport.FailureClassLocal {
				t.Fatalf("class = %q", res.FailureClass)
			}
		})
	}
	bad := classReq()
	bad.Messages = []transport.Message{{Role: "bogus", Content: "x"}}
	check("invalid message", classAdapter(s.URL, nil), bad)
	check("unresolved secret", classAdapter(s.URL, func(p *config.EndpointProfile) {
		p.Auth = &config.SecretRef{Source: "env", Name: "missing"}
	}), classReq())
	refused := classReq()
	refused.BeforeSend = func(*transport.CompleteRequest) error { return errors.New("no") }
	check("refused hook", classAdapter(s.URL, nil), refused)
}

func TestCompleteClassUnreachable(t *testing.T) {
	s := httptest.NewServer(http.NotFoundHandler())
	url := s.URL
	s.Close()
	res, err := classAdapter(url, nil).Complete(context.Background(), classReq())
	if err != nil {
		t.Fatal(err)
	}
	if res.TransportStatus != transport.TransportStatusUnreachable || res.FailureClass != transport.FailureClassUnreachable {
		t.Fatalf("got %q %q", res.TransportStatus, res.FailureClass)
	}
}

func TestCompleteClassResetIsUncertain(t *testing.T) {
	s := serve(t, func(w http.ResponseWriter, r *http.Request) {
		hj := w.(http.Hijacker)
		c, _, _ := hj.Hijack()
		_ = c.Close()
	})
	res, err := classAdapter(s.URL, nil).Complete(context.Background(), classReq())
	if err != nil {
		t.Fatal(err)
	}
	if res.TransportStatus != transport.TransportStatusReset || res.FailureClass != transport.FailureClassUncertain {
		t.Fatalf("got %q %q", res.TransportStatus, res.FailureClass)
	}
}

func TestCompleteClassTimeoutIsUncertain(t *testing.T) {
	release := make(chan struct{})
	s := serve(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	t.Cleanup(func() { close(release) })
	req := classReq()
	req.Deadline = time.Now().Add(100 * time.Millisecond)
	res, err := classAdapter(s.URL, nil).Complete(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if res.TransportStatus != transport.TransportStatusTimeout || res.FailureClass != transport.FailureClassUncertain {
		t.Fatalf("got %q %q", res.TransportStatus, res.FailureClass)
	}
}

func TestCompleteClass2xxBodyReadResetIsUncertain(t *testing.T) {
	s := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000")
		w.WriteHeader(200)
		_, _ = w.Write([]byte("partial"))
		hj := w.(http.Hijacker)
		c, _, _ := hj.Hijack()
		_ = c.Close()
	})
	res, _ := classAdapter(s.URL, nil).Complete(context.Background(), classReq())
	if res.FailureClass != transport.FailureClassUncertain {
		t.Fatalf("class = %q (%s)", res.FailureClass, res.TransportStatus)
	}
}

func TestCompleteClass2xxBodyReadTimeoutIsUncertain(t *testing.T) {
	release := make(chan struct{})
	s := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000")
		w.WriteHeader(200)
		_, _ = w.Write([]byte("partial"))
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	t.Cleanup(func() { close(release) })
	req := classReq()
	req.Deadline = time.Now().Add(200 * time.Millisecond)
	res, _ := classAdapter(s.URL, nil).Complete(context.Background(), req)
	if res.FailureClass != transport.FailureClassUncertain {
		t.Fatalf("class = %q (%s)", res.FailureClass, res.TransportStatus)
	}
}

func TestCompleteClassEngineCancelHasNoClass(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	s := serve(t, func(w http.ResponseWriter, r *http.Request) {
		cancel()
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	})
	res, _ := classAdapter(s.URL, nil).Complete(ctx, classReq())
	if res.TransportStatus != transport.TransportStatusCancelled || res.FailureClass != transport.FailureClassNone {
		t.Fatalf("got %q %q", res.TransportStatus, res.FailureClass)
	}
}

func TestCompleteClassRedirect(t *testing.T) {
	t.Run("own host followed", func(t *testing.T) {
		s := serve(t, func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/final") {
				w.WriteHeader(500)
				return
			}
			http.Redirect(w, r, "/v1/final", http.StatusTemporaryRedirect)
		})
		res, err := classAdapter(s.URL, nil).Complete(context.Background(), classReq())
		if err != nil || res.FailureClass != transport.FailureClassServerFault {
			t.Fatalf("got %q %v", res.FailureClass, err)
		}
	})
	t.Run("failure after followed redirect", func(t *testing.T) {
		var base string
		s := serve(t, func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/final") {
				hj := w.(http.Hijacker)
				c, _, _ := hj.Hijack()
				_ = c.Close()
				return
			}
			http.Redirect(w, r, base+"/final", http.StatusTemporaryRedirect)
		})
		base = s.URL
		res, _ := classAdapter(s.URL, nil).Complete(context.Background(), classReq())
		if res.FailureClass != transport.FailureClassUncertain {
			t.Fatalf("class = %q", res.FailureClass)
		}
	})
	t.Run("foreign host not followed", func(t *testing.T) {
		foreign := serve(t, func(w http.ResponseWriter, r *http.Request) { t.Error("followed") })
		s := serve(t, func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, foreign.URL, http.StatusFound)
		})
		res, _ := classAdapter(s.URL, nil).Complete(context.Background(), classReq())
		if res.FailureClass != transport.FailureClassRejectedConfig {
			t.Fatalf("class = %q", res.FailureClass)
		}
	})
}

func TestCompleteClassBodyReadFailureKeepsStatusAndWait(t *testing.T) {
	s := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "30")
		w.Header().Set("Content-Length", "1000")
		w.WriteHeader(429)
		_, _ = w.Write([]byte("partial"))
		hj := w.(http.Hijacker)
		c, _, _ := hj.Hijack()
		_ = c.Close()
	})
	res, _ := classAdapter(s.URL, nil).Complete(context.Background(), classReq())
	if res.FailureClass != transport.FailureClassThrottled {
		t.Fatalf("class = %q (%s)", res.FailureClass, res.TransportStatus)
	}
	if res.ServerWait.Kind != transport.ServerWaitDuration || res.ServerWait.Millis != 30000 {
		t.Fatalf("wait = %+v", res.ServerWait)
	}
}

func TestCompleteClass2xxOverBodyLimitIsUncertain(t *testing.T) {
	s := serve(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", 100)))
	})
	res, err := classAdapter(s.URL, nil, WithMaxBodySize(10)).Complete(context.Background(), classReq())
	if err != nil {
		t.Fatal(err)
	}
	if res.TransportStatus != transport.TransportStatusBodyTooLarge || res.FailureClass != transport.FailureClassUncertain {
		t.Fatalf("got %q %q", res.TransportStatus, res.FailureClass)
	}
}

func TestCompleteClassWithGoErrorHasAllMembers(t *testing.T) {
	s := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "5")
		w.Header().Set("X-Should-Retry", "false")
		w.Header().Set("Content-Length", "1000")
		w.WriteHeader(503)
		_, _ = w.Write([]byte("partial"))
		hj := w.(http.Hijacker)
		c, _, _ := hj.Hijack()
		_ = c.Close()
	})
	// A body read failing with an error that is neither reset, timeout nor cancel.
	a := classAdapter(s.URL, nil)
	a.client.Transport = errBodyTransport{inner: http.DefaultTransport}
	res, err := a.Complete(context.Background(), classReq())
	if err == nil {
		t.Fatal("want Go error")
	}
	if res.FailureClass != transport.FailureClassThrottled || !res.NoRetry ||
		res.ServerWait != (transport.ServerWait{Kind: transport.ServerWaitDuration, Millis: 5000}) {
		t.Fatalf("got %+v", res)
	}
}

type errBodyTransport struct{ inner http.RoundTripper }

func (e errBodyTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := e.inner.RoundTrip(r)
	if err != nil {
		return nil, err
	}
	_ = resp.Body.Close()
	resp.Body = errBody{}
	return resp, nil
}

type errBody struct{}

func (errBody) Read([]byte) (int, error) { return 0, fmt.Errorf("boom") }
func (errBody) Close() error             { return nil }
