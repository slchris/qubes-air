"""Destructive fixtures ONLY inside the dedicated disposable QEMU test guest."""
import importlib.util
import json
import os
from pathlib import Path
import secrets
import subprocess

SERVICE = "/etc/qubes-rpc/qubesair.UnlockData"
CHECKER = "/usr/lib/qubes-air/data-mount.py"
MAPPER = "/dev/mapper/qubesair-data"
DATA = Path("/data")


def command(*argv, input=None):
    return subprocess.run(list(argv), input=input, text=True, check=True, timeout=30,
                          stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                          env={"PATH": "/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL": "C"}).stdout.strip()


def require_guest():
    if os.geteuid() != 0 or Path("/etc/qubes-air-mount-test").read_text().strip() != "disposable-qemu-data-identity":
        raise RuntimeError("dedicated test guest required")
    if command("systemd-detect-virt", "--vm") not in ("qemu", "kvm"):
        raise RuntimeError("QEMU guest required")
    devices = []
    for lun in (1, 2):
        paths = list(Path("/dev/disk/by-path").glob(f"*-scsi-0:0:0:{lun}"))
        if len(paths) != 1:
            raise RuntimeError("dedicated SCSI fixture layout required")
        device = str(paths[0].resolve(strict=True))
        if command("blockdev", "--getsize64", device) != str(256 * 1024 * 1024):
            raise RuntimeError("only the 256 MiB fixture disks may be used")
        devices.append(device)
    if Path(MAPPER).exists() or DATA.exists():
        raise RuntimeError("clean guest required")
    return devices


def check(stage, accepted):
    result = subprocess.run(["/usr/bin/python3", "-I", CHECKER, stage], check=False,
                            stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=10)
    assert result.returncode == (0 if accepted else 69), (stage, result.returncode, result.stderr)


def unlock(key, accepted):
    reply = json.loads(command(SERVICE, input=key))
    assert reply["unlocked"] is accepted, reply


def verify_read_error():
    spec = importlib.util.spec_from_file_location("data_mount", CHECKER)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    # This disposable device has no backing storage. Every read returns EIO.
    name = "qubes-air-fixture-read-error"
    device = Path("/dev/mapper") / name
    if device.exists():
        raise RuntimeError("read-error fixture already exists")
    command("dmsetup", "create", name, "--table", "0 524288 error")
    try:
        old_probe = subprocess.run(["blkid", "-p", "-o", "value", "-s", "TYPE", "-s", "PTTYPE", str(device)],
                                   check=False, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=10)
        assert old_probe.returncode == 2 and old_probe.stdout == b"", "CLI ambiguity not reproduced"
        try:
            module.probe_signature(device)
        except (OSError, RuntimeError):
            pass
        else:
            raise AssertionError("an I/O error was accepted as a signature result")
    finally:
        command("dmsetup", "remove", name)
    print("native signature probe rejects real EIO; CLI returns empty exit 2", flush=True)


def verify_foreign_mount(key):
    command("umount", "/data")
    command("mount", "-t", "tmpfs", "-o", "mode=0755", "tmpfs", "/data")
    foreign = DATA / "foreign-marker"
    foreign.write_text("retain foreign mount")
    # The previous implementation is injected only by the local regression
    # runner, and exercises the same old privileged half on this fixture.
    old = Path("/tmp/old-unlock-data")
    if old.exists():
        result = subprocess.run(["/bin/bash", str(old), "__unlock"], input=key, text=True,
                                stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=15, check=True,
                                env={"PATH": "/usr/sbin:/usr/bin:/sbin:/bin", "QUBESAIR_UNLOCK_INNER": "1"})
        assert json.loads(result.stdout)["unlocked"] is True, "old bug was not reproduced"
    check("mounted", False)
    unlock(key, False)
    assert foreign.read_text() == "retain foreign mount"
    command("umount", "/data")
    command("mount", "-t", "ext4", MAPPER, "/data")


def verify_mount_changes(key):
    child = DATA / "nested"
    child.mkdir()
    command("mount", "-t", "tmpfs", "tmpfs", str(child))
    unlock(key, False)
    command("umount", str(child))
    command("mount", "-o", "remount,ro", "/data")
    unlock(key, False)
    command("mount", "-o", "remount,rw", "/data")
    DATA.chmod(0o777)
    unlock(key, False)
    DATA.chmod(0o755)
    os.chown(DATA, 1000, 1000)
    unlock(key, False)
    os.chown(DATA, 0, 0)
    check("mounted", True)


def verify_wrong_mapping(key, foreign_device):
    command("umount", "/data")
    command("cryptsetup", "close", "qubesair-data")
    command("cryptsetup", "luksFormat", "--type", "luks2", "--batch-mode", "--pbkdf", "pbkdf2",
            "--pbkdf-force-iterations", "1000", "--key-file", "-", foreign_device, input=key)
    command("cryptsetup", "open", "--key-file", "-", foreign_device, "qubesair-data", input=key)
    check("preflight", False)
    unlock(key, False)
    command("cryptsetup", "close", "qubesair-data")
    DATA.rmdir()
    target = Path("/tmp/symlink-target")
    target.mkdir()
    DATA.symlink_to(target, target_is_directory=True)
    unlock(key, False)
    assert list(target.iterdir()) == []
    DATA.unlink()
    unlock(key, True)


def main():
    _, foreign_device = require_guest()
    key = secrets.token_urlsafe(32)
    verify_read_error()
    check("preflight", True)
    assert command("/usr/bin/python3", "-I", CHECKER, "probe-raw") == "unrecognized"
    check("mounted", False)
    unlock(key, True)
    check("mounted", True)
    assert command("/usr/bin/python3", "-I", CHECKER, "probe-mapped") == "ext4"
    probe = DATA / "persistence-probe"
    probe.write_text("encrypted filesystem retained across close and reopen\n")
    unlock(key, True)
    verify_foreign_mount(key)
    verify_mount_changes(key)
    verify_wrong_mapping(key, foreign_device)
    assert probe.read_text() == "encrypted filesystem retained across close and reopen\n"
    check("mounted", True)
    unlock(key, True)
    command("umount", "/data")
    check("mounted", False)
    command("cryptsetup", "close", "qubesair-data")
    print("QUBES_AIR_DATA_MOUNT_TEST_PASS", flush=True)


if __name__ == "__main__":
    main()
