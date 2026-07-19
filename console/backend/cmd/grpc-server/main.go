// Command grpc-server is a standalone Remote-Relay gRPC server for testing the
// transport end-to-end (e.g. on a real machine). It loads mTLS material from
// files, listens with RequireAndVerifyClientCert, and uses an in-process echo
// invoker (NOT real qrexec) so it can run anywhere for a smoke test.
//
// Flags:
//
//	-listen host:port   (default 127.0.0.1:8443)
//	-cert / -key / -ca  server cert/key and client CA (PEM files)
//
// For a real Remote-Relay use NewServerWithQrexec (shells to qrexec-client-vm);
// this command exists to exercise the wire on a host without remote qubes.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"flag"
	"fmt"
	"log"
	"os"

	transportgrpc "github.com/slchris/qubes-air/console/internal/transport/grpc"
)

type echoInvoker struct{}

func (echoInvoker) Invoke(_ context.Context, target, service string, in []byte) ([]byte, error) {
	return []byte(fmt.Sprintf("handled[%s/%s]:%s", target, service, string(in))), nil
}

func main() {
	log.SetPrefix("grpc-server: ")
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)

	listen := flag.String("listen", "127.0.0.1:8443", "listen address")
	certFile := flag.String("cert", "", "server cert PEM (required)")
	keyFile := flag.String("key", "", "server key PEM (required)")
	caFile := flag.String("ca", "", "client CA PEM (required for mTLS)")
	qrexecMode := flag.Bool("qrexec", false, "use the real qrexec invoker (shell to qrexec-client-vm) instead of the in-process echo invoker")
	flag.Parse()

	tlsCfg, err := serverTLS(*certFile, *keyFile, *caFile)
	if err != nil {
		log.Fatalf("mTLS: %v", err)
	}

	cfg := transportgrpc.ServerConfig{Listen: *listen, TLS: tlsCfg}
	var srv *transportgrpc.Server
	if *qrexecMode {
		// Production Remote-Relay: forward to qrexec-client-vm (post remote dom0
		// re-check). Requires a Qubes remote host with qrexec.
		srv = transportgrpc.NewServerWithQrexec(cfg)
		log.Printf("invoker: real qrexec (qrexec-client-vm)")
	} else {
		// Test/smoke mode: echo invoker, runs anywhere.
		srv = transportgrpc.NewServer(cfg, echoInvoker{})
		log.Printf("invoker: in-process echo (test mode; pass -qrexec for real forwarding)")
	}
	log.Printf("listening on %s (mTLS, RequireAndVerifyClientCert)", *listen)
	if err := srv.Serve(context.Background()); err != nil {
		log.Fatalf("serve: %v", err)
	}
}

func serverTLS(certFile, keyFile, caFile string) (*tls.Config, error) {
	if certFile == "" || keyFile == "" || caFile == "" {
		return nil, fmt.Errorf("-cert, -key and -ca are all required")
	}
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("load server cert/key: %w", err)
	}
	caPEM, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("read CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("CA %q has no valid certificates", caFile)
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientCAs:    pool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS12,
	}, nil
}
