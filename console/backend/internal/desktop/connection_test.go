package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func testSocket(t *testing.T) (*net.UnixListener, string) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "air-desktop-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(dir)) })
	path := filepath.Join(dir, "control.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	return listener, path
}

func desktopPeer(*net.UnixConn) (uint32, error) { return DesktopUID, nil }

func testServer(t *testing.T, listener *net.UnixListener, config server) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- config.serve(ctx, listener) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			require.NoError(t, err)
		case <-time.After(2 * time.Second):
			t.Error("server did not stop")
		}
	})
	return cancel
}

func TestSocketRoundtripAndRepeatedExplicitRequests(t *testing.T) {
	listener, path := testSocket(t)
	var calls atomic.Int32
	testServer(t, listener, server{peer: desktopPeer, timeout: time.Second, maxClients: 2,
		handle: func(_ context.Context, request Request) Response {
			calls.Add(1)
			return Response{OK: true, App: request.App, Display: ":100"}
		}})
	request := Request{Operation: "launch", App: "editor.desktop"}
	for range 2 {
		reply := call(context.Background(), path, desktopPeer, request, time.Second)
		require.True(t, reply.OK, "%+v", reply)
	}
	require.EqualValues(t, 2, calls.Load(), "only explicit calls launch again")
}

func TestServerRefusesInvalidRequestAndPeer(t *testing.T) {
	for _, uid := range []uint32{0, DesktopUID, 1001} {
		t.Run(string(rune('a'+uid%10)), func(t *testing.T) {
			listener, path := testSocket(t)
			var calls atomic.Int32
			testServer(t, listener, server{timeout: time.Second, maxClients: 1,
				peer:   func(*net.UnixConn) (uint32, error) { return uid, nil },
				handle: func(context.Context, Request) Response { calls.Add(1); return Response{OK: true} }})
			conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
			require.NoError(t, err)
			defer conn.Close()
			_, _ = conn.Write([]byte(`{"operation":"menus","path":"unexpected"}`))
			_ = conn.CloseWrite()
			var reply Response
			require.NoError(t, json.NewDecoder(conn).Decode(&reply))
			if uid == 1001 {
				require.Equal(t, 77, reply.Code)
			} else {
				require.Equal(t, 64, reply.Code)
			}
			require.Zero(t, calls.Load())
		})
	}
}

func TestClientChecksPeerBeforeSending(t *testing.T) {
	listener, path := testSocket(t)
	read := make(chan int, 1)
	go func() {
		conn, err := listener.AcceptUnix()
		if err != nil {
			read <- -1
			return
		}
		defer conn.Close()
		data := make([]byte, 512)
		n, _ := conn.Read(data)
		read <- n
	}()
	reply := call(context.Background(), path, func(*net.UnixConn) (uint32, error) { return 0, nil },
		Request{Operation: "menus"}, time.Second)
	require.Equal(t, 77, reply.Code)
	require.Zero(t, <-read)
}

func TestClientCancellationAndUncertainResultNeverRetry(t *testing.T) {
	listener, path := testSocket(t)
	var calls atomic.Int32
	dispatched := make(chan struct{})
	testServer(t, listener, server{peer: desktopPeer, timeout: time.Second, maxClients: 1,
		handle: func(ctx context.Context, _ Request) Response {
			calls.Add(1)
			close(dispatched)
			<-ctx.Done()
			return failure(75)
		}})
	ctx, cancel := context.WithCancel(context.Background())
	go func() { <-dispatched; cancel() }()
	start := time.Now()
	reply := call(ctx, path, desktopPeer, Request{Operation: "launch", App: "editor"}, time.Second)
	require.Equal(t, 75, reply.Code)
	require.Less(t, time.Since(start), time.Second)
	require.EqualValues(t, 1, calls.Load())
}

func TestServerConcurrencyLimitAndCancellation(t *testing.T) {
	listener, path := testSocket(t)
	dispatched := make(chan struct{})
	stopped := make(chan struct{})
	cancel := testServer(t, listener, server{peer: desktopPeer, timeout: time.Second, maxClients: 1,
		handle: func(ctx context.Context, _ Request) Response {
			close(dispatched)
			<-ctx.Done()
			close(stopped)
			return failure(75)
		}})
	first := make(chan Response, 1)
	go func() {
		first <- call(context.Background(), path, desktopPeer, Request{Operation: "menus"}, time.Second)
	}()
	<-dispatched
	reply := call(context.Background(), path, desktopPeer, Request{Operation: "menus"}, time.Second)
	require.False(t, reply.OK)
	cancel()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("worker was not canceled")
	}
	require.False(t, (<-first).OK)
}

func TestServerBoundsIncompleteAndOversizedInput(t *testing.T) {
	for _, raw := range []string{`{"operation":`, strings.Repeat("x", MaxRequestBytes+1)} {
		listener, path := testSocket(t)
		testServer(t, listener, server{peer: desktopPeer, timeout: 50 * time.Millisecond, maxClients: 1,
			handle: func(context.Context, Request) Response { t.Error("unexpected dispatch"); return failure(70) }})
		conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
		require.NoError(t, err)
		_, err = conn.Write([]byte(raw))
		require.NoError(t, err)
		require.NoError(t, conn.SetReadDeadline(time.Now().Add(time.Second)))
		var reply Response
		err = json.NewDecoder(conn).Decode(&reply)
		if err == nil {
			require.False(t, reply.OK)
		}
		_ = conn.Close()
	}
}

func TestClientRejectsMalformedOrOversizedReply(t *testing.T) {
	for _, raw := range []string{`{"ok":true,"app":"other","display":":100"}`, strings.Repeat("x", MaxResponseBytes+1)} {
		listener, path := testSocket(t)
		done := make(chan struct{})
		go func() {
			defer close(done)
			conn, err := listener.AcceptUnix()
			if err != nil {
				return
			}
			defer conn.Close()
			_, _ = decodeRequest(conn)
			_, _ = conn.Write([]byte(raw))
		}()
		reply := call(context.Background(), path, desktopPeer, Request{Operation: "launch", App: "editor"}, time.Second)
		require.Equal(t, 75, reply.Code)
		<-done
	}
}

func TestClientUnavailableInvalidAndPeerError(t *testing.T) {
	require.Equal(t, 64, call(context.Background(), "unused", desktopPeer, Request{}, time.Second).Code)
	require.Equal(t, 69, call(context.Background(), "/nonexistent/desktop.sock", desktopPeer, Request{Operation: "menus"}, time.Second).Code)
	listener, path := testSocket(t)
	defer listener.Close()
	require.Equal(t, 77, call(context.Background(), path, func(*net.UnixConn) (uint32, error) { return 0, errors.New("peer unavailable") }, Request{Operation: "menus"}, time.Second).Code)
}
