#!/usr/bin/python3
"""Read-only proof of the fixed plain or encrypted data mount. No key input."""
import ctypes
import os
from pathlib import Path
import re
import stat
import subprocess
import sys

BY_PATH = Path("/dev/disk/by-path")
MAPPER = Path("/dev/mapper/qubesair-data")
DATA = Path("/data")
SYSFS = Path("/sys/dev/block")
MOUNTINFO = Path("/proc/self/mountinfo")
UUID = re.compile(r"[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}")


def read_small(path, limit=1024):
    with path.open(encoding="ascii") as source:
        value = source.read(limit + 1)
    if len(value) > limit:
        raise RuntimeError("oversized device metadata")
    return value.strip()


def device_number(number):
    return f"{os.major(number)}:{os.minor(number)}"


def data_device(required=True):
    candidates = {path.resolve(strict=True) for path in BY_PATH.glob("*-scsi-0:0:0:1")}
    if not candidates and not required:
        return None, None
    if len(candidates) != 1:
        raise RuntimeError("data device absent or ambiguous")
    device = candidates.pop()
    info = device.stat()
    if not stat.S_ISBLK(info.st_mode) or info.st_uid != 0:
        raise RuntimeError("data device is not a root-owned block device")
    return device, info.st_rdev


def mapping_device(device, backing, required):
    try:
        info = MAPPER.stat()
    except FileNotFoundError:
        if required or MAPPER.is_symlink():
            raise RuntimeError("data mapping missing") from None
        return None
    if not stat.S_ISBLK(info.st_mode) or info.st_uid != 0:
        raise RuntimeError("data mapper is not a root-owned block device")
    sysfs = SYSFS / device_number(info.st_rdev)
    if read_small(sysfs / "dm/name") != "qubesair-data" or read_small(sysfs / "dm/suspended") != "0":
        raise RuntimeError("data mapping unavailable")
    result = subprocess.run(["/usr/sbin/cryptsetup", "luksUUID", str(device)], check=True,
                            stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL,
                            env={"PATH": "/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL": "C"},
                            timeout=5, text=True)
    uuid = result.stdout.strip()
    if not UUID.fullmatch(uuid):
        raise RuntimeError("data LUKS identity unavailable")
    expected = {f"CRYPT-LUKS{version}-{uuid.replace('-', '')}-qubesair-data" for version in (1, 2)}
    if read_small(sysfs / "dm/uuid") not in expected:
        raise RuntimeError("data LUKS identity differs from mapping")
    slaves = list((sysfs / "slaves").iterdir())
    if len(slaves) != 1 or read_small(slaves[0] / "dev") != device_number(backing):
        raise RuntimeError("data mapping uses another backing device")
    return info.st_rdev


def mount_records():
    records = []
    for line in read_small(MOUNTINFO, 1024 * 1024).splitlines():
        fields = line.split()
        if len(fields) < 10:
            raise RuntimeError("invalid mount record")
        separator = fields.index("-", 6)
        if len(fields) != separator + 4:
            raise RuntimeError("invalid mount record")
        records.append((fields[2], fields[3], fields[4], fields[5].split(","),
                        fields[separator + 1], fields[separator + 3].split(",")))
    return records


def check_mount(mapping, required):
    try:
        directory = DATA.lstat()
    except FileNotFoundError:
        directory = None
    if directory is not None and (not stat.S_ISDIR(directory.st_mode) or directory.st_uid != 0
                                  or stat.S_IMODE(directory.st_mode) & 0o022):
        raise RuntimeError("data directory is unsafe")
    records = mount_records()
    mounts = [record for record in records if record[2] == "/data"]
    # A child mount could hide the future HOME. Another mount of the mapper
    # makes a bind mount at /data indistinguishable from the primary mount.
    for record in records:
        if record[2].startswith("/data/"):
            raise RuntimeError("data contains a nested mount")
        if mapping is not None and record[0] == device_number(mapping) and record[2] != "/data":
            raise RuntimeError("data mapping mounted elsewhere")
    if not mounts and not required:
        return False
    if len(mounts) != 1 or directory is None or mapping is None:
        raise RuntimeError("data mount absent or ambiguous")
    device, root, _, options, filesystem, super_options = mounts[0]
    if device != device_number(mapping) or directory.st_dev != mapping or root != "/" \
            or filesystem != "ext4" or "rw" not in options or "rw" not in super_options:
        raise RuntimeError("data is not the verified writable filesystem root")
    return True


def check_plain(stage):
    device, backing = data_device(stage != "plain-preflight")
    required = stage in ("plain-mounted", "plain-ready")
    mounted = check_mount(backing, required)
    if stage == "plain-unmounted":
        if mounted:
            raise RuntimeError("data device is already mounted")
        print(device)
    elif stage == "plain-preflight":
        print("mounted" if mounted else (str(device) if device is not None else "absent"))


def check(stage):
    if stage in ("plain-preflight", "plain-unmounted", "plain-mounted", "plain-ready"):
        check_plain(stage)
        return
    device, backing = data_device()
    mapping = mapping_device(device, backing, stage not in ("preflight", "probe-raw"))
    check_mount(mapping, stage == "mounted")
    if stage in ("probe-raw", "probe-mapped"):
        print(probe_signature(device if stage == "probe-raw" else MAPPER))


def probe_library():
    # The CLI folds open/read errors into its "nothing found" exit status.
    # Bind the read-only low-level API so errors cannot authorize formatting.
    library = ctypes.CDLL("libblkid.so.1")
    pointer = ctypes.c_void_p
    signatures = {
        "blkid_new_probe_from_filename": ([ctypes.c_char_p], pointer),
        "blkid_free_probe": ([pointer], None),
        "blkid_probe_enable_superblocks": ([pointer, ctypes.c_int], ctypes.c_int),
        "blkid_probe_enable_partitions": ([pointer, ctypes.c_int], ctypes.c_int),
        "blkid_probe_set_superblocks_flags": ([pointer, ctypes.c_int], ctypes.c_int),
        "blkid_do_safeprobe": ([pointer], ctypes.c_int),
        "blkid_probe_has_value": ([pointer, ctypes.c_char_p], ctypes.c_int),
        "blkid_probe_lookup_value": ([pointer, ctypes.c_char_p, ctypes.POINTER(ctypes.c_char_p),
                                      ctypes.POINTER(ctypes.c_size_t)], ctypes.c_int),
    }
    for name, (arguments, result) in signatures.items():
        function = getattr(library, name)
        function.argtypes = arguments
        function.restype = result
    return library


def probe_signature(device):
    library = probe_library()
    probe = library.blkid_new_probe_from_filename(os.fsencode(device))
    if not probe:
        raise RuntimeError("cannot open data signature probe")
    try:
        for function, flag in ((library.blkid_probe_enable_superblocks, 1),
                               (library.blkid_probe_enable_partitions, 1),
                               (library.blkid_probe_set_superblocks_flags, 1 << 5)):  # TYPE
            if function(probe, flag) != 0:
                raise RuntimeError("cannot configure data signature probe")
        status = library.blkid_do_safeprobe(probe)
        if status == 1:
            return "unrecognized"
        if status != 0:
            raise RuntimeError("data signature probe failed or is ambiguous")
        if library.blkid_probe_has_value(probe, b"PTTYPE") != 0:
            return "other"
        value = ctypes.c_char_p()
        size = ctypes.c_size_t()
        if library.blkid_probe_lookup_value(probe, b"TYPE", ctypes.byref(value), ctypes.byref(size)) != 0:
            return "other"
        return "ext4" if size.value == 5 and value.value == b"ext4" else "other"
    finally:
        library.blkid_free_probe(probe)


def main(args):
    if len(args) != 1 or args[0] not in ("preflight", "mapped", "mounted", "probe-raw", "probe-mapped",
                                       "plain-preflight", "plain-unmounted", "plain-mounted", "plain-ready"):
        return 64
    if os.geteuid() != 0:
        return 77
    try:
        check(args[0])
    except (OSError, ValueError, RuntimeError, subprocess.SubprocessError):
        print("qubes-air: data identity could not be verified", file=sys.stderr)
        return 69
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
