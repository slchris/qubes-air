"""Prevent fixtures with fixed runtime paths from running on a real host."""
import os
from pathlib import Path


def require_container():
    if os.getuid() != 0 or not Path("/.dockerenv").exists() or os.environ.get("QUBESAIR_DESKTOP_TEST_CONTAINER") != "1":
        raise RuntimeError("desktop integration requires its disposable test container")
