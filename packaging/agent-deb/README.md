# qubes-air-agent package

The RemoteVM agent listens over mTLS and runs explicitly enabled qrexec services.
See the [agent design](../../docs/remote-agent-design.md) and
[bootstrap flow](../../docs/bootstrap-design.md) for the current protocol.

## Installed files

| Path | Purpose |
|---|---|
| `/usr/bin/qubes-air-agent` | Agent binary |
| `/lib/systemd/system/qubes-air-agent.service` | Unit enabled on installation; a stopped unit is not started by postinst |
| `/etc/qubes-rpc/` | Service implementations |
| `/usr/bin/qubes-air-desktop` | Fixed Unix-socket desktop client and non-root broker |
| `/usr/bin/qubes-desktop-run` | Thin app-ID wrapper for menu entries |
| `/usr/lib/qubes-air/desktop-worker.py` | Unprivileged GIO Desktop Entry worker |
| `/usr/lib/qubes-air/xpra-session.py` | Fixed non-root Xpra/broker session supervisor |
| `/lib/systemd/system/qubes-air-desktop.service` | Opt-in desktop unit, never enabled or started by package hooks |

On upgrade, postinst uses `try-restart` for an already running unit. Cloud-init's installer starts the
unit after preparing its configuration. The package default service allowlist contains only Ping.

## Desktop boundary and deployment prerequisites

GetAppmenus, StartApp and qubes-desktop-run use only the fixed local broker socket. The broker runs
as the existing UID 1000 account and uses GIO for menu visibility, user overrides, nested desktop IDs
and argv expansion. The root agent never parses Desktop Entry Exec values. Its ProtectHome and
PrivateTmp settings remain enabled. The package declares Python GI, GLib introspection, D-Bus and
X11 utility dependencies; it does not create a user, install Xpra, open a port or enable desktop.

Activation requires Xpra **6.5.5**, PyGObject **3.44.2 or newer**, an existing UID 1000 account and its
accessible HOME. The tested desktop dependency profile is Debian 13 with the six upstream packages
pinned in [packages.sha256](../xpra-test/packages.sha256); the base agent package still supports its
Bookworm installation test and does not install Xpra. The service creates private runtime directories
and a private D-Bus session. Its supervisor exclusively creates the fixed mode-0600 Xauthority,
starts UID 1000 Xpra on `:100`, and waits for a listening private socket and authenticated X11 probe
before starting the broker. An existing authentication file, unsafe runtime directory, wrong dependency
version or failed child refuses the session. Worker/child stdin,
stdout and stderr are disconnected from RPC data. Successful launch acknowledges GIO acceptance,
not window visibility. Result 75 means dispatch may have happened: never retry automatically.

The socket is private to root and UID 1000, with kernel peer verification in both directions. One
request has a 10 second deadline, 512 byte input and 1 MiB output limit; four requests can run at
once. Menus have at most 256 applications. Desktop applications share the broker unit's UID and
cgroup and can outlive an RPC. The supervisor stops the broker before Xpra; either child exiting
fails the whole session. Stopping/restarting the unit terminates those applications and removes
its runtime directories, including Xauthority; a cold start creates a new cookie.
The broker refuses an existing socket instead of unlinking it. Package upgrades keep an existing
desktop session running; apply the updated broker during an explicit maintenance restart.

The server refuses mmap negotiation, control on its sole socket, clipboard, file transfer, shell,
new commands and client shutdown. Its fixed environment ignores external Xpra configuration. Xpra
6.5.5 needs `XPRA_ENFORCE_FEATURES=0` to import encoder helpers while `--mmap=no` disables the actual
subsystem. The control handler stays loaded to return a refusal with `control=no` on the socket.
These choices are covered by real-client capability and negative negotiation tests.

The encrypted data disk HOME mapping and production Xpra session deployment are still pending. The
package's Linux/Xvfb tests do not establish RemoteVM GUI availability. See the
[desktop broker record](../../docs/reviews/2026-10-24-desktop-broker.md) for package evidence. The agent's
fixed peer-verified Xpra socket and real Xpra integration checks are documented in the
[Xpra record](../../docs/reviews/2026-10-24-xpra-session.md). The shared supervisor and dependency
profile are covered by the [session record](../../docs/reviews/2026-10-24-desktop-session-policy.md);
these container checks do not prove a production systemd/Qubes GUI deployment.

## Identity on first boot

Cloud-init provides `/etc/qubes-air/ca.pem`, `bootstrap-token` and `agent.env`, together with pinned
artifact metadata. It does **not** deliver an agent private key or an issued agent certificate.

The unit supplies the CA, certificate and key **paths**. A cleanly absent certificate/key pair is
expected on first boot: the agent generates its key locally, submits a CSR through bootstrap, then
persists its issued identity. Corrupt, unreadable or partially present identity files are errors.
A missing CA or missing bootstrap material is not repaired by copying a private key from Console.

## Diagnostics

```bash
systemctl status qubes-air-agent
journalctl -u qubes-air-agent -n 50
```

- Missing `agent.env`: inspect cloud-init configuration delivery.
- Missing or invalid CA, partial identity pair: inspect file presence and permissions without logging keys.
- No certificate on a fresh boot: inspect token availability, Console reachability and CSR issuance.
- `--ca, --cert and --key are all required`: mandatory path arguments were omitted.
- `exec format error`: check package architecture with `dpkg -I`; this build targets amd64.

After correcting the underlying failure, a unit that hit its start limit can be restarted:

```bash
systemctl reset-failed qubes-air-agent
systemctl start qubes-air-agent
```

A unit that hit its start limit does **not** restart itself again within that boot once the cause is
fixed: the unit sets `StartLimitIntervalSec=300` with `StartLimitBurst=5` and restarts with
`RestartSec=5` (`qubes-air-agent.service`:10-11, `:32-33`), so once those five starts are used up it
stays failed until the failed state is cleared. The unit is enabled
(`[Install] WantedBy=multi-user.target`, `:44-45`; `postinst` runs `systemctl enable`), so a reboot
does start it again — with the cause still unfixed, it just fails again. Console shows such an agent
as `agent_health=unreachable` with `agent_recovery=manual` — which means "nothing has answered for
longer than this unit's restart budget", not "the unit hit its start limit", because Console has no
channel into the guest beyond the agent's own listener. The reading is only rendered for a qube that
has a compute instance, so a suspended or released qube never shows it. Detection limits and the full
recovery procedure, with the commands to confirm recovery, are in
[the RemoteVM runbook](../../docs/runbook-remotevm.md) §11.

Exec, FileCopy and RekeyData require Python 3 (declared as a package dependency) and explicit
service/policy configuration. Exec accepts JSON argv, FileCopy uses directory descriptors,
UnlockData opens the encrypted data disk and RekeyData migrates a legacy disk to its own key; all
inherit the agent sandbox. The unit requires `QUBESAIR_REVOCATION_URL` for CA-signed revocation
status. Deployment requirements and failure behavior are documented in
[security controls](../../docs/security-controls.md).

## Build

From the repository root:

```bash
scripts/build-agent-deb.sh
```

The [Dockerfile](Dockerfile) defines the cross-compilation and package layout. Build success does not
replace installation, upgrade, bootstrap and real-provider acceptance tests.

## Install and upgrade smoke test

```bash
make agent-deb-test            # or: scripts/test-agent-deb.sh
```

Builds an old and the current package, then inside `debian:bookworm-slim` installs the old one
(resolving Python/GIO/D-Bus/X11 dependencies), checks the installed layout and version output, exercises the
startup refusals (missing mTLS paths, no identity and no bootstrap token, missing revocation URL,
empty allowlist), upgrades to the current build and verifies an operator conffile edit survives,
then runs real kernel peer, GIO and authenticated Xvfb tests through the installed desktop helpers,
and removes the package. Desktop remains disabled throughout. The container has no published ports;
Xvfb listens locally with a temporary authentication file. It needs Docker and network access for apt and is deliberately not part of
`make pre-commit`; CI runs it in the `agent-package` job. First bootstrap against a real console
remains a QA-01 item.
