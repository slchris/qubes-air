// Command grpc-smoke is a standalone real-machine smoke test for the gRPC
// transport: it generates a throwaway CA + server/client leaf certs in memory,
// stands up the mTLS gRPC server, dials it with the client, drives one forward
// Call over the Tunnel, and prints the round-trip result.
//
// It proves the compiled transport binary actually runs and completes an mTLS
// bidi-stream round trip on the target host (e.g. a real Qubes AppVM). It does
// NOT touch qrexec — the server side uses an in-process echo invoker.
//
// Usage: grpc-smoke [-addr 127.0.0.1:0]
package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"flag"
	"fmt"
	"log"
	"math/big"
	"net"
	"os"
	"time"

	transportgrpc "github.com/slchris/qubes-air/console/internal/transport/grpc"
)

// echoInvoker stands in for the remote qrexec executor: it tags the input so we
// can confirm the request reached the server side.
type echoInvoker struct{}

func (echoInvoker) Invoke(_ context.Context, target, service string, in []byte) ([]byte, error) {
	return []byte(fmt.Sprintf("handled[%s/%s]:%s", target, service, string(in))), nil
}

func main() {
	addr := flag.String("addr", "127.0.0.1:0", "listen address for the smoke server")
	flag.Parse()

	if err := run(*addr); err != nil {
		log.Printf("SMOKE FAIL: %v", err)
		os.Exit(1)
	}
	log.Printf("SMOKE OK")
}

func run(listenAddr string) error {
	caCert, caKey := mustCA()
	serverTLS := mustServerTLS(caCert, caKey)
	clientTLS := mustClientTLS(caCert, caKey)

	// Pick a concrete port up front so the client knows where to dial.
	lis, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	addr := lis.Addr().String()
	_ = lis.Close()
	log.Printf("smoke: server addr = %s", addr)

	srv := transportgrpc.NewServer(transportgrpc.ServerConfig{Listen: addr, TLS: serverTLS}, echoInvoker{})
	srvCtx, srvCancel := context.WithCancel(context.Background())
	defer srvCancel()
	go func() {
		if err := srv.Serve(srvCtx); err != nil {
			log.Printf("smoke: server exited: %v", err)
		}
	}()
	waitDial(addr)

	cli := transportgrpc.NewClient(transportgrpc.ClientConfig{
		RemoteEndpoint: addr,
		RelayName:      "sys-relay-smoke",
		RemoteName:     "remote-smoke",
		KeepAlive:      500 * time.Millisecond,
		ReconnectMin:   50 * time.Millisecond,
		ReconnectMax:   500 * time.Millisecond,
		TLS:            clientTLS,
	}, nil)
	cliCtx, cliCancel := context.WithCancel(context.Background())
	defer cliCancel()
	go func() { _ = cli.Start(cliCtx) }()

	// Retry Call until the tunnel is established.
	deadline := time.Now().Add(5 * time.Second)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		out, err := cli.Call(ctx, "remote-gpu", "qubesair.Ping", []byte("ping"))
		cancel()
		if err == nil {
			log.Printf("smoke: round-trip response = %q", string(out))
			want := "handled[remote-gpu/qubesair.Ping]:ping"
			if string(out) != want {
				return fmt.Errorf("unexpected response %q, want %q", out, want)
			}
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("Call never succeeded: %w", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func waitDial(addr string) {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			_ = c.Close()
			return
		}
		time.Sleep(30 * time.Millisecond)
	}
}

// --- throwaway TLS material ---

func mustCA() (*x509.Certificate, *ecdsa.PrivateKey) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "smoke-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	cert, _ := x509.ParseCertificate(der)
	return cert, key
}

func mustLeaf(ca *x509.Certificate, caKey *ecdsa.PrivateKey, cn string, server bool) tls.Certificate {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	if server {
		tmpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
		tmpl.IPAddresses = []net.IP{net.ParseIP("127.0.0.1")}
		tmpl.DNSNames = []string{"localhost"}
	} else {
		tmpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
	}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, _ := x509.MarshalECPrivateKey(key)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	pair, _ := tls.X509KeyPair(certPEM, keyPEM)
	return pair
}

func mustServerTLS(ca *x509.Certificate, caKey *ecdsa.PrivateKey) *tls.Config {
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	return &tls.Config{
		Certificates: []tls.Certificate{mustLeaf(ca, caKey, "remote-relay", true)},
		ClientCAs:    pool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS12,
	}
}

func mustClientTLS(ca *x509.Certificate, caKey *ecdsa.PrivateKey) *tls.Config {
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	return &tls.Config{
		Certificates: []tls.Certificate{mustLeaf(ca, caKey, "sys-relay", false)},
		RootCAs:      pool,
		ServerName:   "localhost",
		MinVersion:   tls.VersionTLS12,
	}
}
