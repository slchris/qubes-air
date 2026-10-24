"""Container-only fixtures: authenticated Xvfb, then the GIO and broker suites."""
import os
from pathlib import Path
import secrets
import subprocess
import time

from desktop_test_support import require_container


def main():
    require_container()
    runtime = Path("/run/qubes-air-desktop")
    broker = Path("/run/qubes-air-desktop-broker")
    for directory in (runtime, broker):
        directory.mkdir(mode=0o700)
        os.chown(directory, 1000, 1000)
    auth = runtime / "Xauthority"
    subprocess.run(["xauth", "-f", str(auth), "add", ":100", ".", secrets.token_hex(16)],
                   check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    os.chown(auth, 1000, 1000)
    os.chmod(auth, 0o600)
    env = dict(os.environ, DISPLAY=":100", XAUTHORITY=str(auth), HOME="/data/home")
    server = subprocess.Popen(["Xvfb", ":100", "-screen", "0", "800x600x24", "-nolisten", "tcp", "-auth", str(auth)],
                              env=env, user=1000, group=1000, extra_groups=[],
                              stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    try:
        ready = False
        for _ in range(100):
            result = subprocess.run(["xdpyinfo"], env=env, user=1000, group=1000, extra_groups=[],
                                    stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=1, check=False)
            if result.returncode == 0:
                ready = True
                break
            time.sleep(0.02)
        if not ready:
            raise RuntimeError("Xvfb fixture did not become ready")
        subprocess.run(["/usr/bin/python3", "-m", "unittest", "discover", "-s", str(Path(__file__).parent), "-v"],
                       check=True, env=env)
    finally:
        server.terminate()
        server.wait(timeout=5)


if __name__ == "__main__":
    main()
