package agent

import (
	"context"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/slchris/qubes-air/console/internal/pki"
	"github.com/slchris/qubes-air/console/internal/repository"
	"github.com/stretchr/testify/require"
)

func TestRevocationsRefreshAndFailClosed(t *testing.T) {
	ca, err := pki.NewCA("test", time.Hour)
	require.NoError(t, err)
	fp := strings.Repeat("ab", 32)
	document, err := ca.SignRevocations(nil, time.Now())
	require.NoError(t, err)
	var mu sync.Mutex
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.WriteHeader(status)
		_, _ = w.Write(document)
	}))
	defer srv.Close()
	registry, err := NewRevocationRegistry(srv.URL, []*x509.Certificate{ca.Cert})
	require.NoError(t, err)
	_, err = registry.Authorize(context.Background(), fp)
	require.NoError(t, err)
	mu.Lock()
	document, err = ca.SignRevocations([]string{fp}, time.Now())
	mu.Unlock()
	require.NoError(t, err)
	registry.fetched = time.Time{}
	_, err = registry.Authorize(context.Background(), fp)
	require.ErrorIs(t, err, repository.ErrCertRevoked)
	mu.Lock()
	status = http.StatusServiceUnavailable
	mu.Unlock()
	registry.fetched = time.Time{}
	_, err = registry.Authorize(context.Background(), strings.Repeat("cd", 32))
	require.Error(t, err, "must not fall back to cached state after failed refresh")
	restarted, err := NewRevocationRegistry(srv.URL, []*x509.Certificate{ca.Cert})
	require.NoError(t, err)
	_, err = restarted.Authorize(context.Background(), fp)
	require.Error(t, err, "restart must not bypass unavailable status")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	registry.gate <- struct{}{}
	_, err = registry.Authorize(ctx, fp)
	<-registry.gate
	require.ErrorIs(t, err, context.Canceled)
}

func TestRevocationsRejectReplayInvalidDataAndRedirects(t *testing.T) {
	ca, err := pki.NewCA("test", time.Hour)
	require.NoError(t, err)
	now := time.Now()
	document, err := ca.SignRevocations(nil, now)
	require.NoError(t, err)
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "/status", http.StatusTemporaryRedirect)
			return
		}
		_, _ = w.Write(document)
	}))
	defer srv.Close()
	reg, err := NewRevocationRegistry(srv.URL+"/status", []*x509.Certificate{ca.Cert})
	require.NoError(t, err)
	fp := strings.Repeat("ef", 32)
	_, err = reg.Authorize(context.Background(), fp)
	require.NoError(t, err)
	for _, issued := range []time.Time{now.Add(-time.Second), now.Add(-10 * time.Minute)} {
		replacement, signErr := ca.SignRevocations(nil, issued)
		require.NoError(t, signErr)
		mu.Lock()
		document = replacement
		mu.Unlock()
		reg.fetched = time.Time{}
		_, err = reg.Authorize(context.Background(), fp)
		require.Error(t, err)
	}
	for _, bad := range [][]byte{[]byte(`{"payload":"AA==","signature":"AA=="}`), []byte(strings.Repeat("x", 1024*1024+1))} {
		mu.Lock()
		document = bad
		mu.Unlock()
		reg.fetched = time.Time{}
		_, err = reg.Authorize(context.Background(), fp)
		require.Error(t, err)
	}
	reg, err = NewRevocationRegistry(srv.URL+"/redirect", []*x509.Certificate{ca.Cert})
	require.NoError(t, err)
	_, err = reg.Authorize(context.Background(), fp)
	require.Error(t, err)
}

func TestRevocationConcurrentReadsShareValidatedSnapshot(t *testing.T) {
	ca, err := pki.NewCA("test", time.Hour)
	require.NoError(t, err)
	doc, err := ca.SignRevocations(nil, time.Now())
	require.NoError(t, err)
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); _, _ = w.Write(doc) }))
	defer srv.Close()
	reg, err := NewRevocationRegistry(srv.URL, []*x509.Certificate{ca.Cert})
	require.NoError(t, err)
	results := make(chan error, 20)
	for i := 0; i < 20; i++ {
		go func() { _, err := reg.Authorize(context.Background(), strings.Repeat("ab", 32)); results <- err }()
	}
	for i := 0; i < 20; i++ {
		require.NoError(t, <-results)
	}
	require.EqualValues(t, 1, calls.Load())
}
