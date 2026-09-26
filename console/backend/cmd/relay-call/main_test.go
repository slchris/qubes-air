package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/slchris/qubes-air/console/internal/pki"
	"github.com/slchris/qubes-air/console/internal/transport"
	transportgrpc "github.com/slchris/qubes-air/console/internal/transport/grpc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// echoInvoker answers every call with the target and service it was asked for.
type echoInvoker struct{}

func (echoInvoker) Invoke(_ context.Context, target, service string, _ []byte) (transport.Result, error) {
	return transport.Result{Stdout: []byte(target + " " + service)}, nil
}

func newTestCA(t *testing.T) *pki.CA {
	t.Helper()
	ca, err := pki.NewCA("qubes-air-console", 0)
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}
	return ca
}

// startAgent runs a real mTLS agent on loopback presenting a certificate for
// serverCN from serverCA, and accepting clients from clientCA. Only the server
// certificate varies between cases, so a refused call can only be relay-call
// refusing the agent — the agent itself accepts the relay every time.
func startAgent(t *testing.T, serverCA, clientCA *pki.CA, serverCN string) string {
	t.Helper()
	bundle, err := serverCA.IssueAgentCert(serverCN, time.Hour)
	if err != nil {
		t.Fatalf("IssueAgentCert: %v", err)
	}
	pair, err := tls.X509KeyPair([]byte(bundle.CertPEM), []byte(bundle.KeyPEM))
	if err != nil {
		t.Fatalf("agent key pair: %v", err)
	}
	clients := x509.NewCertPool()
	clients.AddCert(clientCA.Cert)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	var lc net.ListenConfig
	lis, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := lis.Addr().String()
	_ = lis.Close() // Serve re-listens on the same address

	srv := transportgrpc.NewServer(transportgrpc.ServerConfig{
		Listen: addr,
		TLS: &tls.Config{
			Certificates: []tls.Certificate{pair},
			ClientCAs:    clients,
			ClientAuth:   tls.RequireAndVerifyClientCert,
			MinVersion:   tls.VersionTLS13,
		},
	}, echoInvoker{})
	go func() { _ = srv.Serve(ctx) }()

	var d net.Dialer
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		c, err := d.DialContext(ctx, "tcp", addr)
		if err == nil {
			_ = c.Close()
			return addr
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("agent never listened on %s", addr)
	return ""
}

// TestDialAndCall_VerifiesTheAgent drives relay-call's own dial path, with a
// client certificate from its own mint path, against a real agent. The right
// agent answers; an agent from another CA, or another qube's valid agent, never
// gets a call — the tunnel is refused at the handshake, so the call can only
// time out waiting for it.
func TestDialAndCall_VerifiesTheAgent(t *testing.T) {
	ca := newTestCA(t)
	pair, pool := mintFromCA(ca)

	t.Run("the agent it dialed", func(t *testing.T) {
		addr := startAgent(t, ca, ca, pki.AgentCommonName("target"))
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		res, err := dialAndCall(ctx, pair, pool, addr, "target", "qubesair.Ping", nil)
		if err != nil {
			t.Fatalf("call to the right agent failed: %v", err)
		}
		if got := string(res.Stdout); got != "target qubesair.Ping" {
			t.Fatalf("agent answered %q", got)
		}
	})

	refused := []struct {
		name     string
		serverCA *pki.CA // the CA that issued the agent's certificate
		serves   string  // the qube that agent is really for
	}{
		{"wrong CA", newTestCA(t), "target"},
		{"wrong target", ca, "other"},
	}
	for _, tc := range refused {
		t.Run(tc.name, func(t *testing.T) {
			addr := startAgent(t, tc.serverCA, ca, pki.AgentCommonName(tc.serves))

			// Control: a client that trusts this agent's CA and expects its real
			// name gets an answer. startAgent frees its port before Serve binds
			// it again, so without this a refusal below could come from whatever
			// else took the port, or from a dead one, rather than from relay-call
			// refusing this agent.
			trusts := x509.NewCertPool()
			trusts.AddCert(tc.serverCA.Cert)
			ctrlCtx, ctrlCancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer ctrlCancel()
			if _, err := dialAndCall(ctrlCtx, pair, trusts, addr, tc.serves, "qubesair.Ping", nil); err != nil {
				t.Fatalf("control: the agent on %s does not answer a client that trusts it: %v", addr, err)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
			defer cancel()
			_, err := dialAndCall(ctx, pair, pool, addr, "target", "qubesair.Ping", nil)
			if err == nil {
				t.Fatal("relay-call reached an agent it must refuse")
			}
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("got %v, want the tunnel never to come up", err)
			}
		})
	}
}

// TestNewClient_RefusesWithoutTarget — an empty target would otherwise pin the
// agent certificate to "agent-", which is no qube; refuse before dialing.
func TestNewClient_RefusesWithoutTarget(t *testing.T) {
	pair, pool := mintFromCA(newTestCA(t))
	if _, err := newClient(pair, pool, "127.0.0.1:1", ""); err == nil ||
		!strings.Contains(err.Error(), "no remote name") {
		t.Fatalf("got %v, want a refusal naming the missing remote", err)
	}
	if err := dialAndStream(context.Background(), pair, pool, "127.0.0.1:1", "", "qubesair.StreamTCP+22"); err == nil {
		t.Fatal("stream without a target was allowed to dial")
	}
}

// caStore is an in-memory credential store for loadCA.
type caStore struct{ rows []models.Credential }

func (s *caStore) List(context.Context) ([]models.Credential, error) { return s.rows, nil }

func (s *caStore) GetSecret(_ context.Context, id string) (string, error) {
	for _, r := range s.rows {
		if r.ID == id {
			return r.Description, nil // the fixture keeps the secret here
		}
	}
	return "", errors.New("no such row")
}

func (s *caStore) put(t *testing.T, id, name, typ string, ca *pki.CA, key bool) {
	t.Helper()
	certPEM, keyPEM, err := ca.MarshalCA()
	require.NoError(t, err)
	secret := certPEM
	if key {
		secret = keyPEM
	}
	s.rows = append(s.rows, models.Credential{ID: id, Name: name, Type: typ, Description: secret})
}

// TestLoadCAFailsClosedOnPlantedRows — the tool reads the CA the way the
// console does: with the genuine pair it gets the genuine CA, and a planted
// look-alike next to it (a duplicate key, a long-s certificate name) makes
// the load fail instead of handing the tool the newest or first match.
func TestLoadCAFailsClosedOnPlantedRows(t *testing.T) {
	genuine, err := pki.NewCA("genuine", time.Hour)
	require.NoError(t, err)
	attacker, err := pki.NewCA("attacker", time.Hour)
	require.NoError(t, err)
	ctx := context.Background()

	store := &caStore{}
	store.put(t, "cert", models.ConsoleCACertName, models.ConsoleRowType, genuine, false)
	store.put(t, "key", models.ConsoleCAKeyName, models.ConsoleRowType, genuine, true)
	ca, err := loadCA(ctx, store)
	require.NoError(t, err)
	assert.Equal(t, genuine.Cert.Raw, ca.Cert.Raw)

	for label, planted := range map[string]models.Credential{
		"duplicate key":    {ID: "planted", Name: models.ConsoleCAKeyName, Type: models.ConsoleRowType},
		"long-s cert name": {ID: "planted", Name: "qube\u017f-air-ca-cert", Type: models.ConsoleRowType},
	} {
		plantedStore := &caStore{rows: append([]models.Credential(nil), store.rows...)}
		key := planted.Name == models.ConsoleCAKeyName
		plantedStore.put(t, planted.ID, planted.Name, planted.Type, attacker, key)
		ca, err := loadCA(ctx, plantedStore)
		assert.ErrorIs(t, err, models.ErrConsoleRowConflict, label)
		assert.Nil(t, ca, label)
	}
}
