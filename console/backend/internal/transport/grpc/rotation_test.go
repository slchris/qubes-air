package grpc

import (
	"crypto/tls"
	"errors"
	"sync/atomic"
	"testing"
)

// TestResolveTLS_PrefersProvider verifies the TLSProvider is preferred over the
// static TLS and is called fresh each time (the rotation mechanism).
func TestResolveTLS_PrefersProvider(t *testing.T) {
	var calls int32
	static := &tls.Config{ServerName: "static"}
	fresh := &tls.Config{ServerName: "fresh"}

	c := NewClient(ClientConfig{
		TLS: static,
		TLSProvider: func() (*tls.Config, error) {
			atomic.AddInt32(&calls, 1)
			return fresh, nil
		},
	}, nil)

	for i := 0; i < 3; i++ {
		got, err := c.resolveTLS()
		if err != nil {
			t.Fatalf("resolveTLS: %v", err)
		}
		if got.ServerName != "fresh" {
			t.Errorf("resolveTLS returned %q, want the provider's config", got.ServerName)
		}
	}
	if n := atomic.LoadInt32(&calls); n != 3 {
		t.Errorf("provider called %d times, want 3 (called fresh each connect)", n)
	}
}

// TestResolveTLS_FallsBackToStatic verifies that without a provider the static
// TLS is used, and that a nil static TLS is a loud error (no insecure dial).
func TestResolveTLS_FallsBackToStatic(t *testing.T) {
	static := &tls.Config{ServerName: "static"}
	c := NewClient(ClientConfig{TLS: static}, nil)
	got, err := c.resolveTLS()
	if err != nil {
		t.Fatalf("resolveTLS: %v", err)
	}
	if got.ServerName != "static" {
		t.Errorf("got %q, want static", got.ServerName)
	}

	// No provider and no static → error, never an insecure dial.
	c2 := NewClient(ClientConfig{}, nil)
	if _, err := c2.resolveTLS(); err == nil {
		t.Error("expected error when neither TLS nor TLSProvider is set")
	}
}

// TestResolveTLS_ProviderError propagates a provider error (e.g. vault ask
// denied) rather than dialing insecurely.
func TestResolveTLS_ProviderError(t *testing.T) {
	c := NewClient(ClientConfig{
		TLSProvider: func() (*tls.Config, error) { return nil, errors.New("vault denied") },
	}, nil)
	if _, err := c.resolveTLS(); err == nil {
		t.Error("expected provider error to propagate")
	}
}

// TestClientServerRoundTrip_WithRotatingProvider proves an end-to-end round trip
// works when the client obtains its cert from a TLSProvider (the rotation path),
// not just a static config.
func TestClientServerRoundTrip_WithRotatingProvider(t *testing.T) {
	caCert, caKey := mkCA(t)
	serverTLS := mkServerTLS(t, caCert, caKey)

	srvAddr := startTestServer(t, serverTLS)

	// Provider hands out a freshly-built (but valid) client cert each call,
	// simulating a rotated cert fetched from vault.
	var provCalls int32
	provider := func() (*tls.Config, error) {
		atomic.AddInt32(&provCalls, 1)
		return mkClientTLS(t, caCert, caKey), nil
	}

	out := dialAndCall(t, ClientConfig{
		RemoteEndpoint: srvAddr,
		RelayName:      "sys-relay",
		RemoteName:     "remote",
		TLSProvider:    provider,
	})
	if got, want := string(out), "handled[remote-gpu/qubesair.Ping]:ping"; got != want {
		t.Fatalf("round-trip = %q, want %q", got, want)
	}
	if atomic.LoadInt32(&provCalls) == 0 {
		t.Error("TLSProvider was never called")
	}
}
