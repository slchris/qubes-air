package proxmox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// SSHConfig is the login used to write cloud-init snippets to a cluster node.
//
// The PVE API has no endpoint for uploading a snippet: cloud-init user-data
// must land in the node's snippets store on disk. The key is generated inside
// the console qube and never leaves it; only its public half is installed on
// the nodes.
type SSHConfig struct {
	// KnownHostsFile pins the public keys of every allowed cluster node.
	KnownHostsFile string
	Username       string
	PrivateKey     string
	Timeout        time.Duration
}

// snippetNameRE bounds the file name the adapter writes. The name is generated
// from the console's content-addressed snippet helper, not from user input, but
// it is validated anyway so a future caller cannot turn it into a shell path.
var snippetNameRE = regexp.MustCompile(`^[A-Za-z0-9._-]+\.ya?ml$`)

// dialNode opens an SSH client to a cluster node using cfg.
func nodeSSHConfig(cfg SSHConfig) (*ssh.ClientConfig, error) {
	if cfg.KnownHostsFile == "" {
		return nil, errors.New("proxmox: SSH known_hosts file is required")
	}
	hostKeys, err := knownhosts.New(cfg.KnownHostsFile)
	if err != nil {
		return nil, fmt.Errorf("proxmox: load SSH host keys: %w", err)
	}
	if cfg.PrivateKey == "" {
		return nil, errors.New("proxmox: no SSH private key configured")
	}
	signer, err := ssh.ParsePrivateKey([]byte(cfg.PrivateKey))
	if err != nil {
		return nil, fmt.Errorf("proxmox: parse SSH key: %w", err)
	}
	username := cfg.Username
	if username == "" {
		username = "root"
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	return &ssh.ClientConfig{
		User: username, Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: hostKeys, Timeout: timeout,
	}, nil
}

// dialNode verifies the server host key before sending any commands or data.
func dialNode(ctx context.Context, node string, cfg SSHConfig) (*ssh.Client, error) {
	clientCfg, err := nodeSSHConfig(cfg)
	if err != nil {
		return nil, err
	}

	addr := net.JoinHostPort(node, "22")
	ctx, cancel := context.WithTimeout(ctx, clientCfg.Timeout)
	defer cancel()
	dialer := &net.Dialer{Timeout: clientCfg.Timeout}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("proxmox: ssh dial %s: %w", addr, err)
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	sshConn, chans, reqs, err := ssh.NewClientConn(conn, addr, clientCfg)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("proxmox: ssh handshake %s: %w", addr, err)
	}
	return ssh.NewClient(sshConn, chans, reqs), nil
}

// runSSH runs a fixed command on the node and returns its stdout.
//
// The command is assembled by this package from validated inputs (an IP that
// already parsed as an address); callers must not pass user-controlled text.
// It exists for node-local checks the PVE API cannot express, such as whether
// an address is already claimed on the bridge.
func runSSH(ctx context.Context, node string, cfg SSHConfig, command string) (string, error) {
	client, err := dialNode(ctx, node, cfg)
	if err != nil {
		return "", err
	}
	defer func() { _ = client.Close() }()

	session, err := client.NewSession()
	if err != nil {
		return "", fmt.Errorf("proxmox: ssh session %s: %w", node, err)
	}
	defer func() { _ = session.Close() }()

	var out, errOut bytes.Buffer
	session.Stdout = &out
	session.Stderr = &errOut
	if err := session.Run(command); err != nil {
		return out.String(), fmt.Errorf("proxmox: ssh %s: %w: %s", node, err, strings.TrimSpace(errOut.String()))
	}
	return out.String(), nil
}

// uploadSnippet writes content to the node's local snippets store over SSH.
//
// The remote command is a fixed `cat` with a validated name; no part of the
// content or the name is interpreted by a shell.
func uploadSnippet(ctx context.Context, node string, cfg SSHConfig, name string, content []byte) error {
	if !snippetNameRE.MatchString(name) {
		return fmt.Errorf("proxmox: refusing unsafe snippet name %q", name)
	}
	client, err := dialNode(ctx, node, cfg)
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()

	session, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("proxmox: ssh session %s: %w", node, err)
	}
	defer func() { _ = session.Close() }()

	session.Stdin = bytes.NewReader(content)
	// Fixed path, validated name, no shell interpolation of either.
	if err := session.Run("cat > /var/lib/vz/snippets/" + name); err != nil {
		return fmt.Errorf("proxmox: upload snippet %s to %s: %w", name, node, err)
	}
	return nil
}
