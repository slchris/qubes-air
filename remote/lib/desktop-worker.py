#!/usr/bin/python3
"""One bounded GIO operation, always as the existing desktop user (uid 1000).

The Go broker supplies a fixed environment and enforces output/time/concurrency
limits. GIO owns Desktop Entry parsing, precedence, IDs and argv expansion.
"""
import json
import os
import re
import stat
import subprocess
import sys
import unicodedata

MAX_APPS = 256
MAX_OUTPUT = 1 << 20
MESSAGES = {64: "invalid desktop request", 69: "desktop session unavailable",
            70: "desktop operation failed", 75: "desktop operation incomplete; do not retry automatically",
            77: "desktop peer is not authorized"}


def failure(code):
    return {"ok": False, "code": code, "error": MESSAGES[code]}


def valid_id(app):
    return bool(re.fullmatch(r"[A-Za-z0-9._+-]{1,113}", app)) and app != "." and ".." not in app


def visible(app):
    return (app.should_show() and not app.get_is_hidden() and not app.get_nodisplay()
            and "Screensaver" not in (app.get_categories() or "").split(";"))


def clean_text(value):
    return (bool(value) and len(value.encode("utf-8")) <= 2048
            and all(unicodedata.category(char) != "Cc" for char in value))


def menu_fields(app, app_id):
    name = app.get_name()
    if not clean_text(name):
        return []
    fields = [("Name", name), ("Type", "Application"), ("Exec", "qubes-desktop-run " + app_id)]
    comment = app.get_description()
    if clean_text(comment):
        fields.append(("Comment", comment))
    icon = app.get_string("Icon") or ""
    if re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._-]{0,127}", icon):
        fields.append(("Icon", icon))
    return [f"{app_id}:{key}={value}\n" for key, value in fields]


def menus(gio):
    lines, seen = [], set()
    for app in sorted(gio.AppInfo.get_all(), key=lambda app: app.get_id() or ""):
        app_id = app.get_id() or ""
        if not valid_id(app_id) or app_id in seen or not visible(app):
            continue
        fields = menu_fields(app, app_id)
        if not fields:
            continue
        seen.add(app_id)
        if len(seen) > MAX_APPS:
            return failure(70)
        lines.extend(fields)
    return {"ok": True, "menu": "".join(lines)}


def display_available():
    if os.environ.get("DISPLAY") != ":100":
        return False
    path = "/run/qubes-air-desktop/Xauthority"
    if os.environ.get("XAUTHORITY") != path:
        return False
    try:
        info = os.lstat(path)
        if not stat.S_ISREG(info.st_mode) or info.st_uid != 1000 or stat.S_IMODE(info.st_mode) != 0o600:
            return False
        result = subprocess.run(["/usr/bin/xdpyinfo", "-display", ":100"], timeout=2,
                                stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                                check=False)
        return result.returncode == 0
    except (OSError, subprocess.TimeoutExpired):
        return False


def launch(gio, glib, app_id):
    canonical = app_id if app_id.endswith(".desktop") else app_id + ".desktop"
    try:
        app = gio.DesktopAppInfo.new(canonical)
    except TypeError:
        # Bookworm's GI binding raises TypeError when the constructor returns
        # NULL for a missing or hidden ID. Nothing has been dispatched yet.
        return failure(69)
    if app is None or not visible(app) or not display_available():
        return failure(69)
    # Replacing all three descriptors prevents a long-lived app from retaining
    # the worker's JSON pipe or a caller's payload. No shell parses Exec here.
    with open(os.devnull, "rb+", buffering=0) as null:
        accepted = app.launch_uris_as_manager_with_fds(
            [], gio.AppLaunchContext(), glib.SpawnFlags.SEARCH_PATH,
            None, None, None, None, null.fileno(), null.fileno(), null.fileno())
    if not accepted:
        return failure(70)
    return {"ok": True, "app": app_id, "display": ":100"}


def execute(args):
    valid = args == ["menus"] or (len(args) == 2 and args[0] == "launch" and valid_id(args[1]))
    if not valid:
        return failure(64)
    if os.getuid() != 1000 or os.geteuid() != 1000:
        return failure(77)
    try:
        import gi
        gi.require_version("Gio", "2.0")
        from gi.repository import Gio, GLib
        if args[0] == "menus":
            return menus(Gio)
        return launch(Gio, GLib, args[1])
    except Exception:
        # Never expose filesystem paths, desktop values or D-Bus diagnostics.
        # A launch error may occur after activation; callers must not replay it.
        return failure(75 if args[0] == "launch" else 70)


def main():
    reply = execute(sys.argv[1:])
    encoded = json.dumps(reply, ensure_ascii=False, separators=(",", ":")) + "\n"
    if len(encoded.encode("utf-8")) > MAX_OUTPUT:
        reply = failure(70)
        encoded = json.dumps(reply) + "\n"
    sys.stdout.write(encoded)
    return reply.get("code", 0)


if __name__ == "__main__":
    sys.exit(main())
