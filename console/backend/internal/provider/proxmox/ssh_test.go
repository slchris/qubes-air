package proxmox

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
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

// The exact bytes the node's shell receives. Pinned verbatim because every
// property of the upload — who can read the token, what a failure leaves behind
// — lives in this one string, and a "harmless" edit to it is how the old
// umask-dependent `cat > file` would come back.
func TestSnippetWriteCommandIsFixedAndOwnerOnly(t *testing.T) {
	w, err := newSnippetWrite(nodeSnippetDir, "qubes-air-dev-work.yaml")
	require.NoError(t, err)

	part := "'/var/lib/vz/snippets/.qubes-air-dev-work.yaml.part'"
	final := "'/var/lib/vz/snippets/qubes-air-dev-work.yaml'"
	assert.Equal(t,
		"umask 077 && set -C && rm -f "+part+" && cat > "+part+" && mv -f "+part+" "+final+
			" || { rm -f "+part+" && exit 1; exit 3; }",
		w.cmd)
	assert.Equal(t, "/var/lib/vz/snippets/.qubes-air-dev-work.yaml.part", w.part)
}

// Validation happens before any shell sees the string, for both halves of the
// path. Each of these would either escape the snippets directory or give the
// remote shell something to interpret.
func TestSnippetWriteRejectsUnsafeNamesAndDirs(t *testing.T) {
	for _, name := range []string{
		"", ".yaml", "x.yml.txt", "../x.yaml", "a/b.yaml", "x;id.yaml", "x y.yaml",
		"$(id).yaml", "`id`.yaml", "x'.yaml", "x\n.yaml", "x|id.yaml", "x&.yaml",
	} {
		_, err := newSnippetWrite(nodeSnippetDir, name)
		require.Error(t, err, "name %q", name)
		assert.Contains(t, err.Error(), "unsafe snippet name")
	}
	for _, dir := range []string{
		"", "relative/dir", "/var/lib/vz/snippets/", "/var/lib/vz/../../etc", "//etc",
		"/tmp/x y", "/tmp/x;id", "/tmp/$(id)", "/tmp/x'y",
	} {
		_, err := newSnippetWrite(dir, "qubes-air-x.yaml")
		require.Error(t, err, "dir %q", dir)
		assert.Contains(t, err.Error(), "unsafe snippet directory")
	}
}

// snippetShells are the shells the command is run under here. /bin/sh is dash
// on Debian and on the CI runner; bash is Debian's default login shell for root
// and so, presumably, what runs the command on a PVE node. noclobber and exit
// statuses differ in detail between the two (dash reports a refused redirect
// as 2, bash as 1), so each property is checked under every one present.
func snippetShells(t *testing.T) []string {
	t.Helper()
	var shells []string
	for _, sh := range []string{"/bin/sh", "/bin/bash", "/bin/dash"} {
		if _, err := os.Stat(sh); err == nil {
			shells = append(shells, sh)
		}
	}
	require.NotEmpty(t, shells, "no POSIX shell to run the snippet command under")
	return shells
}

// forEachShell runs fn as one subtest per shell.
func forEachShell(t *testing.T, fn func(t *testing.T, shell string)) {
	t.Helper()
	for _, sh := range snippetShells(t) {
		t.Run(filepath.Base(sh), func(t *testing.T) { fn(t, sh) })
	}
}

// runSnippetWriteLocally executes the command the way the node's login shell
// does, after first setting umask 000. Starting from the most permissive umask
// is what makes the mode assertions mean something: a file that comes out 0600
// got there because of the command, not because the test environment is strict.
// env entries are added to (and override) the test's environment.
func runSnippetWriteLocally(t *testing.T, shell string, w snippetWrite, content string, env ...string) int {
	t.Helper()
	cmd := exec.Command(shell, "-c", "umask 000; "+w.cmd)
	cmd.Stdin = strings.NewReader(content)
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return 0
	}
	var exit *exec.ExitError
	require.ErrorAs(t, err, &exit, "%s did not run: %s", shell, out)
	return exit.ExitCode()
}

// The G-D7 property itself, checked on a real filesystem: the stored snippet is
// 0600, including when it replaces a 0644 copy left by an upload made before
// the command set a umask, and when a readable staging file is in the way.
func TestSnippetWriteStoresAnOwnerOnlyFile(t *testing.T) {
	forEachShell(t, func(t *testing.T, shell string) {
		dir := t.TempDir()
		name := "qubes-air-dev-work.yaml"
		final := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(final, []byte("old: spent-token\n"), 0o600))
		require.NoError(t, os.Chmod(final, 0o644)) // what the umask-less `cat >` left behind

		w, err := newSnippetWrite(dir, name)
		require.NoError(t, err)
		// A stale, readable staging file must not be reused either: writing into it
		// would keep its mode, and the rename would publish that mode.
		require.NoError(t, os.WriteFile(w.part, []byte("stale"), 0o600))
		require.NoError(t, os.Chmod(w.part, 0o644))
		require.Equal(t, 0, runSnippetWriteLocally(t, shell, w, "token: live-secret\n"))

		info, err := os.Stat(final)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "a token-bearing snippet must not be readable by other accounts")
		got, err := os.ReadFile(final)
		require.NoError(t, err)
		assert.Equal(t, "token: live-secret\n", string(got))
		_, err = os.Stat(w.part)
		assert.True(t, os.IsNotExist(err), "the staging file must not outlive a successful write")
	})
}

// fakeRmScript stands in for rm(1). It does the real removal and then, the
// first time only, puts something at the path it just removed: the window
// between the command's `rm -f` and its `cat >`, which is where a co-writer
// racing the upload would act. $PLANT picks what: "file" is a 0644 regular
// file, "symlink" a symlink to $PLANT_TARGET. $PLANT_ONCE ends up holding
// "planted" only if that worked, so a broken fixture cannot pass as a refused
// write. Later calls, such as the failure path's cleanup, are plain rm.
const fakeRmScript = `#!/bin/sh
/bin/rm "$@" || exit
[ -e "$PLANT_ONCE" ] && exit 0
: > "$PLANT_ONCE"
for path do :; done
case $PLANT in
file) printf planted > "$path" && chmod 644 "$path" ;;
symlink) ln -s "$PLANT_TARGET" "$path" ;;
*) false ;;
esac && echo planted > "$PLANT_ONCE"
exit 0
`

// plantAfterRm returns environment entries that put fakeRmScript first on
// PATH, and the marker file it reports through.
func plantAfterRm(t *testing.T, plant, target string) (env []string, marker string) {
	t.Helper()
	bin := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bin, "rm"), []byte(fakeRmScript), 0o700))
	marker = filepath.Join(t.TempDir(), "planted")
	return []string{
		"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"PLANT_ONCE=" + marker,
		"PLANT=" + plant,
		"PLANT_TARGET=" + target,
	}, marker
}

// Defense in depth for the staging path: something that appears there after
// `rm -f` must make the write fail — never receive the token, never be
// published — and the failure path must remove it. Without `set -C` each of
// these cases writes the token through the planted entry and reports success.
// A FIFO or device planted there is not covered; see snippetWrite.
func TestSnippetWriteRefusesAStagingPathPlantedAfterRemoval(t *testing.T) {
	for _, tc := range []struct {
		name  string
		plant string
		// victim, when set, is the content of the regular file the planted
		// symlink points at.
		victim string
	}{
		{name: "regular file", plant: "file"},
		{name: "dangling symlink", plant: "symlink"},
		{name: "symlink to a regular file", plant: "symlink", victim: "victim\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			forEachShell(t, func(t *testing.T, shell string) {
				dir := t.TempDir()
				name := "qubes-air-dev-work.yaml"
				final := filepath.Join(dir, name)
				require.NoError(t, os.WriteFile(final, []byte("old: spent-token\n"), 0o600))
				target := filepath.Join(t.TempDir(), "elsewhere")
				if tc.victim != "" {
					require.NoError(t, os.WriteFile(target, []byte(tc.victim), 0o600))
				}
				w, err := newSnippetWrite(dir, name)
				require.NoError(t, err)
				env, marker := plantAfterRm(t, tc.plant, target)

				status := runSnippetWriteLocally(t, shell, w, "token: live-secret\n", env...)
				planted, err := os.ReadFile(marker)
				require.NoError(t, err)
				require.Equal(t, "planted\n", string(planted), "the fixture never planted anything")
				assert.Equal(t, snippetWriteFailed, status, "a planted staging path must fail the write")

				got, err := os.ReadFile(final)
				require.NoError(t, err)
				assert.Equal(t, "old: spent-token\n", string(got), "the planted entry must not be published")
				_, err = os.Lstat(w.part)
				assert.True(t, os.IsNotExist(err), "the failure path must remove what was planted")
				if tc.victim != "" {
					got, err = os.ReadFile(target)
					require.NoError(t, err)
					assert.Equal(t, tc.victim, string(got), "the token was written through the symlink")
				} else {
					_, err = os.Lstat(target)
					assert.True(t, os.IsNotExist(err), "the token was written through the symlink")
				}
			})
		})
	}
}

// A write that fails removes its staging file and says so with status 1 — the
// half-written copy may already hold the token.
func TestSnippetWriteFailureRemovesTheStagingFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the directory permissions this failure depends on")
	}
	forEachShell(t, func(t *testing.T, shell string) {
		dir := t.TempDir()
		w, err := newSnippetWrite(dir, "qubes-air-dev-work.yaml")
		require.NoError(t, err)
		require.NoError(t, os.Chmod(dir, 0o500))
		t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

		assert.Equal(t, snippetWriteFailed, runSnippetWriteLocally(t, shell, w, "token: live-secret\n"))
		_, err = os.Stat(w.part)
		assert.True(t, os.IsNotExist(err))
		_, err = os.Stat(filepath.Join(dir, "qubes-air-dev-work.yaml"))
		assert.True(t, os.IsNotExist(err), "a failed write must not publish a snippet")
	})
}

// When the staging file cannot be removed the command must not report a plain
// failure: status 3 is what lets the console tell the operator a token-bearing
// file is still on the node.
func TestSnippetWriteReportsAStagingFileItCannotRemove(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the directory permissions this failure depends on")
	}
	forEachShell(t, func(t *testing.T, shell string) {
		dir := t.TempDir()
		w, err := newSnippetWrite(dir, "qubes-air-dev-work.yaml")
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(w.part, []byte("stale"), 0o600))
		require.NoError(t, os.Chmod(dir, 0o500))
		t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

		assert.Equal(t, snippetWritePartialLeft, runSnippetWriteLocally(t, shell, w, "token: live-secret\n"))
		_, err = os.Stat(w.part)
		assert.NoError(t, err, "the fixture should still hold the file the command reported")
	})
}

// fakeNode is an in-process SSH server standing in for a PVE node. It records
// the exec command and stdin of one session and answers with a fixed exit
// status, so the transport half of the upload is tested without a real node.
// When that status is non-zero it first echoes the stdin it received back on
// stderr and stdout, the way a failing remote command may print what it was
// handed, so the tests can check that none of it reaches the console's error.
type fakeNode struct {
	addr    string
	command chan string
	stdin   chan []byte
}

func startFakeNode(t *testing.T, clientKey ssh.PublicKey, exitStatus uint32) (*fakeNode, ssh.PublicKey) {
	t.Helper()
	_, hostKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	hostSigner, err := ssh.NewSignerFromKey(hostKey)
	require.NoError(t, err)
	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
			if !bytes.Equal(k.Marshal(), clientKey.Marshal()) {
				return nil, errors.New("unknown client key")
			}
			return &ssh.Permissions{}, nil
		},
	}
	cfg.AddHostKey(hostSigner)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	node := &fakeNode{addr: ln.Addr().String(), command: make(chan string, 1), stdin: make(chan []byte, 1)}
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_, chans, reqs, err := ssh.NewServerConn(conn, cfg)
		if err != nil {
			return
		}
		go ssh.DiscardRequests(reqs)
		for nc := range chans {
			node.serveSession(nc, exitStatus)
		}
	}()
	return node, hostSigner.PublicKey()
}

func (n *fakeNode) serveSession(nc ssh.NewChannel, exitStatus uint32) {
	ch, reqs, err := nc.Accept()
	if err != nil {
		return
	}
	defer func() { _ = ch.Close() }()
	for req := range reqs {
		if req.Type != "exec" {
			_ = req.Reply(false, nil)
			continue
		}
		var payload struct{ Command string }
		_ = ssh.Unmarshal(req.Payload, &payload)
		_ = req.Reply(true, nil)
		in, _ := io.ReadAll(ch)
		n.command <- payload.Command
		n.stdin <- in
		if exitStatus != 0 {
			_, _ = ch.Stderr().Write(in)
			_, _ = ch.Write(in)
		}
		_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{exitStatus}))
		return
	}
}

// dialFakeNode connects through nodeSSHConfig, so the host key is pinned by a
// known_hosts entry exactly as it is for a real node.
func dialFakeNode(t *testing.T, exitStatus uint32) (*fakeNode, *ssh.Client) {
	t.Helper()
	private, clientPub := sshTestKey(t)
	node, hostPub := startFakeNode(t, clientPub, exitStatus)
	knownHosts := filepath.Join(t.TempDir(), "known_hosts")
	require.NoError(t, os.WriteFile(knownHosts, []byte(knownhosts.Line([]string{node.addr}, hostPub)+"\n"), 0o600))
	cfg, err := nodeSSHConfig(SSHConfig{PrivateKey: private, KnownHostsFile: knownHosts})
	require.NoError(t, err)
	client, err := ssh.Dial("tcp", node.addr, cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	return node, client
}

// What reaches the node is the fixed command, with the snippet on stdin and
// nowhere else.
func TestSnippetUploadSendsTheFixedCommandAndContentOnStdin(t *testing.T) {
	node, client := dialFakeNode(t, 0)
	w, err := newSnippetWrite(nodeSnippetDir, "qubes-air-dev-work.yaml")
	require.NoError(t, err)

	content := []byte("#cloud-config\ntoken: live-secret\n")
	require.NoError(t, w.run(client, "infra-node1", content))
	gotCmd := <-node.command
	assert.Equal(t, w.cmd, gotCmd)
	assert.NotContains(t, gotCmd, "live-secret", "the token must never be part of the command line")
	assert.Equal(t, content, <-node.stdin)
}

// A failed upload is an error that names the snippet and the node, and never
// echoes the content: it goes to the job log and the API. The fake node sends
// the content back on stderr and stdout as it fails, so this also covers a
// node whose error output repeats the token.
func TestSnippetUploadFailureIsReportedWithoutTheToken(t *testing.T) {
	for _, tc := range []struct {
		status uint32
		want   string
	}{
		{snippetWriteFailed, "upload snippet qubes-air-dev-work.yaml to infra-node1"},
		{snippetWritePartialLeft, "partial copy /var/lib/vz/snippets/.qubes-air-dev-work.yaml.part could not be removed"},
		{127, "upload snippet qubes-air-dev-work.yaml to infra-node1"},
	} {
		_, client := dialFakeNode(t, tc.status)
		w, err := newSnippetWrite(nodeSnippetDir, "qubes-air-dev-work.yaml")
		require.NoError(t, err)

		err = w.run(client, "infra-node1", []byte("token: live-secret\n"))
		require.Error(t, err, "status %d", tc.status)
		assert.Contains(t, err.Error(), tc.want)
		assert.NotContains(t, err.Error(), "live-secret")
		var exit *ssh.ExitError
		require.ErrorAs(t, err, &exit, "the remote status must stay inspectable")
		assert.Equal(t, int(tc.status), exit.ExitStatus())
	}
}

// The fake node really does send the token back when it fails, so the
// assertions above are not vacuous: a session that read stderr would see it.
func TestFakeNodeEchoesTheTokenOnStderrWhenItFails(t *testing.T) {
	_, client := dialFakeNode(t, snippetWriteFailed)
	session, err := client.NewSession()
	require.NoError(t, err)
	defer func() { _ = session.Close() }()
	var stderr bytes.Buffer
	session.Stderr = &stderr
	session.Stdin = strings.NewReader("token: live-secret\n")

	require.Error(t, session.Run("any command"))
	assert.Contains(t, stderr.String(), "live-secret")
}
