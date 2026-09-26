package grpc

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"net"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/slchris/qubes-air/console/internal/agent"
	"github.com/slchris/qubes-air/console/internal/pki"
	"github.com/slchris/qubes-air/console/internal/transport"
	pb "github.com/slchris/qubes-air/console/internal/transport/relaypb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
)

// serviceCallerCert is a verified-chain leaf carrying cn and one URI SAN per
// role; no roles means none.
func serviceCallerCert(cn string, roles ...string) *x509.Certificate {
	cert := &x509.Certificate{Subject: pkix.Name{CommonName: cn}}
	for _, r := range roles {
		u, _ := url.Parse("spiffe://qubes-air/role/" + r)
		cert.URIs = append(cert.URIs, u)
	}
	return cert
}

func verifiedPeer(cert *x509.Certificate) context.Context {
	return peer.NewContext(context.Background(), &peer.Peer{AuthInfo: credentials.TLSInfo{
		State: tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{cert}}},
	}})
}

func TestAuthorizePrivilegedServiceCaller(t *testing.T) {
	unlock := serviceCallerCert(pki.ConsoleUnlockCN, "console")
	bootstrap := serviceCallerCert(pki.ConsoleBootstrapCN, "console")

	tests := []struct {
		name    string
		service string
		ctx     context.Context
		want    string // "" means allowed
	}{
		{name: "unlock by the unlock identity", service: unlockDataService, ctx: verifiedPeer(unlock)},
		{name: "rekey by the unlock identity", service: rekeyDataService, ctx: verifiedPeer(unlock)},
		{name: "argument form is judged by its service", service: rekeyDataService + "+remote", ctx: verifiedPeer(unlock)},
		{name: "begin bootstrap by the bootstrap identity", service: beginBootstrapService, ctx: verifiedPeer(bootstrap)},
		{name: "complete bootstrap by the bootstrap identity", service: completeBootstrapService, ctx: verifiedPeer(bootstrap)},
		{name: "unprivileged service needs no identity", service: "qubesair.Ping", ctx: context.Background()},
		{name: "stream service is not privileged", service: "qubesair.StreamTCP+5900", ctx: context.Background()},

		{name: "bootstrap refused to the unlock identity", service: beginBootstrapService, ctx: verifiedPeer(unlock), want: "restricted to the console-bootstrap"},
		{name: "unlock refused to the bootstrap identity", service: unlockDataService, ctx: verifiedPeer(bootstrap), want: "restricted to the console-unlock"},
		{name: "unlock refused to the probe identity", service: unlockDataService, ctx: verifiedPeer(serviceCallerCert("console-probe", "console")), want: "restricted to the console-unlock"},
		{name: "unlock refused to the renewal identity", service: rekeyDataService, ctx: verifiedPeer(serviceCallerCert("console-renew", "console")), want: "restricted to the console-unlock"},
		{name: "unlock refused to a relay", service: unlockDataService, ctx: verifiedPeer(serviceCallerCert("relay-dom0", "relay")), want: "restricted"},
		{name: "right name with the relay role", service: unlockDataService, ctx: verifiedPeer(serviceCallerCert(pki.ConsoleUnlockCN, "relay")), want: "restricted"},
		{name: "right name with the agent role", service: beginBootstrapService, ctx: verifiedPeer(serviceCallerCert(pki.ConsoleBootstrapCN, "agent")), want: "restricted"},
		{name: "right name with no role", service: unlockDataService, ctx: verifiedPeer(serviceCallerCert(pki.ConsoleUnlockCN)), want: "restricted"},
		{name: "right name with two roles", service: unlockDataService, ctx: verifiedPeer(serviceCallerCert(pki.ConsoleUnlockCN, "console", "relay")), want: "restricted"},
		{name: "argument form refused to the wrong identity", service: unlockDataService + "+remote", ctx: verifiedPeer(serviceCallerCert("console-probe", "console")), want: "restricted"},
		{name: "missing peer", service: unlockDataService, ctx: context.Background(), want: "requires an authenticated console-unlock"},
		{name: "peer without auth info", service: unlockDataService, ctx: peer.NewContext(context.Background(), &peer.Peer{}), want: "requires an authenticated"},
		{name: "peer without a verified chain", service: beginBootstrapService,
			ctx:  peer.NewContext(context.Background(), &peer.Peer{AuthInfo: credentials.TLSInfo{}}),
			want: "requires a verified client certificate"},
		{name: "peer with an empty verified chain", service: beginBootstrapService,
			ctx:  peer.NewContext(context.Background(), &peer.Peer{AuthInfo: credentials.TLSInfo{State: tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{}}}}}),
			want: "requires a verified client certificate"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := authorizePrivilegedServiceCaller(tt.ctx, tt.service)
			if tt.want == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tt.want)
		})
	}
}

// The service names the check keys on must be the ones the agent actually
// registers: a rename on either side would silently turn the check off.
func TestPrivilegedServiceNamesMatchTheAgent(t *testing.T) {
	assert.Equal(t, agent.ServiceBeginBootstrap, beginBootstrapService)
	assert.Equal(t, agent.ServiceCompleteBootstrap, completeBootstrapService)
	for _, script := range []string{unlockDataService, rekeyDataService} {
		_, err := os.Stat("../../../../../remote/qubes-rpc/" + script)
		require.NoError(t, err, "the agent ships %s as a qrexec script", script)
	}
}

// recordingInvoker records whether the forward call reached execution.
type recordingInvoker struct {
	mu    sync.Mutex
	calls []string
}

func (i *recordingInvoker) Invoke(_ context.Context, _, service string, _ []byte) (transport.Result, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.calls = append(i.calls, service)
	return transport.Result{Stdout: []byte("ran " + service)}, nil
}

func (i *recordingInvoker) ran() []string {
	i.mu.Lock()
	defer i.mu.Unlock()
	return append([]string(nil), i.calls...)
}

// A refused privileged call must be answered with a denial frame and must
// never reach the invoker (which would run the service as root).
func TestHandleForwardRejectsUnauthorizedPrivilegedServiceBeforeInvocation(t *testing.T) {
	for _, tc := range []struct {
		service string
		cert    *x509.Certificate
	}{
		{service: unlockDataService, cert: serviceCallerCert("console-probe", "console")},
		{service: rekeyDataService + "+remote", cert: serviceCallerCert(pki.ConsoleBootstrapCN, "console")},
		{service: beginBootstrapService, cert: serviceCallerCert("relay-dom0", "relay")},
		{service: completeBootstrapService, cert: serviceCallerCert(pki.ConsoleUnlockCN, "console")},
	} {
		t.Run(tc.service, func(t *testing.T) {
			invoker := &recordingInvoker{}
			server := &Server{invoker: invoker}
			var sent []*pb.Frame
			server.handleForward(verifiedPeer(tc.cert), "request-1", &pb.RequestHeader{
				TargetQube: "remote", QrexecService: tc.service,
			}, nil, func(frame *pb.Frame) error {
				sent = append(sent, frame)
				return nil
			})
			assert.Empty(t, invoker.ran())
			require.Len(t, sent, 1)
			assert.Equal(t, codeDenied, sent[0].GetError().GetCode())
		})
	}
}

// Over a real mTLS tunnel: the handshake populates VerifiedChains, so a
// CA-signed console certificate with the wrong name is denied at the agent and
// the dedicated one gets through. This is the path production takes.
func TestPrivilegedServiceOverMTLSNeedsTheDedicatedIdentity(t *testing.T) {
	ca, err := pki.NewCA("qubes-air-console", 0)
	require.NoError(t, err)
	invoker := &recordingInvoker{}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := lis.Addr().String()
	require.NoError(t, lis.Close())
	srv := NewServer(ServerConfig{Listen: addr, TLS: mkServerTLSFromCA(t, ca)}, invoker)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.Serve(ctx) }()
	waitDial(t, addr)

	call := func(cn, service string) ([]byte, error) {
		bundle, err := ca.IssueAgentCert(cn, time.Hour)
		require.NoError(t, err)
		pair, err := tls.X509KeyPair([]byte(bundle.CertPEM), []byte(bundle.KeyPEM))
		require.NoError(t, err)
		pool := x509.NewCertPool()
		require.True(t, pool.AppendCertsFromPEM([]byte(bundle.CAPEM)))
		cli := NewClient(ClientConfig{
			RemoteEndpoint: addr, RelayName: cn, RemoteName: "remote-dev",
			TLS: &tls.Config{Certificates: []tls.Certificate{pair}, RootCAs: pool, ServerName: "localhost", MinVersion: tls.VersionTLS13},
		}, nil)
		cliCtx, stop := context.WithCancel(ctx)
		defer stop()
		go func() { _ = cli.Start(cliCtx) }()
		return callWhenReady(t, cli, "remote-dev", service, []byte("key"))
	}

	_, err = call("console-probe", unlockDataService)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "restricted to the console-unlock")
	_, err = call(pki.ConsoleUnlockCN, beginBootstrapService)
	require.Error(t, err)
	assert.Empty(t, invoker.ran(), "no refused call may reach the invoker")

	out, err := call(pki.ConsoleUnlockCN, unlockDataService)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(string(out), "ran "+unlockDataService))
	assert.Equal(t, []string{unlockDataService}, invoker.ran())
}
