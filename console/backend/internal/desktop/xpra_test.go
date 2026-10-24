package desktop

import (
	"context"
	"errors"
	"net"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestXpraDialVerifiesPeerBeforeReturningStream(t *testing.T) {
	for _, tc := range []struct {
		uid     uint32
		err     error
		allowed bool
	}{
		{DesktopUID, nil, true}, {0, nil, false}, {1001, nil, false}, {DesktopUID, errors.New("unavailable credentials"), false},
	} {
		listener, path := testSocket(t)
		done := make(chan bool, 1)
		go func() {
			conn, err := listener.AcceptUnix()
			if err != nil {
				done <- false
				return
			}
			defer conn.Close()
			buf := make([]byte, 1)
			n, _ := conn.Read(buf)
			done <- n == 0
		}()
		conn, err := dialXpra(context.Background(), path, func(*net.UnixConn) (uint32, error) { return tc.uid, tc.err })
		if tc.allowed {
			require.NoError(t, err)
			require.NoError(t, conn.Close())
		} else {
			require.Error(t, err)
			require.Nil(t, conn)
		}
		require.True(t, <-done, "the credential check sends no application data")
	}
}

func TestXpraDialUnavailableAndCanceled(t *testing.T) {
	conn, err := dialXpra(context.Background(), "/nonexistent/xpra.sock", desktopPeer)
	require.Error(t, err)
	require.Nil(t, conn)
	listener, path := testSocket(t)
	defer listener.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	conn, err = dialXpra(ctx, path, desktopPeer)
	require.Error(t, err)
	require.Nil(t, conn)
}
