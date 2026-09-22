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

On upgrade, postinst uses `try-restart` for an already running unit. Cloud-init's installer starts the
unit after preparing its configuration. The package default service allowlist contains only Ping.

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
(resolving the `python3` dependency), checks the installed layout and version output, exercises the
startup refusals (missing mTLS paths, no identity and no bootstrap token, missing revocation URL,
empty allowlist), upgrades to the current build and verifies an operator conffile edit survives,
then removes the package. It needs Docker and network access for apt and is deliberately not part of
`make pre-commit`; CI runs it in the `agent-package` job. First bootstrap against a real console
remains a QA-01 item.
