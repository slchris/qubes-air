package service

import (
	"context"
	"net"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/slchris/qubes-air/console/internal/models"
)

func TestDirectDialerAddressUsesTheAgentPort(t *testing.T) {
	d := NewDirectDialer("0.0.0.0:9443")
	assert.Equal(t, "10.0.0.9:9443", d.Address(&models.Qube{IPAddress: "10.0.0.9"}))

	// A malformed listen address must not silently produce a portless target;
	// agentPortFrom falls back rather than failing, so a probe still happens
	// against the default instead of not at all.
	assert.Equal(t, "10.0.0.9:"+defaultAgentPort,
		NewDirectDialer("nonsense").Address(&models.Qube{IPAddress: "10.0.0.9"}))
}

func TestDirectDialerReachesAListener(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = lis.Close() }()

	host, port, err := net.SplitHostPort(lis.Addr().String())
	require.NoError(t, err)

	conn, err := NewDirectDialer("0.0.0.0:"+port).
		DialAgent(context.Background(), &models.Qube{IPAddress: host})
	require.NoError(t, err)
	assert.NoError(t, conn.Close())
}

// The seam exists so a zone can be reached some other way — GCP IAP and AWS SSM
// open a per-connection tunnel rather than routing. This proves the injection
// point actually diverts the connection, which is the whole claim; without it
// the interface would be decoration.
func TestASubstituteDialerIsWhatActuallyConnects(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = lis.Close() }()

	var calls atomic.Int32
	fake := &countingDialer{
		addr:  lis.Addr().String(),
		calls: &calls,
	}

	// The qube's own address is deliberately unroutable. If anything still
	// dialed it, this would fail — which is exactly the regression to catch:
	// a caller that computes host:port itself and bypasses the dialer.
	qube := &models.Qube{Name: "remote-dev", IPAddress: "192.0.2.1"}

	conn, err := fake.DialAgent(context.Background(), qube)
	require.NoError(t, err)
	require.NoError(t, conn.Close())
	assert.Equal(t, int32(1), calls.Load())

	// And the gRPC adapter must route through the same dialer, ignoring the
	// address gRPC hands it — under IAP that string is not dialable at all.
	f := dialFuncFor(fake, qube)
	require.NotNil(t, f)
	conn, err = f(context.Background(), "192.0.2.1:8443")
	require.NoError(t, err)
	require.NoError(t, conn.Close())
	assert.Equal(t, int32(2), calls.Load(), "the gRPC adapter did not use the injected dialer")
}

func TestDialFuncIsNilWithoutADialer(t *testing.T) {
	// Nil must stay nil: the transport only applies WithContextDialer when the
	// field is set, so this is what keeps the routed path byte-for-byte
	// unchanged by the seam.
	assert.Nil(t, dialFuncFor(nil, &models.Qube{}))
}

type countingDialer struct {
	addr  string
	calls *atomic.Int32
}

func (c *countingDialer) DialAgent(ctx context.Context, _ *models.Qube) (net.Conn, error) {
	c.calls.Add(1)
	return (&net.Dialer{}).DialContext(ctx, "tcp", c.addr)
}

func (c *countingDialer) Address(*models.Qube) string { return c.addr }
