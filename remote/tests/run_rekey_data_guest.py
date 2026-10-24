"""Real keyslot changes ONLY on the two disposable QEMU fixture disks."""
import hashlib
import json
import os
from pathlib import Path
import secrets
import subprocess

from data_mount_support import command, require_guest, unlock

UNIT = "qubes-air-agent.service"
ROOT = Path("/run/qubes-air-rekey-fixture")
MAPPER = "/dev/mapper/qubesair-data"


def setup_unit():
    # Requests exist only in the test guest's unswapped tmpfs. Production passes
    # them through the agent pipe; no test keys are embedded in cloud-init.
    assert command("findmnt", "-n", "-o", "FSTYPE", "-T", "/run") == "tmpfs"
    assert len(Path("/proc/swaps").read_text().splitlines()) == 1
    ROOT.mkdir(mode=0o700)
    Path("/etc/qubes-air").mkdir(mode=0o755, exist_ok=True)
    drop = Path("/etc/systemd/system/qubes-air-agent.service.d")
    drop.mkdir()
    (drop / "99-fixture.conf").write_text(
        "[Service]\nType=oneshot\nRemainAfterExit=yes\nRestart=no\nEnvironmentFile=\nExecStart=\n"
        "ExecStart=/etc/qubes-rpc/qubesair.RekeyData\n"
        "StandardInput=file:/run/qubes-air-rekey-fixture/request.json\n"
        "StandardOutput=file:/run/qubes-air-rekey-fixture/reply.json\n"
        "StandardError=file:/run/qubes-air-rekey-fixture/stderr\n")
    command("systemctl", "daemon-reload")
    command("systemd-analyze", "verify", "/etc/systemd/system/" + UNIT)
    for name, value in (("User", "root"), ("NoNewPrivileges", "yes"),
                        ("PrivateTmp", "yes"), ("ProtectSystem", "full"), ("ProtectHome", "yes")):
        assert command("systemctl", "show", UNIT, "--property=" + name, "--value") == value


def rekey(old, new, reason=None, accepted=False):
    request = ROOT / "request.json"
    request.write_text(json.dumps({"old": old, "new": new}))
    request.chmod(0o600)
    (ROOT / "reply.json").write_text("")
    (ROOT / "stderr").write_text("")
    result = subprocess.run(["systemctl", "restart", UNIT], check=False, timeout=180,
                            stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    assert result.returncode == 0, "fixture unit did not finish successfully"
    # Keep each completed oneshot loaded so reset-failed also resets the real
    # start-limit counter; resetting a never-started/unloaded unit is an error.
    command("systemctl", "reset-failed", UNIT)
    reply = json.loads((ROOT / "reply.json").read_text())
    request.unlink()
    assert reply["rekeyed"] is accepted, reply
    if reason is not None:
        assert reply["reason"] == reason, reply
    if accepted:
        assert reply["old_key_removed"] is True, reply
    return reply


def opens(device, key):
    result = subprocess.run(["cryptsetup", "open", "--test-passphrase", "--key-file", "-", device],
                            input=key, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                            timeout=90, check=False)
    assert result.returncode in (0, 2), "unexpected cryptsetup key-test failure"
    return result.returncode == 0


def header_digest(device):
    result = hashlib.sha256()
    with open(device, "rb") as stream:
        # The fixture uses the default 16 MiB LUKS2 header/keyslot area.
        # Mounted ext4 can update its own metadata independently of migration.
        payload = stream.read(16 * 1024 * 1024)
        assert len(payload) == 16 * 1024 * 1024
        result.update(payload)
    return result.hexdigest()


def add_key(device, current, additional):
    readers = []
    try:
        for key in (current, additional):
            reader, writer = os.pipe()
            readers.append(reader)
            try:
                os.write(writer, key.encode("ascii"))
            finally:
                os.close(writer)
        subprocess.run(["cryptsetup", "luksAddKey", "--pbkdf", "pbkdf2",
                        "--pbkdf-force-iterations", "1000", "--key-file=/dev/fd/" + str(readers[0]),
                        device, "/dev/fd/" + str(readers[1])], pass_fds=readers,
                       stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=True, timeout=90)
    finally:
        for reader in readers:
            os.close(reader)


def verify_invalid_requests(device, old, new):
    before = header_digest(device)
    rekey(old, old, "same_key")
    rekey(old + "\n" + new, "invalid", "bad_key")
    rekey(old, new + "\n", "bad_key")
    rekey(secrets.token_urlsafe(32), new, "no_key")
    assert header_digest(device) == before, "rejected key request changed the LUKS header"
    assert opens(device, old) and not opens(device, new)
    print("invalid key fields and wrong keys preserve the real LUKS disk", flush=True)


def verify_foreign_mount(device, old, new):
    command("umount", "/data")
    command("mount", "-t", "tmpfs", "-o", "mode=0755", "tmpfs", "/data")
    marker = Path("/data/foreign-marker")
    marker.write_text("foreign mount retained")
    before = header_digest(device)
    rekey(old, new, "identity_invalid")
    assert header_digest(device) == before and marker.read_text() == "foreign mount retained"
    command("umount", "/data")
    command("mount", "-t", "ext4", MAPPER, "/data")
    child = Path("/data/nested")
    child.mkdir()
    command("mount", "-t", "tmpfs", "tmpfs", str(child))
    rekey(old, new, "identity_invalid")
    command("umount", str(child))
    command("mount", "-o", "remount,ro", "/data")
    rekey(old, new, "identity_invalid")
    command("mount", "-o", "remount,rw", "/data")
    assert opens(device, old) and not opens(device, new)
    print("foreign, nested and read-only mounts refuse key migration", flush=True)


def verify_wrong_device(device, foreign, old, new):
    command("umount", "/data")
    command("cryptsetup", "close", "qubesair-data")
    before = header_digest(device)
    command("cryptsetup", "luksFormat", "--type", "luks2", "--batch-mode", "--pbkdf", "pbkdf2",
            "--pbkdf-force-iterations", "1000", "--key-file", "-", foreign, input=new)
    command("cryptsetup", "open", "--key-file", "-", foreign, "qubesair-data", input=new)
    rekey(old, new, "identity_invalid")
    command("cryptsetup", "close", "qubesair-data")
    alias = Path("/dev/disk/by-path/rekey-foreign-scsi-0:0:0:1")
    alias.symlink_to(foreign)
    try:
        rekey(old, new, "identity_invalid")
    finally:
        alias.unlink()
    source, = Path("/dev/disk/by-path").glob("*-scsi-0:0:0:1")
    hidden = source.with_name(source.name + ".fixture-hidden")
    source.rename(hidden)
    try:
        rekey(old, new, "identity_invalid")
    finally:
        hidden.rename(source)
    assert header_digest(device) == before
    assert opens(device, old) and not opens(device, new)
    unlock(old, True)
    print("wrong mapper, ambiguous scsi1 and missing disk preserve the original container", flush=True)


def verify_migration(device, old, new):
    uuid = command("cryptsetup", "luksUUID", device)
    rekey(old, new, accepted=True)
    assert opens(device, new) and not opens(device, old)
    assert command("cryptsetup", "luksUUID", device) == uuid
    command("umount", "/data")
    command("cryptsetup", "close", "qubesair-data")
    unlock(new, True)
    assert Path("/data/migration-probe").read_text() == "retained across key migration\n"
    before = header_digest(device)
    rekey(old, new, "already", accepted=True)
    assert header_digest(device) == before
    # Model a restart after add-key succeeded but remove-key did not. Both
    # keys are independently verified before the real service retries cleanup.
    add_key(device, new, old)
    assert opens(device, old) and opens(device, new)
    rekey(old, new, accepted=True)
    assert opens(device, new) and not opens(device, old)
    metadata = json.loads(command("cryptsetup", "luksDump", "--dump-json-metadata", device))
    assert len(metadata["keyslots"]) == 1
    assert command("cryptsetup", "luksUUID", device) == uuid
    print("real migration, old-key refusal, file/UUID retention, repeat and pending-slot cleanup passed", flush=True)


def main():
    device, foreign = require_guest()
    setup_unit()
    old, new = secrets.token_urlsafe(32), secrets.token_urlsafe(32)
    before = header_digest(device)
    rekey(old, new, "not_luks")
    assert header_digest(device) == before
    unlock(old, True)
    with Path("/data/migration-probe").open("w") as probe:
        probe.write("retained across key migration\n")
        probe.flush()
        os.fsync(probe.fileno())
    verify_invalid_requests(device, old, new)
    verify_foreign_mount(device, old, new)
    verify_wrong_device(device, foreign, old, new)
    verify_migration(device, old, new)
    journal = command("journalctl", "-b", "--no-pager", "-o", "cat")
    assert old not in journal and new not in journal, "test key leaked to journal"
    command("umount", "/data")
    command("cryptsetup", "close", "qubesair-data")
    print("packaged agent sandbox and real systemd-run migration path verified", flush=True)
    print("QUBES_AIR_DATA_MOUNT_TEST_PASS", flush=True)


if __name__ == "__main__":
    main()
