package grpc

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"net"
	"net/url"
	"testing"
	"time"

	"github.com/slchris/qubes-air/console/internal/repository"
	pb "github.com/slchris/qubes-air/console/internal/transport/relaypb"
	googlegrpc "google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/stretchr/testify/require"
)

func TestServerWithoutRegistryRejectsMissingRole(t *testing.T) {
	ca, key := mkCA(t)
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	server := mkLeaf(t, ca, key, "server", true)
	client := mkLeaf(t, ca, key, "no-role", false)
	addr := startTestServer(t, &tls.Config{Certificates: []tls.Certificate{server}, ClientCAs: roots, MinVersion: tls.VersionTLS12, MaxVersion: tls.VersionTLS12})
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: time.Second}, "tcp", addr,
		&tls.Config{Certificates: []tls.Certificate{client}, RootCAs: roots, MinVersion: tls.VersionTLS12, MaxVersion: tls.VersionTLS12})
	if conn != nil {
		require.NoError(t, conn.Close())
	}
	require.Error(t, err, "CA and ClientAuth alone must not authorize a caller")
}

func TestRevocationClosesIdleTunnel(t *testing.T) {
	ca, key := mkCA(t)
	serverTLS := mkServerTLS(t, ca, key)
	clientTLS := mkClientTLS(t, ca, key)
	leaf, err := x509.ParseCertificate(clientTLS.Certificates[0].Certificate[0])
	require.NoError(t, err)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := listener.Addr().String()
	require.NoError(t, listener.Close())
	registry := &fakeRegistry{revoked: map[string]bool{}}
	server := NewServer(ServerConfig{Listen: addr, TLS: serverTLS, CertRegistry: registry, ReauthorizeInterval: 20 * time.Millisecond}, tagInvoker{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = server.Serve(ctx) }()
	waitDial(t, addr)
	connection, err := googlegrpc.NewClient(addr, googlegrpc.WithTransportCredentials(credentials.NewTLS(clientTLS)))
	require.NoError(t, err)
	defer connection.Close()
	callCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	stream, err := pb.NewRelayTransportClient(connection).Tunnel(callCtx)
	require.NoError(t, err)
	require.NoError(t, stream.Send(handshakeFrame("remote", "relay")))
	_, err = stream.Recv()
	require.NoError(t, err)
	registry.revoke(repository.Fingerprint(leaf))
	done := make(chan error, 1)
	go func() { _, err := stream.Recv(); done <- err }()
	select {
	case err := <-done:
		require.Error(t, err)
	case <-time.After(time.Second):
		t.Fatal("revoked idle tunnel remained open")
	}
}

func TestServerRejectsInvalidClientIdentity(t *testing.T) {
	ca, key := mkCA(t)
	otherCA, otherKey := mkCA(t)
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	addr := startTestServer(t, &tls.Config{Certificates: []tls.Certificate{mkLeaf(t, ca, key, "server", true)}, ClientCAs: roots, MinVersion: tls.VersionTLS12, MaxVersion: tls.VersionTLS12})
	for _, name := range []string{"wrong CA", "wrong role", "expired", "wrong purpose"} {
		t.Run(name, func(t *testing.T) {
			pair := mkLeaf(t, ca, key, "relay", false)
			if name == "wrong CA" {
				pair = mkLeaf(t, otherCA, otherKey, "relay", false)
			} else {
				leaf, err := x509.ParseCertificate(pair.Certificate[0])
				require.NoError(t, err)
				switch name {
				case "wrong role":
					leaf.URIs = []*url.URL{{Scheme: "spiffe", Host: "qubes-air", Path: "/role/agent"}}
				case "expired":
					leaf.NotAfter = time.Now().Add(-time.Minute)
				case "wrong purpose":
					leaf.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
				}
				der, err := x509.CreateCertificate(rand.Reader, leaf, ca, leaf.PublicKey, key)
				require.NoError(t, err)
				pair.Certificate = [][]byte{der}
				pair.Leaf = nil
			}
			conn, err := tls.DialWithDialer(&net.Dialer{Timeout: time.Second}, "tcp", addr,
				&tls.Config{Certificates: []tls.Certificate{pair}, RootCAs: roots, MinVersion: tls.VersionTLS12, MaxVersion: tls.VersionTLS12})
			if conn != nil {
				require.NoError(t, conn.Close())
			}
			require.Error(t, err)
		})
	}
}
