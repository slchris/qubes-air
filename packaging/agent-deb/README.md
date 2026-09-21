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

Exec/FileCopy require Python 3 (declared as a package dependency) and explicit service/policy
configuration. Exec accepts JSON argv, FileCopy uses directory descriptors; both inherit the agent
sandbox. The unit requires `QUBESAIR_REVOCATION_URL` for CA-signed revocation status. Deployment
requirements and failure behavior are documented in [security controls](../../docs/security-controls.md).

## Build

From the repository root:

```bash
scripts/build-agent-deb.sh
```

The [Dockerfile](Dockerfile) defines the cross-compilation and package layout. Build success does not
replace installation, upgrade, bootstrap and real-provider acceptance tests.
