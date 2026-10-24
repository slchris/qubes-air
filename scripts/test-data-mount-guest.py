#!/usr/bin/env python3
"""Boot fresh QEMU fixtures; never mount test disks on the host or share its paths."""
import argparse
import base64
import hashlib
import json
from pathlib import Path
import re
import shutil
import subprocess
import tempfile

REPO = Path(__file__).resolve().parents[1]


def checked_path(value):
    path = Path(value).resolve(strict=True)
    # QEMU's drive option uses commas as separators. Keep every interpolated
    # path in a narrow grammar, independent of shell quoting (no shell is used).
    if not re.fullmatch(r"/[A-Za-z0-9/._+-]+", str(path)) or not path.is_file():
        raise argparse.ArgumentTypeError("expected a regular file with a simple absolute path")
    return path


def command(*argv):
    subprocess.run(list(argv), check=True, timeout=60)


def verify_image(path, expected):
    if not re.fullmatch(r"[0-9a-f]{128}", expected):
        raise ValueError("a verified SHA512 digest is required")
    digest = hashlib.sha512()
    with path.open("rb") as source:
        for chunk in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(chunk)
    if digest.hexdigest() != expected:
        raise ValueError("image SHA512 mismatch")


def write_seed(root, desktop_binary, plain_data):
    seed = root / "seed"
    seed.mkdir()
    files = []
    sources = [
            ("console/backend/internal/guestassets/data-mount.py", "/usr/lib/qubes-air/data-mount.py", "0644"),
            ("remote/qubes-rpc/qubesair.UnlockData", "/etc/qubes-rpc/qubesair.UnlockData", "0755"),
            ("remote/tests/run_data_mount_guest.py", "/root/run_data_mount_guest.py", "0600")]
    if desktop_binary:
        sources[-1] = ("remote/tests/run_desktop_home_guest.py", "/root/run_data_mount_guest.py", "0600")
        sources.extend([
            ("remote/tests/run_data_mount_guest.py", "/root/data_mount_support.py", "0600"),
            ("remote/lib/data-home.py", "/usr/lib/qubes-air/data-home.py", "0644"),
            ("remote/lib/xpra-session.py", "/usr/lib/qubes-air/xpra-session.py", "0644"),
            ("remote/lib/desktop-worker.py", "/usr/lib/qubes-air/desktop-worker.py", "0644"),
            ("packaging/agent-deb/data.mount", "/etc/systemd/system/data.mount", "0644"),
            ("packaging/agent-deb/qubes-air-desktop.service", "/etc/systemd/system/qubes-air-desktop.service", "0644"),
            ("packaging/agent-deb/qubes-air-desktop-home.service", "/etc/systemd/system/qubes-air-desktop-home.service", "0644"),
            ("packaging/xpra-test/packages.sha256", "/root/desktop-packages.sha256", "0600"),
            (desktop_binary, "/usr/bin/qubes-air-desktop", "0755")])
    if plain_data:
        sources[-1] = ("remote/tests/run_plain_data_mount_guest.py", "/root/run_data_mount_guest.py", "0600")
        sources.extend([
            ("remote/tests/run_data_mount_guest.py", "/root/data_mount_support.py", "0600"),
            ("console/backend/internal/guestassets/data-mount.py", "/usr/local/lib/qubes-air/data-mount.py", "0644"),
            ("console/backend/internal/guestassets/mount-plain-data.sh", "/usr/local/sbin/qubes-air-mount-data", "0700"),
            ("console/backend/internal/guestassets/qubes-air-data.service", "/etc/systemd/system/qubes-air-data.service", "0644"),
            ("console/backend/internal/guestassets/20-data-mount.conf",
             "/etc/systemd/system/qubes-air-agent.service.d/20-data-mount.conf", "0644"),
            ("packaging/agent-deb/qubes-air-agent.service", "/etc/systemd/system/qubes-air-agent.service", "0644"),
            ("packaging/agent-deb/data.mount", "/etc/systemd/system/data.mount", "0644")])
    for source, target, mode in sources:
        files.append({"path": target, "permissions": mode, "encoding": "b64",
                      "content": base64.b64encode((REPO / source).read_bytes()).decode("ascii")})
    files.append({"path": "/etc/qubes-air-mount-test", "permissions": "0600",
                  "content": "disposable-qemu-data-identity\n"})
    if plain_data:
        files.extend([
            {"path": "/etc/qubes-air/fixture.env", "permissions": "0600", "content": "# Disposable fixture only\n"},
            {"path": "/etc/systemd/system/qubes-air-agent.service.d/99-fixture.conf", "permissions": "0644",
             "content": "[Service]\nType=oneshot\nRemainAfterExit=yes\nRestart=no\nEnvironmentFile=\n"
                        "ExecStart=\nExecStart=/usr/bin/touch /run/qubes-air-fixture-agent-started\n"}])
    config = {"hostname": "qubes-air-mount-test", "disable_root": True, "ssh_pwauth": False,
              "package_update": True, "packages": ["cryptsetup", "python3", "libblkid1", "dmsetup", "diffutils"], "write_files": files,
              "runcmd": [["/usr/bin/python3", "-u", "/root/run_data_mount_guest.py"], ["/sbin/poweroff"]]}
    (seed / "user-data").write_text("#cloud-config\n" + json.dumps(config, indent=2) + "\n")
    (seed / "meta-data").write_text("instance-id: qubes-air-mount-fixture\n")
    iso = root / "seed.iso"
    if shutil.which("genisoimage"):
        command("genisoimage", "-quiet", "-output", str(iso), "-volid", "cidata", "-joliet", "-rock", str(seed))
    elif shutil.which("hdiutil"):
        command("hdiutil", "makehybrid", "-iso", "-joliet", "-default-volume-name", "cidata",
                "-o", str(iso), str(seed))
    else:
        raise RuntimeError("genisoimage or hdiutil is required")


def qemu_arguments(root, code):
    args = ["qemu-system-x86_64", "-machine", "q35", "-accel", "tcg,thread=multi", "-cpu", "max",
            "-m", "2048", "-smp", "2",
            "-drive", "if=pflash,format=raw,readonly=on,file=" + str(code),
            "-drive", "if=pflash,format=raw,file=" + str(root / "uefi-vars.fd"),
            "-display", "none", "-serial", "file:" + str(root / "serial.log"),
            "-monitor", "none", "-no-reboot",
            "-drive", "if=virtio,format=qcow2,file=" + str(root / "root.qcow2"),
            "-cdrom", str(root / "seed.iso"), "-device", "virtio-scsi-pci,id=testscsi"]
    for lun in (1, 2):
        args.extend(["-drive", f"if=none,id=test{lun},format=raw,file={root}/data{lun}.raw",
                     "-device", f"scsi-hd,drive=test{lun},bus=testscsi.0,channel=0,scsi-id=0,lun={lun}"])
    # NAT provides package downloads only. No host forwarding, shared folders,
    # host block devices or host cryptsetup/mount operations are configured.
    args.extend(["-nic", "user,model=virtio-net-pci"])
    return args


def run(args):
    verify_image(args.image, args.sha512)
    root = Path(tempfile.mkdtemp(prefix="qubes-air-mount-guest-")).resolve()
    if not re.fullmatch(r"/[A-Za-z0-9/._+-]+", str(root)):
        raise RuntimeError("TMPDIR must have a simple absolute path")
    print("Fixture artifacts: " + str(root), flush=True)
    write_seed(root, args.desktop_binary, args.plain_data)
    shutil.copyfile(args.firmware_vars, root / "uefi-vars.fd")
    command("qemu-img", "create", "-f", "qcow2", "-F", "qcow2", "-b", str(args.image), str(root / "root.qcow2"))
    command("qemu-img", "resize", str(root / "root.qcow2"), "8G")
    for lun in (1, 2):
        command("qemu-img", "create", "-f", "raw", str(root / f"data{lun}.raw"), "256M")
    with (root / "qemu.log").open("w") as log:
        result = subprocess.run(qemu_arguments(root, args.firmware_code), stdout=log, stderr=log,
                                check=False, timeout=900)
    serial = (root / "serial.log").read_text(errors="replace")
    passed = result.returncode == 0 and "QUBES_AIR_DATA_MOUNT_TEST_PASS" in serial
    (root / "result.json").write_text(json.dumps({"passed": passed, "image_sha512": args.sha512,
                                                 "qemu_exit": result.returncode}, indent=2) + "\n")
    if not passed:
        raise RuntimeError("guest acceptance failed; inspect serial.log and qemu.log")
    print("QUBES_AIR_DATA_MOUNT_TEST_PASS", flush=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--image", type=checked_path, required=True)
    parser.add_argument("--sha512", required=True, help="image digest from the trusted Debian manifest")
    parser.add_argument("--firmware-code", type=checked_path, required=True, help="x86_64 UEFI code image")
    parser.add_argument("--firmware-vars", type=checked_path, required=True, help="matching UEFI variables template")
    mode = parser.add_mutually_exclusive_group()
    mode.add_argument("--desktop-binary", type=checked_path,
                        help="current linux/amd64 broker binary; also test encrypted HOME and systemd/Xpra")
    mode.add_argument("--plain-data", action="store_true",
                      help="test the cloud-init plain data script and systemd startup dependency")
    run(parser.parse_args())


if __name__ == "__main__":
    main()
