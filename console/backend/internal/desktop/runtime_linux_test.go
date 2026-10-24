//go:build linux

package desktop

import (
	"context"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRealKernelPeerCredentials(t *testing.T) {
	listener, path := testSocket(t)
	client, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
	require.NoError(t, err)
	defer client.Close()
	conn, err := listener.AcceptUnix()
	require.NoError(t, err)
	defer conn.Close()
	for _, side := range []*net.UnixConn{client, conn} {
		uid, err := peerUID(side)
		require.NoError(t, err)
		require.EqualValues(t, os.Getuid(), uid)
	}
}

func TestXpraDialUsesKernelServerIdentity(t *testing.T) {
	_, path := testSocket(t)
	conn, err := dialXpra(context.Background(), path, peerUID)
	if os.Getuid() != DesktopUID {
		require.Error(t, err, "an actual non-desktop listener must be rejected")
		require.Nil(t, conn)
		return
	}
	require.NoError(t, err)
	require.NoError(t, conn.Close())
}

func TestSocketDoesNotReplaceExistingEndpoint(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.Chmod(dir, 0700))
	path := filepath.Join(dir, "control.sock")
	listener, err := listenSocket(path, uint32(os.Getuid()))
	require.NoError(t, err)
	defer listener.Close()
	_, err = listenSocket(path, uint32(os.Getuid()))
	require.Error(t, err, "second listener must leave the first intact")
	before, err := os.Lstat(path)
	require.NoError(t, err)
	require.NoError(t, listener.Close())
	after, err := os.Lstat(path)
	require.NoError(t, err, "systemd owns socket cleanup, not the broker")
	require.True(t, os.SameFile(before, after))
	_, err = listenSocket(path, uint32(os.Getuid()))
	require.Error(t, err, "a stale endpoint is never unlinked")
}

func TestRuntimeDirectoryAndRootRefusal(t *testing.T) {
	dir := t.TempDir()
	uid := uint32(os.Getuid())
	require.NoError(t, os.Chmod(dir, 0755))
	_, err := listenSocket(filepath.Join(dir, "control.sock"), uid)
	require.Error(t, err)
	require.NoError(t, os.Chmod(dir, 0700))
	_, err = listenSocket(filepath.Join(dir, "control.sock"), uid+1)
	require.Error(t, err)
	link := filepath.Join(t.TempDir(), "runtime")
	require.NoError(t, os.Symlink(dir, link))
	_, err = listenSocket(filepath.Join(link, "control.sock"), uid)
	require.Error(t, err)
	if os.Getuid() == 0 {
		require.Error(t, Run(context.Background()))
	}
}

func TestSessionEnvironmentDoesNotInheritCallerConfiguration(t *testing.T) {
	account := &user.User{Uid: "1000", Username: "desktop", HomeDir: "/home/desktop"}
	t.Setenv("DISPLAY", "other:1")
	t.Setenv("PYTHONPATH", "/unexpected")
	t.Setenv("HOME", "/root")
	env, err := sessionEnvironment(account, "unix:path=/tmp/dbus-fixture")
	require.NoError(t, err)
	require.Contains(t, env, "HOME=/home/desktop")
	require.Contains(t, env, "DISPLAY=:100")
	require.NotContains(t, env, "PYTHONPATH=/unexpected")
	_, err = sessionEnvironment(account, "tcp:host=localhost")
	require.Error(t, err)
	account.HomeDir = "/"
	_, err = sessionEnvironment(account, "unix:path=/tmp/dbus-fixture")
	require.Error(t, err)
}
