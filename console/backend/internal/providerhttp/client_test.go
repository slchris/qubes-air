package providerhttp

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func serverCertificate(t *testing.T, ip string, expiry time.Time) (tls.Certificate, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	require.NoError(t, err)
	ca, err = x509.ParseCertificate(caDER)
	require.NoError(t, err)
	leaf := &x509.Certificate{SerialNumber: big.NewInt(2), NotBefore: time.Now().Add(-time.Hour),
		NotAfter: expiry, IPAddresses: []net.IP{net.ParseIP(ip)},
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, leaf, ca, &key.PublicKey, key)
	require.NoError(t, err)
	return tls.Certificate{Certificate: [][]byte{der, caDER}, PrivateKey: key}, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}))
}

func TestHTTPSIdentityVerification(t *testing.T) {
	for _, tc := range []struct {
		name, ip         string
		expired, wrongCA bool
	}{
		{name: "valid", ip: "127.0.0.1"}, {name: "wrong target", ip: "192.0.2.1"},
		{name: "expired", ip: "127.0.0.1", expired: true}, {name: "wrong CA", ip: "127.0.0.1", wrongCA: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			expiry := time.Now().Add(time.Hour)
			if tc.expired {
				expiry = time.Now().Add(-time.Minute)
			}
			cert, ca := serverCertificate(t, tc.ip, expiry)
			srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
			srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
			srv.StartTLS()
			t.Cleanup(srv.Close)
			if tc.wrongCA {
				_, ca = serverCertificate(t, "127.0.0.1", time.Now().Add(time.Hour))
			}
			client, err := NewClient(srv.URL, ca, time.Second)
			require.NoError(t, err)
			req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
			require.NoError(t, err)
			response, err := client.Do(req)
			if tc.name != "valid" {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.NoError(t, response.Body.Close())
		})
	}
}

func TestEndpointAndRedirectRejection(t *testing.T) {
	for _, endpoint := range []string{"http://localhost", "https://u:p@localhost", "https://localhost?q=x", "https://localhost#x", "https://", "file:///tmp/a"} {
		_, err := NewClient(endpoint, "", time.Second)
		require.Error(t, err)
	}
	_, err := NewClient("https://localhost", "not PEM", time.Second)
	require.Error(t, err)
	destinationCalls := 0
	target := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { destinationCalls++ }))
	defer target.Close()
	source := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	ca := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: source.Certificate().Raw}))
	client, err := NewClient(source.URL, ca, time.Second)
	require.NoError(t, err)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, source.URL, nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer test-token")
	response, err := client.Do(req)
	if response != nil {
		require.NoError(t, response.Body.Close())
	}
	require.Error(t, err)
	require.Zero(t, destinationCalls)
}
