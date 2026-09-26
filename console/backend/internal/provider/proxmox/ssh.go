package proxmox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"path"
	"regexp"
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

// nodeSSHConfig builds the SSH client config for a cluster node from cfg.
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

// nodeSnippetDir is the node-local snippets store of the default "local" dir
// datastore (path /var/lib/vz, content type "snippets").
const nodeSnippetDir = "/var/lib/vz/snippets"

// snippetDirRE bounds the directory half of the remote command the same way
// snippetNameRE bounds the file half: an absolute path of characters no POSIX
// shell treats specially, so the fixed command below never needs escaping.
var snippetDirRE = regexp.MustCompile(`^/[A-Za-z0-9._/-]+$`)

// Exit statuses of a snippetWrite command. The command maps every failure onto
// one of these two, so the console can tell "nothing was left behind" from "a
// partial copy of the snippet is still on the node".
const (
	snippetWriteFailed      = 1
	snippetWritePartialLeft = 3
)

// snippetWrite is the fixed remote command that stores one snippet, read from
// stdin, at dir/name so that only its owner can read it.
//
// The snippet carries the qube's one-time bootstrap token, which AGENTS.md §5
// treats as a secret. The command used to be a bare `cat > file`, so the mode
// was whatever the SSH login's umask produced — 0644 under Debian's default
// 022 — and every local account on the node could read a live token. The
// reader that matters is qemu-server generating the cloud-init drive at VM
// start as root, so owner-only is enough. That the reader is root is inferred
// from how PVE starts VMs, not observed here; step 9 of
// docs/acceptance-real-machine.md is where a real node confirms it.
//
//   - `umask 077` makes every file the command creates 0600.
//   - The bytes go into a temp file that is removed first, so they only ever
//     land in an inode created under that umask. `cat > existing` would keep
//     the old inode's mode, and a copy uploaded before this change is 0644.
//   - `set -C` (noclobber) makes the shell create the temp file with O_EXCL
//     when nothing is there, and refuse the redirect when a regular file is.
//     A regular file, a dangling symlink or a symlink to a regular file that
//     appears between `rm -f` and `cat >` therefore fails the write instead of
//     receiving the token, and the failure path below removes it. ssh_test.go
//     checks this under bash and dash.
//   - `mv -f` renames the temp over the final name, so a 0644 file from an older
//     upload is replaced rather than rewritten in place, and the name a VM
//     references never points at a file this command has just truncated.
//   - On any failure the temp is removed and the status is snippetWriteFailed;
//     if even that removal fails the status is snippetWritePartialLeft, so a
//     leftover token-bearing file is reported rather than silently kept.
//
// What this does not defend against is another account that can write to the
// snippets directory. Such an account can plant a FIFO or a device, or a
// symlink to one, at the temp path; both shells open an existing non-regular
// file without O_EXCL even under noclobber. It could also simply replace the
// final file. The command assumes that only the SSH login can
// write to dir. On PVE that login is root by default, and /var/lib/vz/snippets
// is expected to be root-owned and not group- or world-writable. That is
// inferred, not observed on the cluster, so step 9 of
// docs/acceptance-real-machine.md checks it. A default ACL on the directory
// would also override the umask, so the same step checks for one.
//
// The temp name depends only on the snippet name, so two uploads of one snippet
// to the same node at the same time would share it. For one qube that does not
// happen: every provision or resume holds the qube's claim (claimAndEnqueue in
// internal/service/qube_service.go) for its whole run. Two qubes that share a
// name would already overwrite each other's final snippet; the shared temp name
// adds nothing to that. A per-upload random suffix would drop the assumption,
// but a temp file left by a killed session would then never be reclaimed. With
// the fixed name, the next upload's `rm -f` removes it.
//
// dir and name are validated to characters with no meaning to a POSIX shell,
// then single-quoted anyway. The content is never part of the command; it only
// travels on stdin. The command needs a POSIX login shell on the node, which is
// the PVE default.
type snippetWrite struct {
	name string
	// part is the staging path, reported when a failed write cannot remove it.
	part string
	cmd  string
}

// newSnippetWrite validates dir and name and builds the command for them.
func newSnippetWrite(dir, name string) (snippetWrite, error) {
	if !snippetNameRE.MatchString(name) {
		return snippetWrite{}, fmt.Errorf("proxmox: refusing unsafe snippet name %q", name)
	}
	if !snippetDirRE.MatchString(dir) || path.Clean(dir) != dir {
		return snippetWrite{}, fmt.Errorf("proxmox: refusing unsafe snippet directory %q", dir)
	}
	// A dot file, so PVE does not list it as snippet content while it exists.
	part := dir + "/." + name + ".part"
	final := dir + "/" + name
	cmd := fmt.Sprintf("umask 077 && set -C && rm -f '%[1]s' && cat > '%[1]s' && mv -f '%[1]s' '%[2]s'"+
		" || { rm -f '%[1]s' && exit %[3]d; exit %[4]d; }",
		part, final, snippetWriteFailed, snippetWritePartialLeft)
	return snippetWrite{name: name, part: part, cmd: cmd}, nil
}

// uploadSnippet writes content to the node's local snippets store over SSH,
// readable by the SSH login only (see snippetWrite).
//
// The name is validated before anything is dialed, and the remote command is
// fixed; no part of the content is interpreted by a shell.
func uploadSnippet(ctx context.Context, node string, cfg SSHConfig, name string, content []byte) error {
	w, err := newSnippetWrite(nodeSnippetDir, name)
	if err != nil {
		return err
	}
	client, err := dialNode(ctx, node, cfg)
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()
	return w.run(client, node, content)
}

// run executes the command on an established connection with content on stdin,
// and turns its exit status into an error that says what, if anything, was left
// on the node.
//
// The error never carries the content or the remote stderr: the content is the
// bootstrap token, and job logs and API errors are no place for it.
func (w snippetWrite) run(client *ssh.Client, node string, content []byte) error {
	session, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("proxmox: ssh session %s: %w", node, err)
	}
	defer func() { _ = session.Close() }()

	session.Stdin = bytes.NewReader(content)
	err = session.Run(w.cmd)
	if err == nil {
		return nil
	}
	var exit *ssh.ExitError
	if errors.As(err, &exit) && exit.ExitStatus() == snippetWritePartialLeft {
		return fmt.Errorf("proxmox: upload snippet %s to %s failed and the partial copy %s could not be removed; "+
			"it may hold the bootstrap token, delete it on the node: %w", w.name, node, w.part, err)
	}
	return fmt.Errorf("proxmox: upload snippet %s to %s: %w", w.name, node, err)
}
