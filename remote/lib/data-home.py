#!/usr/bin/python3
"""Fixed root-only HOME preparation; no migration, recursion or path arguments."""
import os
from pathlib import Path
import pwd
import stat
import subprocess
import sys

DATA_ROOT = Path("/data")
DESKTOP_HOME = DATA_ROOT / "home"
DESKTOP_UID = 1000
DESKTOP_GID = 1000


def check_xpra_units():
    # Upstream Xpra's socket activates a separate root proxy on TCP 14500.
    # Deployment must mask both units before installing Xpra. This fixed check
    # refuses activation; it never changes another service's configuration.
    for unit in ("xpra-server.socket", "xpra-server.service"):
        result = subprocess.run(["/usr/bin/systemctl", "show", unit,
                                 "--property=LoadState", "--property=ActiveState"],
                                check=True, timeout=10, text=True, stdin=subprocess.DEVNULL,
                                stdout=subprocess.PIPE, stderr=subprocess.DEVNULL,
                                env={"PATH": "/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL": "C"})
        if sorted(result.stdout.splitlines()) != ["ActiveState=inactive", "LoadState=masked"]:
            raise RuntimeError("upstream Xpra proxy units must be masked and inactive")


def prepare_home(account):
    if (account.pw_uid, account.pw_gid, account.pw_dir) != (DESKTOP_UID, DESKTOP_GID, str(DESKTOP_HOME)):
        raise RuntimeError("desktop account must use the fixed encrypted home")
    subprocess.run(["/usr/bin/python3", "-I", "/usr/lib/qubes-air/data-mount.py", "mounted"],
                   check=True, timeout=10, stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL,
                   stderr=subprocess.DEVNULL, env={"PATH": "/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL": "C"})
    check_xpra_units()
    try:
        DESKTOP_HOME.mkdir(mode=0o700)
    except FileExistsError:
        # No read permission on the UID 1000 / 0700 HOME is needed for lstat.
        # The root unit intentionally has no DAC override capability.
        info = DESKTOP_HOME.lstat()
    else:
        fd = os.open(DESKTOP_HOME, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
        try:
            os.fchown(fd, DESKTOP_UID, DESKTOP_GID)
            info = os.fstat(fd)
        finally:
            os.close(fd)
    # /data is verified root-owned and not replaceable by the desktop user.
    # Never follow or repair an existing entry, including a partially prepared
    # directory after a previous failure. That requires operator inspection.
    if not stat.S_ISDIR(info.st_mode) \
            or (info.st_uid, info.st_gid, stat.S_IMODE(info.st_mode)) != (DESKTOP_UID, DESKTOP_GID, 0o700) \
            or info.st_dev != DATA_ROOT.stat().st_dev:
        raise RuntimeError("desktop home identity or permissions are unsafe")


def main(args):
    if args:
        return 64
    if os.geteuid() != 0 or os.getuid() != 0:
        return 77
    try:
        os.umask(0o077)
        prepare_home(pwd.getpwuid(DESKTOP_UID))
    except (OSError, RuntimeError, KeyError, subprocess.SubprocessError):
        print("qubes-air-desktop: encrypted HOME or desktop prerequisites unavailable", file=sys.stderr)
        return 69
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
