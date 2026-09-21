package proxmox

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

func sshTestKey(t *testing.T) (string, ssh.PublicKey) {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	der, err := x509.MarshalPKCS8PrivateKey(key)
	require.NoError(t, err)
	signer, err := ssh.NewSignerFromKey(key)
	require.NoError(t, err)
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})), signer.PublicKey()
}

func TestNodeSSHHostKeyVerification(t *testing.T) {
	private, host := sshTestKey(t)
	_, wrong := sshTestKey(t)
	file := filepath.Join(t.TempDir(), "known_hosts")
	require.NoError(t, os.WriteFile(file, []byte(knownhosts.Line([]string{"127.0.0.1"}, host)+"\n"), 0o600))
	cfg, err := nodeSSHConfig(SSHConfig{PrivateKey: private, KnownHostsFile: file})
	require.NoError(t, err)
	addr := &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 22}
	require.NoError(t, cfg.HostKeyCallback("127.0.0.1:22", addr, host))
	require.Error(t, cfg.HostKeyCallback("127.0.0.1:22", addr, wrong))
	unknown := &net.TCPAddr{IP: net.ParseIP("127.0.0.2"), Port: 22}
	require.Error(t, cfg.HostKeyCallback("127.0.0.2:22", unknown, host))
	_, err = nodeSSHConfig(SSHConfig{PrivateKey: private})
	require.Error(t, err)
	require.NoError(t, os.WriteFile(file, []byte("@revoked "+knownhosts.Line([]string{"127.0.0.1"}, host)+"\n"), 0o600))
	cfg, err = nodeSSHConfig(SSHConfig{PrivateKey: private, KnownHostsFile: file})
	require.NoError(t, err)
	require.Error(t, cfg.HostKeyCallback("127.0.0.1:22", addr, host))
}
