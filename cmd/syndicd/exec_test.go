package main

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/sandbox"
)

func TestExecOptionsWiresSupervisorAndBackend(t *testing.T) {
	dir := t.TempDir()
	o, err := execOptions(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(o.Supervisor.Command) != 2 || o.Supervisor.Command[1] != sandbox.SupervisorArg {
		t.Errorf("supervisor command %v", o.Supervisor.Command)
	}
	if o.NewBackend == nil {
		t.Error("no backend constructor")
	}
	if o.SelfTest.StateDir != dir || o.SelfTest.Helper == "" {
		t.Errorf("self-test %+v", o.SelfTest)
	}
}

func TestExecOptionsWiresNetworkFixture(t *testing.T) {
	o, err := execOptions(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if o.SelfTest.Network == nil {
		t.Fatal("no network fixture")
	}
	fx, err := o.SelfTest.Network()
	if err != nil {
		t.Fatal(err)
	}
	defer fx.Close()
	u, err := url.Parse(fx.ProxyURL)
	if err != nil {
		t.Fatal(err)
	}
	// Reach the listener the way the command does, from the host.
	network, addr := fx.Listener.Network, fx.Listener.Address
	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			Proxy: http.ProxyURL(&url.URL{Scheme: "http", User: u.User, Host: "unused"}),
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, addr)
			},
		},
	}
	resp, err := client.Get("http://" + sandbox.SelfTestHost + "/")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("self-test host: status %d", resp.StatusCode)
	}
	resp, err = client.Get("http://example.invalid/")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("other host: status %d", resp.StatusCode)
	}
	c, err := net.DialTimeout("tcp", fx.OtherAddr, time.Second)
	if err != nil {
		t.Fatalf("other listener: %v", err)
	}
	_ = c.Close()
	fx.Close()
	if _, err := net.DialTimeout(network, addr, time.Second); err == nil {
		t.Error("listener still open after Close")
	}
}
