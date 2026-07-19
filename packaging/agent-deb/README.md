# qubes-air-agent

The Qubes Air RemoteVM agent. It listens for the local relay over mutual TLS and
executes the qrexec services in `/etc/qubes-rpc`.

## What this package installs

| Path | Purpose |
| --- | --- |
| `/usr/bin/qubes-air-agent` | the agent binary |
| `/lib/systemd/system/qubes-air-agent.service` | the unit (enabled on install, **not** started) |
| `/etc/qubes-rpc/` | qrexec service implementations |

## What this package does *not* install

`/etc/qubes-air/` — the CA, the certificate, the private key, and `agent.env` —
is rendered by the console and delivered by cloud-init. The agent refuses to
start without all three PEM files, so a freshly installed package intentionally
leaves a stopped unit behind until that material arrives.

## Diagnosing a unit that will not start

```
systemctl status qubes-air-agent
journalctl -u qubes-air-agent -n 50
```

Common causes, in the order they actually occur:

- **`Failed to load environment files`** — `/etc/qubes-air/agent.env` is missing.
  The console never rendered an identity for this host, or cloud-init did not run.
- **`load key pair: no such file or directory`** — the env file arrived but the
  certificates did not. Same cause, partial delivery.
- **`--ca, --cert and --key are all required`** — the unit was started by hand
  without the packaged `ExecStart`.
- **`exec format error`** — the wrong architecture was installed. This package is
  `amd64`; verify with `dpkg -I` before blaming anything else.

If the start limit was reached before the certificates arrived, systemd will
refuse further starts until the failure is cleared:

```
systemctl reset-failed qubes-air-agent
systemctl start qubes-air-agent
```

## Building

From the repository root:

```
scripts/build-agent-deb.sh
```

See `packaging/agent-deb/Dockerfile` for why the build cross-compiles rather
than emulating the target architecture.
