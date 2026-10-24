"""Exercise plain data identity ONLY on the dedicated disposable QEMU disks."""
from contextlib import contextmanager
import hashlib
import os
from pathlib import Path
import subprocess

from data_mount_support import command, require_guest

SCRIPT = "/usr/local/sbin/qubes-air-mount-data"
CHECKER = "/usr/local/lib/qubes-air/data-mount.py"
DATA = Path("/data")
AGENT_MARKER = Path("/run/qubes-air-fixture-agent-started")


def run_script(accepted):
    result = subprocess.run(["/bin/sh", SCRIPT], check=False, stdout=subprocess.PIPE,
                            stderr=subprocess.PIPE, timeout=90,
                            env={"PATH": "/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL": "C"})
    assert (result.returncode == 0) == accepted, result.stderr.decode(errors="replace")
    if not accepted:
        print("expected refusal: " + result.stderr.decode(errors="replace").strip(), flush=True)


def digest(device):
    value = hashlib.sha256()
    with open(device, "rb") as source:
        for chunk in iter(lambda: source.read(1024 * 1024), b""):
            value.update(chunk)
    return value.hexdigest()


@contextmanager
def substitute_scsi(device):
    link, = Path("/dev/disk/by-path").glob("*-scsi-0:0:0:1")
    target = os.readlink(link)
    link.unlink()
    try:
        if device is not None:
            link.symlink_to(device)
        yield
    finally:
        if device is not None:
            link.unlink()
        link.symlink_to(target)


def check_directory_before_format(device):
    original = digest(device)
    DATA.mkdir(mode=0o755)
    command("mount", "-t", "tmpfs", "-o", "mode=0755", "tmpfs", "/data")
    foreign = DATA / "foreign-marker"
    foreign.write_text("retain foreign mount")
    run_script(False)
    assert foreign.read_text() == "retain foreign mount"
    command("umount", "/data")
    DATA.chmod(0o777)
    run_script(False)
    DATA.chmod(0o755)
    os.chown(DATA, 1000, 1000)
    run_script(False)
    os.chown(DATA, 0, 0)
    DATA.rmdir()
    target = Path("/tmp/plain-data-symlink-target")
    target.mkdir()
    DATA.symlink_to(target, target_is_directory=True)
    run_script(False)
    assert list(target.iterdir()) == []
    DATA.unlink()
    assert digest(device) == original, "unsafe mounts or directories changed the blank disk"
    print("foreign mount, permissions and symlink refused before formatting", flush=True)


def check_device_before_format(device, other):
    extra = Path("/dev/disk/by-path/fixture-extra-scsi-0:0:0:1")
    extra.symlink_to(other)
    run_script(False)
    extra.unlink()
    os.chown(device, 1000, 1000)
    try:
        run_script(False)
    finally:
        os.chown(device, 0, 0)
    regular = Path("/tmp/plain-regular-device")
    regular.write_bytes(b"retain regular file")
    with substitute_scsi(regular):
        run_script(False)
    assert regular.read_bytes() == b"retain regular file"
    with substitute_scsi("/dev/fixture-missing-device"):
        run_script(False)
    with substitute_scsi(None):
        run_script(False)
        ready = subprocess.run(["/usr/bin/python3", "-I", CHECKER, "plain-ready"],
                               check=False, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=10)
        assert ready.returncode == 69
    print("ambiguous, non-root, non-block, dangling and absent devices refused", flush=True)


def check_nonzero_and_error(device):
    with open(device, "r+b") as disk:
        disk.seek(-1, os.SEEK_END)
        disk.write(b"x")
    run_script(False)
    with open(device, "r+b") as disk:
        disk.seek(-1, os.SEEK_END)
        assert disk.read(1) == b"x"
        disk.seek(-1, os.SEEK_END)
        disk.write(b"\0")
    name = "qubes-air-plain-fixture-eio"
    error_device = Path("/dev/mapper") / name
    if error_device.exists():
        raise RuntimeError("error fixture already exists")
    command("dmsetup", "create", name, "--table", "0 524288 error")
    try:
        with substitute_scsi(error_device):
            run_script(False)
    finally:
        command("dmsetup", "remove", name)
    print("nonzero tail preserved and real EIO refused", flush=True)


def check_foreign_ext4(device, other):
    command("umount", "/data")
    command("mkfs.ext4", "-q", other)
    command("mount", "-t", "ext4", other, "/data")
    foreign = DATA / "foreign-marker"
    foreign.write_text("retain other filesystem")
    run_script(False)
    assert foreign.read_text() == "retain other filesystem"
    command("umount", "/data")
    command("mount", "-t", "ext4", device, "/data")


def check_mount_layout(device):
    nested = DATA / "nested"
    nested.mkdir()
    command("mount", "-t", "tmpfs", "tmpfs", str(nested))
    run_script(False)
    command("umount", str(nested))
    command("mount", "-t", "tmpfs", "-o", "mode=0755", "tmpfs", "/data")
    run_script(False)
    command("umount", "/data")
    command("mount", "-o", "remount,ro", "/data")
    run_script(False)
    command("mount", "-o", "remount,rw", "/data")
    DATA.chmod(0o777)
    run_script(False)
    DATA.chmod(0o755)
    os.chown(DATA, 1000, 1000)
    run_script(False)
    os.chown(DATA, 0, 0)
    elsewhere = Path("/mnt/plain-data-fixture")
    elsewhere.mkdir()
    command("umount", "/data")
    command("mount", "-t", "ext4", device, str(elsewhere))
    run_script(False)
    for source in (elsewhere, elsewhere / "nested"):
        command("mount", "--bind", str(source), "/data")
        run_script(False)
        command("umount", "/data")
    command("umount", str(elsewhere))
    run_script(True)
    print("foreign, nested, stacked, readonly and bind mounts refused", flush=True)


def start_agent(accepted):
    result = subprocess.run(["systemctl", "start", "qubes-air-agent.service"], check=False,
                            stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=45)
    assert (result.returncode == 0) == accepted, result.stderr.decode(errors="replace")
    assert AGENT_MARKER.exists() == accepted


def stop_agent():
    command("systemctl", "stop", "qubes-air-agent.service")
    AGENT_MARKER.unlink()


def check_systemd_gate():
    command("systemctl", "daemon-reload")
    start_agent(True)
    stop_agent()
    # The data oneshot remains active, so this failure proves ExecStartPre
    # rechecks identity rather than trusting an earlier successful activation.
    command("mount", "-o", "remount,ro", "/data")
    start_agent(False)
    command("systemctl", "stop", "qubes-air-data.service")
    command("systemctl", "reset-failed")
    # With both units inactive, the same failure proves Requires prevents
    # agent startup when the mount unit itself fails.
    start_agent(False)
    command("mount", "-o", "remount,rw", "/data")
    command("systemctl", "reset-failed")
    start_agent(True)
    stop_agent()
    command("systemctl", "stop", "qubes-air-data.service")
    print("systemd dependency and repeat-start identity gate passed", flush=True)


def main():
    device, other = require_guest()
    check_directory_before_format(device)
    check_device_before_format(device, other)
    check_nonzero_and_error(device)
    run_script(True)
    command("/usr/bin/python3", "-I", CHECKER, "plain-mounted")
    uuid = command("blkid", "-s", "UUID", "-o", "value", device)
    probe = DATA / "persistence-probe"
    probe.write_text("plain filesystem retained across mount validation\n")
    run_script(True)
    check_foreign_ext4(device, other)
    check_mount_layout(device)
    check_systemd_gate()
    command("umount", "/data")
    run_script(True)
    assert command("blkid", "-s", "UUID", "-o", "value", device) == uuid
    assert probe.read_text() == "plain filesystem retained across mount validation\n"
    command("/usr/bin/python3", "-I", CHECKER, "plain-ready")
    command("umount", "/data")
    print("QUBES_AIR_DATA_MOUNT_TEST_PASS", flush=True)


if __name__ == "__main__":
    main()
