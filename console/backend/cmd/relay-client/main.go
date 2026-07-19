// Command relay-client is the gRPC transport relay client daemon. It runs IN the
// relay qube (e.g. mgmt-jump), reads the transport config from the environment
// (QUBES_AIR_TRANSPORT_*, as rendered by the mgmt.remotevm.grpc-relay salt
// state into relay.env), dials the remote Remote-Relay OUTBOUND over mTLS, and
// keeps one long-lived bidirectional Tunnel alive (reconnecting on drop) until
// it receives SIGINT/SIGTERM.
//
// This is the process the systemd unit qubesair-relay-client.service runs.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/slchris/qubes-air/console/internal/config"
	transportgrpc "github.com/slchris/qubes-air/console/internal/transport/grpc"
)

func main() {
	log.SetPrefix("relay-client: ")
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)

	// Config comes purely from the environment (relay.env). Pass "" so Load uses
	// defaults + env overrides without needing a config file on the relay.
	cfg, err := config.Load("")
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	tc := cfg.Transport
	if !tc.Enabled {
		log.Fatalf("QUBES_AIR_TRANSPORT_ENABLED is not true; nothing to do")
	}
	if tc.RemoteEndpoint == "" {
		log.Fatalf("QUBES_AIR_TRANSPORT_REMOTE_ENDPOINT is empty")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	reverse := transportgrpc.NewReverseHandler(transportgrpc.ReverseConfig{
		LocalTarget: tc.ReverseLocalTarget,
	})

	clientCfg := transportgrpc.ClientConfig{
		RemoteEndpoint: tc.RemoteEndpoint,
		RelayName:      tc.RelayName,
		RemoteName:     tc.RemoteName,
		KeepAlive:      time.Duration(tc.KeepAliveSeconds) * time.Second,
		ReconnectMin:   time.Duration(tc.ReconnectMinSeconds) * time.Second,
		ReconnectMax:   time.Duration(tc.ReconnectMaxSeconds) * time.Second,
	}
	if tc.VaultCerts {
		// Rotation-aware: re-fetch certs from vault on each reconnect, so a vault
		// cert rotation takes effect without restarting the daemon.
		clientCfg.TLSProvider = transportgrpc.VaultTLSProvider(transportgrpc.VaultCertConfig{
			VaultQube:  tc.VaultQube,
			CertName:   tc.VaultCertName,
			KeyName:    tc.VaultKeyName,
			CAName:     tc.VaultCAName,
			ServerName: tc.RemoteName,
		})
	} else {
		tlsCfg, err := obtainMTLS(ctx, tc)
		if err != nil {
			log.Fatalf("mTLS setup: %v", err)
		}
		clientCfg.TLS = tlsCfg
	}

	client := transportgrpc.NewClient(clientCfg, reverse)

	log.Printf("starting: endpoint=%s relay=%s remote=%s reverse_target=%q vault_certs=%v",
		tc.RemoteEndpoint, tc.RelayName, tc.RemoteName, tc.ReverseLocalTarget, tc.VaultCerts)

	// Start blocks until ctx is cancelled (signal), maintaining the tunnel.
	if err := client.Start(ctx); err != nil && ctx.Err() == nil {
		log.Fatalf("client stopped: %v", err)
	}
	log.Printf("shutdown")
}

// obtainMTLS builds the client TLS config from vault-cloud (via qrexec ask) when
// VaultCerts is set, otherwise from the configured cert/key/CA file paths.
func obtainMTLS(ctx context.Context, tc config.TransportConfig) (*tls.Config, error) {
	if tc.VaultCerts {
		return transportgrpc.FetchClientMTLS(ctx, transportgrpc.VaultCertConfig{
			VaultQube:  tc.VaultQube,
			CertName:   tc.VaultCertName,
			KeyName:    tc.VaultKeyName,
			CAName:     tc.VaultCAName,
			ServerName: tc.RemoteName,
		})
	}
	cert, err := tls.LoadX509KeyPair(tc.CertFile, tc.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("load client cert/key: %w", err)
	}
	cfg := &tls.Config{
		Certificates: []tls.Certificate{cert},
		ServerName:   tc.RemoteName,
		MinVersion:   tls.VersionTLS12,
	}
	if tc.CAFile != "" {
		pem, err := os.ReadFile(tc.CAFile)
		if err != nil {
			return nil, fmt.Errorf("read CA file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("CA file %q has no valid certificates", tc.CAFile)
		}
		cfg.RootCAs = pool
	}
	return cfg, nil
}
