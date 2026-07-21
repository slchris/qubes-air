package service

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// agentInstallDir is where the agent's mTLS material lands on the remote.
const agentInstallDir = "/etc/qubes-air"

// agentInstallerPath is the package installer this document delivers, and
// agentFailureMarker is where that installer records why a qube ended up with
// no agent. The marker is a file rather than only a log line because it
// outlives log rotation and answers "why is this qube unreachable" without
// replaying the boot.
const (
	agentInstallerPath = "/usr/local/sbin/qubes-air-install-agent"
	agentFailureMarker = agentInstallDir + "/AGENT-INSTALL-FAILED"
)

// dataMountScriptPath and dataMountUnitPath install the persistent-data disk
// mount. The compute VM is ephemeral — every resume rebuilds it from the
// template and throws its root disk away — while the data disk is a separate,
// retained volume (the storage-holder VM owns it; see
// terraform/modules/remote-qube-base) that gets reattached as scsi1 on every
// boot. Without this, anything a user writes lands on the ephemeral root and
// vanishes on the next resume, which defeats the entire storage/compute split.
const (
	dataMountScriptPath = "/usr/local/sbin/qubes-air-mount-data"
	dataMountUnitPath   = "/etc/systemd/system/qubes-air-data.service"
	dataMountService    = "qubes-air-data.service"
	dataMountPoint      = "/data"
	dataDiskLabel       = "qubesair-data"
)

// AgentPackage pins the .deb that puts the agent binary on a booting qube.
//
// The binary is deliberately not baked into the VM image; it is fetched at
// first boot from the LAN artifact store. That store takes unauthenticated
// uploads and serves them over plain HTTP, so SHA256 here is not a guard
// against corrupt downloads — it is the only integrity control in the chain.
// It can be trusted despite the untrusted download because it travels inside
// this document, which reaches the guest over console -> terraform SFTP ->
// Proxmox snippet -> cloud-init.
type AgentPackage struct {
	URL     string
	SHA256  string
	Version string
	// AptMirror and AptSecurityMirror point the guest at a local Debian mirror.
	//
	// They live here because setting user-data REPLACES a template's vendor
	// data, so a mirror the template configured at boot silently stops being
	// applied. Measured on real hardware: with the public redirector, installing
	// qemu-guest-agent and curl took 857s — 99% of a 15-minute provision, enough
	// to push the apply past the executor's timeout. Empty leaves the image's
	// own sources alone.
	AptMirror         string
	AptSecurityMirror string
}

// sha256Hex matches the bare 64-character digest sha256sum(1) expects.
var sha256Hex = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)

// resolve returns the values to embed, plus the reason the package is not
// deliverable when it is not.
//
// A URL without a digest is treated as NOT deliverable on purpose. Installing
// an unpinned .deb as root from an unauthenticated endpoint hands every new
// qube to whoever uploaded last; a qube that loudly has no agent is the
// recoverable failure, so that is the direction this falls in.
func (p AgentPackage) resolve() (url, sha, reason string) {
	switch {
	case p.URL == "" && p.SHA256 == "":
		return "", "", "orchestrator.agent_package_url and agent_package_sha256 are not set on the console"
	case p.URL == "":
		return "", "", "orchestrator.agent_package_url is not set on the console"
	case p.SHA256 == "":
		return "", "", "orchestrator.agent_package_sha256 is not set; an unpinned package will not be installed"
	case !strings.HasPrefix(p.URL, "http://") && !strings.HasPrefix(p.URL, "https://"):
		return "", "", "orchestrator.agent_package_url is not an http(s) URL"
	case strings.ContainsAny(p.URL, "'\"`\\ \t\r\n"):
		// The URL is single-quoted in the installer script below. Rejecting the
		// whole class of quoting characters keeps a console-side typo from
		// becoming shell injection as root in every guest.
		return "", "", "orchestrator.agent_package_url contains quotes or whitespace and was rejected"
	case !sha256Hex.MatchString(p.SHA256):
		return "", "", "orchestrator.agent_package_sha256 is not 64 hex characters"
	}
	// Lowercased because sha256sum(1) emits lowercase and its -c comparison is
	// textual; an uppercase digest would fail verification on a correct file.
	return p.URL, strings.ToLower(p.SHA256), ""
}

// AgentIdentityDoc is everything per-qube that cloud-init delivers.
//
// Note what is NOT here: a certificate and a private key. Both used to be, and
// removing them is the point of the bootstrap token (docs/bootstrap-design.md
// section 9). The agent now generates its own key at first boot and exchanges
// the token for a signed certificate over a channel the console dials, so the
// key exists only on the machine it authenticates.
//
// The struct is what makes that hard to undo by accident: adding key material
// back means adding a field, in this file, next to this comment.
type AgentIdentityDoc struct {
	// CAPEM is the console's CA. Public by nature — the agent verifies the
	// console's client certificate with it, and it is the trust root the agent
	// refuses to let any later exchange replace.
	CAPEM string
	// BootstrapToken authorizes issuing exactly one certificate, for exactly
	// one qube, once, before it expires.
	BootstrapToken string
}

// validate refuses a document that could not produce a working agent.
//
// Both fields are required, and neither failure is worth being lenient about:
// without the CA the agent cannot tell the console from anyone else who reaches
// its port, and without the token the console cannot tell the agent from anyone
// answering at its address. A guest that boots with half of this looks
// provisioned and can never authenticate.
func (d AgentIdentityDoc) validate() error {
	if strings.TrimSpace(d.CAPEM) == "" {
		return fmt.Errorf("no CA to deliver: the agent would have nothing to verify the console against")
	}
	if strings.TrimSpace(d.BootstrapToken) == "" {
		return fmt.Errorf("no bootstrap token to deliver: the agent could never be issued a certificate")
	}
	return nil
}

// RenderAgentUserData produces the cloud-init user-data that installs an
// agent's identity and the agent itself on first boot.
//
// This is the delivery half of bootstrap: cloud-init is the only channel that
// exists before the agent is running, and it is what breaks the chicken-and-egg
// — the agent cannot authenticate to ask for an identity until it has one.
//
// It carries the agent package too, because the binary is not in the VM image —
// a real deployment produced a qube where every other step succeeded and the
// agent unit simply did not exist.
//
// The output is deliberately a cloud-config document rather than a script.
// write_files is declarative, idempotent, and sets permissions atomically —
// a shell script doing the same has to get ordering and umask right, and gets
// them wrong quietly.
func RenderAgentUserData(remoteName string, id AgentIdentityDoc, listen string, pkg AgentPackage, encryptData bool) (string, error) {
	if err := id.validate(); err != nil {
		return "", err
	}
	if remoteName == "" {
		return "", fmt.Errorf("remote name is required: the agent reports it and qubesair.Ping returns it")
	}
	if listen == "" {
		listen = "0.0.0.0:8443"
	}

	// The token is mode 0600 and the CA 0644. The distinction still matters,
	// though it costs less than it used to: a world-readable token lets any
	// local account on the guest claim this qube's identity before the agent
	// does. The blast radius is one qube and one boot rather than a 90-day
	// credential, which is the trade the token design bought.
	var b strings.Builder
	b.WriteString("#cloud-config\n")
	b.WriteString("# Generated by the Qubes Air console — installs this agent's mTLS identity\n")
	b.WriteString("# and the agent package itself.\n")
	b.WriteString("#\n")
	b.WriteString("# The agent binary is NOT in the VM image: it is fetched at first boot from the\n")
	b.WriteString("# artifact store and verified against the SHA256 pinned below. That store has no\n")
	b.WriteString("# authentication and serves over plain HTTP, so the digest is the only integrity\n")
	b.WriteString("# control in the chain — it is trustworthy only because it travels in THIS\n")
	b.WriteString("# document. Never let the guest install a package this document did not pin.\n")
	b.WriteString("#\n")
	b.WriteString("# This document contains NO private key. It carries the public CA and a\n")
	b.WriteString("# one-shot bootstrap token; the agent generates its own key at first boot and\n")
	b.WriteString("# sends only a CSR, so the key never leaves the machine it authenticates.\n")
	b.WriteString("# The token authorizes exactly one certificate, for one qube, once.\n")
	b.WriteString("# See docs/bootstrap-design.md section 9.\n")
	b.WriteString("#\n")
	b.WriteString("# This document is SELF-SUFFICIENT on purpose. Setting user-data REPLACES a\n")
	b.WriteString("# template's own cicustom entry, so anything the template relied on its vendor\n")
	b.WriteString("# snippet to do stops happening. qemu-guest-agent is the one that matters:\n")
	b.WriteString("# without it terraform waits for an IP the guest never reports, and the apply\n")
	b.WriteString("# hangs until its timeout with the VM sitting there running.\n")
	// The guest must know its own name.
	//
	// Without this every qube in the fleet comes up as "localhost" with an EMPTY
	// /etc/hostname — verified on real hardware — so nothing in a log line says
	// which machine produced it, and fleet-wide troubleshooting has to correlate
	// by IP. Empty is worse than wrong: tools that read /etc/hostname directly
	// get nothing at all.
	//
	// It matters more here than on an ordinary VM because this name already
	// exists in three places that must agree — the Proxmox VM name, the agent
	// certificate's common name (agent-<name>), and QUBESAIR_REMOTE_NAME below.
	// Leaving the OS itself out of that set is how they start to drift.
	//
	// manage_etc_hosts keeps /etc/hosts consistent with it; without that a
	// hostname with no matching entry makes anything resolving its own name
	// wait for a DNS timeout first.
	writeAptMirror(&b, pkg)
	fmt.Fprintf(&b, "hostname: %s\n", remoteName)
	b.WriteString("preserve_hostname: false\n")
	b.WriteString("manage_etc_hosts: true\n")
	b.WriteString("packages:\n")
	b.WriteString("  - qemu-guest-agent\n")
	// curl fetches the agent package. The image normally has it; naming it here
	// removes the branch where it does not, and costs nothing when it is
	// already installed.
	b.WriteString("  - curl\n")
	if encryptData {
		// cryptsetup is what qubesair.UnlockData drives. Named here so a blank
		// image can still open its LUKS disk; the console pushes the key later.
		b.WriteString("  - cryptsetup\n")
	}
	b.WriteString("write_files:\n")

	// The CA is public — it is what the agent verifies the console WITH, not a
	// secret. The token is not public, but it authorizes exactly one
	// certificate, for exactly one qube, once, and only until it expires.
	//
	// There is deliberately no agent.pem and no agent-key.pem here. The agent
	// generates its own key at first boot and hands out only a CSR, so the
	// private key never exists anywhere but the machine it authenticates. That
	// closes the hole this document used to be: on Proxmox, cloud-init data is
	// readable through the API by anyone holding VM.Config.Cloudinit, which
	// made that permission equivalent to holding every agent identity ever
	// delivered.
	writeFile(&b, agentInstallDir+"/ca.pem", "0644", id.CAPEM)
	writeFile(&b, agentInstallDir+"/bootstrap-token", "0600", id.BootstrapToken)
	writeFile(&b, agentInstallDir+"/agent.env",
		"0644", fmt.Sprintf("QUBESAIR_REMOTE_NAME=%s\nQUBESAIR_LISTEN=%s\n", remoteName, listen))

	// The installer is delivered as a file rather than inlined into runcmd so
	// its quoting is YAML's problem, not a shell-inside-a-flow-sequence problem.
	// 0700: it is the thing that installs a root-owned service.
	writeFile(&b, agentInstallerPath, "0700", agentInstallerScript(pkg))

	// Persistent-data disk mount: a script plus a systemd unit that runs it on
	// every boot. It resolves the data disk by its stable scsi address, formats
	// it ONLY on the first ever boot, and mounts it at /data — the mechanism that
	// makes the retained data disk actually usable. Delivered here (rather than in
	// the VM image) so it ships with the same document as the rest of the agent
	// contract and needs no template change. 0700: it mounts filesystems as root.
	//
	// Skipped entirely for an encrypted qube: the disk is a LUKS container with
	// no key on the machine, so a boot-time mount could only fail (or, worse,
	// mistake the ciphertext for a blank disk). The console opens it via
	// qubesair.UnlockData after bootstrap instead.
	if !encryptData {
		writeFile(&b, dataMountScriptPath, "0700", dataDiskMountScript())
		writeFile(&b, dataMountUnitPath, "0644", dataDiskMountUnit())
	}

	// Start the agent only after the files exist. cloud-init runs runcmd after
	// write_files, so the ordering is guaranteed rather than hoped for.
	b.WriteString("runcmd:\n")
	b.WriteString("  - [ chown, -R, 'root:root', " + agentInstallDir + " ]\n")
	b.WriteString("  - [ chmod, '0750', " + agentInstallDir + " ]\n")
	// The guest agent must be running for terraform to learn the VM's address.
	// Enabled explicitly rather than trusting the package's own preset.
	//
	// It comes BEFORE the agent installer and must stay there. cloud-init's
	// runcmd script has no "set -e", so a failing installer cannot undo what
	// already ran — but an installer that hangs on a download would strand
	// terraform waiting for an IP the guest never reports, and the apply sits
	// there until it times out. That happened once already.
	b.WriteString("  - [ systemctl, enable, --now, qemu-guest-agent ]\n")
	// Mount the persistent data disk before the agent (and before any workload)
	// can write to /data. --now runs it immediately on this boot; the unit's
	// WantedBy also arms it for later reboots. On resume, cloud-init re-runs and
	// re-enables it, and the script's format-only-if-blank guard means the data
	// already on the reattached disk is mounted, never wiped. Skipped for an
	// encrypted qube — the console opens /data via qubesair.UnlockData instead.
	if !encryptData {
		b.WriteString("  - [ systemctl, enable, --now, " + dataMountService + " ]\n")
	}
	// Downloads, verifies and installs the agent package, then proves the unit
	// is actually running. Its non-zero exit is deliberate: it surfaces in
	// "cloud-init status --long" instead of leaving a dead agent looking like a
	// clean boot.
	b.WriteString("  - [ " + agentInstallerPath + " ]\n")

	return b.String(), nil
}

// agentInstallerScript renders the guest-side installer.
//
// Every branch ends in either a running agent or a loud, recorded failure.
// That is the whole point: the incident this replaces was a qube that booted
// clean, reported healthy and had no agent, because enabling a unit that does
// not exist succeeds quietly enough to miss.
func agentInstallerScript(pkg AgentPackage) string {
	url, sha, reason := pkg.resolve()
	return strings.NewReplacer(
		"@URL@", url,
		"@SHA@", sha,
		"@VERSION@", strings.Map(dropShellQuoting, pkg.Version),
		"@REASON@", reason,
		"@MARKER@", agentFailureMarker,
	).Replace(agentInstallerTemplate)
}

// dataDiskMountScript renders the boot-time data-disk mount.
//
// It resolves the data disk by its stable scsi address (scsi1 -> LUN 1) rather
// than by /dev/sdX. Device letters are assigned in probe order and are NOT
// stable: on this hardware the OS disk has come up as /dev/sdb, so a script that
// hard-codes /dev/sdb for the data disk would eventually reformat the OS. The
// by-path glob matches only the whole data disk — the OS disk (…0:0:0:0) and any
// partition (…-part*) never match.
//
// It formats the disk ONLY when it carries no filesystem yet — its first ever
// boot. On every resume thereafter the reattached disk already holds ext4, so
// mkfs is skipped and the data is mounted untouched. That one guard is what makes
// "the data disk is retained" mean anything to a user.
func dataDiskMountScript() string {
	return strings.NewReplacer(
		"@BYPATH@", "/dev/disk/by-path/*-scsi-0:0:0:1",
		"@MOUNT@", dataMountPoint,
		"@LABEL@", dataDiskLabel,
	).Replace(dataDiskMountTemplate)
}

// dataDiskMountUnit renders the systemd unit that runs the mount script. It is a
// oneshot with RemainAfterExit so cloud-init's "enable --now" both arms it for
// future reboots and runs it now, and Before=qubes-air-agent.service keeps /data
// present before the agent (and anything it launches) can write there.
func dataDiskMountUnit() string {
	return strings.NewReplacer("@SCRIPT@", dataMountScriptPath).Replace(dataDiskMountUnitTemplate)
}

// dataDiskMountTemplate is the mount script body. Placeholders are substituted by
// dataDiskMountScript; none of the substituted values are attacker-controlled
// (they are compile-time constants), so no quoting dance is required.
const dataDiskMountTemplate = `#!/bin/sh
# qubes-air-mount-data — format-if-blank and mount the persistent data disk.
# Managed by the Qubes Air console; delivered via cloud-init. See cloudinit.go.
set -eu

# Resolve the data disk by its stable scsi address (scsi1). Wait briefly for the
# by-path symlink in case udev has not settled; a genuinely diskless qube falls
# through the loop and the script becomes a no-op.
dev=""
i=0
while [ "$i" -lt 15 ]; do
    for p in @BYPATH@; do
        [ -e "$p" ] || continue
        dev="$(readlink -f "$p")"
        break
    done
    [ -n "$dev" ] && break
    i=$((i + 1))
    sleep 1
done
if [ -z "$dev" ]; then
    echo "qubes-air-mount-data: no data disk (scsi1) attached; nothing to mount" >&2
    exit 0
fi

# Format ONLY a blank disk. blkid prints an existing filesystem's type and
# nothing for a blank disk, so an empty result — and only an empty result —
# triggers mkfs. Whole-disk ext4 (no partition table) keeps reattach trivial.
fstype="$(blkid -o value -s TYPE "$dev" 2>/dev/null || true)"
if [ -z "$fstype" ]; then
    echo "qubes-air-mount-data: $dev is blank; creating ext4 (first boot)" >&2
    mkfs.ext4 -q -L @LABEL@ "$dev"
fi

mkdir -p @MOUNT@
if mountpoint -q @MOUNT@; then
    echo "qubes-air-mount-data: @MOUNT@ already mounted" >&2
else
    mount "$dev" @MOUNT@
    echo "qubes-air-mount-data: mounted $dev at @MOUNT@" >&2
fi
`

// dataDiskMountUnitTemplate is the systemd unit body. @SCRIPT@ is a compile-time
// constant path, so it needs no escaping.
const dataDiskMountUnitTemplate = `[Unit]
Description=Mount the Qubes Air persistent data disk
After=local-fs.target
Before=qubes-air-agent.service

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=@SCRIPT@

[Install]
WantedBy=multi-user.target
`

// dropShellQuoting strips characters that would break out of the single quotes
// the installer wraps these values in. Only the advisory version string needs
// it; the URL and digest are rejected outright by AgentPackage.resolve.
func dropShellQuoting(r rune) rune {
	if strings.ContainsRune("'\"`\\\n\r", r) {
		return -1
	}
	return r
}

// agentInstallerTemplate is the installer body. Placeholders are substituted by
// agentInstallerScript; every substituted value is single-quoted in the script,
// and resolve() guarantees none of them can close that quote.
const agentInstallerTemplate = `#!/bin/sh
# Installs the Qubes Air agent package pinned by the console.
#
# The agent binary is not in the VM image, so this script is the only thing that
# ever puts it on a remote. It is written to fail loudly: a qube with no agent
# must never look like a qube that came up fine.
set -u

PKG_URL='@URL@'
PKG_SHA='@SHA@'
PKG_VERSION='@VERSION@'
SKIP_REASON='@REASON@'
MARKER='@MARKER@'
# /run is tmpfs: an unverified download never reaches persistent storage.
DEB=/run/qubes-air-agent.deb

say() {
    logger -t qubes-air-agent-install -p daemon.notice "$1" 2>/dev/null || true
    echo "qubes-air-agent-install: $1" >&2
}

# shout is for states an operator has to know about. It hits three places on
# purpose: cloud-init-output.log (stderr), the journal, and the serial console
# an operator may be watching while the VM boots.
shout() {
    logger -s -t qubes-air-agent-install -p daemon.err "$1" 2>/dev/null || true
    echo "qubes-air-agent-install: $1" >&2
    echo "qubes-air-agent-install: $1" > /dev/console 2>/dev/null || true
}

fail() {
    shout "AGENT NOT INSTALLED: $1"
    # Outlives the logs, and is what a later "why is this qube unreachable"
    # can read over ssh without replaying the boot.
    printf 'AGENT NOT INSTALLED: %s\n' "$1" > "$MARKER" 2>/dev/null || true
    rm -f "$DEB"
    exit 1
}

rm -f "$MARKER"

if [ -z "$PKG_URL" ] || [ -z "$PKG_SHA" ]; then
    # No package pinned. Fall back to a unit already baked into the image so an
    # older image still works, but say so either way — an unpinned console
    # cannot deliver agents, and that is a console-side bug to go fix.
    shout "no agent package pinned: $SKIP_REASON"
    if systemctl enable --now qubes-air-agent >/dev/null 2>&1 &&
       systemctl is-active --quiet qubes-air-agent; then
        say "started a qubes-air-agent unit already present in the image"
        exit 0
    fi
    fail "qubes-air-agent unit not present and no package URL to install one from"
fi

fetch() {
    if command -v curl >/dev/null 2>&1; then
        curl -fsS --retry 3 --retry-delay 2 --max-time 120 -o "$DEB" "$PKG_URL"
    elif command -v wget >/dev/null 2>&1; then
        wget -q -T 30 -t 3 -O "$DEB" "$PKG_URL"
    else
        say "neither curl nor wget is available"
        return 1
    fi
}

fetch || fail "download failed: $PKG_URL"

# Fail closed, before dpkg ever sees the file. The artifact store has no
# authentication and delivers over plain HTTP, so anyone on the LAN can replace
# this package; the digest below arrived over the trusted console -> terraform
# -> snippet path and is the only thing that makes the download safe to run.
if ! printf '%s  %s\n' "$PKG_SHA" "$DEB" | sha256sum -c - >/dev/null 2>&1; then
    GOT=$(sha256sum < "$DEB" 2>/dev/null | cut -d ' ' -f 1)
    fail "SHA256 mismatch for $PKG_URL (expected $PKG_SHA, got ${GOT:-unreadable}); refusing to install"
fi

dpkg -i "$DEB" >/dev/null 2>&1 || fail "dpkg -i failed for $PKG_URL"
rm -f "$DEB"

# The package ships the unit under /lib/systemd/system; reload so systemd sees
# it even if the package carries no postinst helper of its own.
systemctl daemon-reload >/dev/null 2>&1 || true
systemctl enable --now qubes-air-agent >/dev/null 2>&1 ||
    fail "qubes-air-agent failed to start; see journalctl -u qubes-air-agent"

# Assert, do not assume. "enable --now" exiting 0 is not the same as a running
# agent: the agent refuses to start without --ca/--cert/--key, and an agent that
# exits immediately is precisely the silent failure this path exists to remove.
#
# One check is not enough. The unit is Type=simple, so systemd calls it started
# the moment it forks — BEFORE exec. A binary that cannot exec at all (a package
# built for the wrong architecture is the realistic case: dpkg validates the
# Architecture field, never the ELF header) passes an immediate is-active and
# only then flips to failed. So settle first, and fail on anything that is not
# still active at the end of the window.
settled=0
for _ in 1 2 3 4 5; do
    sleep 1
    state="$(systemctl is-active qubes-air-agent 2>/dev/null || true)"
    case "$state" in
        failed)
            fail "qubes-air-agent entered failed state after install; see journalctl -u qubes-air-agent" ;;
        active)
            settled=$((settled + 1))
            [ "$settled" -ge 2 ] && break ;;
        *)
            # activating / auto-restart: still deciding, keep watching.
            settled=0 ;;
    esac
done
[ "$settled" -ge 2 ] ||
    fail "qubes-air-agent did not stay running after install (last state: ${state:-unknown}); see journalctl -u qubes-air-agent"

say "installed qubes-air-agent ${PKG_VERSION:-unversioned} and confirmed it is running"
exit 0
`

// writeFile appends one write_files entry.
//
// Content is emitted as a YAML literal block, which needs no escaping of quotes
// or backslashes — PEM is multi-line and would otherwise have to be quoted
// correctly every time, and a single mistake produces a file that looks right
// and does not parse.
func writeFile(b *strings.Builder, path, mode, content string) {
	fmt.Fprintf(b, "  - path: %s\n", path)
	fmt.Fprintf(b, "    permissions: '%s'\n", mode)
	b.WriteString("    owner: root:root\n")
	b.WriteString("    content: |\n")
	for _, line := range strings.Split(strings.TrimRight(content, "\n"), "\n") {
		// Blank lines are emitted truly empty rather than as bare indentation.
		// Both parse identically, but trailing whitespace is the kind of thing
		// an editor or a linter silently rewrites, and rewriting it inside a
		// literal block changes a delivered file's bytes.
		if line == "" {
			b.WriteString("\n")
			continue
		}
		fmt.Fprintf(b, "      %s\n", line)
	}
}

// SnippetVolumeID is the terraform user_data_file_id for a snippet file.
//
// Proxmox addresses a snippet as "<datastore>:snippets/<file>". The datastore
// must declare the "snippets" content type, or PVE will not resolve the volume
// even when the file is present on disk.
//
// Takes the FILE NAME rather than the qube name, because under shared-storage
// delivery the name carries a content hash — see ContentAddressedSnippetName.
func SnippetVolumeID(datastore, fileName string) string {
	if datastore == "" {
		datastore = "local"
	}
	return fmt.Sprintf("%s:snippets/%s", datastore, fileName)
}

// SnippetFileName is the on-disk name used by the SFTP delivery path, where
// terraform owns the upload and tracks content through its own checksum.
func SnippetFileName(qubeName string) string {
	return fmt.Sprintf("qubes-air-%s.yaml", qubeName)
}

// snippetHashLen is how much of the content digest goes in the file name.
// 12 hex characters is 48 bits — far past accidental collision for a fleet,
// and short enough that the name stays readable in a log line.
const snippetHashLen = 12

// ContentAddressedSnippetName names a snippet after the qube AND the bytes it
// contains.
//
// This is what keeps the shared-storage delivery path correct, and it replaces
// a guarantee rather than adding one. On the SFTP path, terraform's
// `checksum = filesha256(...)` is what makes the file resource depend on
// CONTENT; without it terraform tracks only the path, and a re-rendered
// identity at the same path is invisible — apply reports success while the node
// keeps the old file. That was observed on real hardware, and its worse form is
// that certificate rotation can never land while every apply looks green (see
// docs/bootstrap-design.md §7).
//
// Shared storage deletes that resource, and a bare volume-ID string has nowhere
// to hang a checksum. So the digest moves into the name: different content
// yields a different file name, hence a different volume id, and
// user_data_file_id is ForceNew on the VM — the compute instance rebuilds and
// cloud-init reads the new document. Same end behavior as today, but derived
// from the content by construction instead of by remembering to pass a
// checksum.
//
// It also makes identities content-addressed rather than overwritten in place,
// so a superseded document is never silently replaced under a running VM.
func ContentAddressedSnippetName(qubeName, userData string) string {
	sum := sha256.Sum256([]byte(userData))
	return fmt.Sprintf("qubes-air-%s-%s.yaml", qubeName, hex.EncodeToString(sum[:])[:snippetHashLen])
}

// snippetNamePattern matches any snippet this console has ever written for a
// qube, in either naming scheme. Used to collect superseded versions.
//
// Anchored on both ends and with the hash restricted to hex so that a qube
// named "web" cannot match files belonging to "web-01": the separator alone
// would not distinguish them, but "web-01-<hash>" fails the hex check for the
// segment that would have to be "01-<hash>".
func snippetNamePattern(qubeName string) *regexp.Regexp {
	return regexp.MustCompile(
		`^qubes-air-` + regexp.QuoteMeta(qubeName) + `(-[0-9a-f]{` + fmt.Sprint(snippetHashLen) + `})?\.yaml$`)
}

// WriteAgentUserData persists rendered user-data where terraform can upload it.
//
// The file is written to disk rather than passed through tfvars because
// terraform's source_file records only the path, size and volume id in state,
// while source_raw would put the content there — and the content is a private
// key. This repository's state design forbids credentials entering state at all
// (see terraform/main.tf), so the choice is load-bearing, not stylistic.
//
// Mode 0600: this file holds an agent's private key for as long as it sits on
// the console's disk.
func WriteAgentUserData(dir, qubeName, userData string) (string, error) {
	if dir == "" {
		return "", fmt.Errorf("no directory configured for agent identity files")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create identity dir: %w", err)
	}
	path := filepath.Join(dir, SnippetFileName(qubeName))

	// Write via a temp file and rename so terraform can never read a partially
	// written identity — half a private key is not a recoverable error, it is a
	// VM that boots and cannot authenticate.
	tmp, err := os.CreateTemp(dir, ".identity-*.yaml")
	if err != nil {
		return "", fmt.Errorf("create temp identity: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op once renamed

	if _, err := tmp.WriteString(userData); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("write identity: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("close identity: %w", err)
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return "", fmt.Errorf("chmod identity: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return "", fmt.Errorf("place identity: %w", err)
	}
	return path, nil
}

// WriteSharedAgentUserData writes a content-addressed identity into the
// directory PVE nodes read snippets from, and returns the file name.
//
// This is the shared-storage delivery path: the console writes where the
// hypervisors can already see, so nothing has to SFTP into a node — which is
// the entire point, since snippet upload is the one thing the PVE API cannot
// do (docs/bootstrap-design.md §4.1).
//
// Writing the CURRENT file before reaping the old ones is deliberate and is
// the only safe order. The reverse would open a window in which a qube's
// identity does not exist on the share at all, and a VM starting in that
// window gets a cicustom volume PVE cannot resolve.
//
// Mode 0644, not 0600: the PVE nodes read this file as a different user
// entirely. Confidentiality here comes from who can mount the share, not from
// the file bits — which is exactly why this path must only ever carry the
// bootstrap token and the public CA, never a private key.
func WriteSharedAgentUserData(dir, qubeName, userData string) (string, error) {
	if dir == "" {
		return "", fmt.Errorf("no directory configured for agent identity files")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create snippet dir: %w", err)
	}

	name := ContentAddressedSnippetName(qubeName, userData)
	if err := writeSnippetAtomic(dir, name, userData); err != nil {
		return "", err
	}

	// Best effort: a leftover old version is inert (nothing references it) and
	// failing the provision over it would trade a working qube for tidiness.
	if err := reapSupersededSnippets(dir, qubeName, name); err != nil {
		log.Printf("cloudinit: could not remove superseded snippets for %q: %v", qubeName, err)
	}
	return name, nil
}

// writeSnippetAtomic places one snippet via a temp file and a rename, so a node
// can never read a half-written identity.
func writeSnippetAtomic(dir, name, userData string) error {
	tmp, err := os.CreateTemp(dir, ".identity-*.yaml")
	if err != nil {
		return fmt.Errorf("create temp identity: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op once renamed

	if _, err := tmp.WriteString(userData); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write identity: %w", err)
	}
	// Flush before the rename. On a network filesystem the rename can be
	// visible to another client while the bytes are not, and a node that reads
	// an empty snippet boots a qube with no identity at all.
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync identity: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close identity: %w", err)
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return fmt.Errorf("chmod identity: %w", err)
	}
	if err := os.Rename(tmpName, filepath.Join(dir, name)); err != nil {
		return fmt.Errorf("place identity: %w", err)
	}
	return nil
}

// FindSharedAgentUserData returns the name of a qube's current identity
// snippet on the share, or "" when it has none.
//
// The share is the source of truth for which document a qube is running,
// because the name encodes the content and nothing else can reconstruct it.
//
// More than one match is an ERROR rather than a pick. Two content-addressed
// files mean two different identities exist for one qube and there is no
// evidence here about which the VM actually booted with — choosing would be a
// coin flip that silently rebuilds a healthy qube with the wrong document.
// WriteSharedAgentUserData reaps superseded versions, so this state means that
// reaping failed, and an operator should see that rather than have it papered
// over.
func FindSharedAgentUserData(dir, qubeName string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	pattern := snippetNamePattern(qubeName)
	var found []string
	for _, e := range entries {
		if !e.IsDir() && pattern.MatchString(e.Name()) {
			found = append(found, e.Name())
		}
	}
	switch len(found) {
	case 0:
		return "", nil
	case 1:
		return found[0], nil
	default:
		return "", fmt.Errorf(
			"qube %q has %d identity snippets on the share (%s); superseded ones were not removed, "+
				"and picking between them could rebuild the qube with an identity it never had",
			qubeName, len(found), strings.Join(found, ", "))
	}
}

// reapSupersededSnippets removes a qube's older identity documents, keeping the
// one named by keep.
//
// Content addressing means every re-render leaves a new file behind, so
// something has to collect them or the share grows without bound. Scoped to one
// qube's own files by construction: a pattern that could match another qube's
// name would delete an identity belonging to a running VM.
func reapSupersededSnippets(dir, qubeName, keep string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	pattern := snippetNamePattern(qubeName)
	var firstErr error
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || name == keep || !pattern.MatchString(name) {
			continue
		}
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !os.IsNotExist(err) && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// RemoveAgentUserData deletes a qube's identity files from the console.
//
// Called when a qube is purged. On the SFTP path the copy on the Proxmox node
// is removed by terraform along with the compute VM; this removes the console's
// own. On the shared path there is only one copy and this is what removes it,
// so every content-addressed version is swept, not just the current name —
// which the caller does not know once the qube's config is gone.
func RemoveAgentUserData(dir, qubeName string) error {
	if dir == "" {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	pattern := snippetNamePattern(qubeName)
	var firstErr error
	for _, e := range entries {
		if e.IsDir() || !pattern.MatchString(e.Name()) {
			continue
		}
		if err := os.Remove(filepath.Join(dir, e.Name())); err != nil && !os.IsNotExist(err) && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// writeAptMirror emits the apt stanza pointing the guest at a local mirror.
//
// Emitted before packages: so the sources are in place when cloud-init installs
// them. Nothing is written when no mirror is configured — an empty apt block
// would replace the image's working sources with nothing.
func writeAptMirror(b *strings.Builder, pkg AgentPackage) {
	primary := strings.TrimSpace(pkg.AptMirror)
	if primary == "" {
		return
	}
	security := strings.TrimSpace(pkg.AptSecurityMirror)
	if security == "" {
		// Better than omitting the security suite, which would leave it pointed
		// at the public redirector and reintroduce most of the delay — but
		// Debian serves security from a separate path, so this is a fallback
		// worth overriding.
		security = primary
	}
	b.WriteString("apt:\n")
	b.WriteString("  primary:\n")
	b.WriteString("    - arches: [default]\n")
	fmt.Fprintf(b, "      uri: %s\n", primary)
	b.WriteString("  security:\n")
	b.WriteString("    - arches: [default]\n")
	fmt.Fprintf(b, "      uri: %s\n", security)
}
