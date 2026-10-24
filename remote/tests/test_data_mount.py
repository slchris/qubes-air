"""Read-only data identity checks must fail before a mount can be trusted."""
import ctypes
import importlib.util
import io
import os
from pathlib import Path
import stat
import subprocess
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import Mock, patch

SOURCE = Path(__file__).resolve().parents[2] / "console/backend/internal/guestassets/data-mount.py"
if not SOURCE.is_file():
    SOURCE = Path("/usr/lib/qubes-air/data-mount.py")
SPEC = importlib.util.spec_from_file_location("data_mount", SOURCE)
MOUNT = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MOUNT)


class MountIdentityTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        root = Path(self.temp.name).resolve()
        self.by_path = root / "by-path"
        self.by_path.mkdir()
        self.raw = root / "sdb"
        self.raw.touch()
        (self.by_path / "pci-fixture-scsi-0:0:0:1").symlink_to(self.raw)
        self.mapper = root / "mapper"
        self.mapper.touch()
        self.data = root / "data"
        self.data.mkdir(mode=0o755)
        self.sysfs = root / "sysfs"
        self.dm = self.sysfs / "120:0" / "dm"
        self.dm.mkdir(parents=True)
        self.uuid = "11111111-2222-3333-4444-555555555555"
        (self.dm / "name").write_text("qubesair-data\n")
        (self.dm / "uuid").write_text("CRYPT-LUKS2-" + self.uuid.replace("-", "") + "-qubesair-data\n")
        (self.dm / "suspended").write_text("0\n")
        slaves = self.dm.parent / "slaves/sdb"
        slaves.mkdir(parents=True)
        (slaves / "dev").write_text("8:16\n")
        self.mountinfo = root / "mountinfo"
        self.valid_mount = "41 20 120:0 / /data rw,relatime - ext4 /dev/mapper/qubesair-data rw\n"
        self.mountinfo.write_text(self.valid_mount)
        self.block = True
        self.owner = 0
        self.device_owner = 0
        self.data_dev = os.makedev(120, 0)
        patches = [patch.multiple(MOUNT, BY_PATH=self.by_path, MAPPER=self.mapper, DATA=self.data,
                                  SYSFS=self.sysfs, MOUNTINFO=self.mountinfo),
                   patch.object(Path, "stat", lambda path, *args, **kwargs: self.fake_stat(path, *args, **kwargs)),
                   patch.object(Path, "lstat", lambda path: self.fake_stat(path, follow_symlinks=False)),
                   patch.object(MOUNT.subprocess, "run", return_value=SimpleNamespace(stdout=self.uuid + "\n"))]
        for item in patches:
            value = item.start()
            self.addCleanup(item.stop)
        self.probe = value

    def fake_stat(self, path, *args, **kwargs):
        info = os.stat(path, *args, **kwargs)
        if path in (self.raw, self.mapper) and self.block and not stat.S_ISLNK(info.st_mode):
            device = os.makedev(8, 16) if path == self.raw else os.makedev(120, 0)
            return SimpleNamespace(st_mode=stat.S_IFBLK | 0o600, st_rdev=device, st_uid=self.device_owner)
        if path == self.data:
            return SimpleNamespace(st_mode=info.st_mode, st_dev=self.data_dev, st_uid=self.owner)
        return info

    def reject(self, stage="mounted"):
        with self.assertRaises((RuntimeError, ValueError, OSError, subprocess.SubprocessError)):
            MOUNT.check(stage)

    def test_same_luks_device_mounted_as_writable_ext4(self):
        for stage in ("preflight", "mapped", "mounted"):
            MOUNT.check(stage)
        args = self.probe.call_args
        self.assertEqual(args.args[0], ["/usr/sbin/cryptsetup", "luksUUID", str(self.raw)])
        self.assertEqual(args.kwargs["stdin"], subprocess.DEVNULL)
        self.assertEqual(args.kwargs["timeout"], 5)
        self.assertNotIn("shell", args.kwargs)

    def test_preflight_allows_unopened_disk_and_absent_mount_directory(self):
        self.mountinfo.write_text("")
        self.mapper.unlink()
        self.data.rmdir()
        MOUNT.check("preflight")
        self.probe.assert_not_called()
        self.reject("mapped")
        self.reject("mounted")

    def test_open_mapping_without_mount_requires_final_check(self):
        self.mountinfo.write_text("")
        MOUNT.check("preflight")
        MOUNT.check("mapped")
        self.reject()

    def test_invalid_mounts_are_never_trusted(self):
        cases = [self.valid_mount.replace("120:0", "8:16"),
                 self.valid_mount.replace("ext4", "tmpfs"),
                 self.valid_mount.replace(" / /data ", " /subdir /data "),
                 self.valid_mount.replace(" rw,relatime ", " ro,relatime "),
                 self.valid_mount.replace("qubesair-data rw", "qubesair-data ro"),
                 self.valid_mount * 2,
                 self.valid_mount + "42 41 0:99 / /data/home rw - tmpfs tmpfs rw\n",
                 self.valid_mount + "42 20 120:0 / /elsewhere rw - ext4 /dev/dm-0 rw\n",
                 self.valid_mount.replace(" - ext4 ", " missing-separator ext4 "),
                 "not a mount record\n"]
        for record in cases:
            with self.subTest(record=record):
                self.mountinfo.write_text(record)
                self.reject("preflight")
        self.mountinfo.write_text(self.valid_mount)
        self.data_dev = os.makedev(8, 16)
        self.reject()

    def test_wrong_mapping_name_uuid_and_suspend_state(self):
        for name, bad in [("name", "unrelated"), ("uuid", "CRYPT-PLAIN-qubesair-data"),
                          ("uuid", "CRYPT-LUKS2-" + "9" * 32 + "-qubesair-data"),
                          ("suspended", "1"), ("suspended", ""), ("name", "x" * 2048)]:
            path = self.dm / name
            original = path.read_text()
            with self.subTest(name=name, bad=bad):
                path.write_text(bad)
                self.reject("preflight")
            path.write_text(original)

    def test_header_and_backing_device_must_match(self):
        self.probe.return_value.stdout = "malformed UUID"
        self.reject()
        self.probe.return_value.stdout = self.uuid + "\n"
        self.probe.side_effect = subprocess.TimeoutExpired("cryptsetup", 5)
        self.reject()
        self.probe.side_effect = None
        slave = self.dm.parent / "slaves/sdb/dev"
        slave.write_text("8:32\n")
        self.reject()
        slave.write_text("8:16\n")
        extra = self.dm.parent / "slaves/sdc"
        extra.mkdir()
        (extra / "dev").write_text("8:16\n")
        self.reject()

    def test_luks1_identity_and_dangling_mapper(self):
        path = self.dm / "uuid"
        path.write_text("CRYPT-LUKS1-" + self.uuid.replace("-", "") + "-qubesair-data\n")
        MOUNT.check("mounted")
        self.mountinfo.write_text("")
        self.mapper.unlink()
        self.mapper.symlink_to(self.mapper.parent / "missing-block-node")
        self.reject("preflight")

    def test_missing_mapper_and_non_block_device_are_rejected(self):
        self.block = False
        self.reject("preflight")
        self.block = True
        self.mapper.unlink()
        self.reject("preflight")

    def test_missing_and_ambiguous_scsi_devices_are_rejected(self):
        link = next(self.by_path.iterdir())
        link.unlink()
        self.reject("preflight")
        link.symlink_to(self.raw)
        # Two paths to one device are harmless; two candidates are not.
        extra = self.by_path / "pci-other-scsi-0:0:0:1"
        extra.symlink_to(self.raw)
        MOUNT.check("mounted")
        extra.unlink()
        extra.symlink_to(self.mapper)
        self.reject("preflight")

    def test_mount_directory_is_owned_by_root_and_not_replaceable(self):
        self.owner = 1000
        self.reject("preflight")
        self.owner = 0
        self.data.chmod(0o777)
        self.reject("preflight")
        self.data.rmdir()
        self.data.symlink_to(self.by_path, target_is_directory=True)
        self.reject("preflight")

    def test_fixed_interface_refuses_arguments_and_unprivileged_entry(self):
        with patch.object(MOUNT, "check") as check:
            for args in ([], ["/tmp/arbitrary"], ["mounted", "/data"], ["--help"]):
                self.assertEqual(MOUNT.main(args), 64)
            with patch.object(MOUNT.os, "geteuid", return_value=1000):
                self.assertEqual(MOUNT.main(["mounted"]), 77)
            check.assert_not_called()
            with patch.object(MOUNT.os, "geteuid", return_value=0):
                check.side_effect = RuntimeError("internal diagnostic")
                self.assertEqual(MOUNT.main(["mounted"]), 69)

    def test_probe_modes_use_only_verified_fixed_devices(self):
        with patch.object(MOUNT, "probe_signature", return_value="ext4") as probe:
            with patch("sys.stdout", new_callable=io.StringIO) as output:
                MOUNT.check("probe-mapped")
                self.assertEqual(output.getvalue(), "ext4\n")
            probe.assert_called_once_with(self.mapper)
            probe.reset_mock()
            (self.dm / "uuid").write_text("foreign-mapping")
            self.reject("probe-mapped")
            self.reject("probe-raw")
            probe.assert_not_called()
            self.mapper.unlink()
            self.mountinfo.write_text("")
            with patch("sys.stdout", new_callable=io.StringIO) as output:
                MOUNT.check("probe-raw")
                self.assertEqual(output.getvalue(), "ext4\n")
            probe.assert_called_once_with(self.raw)

    def plain_fixture(self):
        self.mapper.unlink()
        self.data_dev = os.makedev(8, 16)
        self.valid_mount = self.valid_mount.replace("120:0", "8:16")
        self.mountinfo.write_text(self.valid_mount)

    def test_plain_mount_and_ready_require_raw_device_identity(self):
        self.plain_fixture()
        with patch("sys.stdout", new_callable=io.StringIO) as output:
            MOUNT.check("plain-preflight")
            self.assertEqual(output.getvalue(), "mounted\n")
        for stage in ("plain-mounted", "plain-ready"):
            MOUNT.check(stage)
        self.reject("plain-unmounted")
        self.probe.assert_not_called()
        self.mountinfo.write_text("")
        for stage in ("plain-mounted", "plain-ready"):
            self.reject(stage)
        for stage in ("plain-preflight", "plain-unmounted"):
            with patch("sys.stdout", new_callable=io.StringIO) as output:
                MOUNT.check(stage)
                self.assertEqual(output.getvalue(), str(self.raw) + "\n")

    def test_plain_refuses_foreign_subtree_readonly_and_overlapping_mounts(self):
        self.plain_fixture()
        cases = [self.valid_mount.replace("8:16", "8:32"),
                 self.valid_mount.replace("ext4", "tmpfs"),
                 self.valid_mount.replace(" / /data ", " /subdir /data "),
                 self.valid_mount.replace(" rw,relatime ", " ro,relatime "),
                 self.valid_mount.replace("qubesair-data rw", "qubesair-data ro"),
                 self.valid_mount * 2,
                 self.valid_mount + "42 41 0:99 / /data/home rw - tmpfs tmpfs rw\n",
                 self.valid_mount + "42 20 8:16 / /elsewhere rw - ext4 /dev/sdb rw\n",
                 "42 20 8:16 / /elsewhere rw - ext4 /dev/sdb rw\n"]
        for record in cases:
            for stage in ("plain-preflight", "plain-unmounted", "plain-mounted", "plain-ready"):
                with self.subTest(record=record, stage=stage):
                    self.mountinfo.write_text(record)
                    self.reject(stage)
        self.mountinfo.write_text(self.valid_mount)
        self.data_dev = os.makedev(8, 32)
        self.reject("plain-preflight")

    def test_plain_missing_disk_can_wait_but_never_authorizes_startup(self):
        self.plain_fixture()
        next(self.by_path.iterdir()).unlink()
        self.mountinfo.write_text("")
        with patch("sys.stdout", new_callable=io.StringIO) as output:
            MOUNT.check("plain-preflight")
            self.assertEqual(output.getvalue(), "absent\n")
        self.reject("plain-ready")
        self.reject("plain-unmounted")
        self.reject("plain-mounted")
        self.mountinfo.write_text(self.valid_mount)
        self.reject("plain-preflight")
        self.reject("plain-ready")
        self.mountinfo.write_text("")
        self.owner = 1000
        self.reject("plain-ready")

    def test_plain_preflight_requires_root_block_device_and_safe_directory(self):
        self.plain_fixture()
        for field, value in (("block", False), ("device_owner", 1000), ("owner", 1000)):
            original = getattr(self, field)
            setattr(self, field, value)
            self.reject("plain-preflight")
            setattr(self, field, original)
        self.data.chmod(0o777)
        self.reject("plain-preflight")
        self.data.rmdir()
        self.data.symlink_to(self.by_path, target_is_directory=True)
        self.reject("plain-preflight")

    def test_plain_preflight_refuses_ambiguous_and_dangling_scsi_paths(self):
        self.plain_fixture()
        extra = self.by_path / "pci-other-scsi-0:0:0:1"
        extra.symlink_to(self.raw)
        MOUNT.check("plain-ready")
        extra.unlink()
        extra.symlink_to(self.data)
        self.reject("plain-preflight")
        extra.unlink()
        extra.symlink_to(self.by_path / "missing")
        self.reject("plain-preflight")

    def test_plain_entry_is_fixed_and_requires_root(self):
        with patch.object(MOUNT, "check") as check:
            with patch.object(MOUNT.os, "geteuid", return_value=1000):
                for stage in ("plain-preflight", "plain-unmounted", "plain-mounted", "plain-ready"):
                    self.assertEqual(MOUNT.main([stage]), 77)
            for args in (["plain-mounted", "/arbitrary"], ["plain-unknown"], ["plain-ready", "--device"]):
                self.assertEqual(MOUNT.main(args), 64)
            check.assert_not_called()


class SignatureProbeTests(unittest.TestCase):
    def setUp(self):
        self.library = Mock()
        self.handle = 0x100000001
        self.library.blkid_new_probe_from_filename.return_value = self.handle
        self.library.blkid_probe_enable_superblocks.return_value = 0
        self.library.blkid_probe_enable_partitions.return_value = 0
        self.library.blkid_probe_set_superblocks_flags.return_value = 0
        self.library.blkid_do_safeprobe.return_value = 0
        self.library.blkid_probe_has_value.return_value = 0
        self.library.blkid_probe_lookup_value.side_effect = self.lookup
        self.value = b"ext4"
        self.size = 5
        self.device = Path("/dev/mapper/qubesair-data")
        loader = patch.object(MOUNT, "probe_library", return_value=self.library)
        self.loader = loader.start()
        self.addCleanup(loader.stop)

    def lookup(self, handle, name, value, size):
        self.assertEqual(handle, self.handle)
        self.assertEqual(name, b"TYPE")
        ctypes.cast(value, ctypes.POINTER(ctypes.c_char_p))[0] = self.value
        ctypes.cast(size, ctypes.POINTER(ctypes.c_size_t))[0] = self.size
        return 0

    def test_ext4_is_recognized_and_probe_is_freed(self):
        self.assertEqual(MOUNT.probe_signature(self.device), "ext4")
        self.library.blkid_new_probe_from_filename.assert_called_once_with(os.fsencode(self.device))
        self.library.blkid_probe_enable_superblocks.assert_called_once_with(self.handle, 1)
        self.library.blkid_probe_enable_partitions.assert_called_once_with(self.handle, 1)
        self.library.blkid_free_probe.assert_called_once_with(self.handle)

    def test_only_explicit_nothing_detected_result_is_unrecognized(self):
        self.library.blkid_do_safeprobe.return_value = 1
        self.assertEqual(MOUNT.probe_signature(self.device), "unrecognized")
        self.library.blkid_probe_lookup_value.assert_not_called()
        self.library.blkid_free_probe.assert_called_once_with(self.handle)

    def test_io_errors_ambivalence_and_unknown_status_fail_closed(self):
        for result in (-1, -2, -5, 2, 4):
            with self.subTest(result=result):
                self.library.blkid_do_safeprobe.return_value = result
                with self.assertRaises(RuntimeError):
                    MOUNT.probe_signature(self.device)
                self.library.blkid_free_probe.assert_called_with(self.handle)
        self.library.blkid_probe_lookup_value.assert_not_called()

    def test_open_or_library_failure_never_runs_probe(self):
        self.library.blkid_new_probe_from_filename.return_value = None
        with self.assertRaises(RuntimeError):
            MOUNT.probe_signature(self.device)
        self.library.blkid_do_safeprobe.assert_not_called()
        self.library.blkid_free_probe.assert_not_called()
        self.loader.side_effect = OSError("library unavailable")
        with self.assertRaises(OSError):
            MOUNT.probe_signature(self.device)

    def test_configuration_failure_frees_probe_without_reading(self):
        for name in ("blkid_probe_enable_superblocks", "blkid_probe_enable_partitions",
                     "blkid_probe_set_superblocks_flags"):
            with self.subTest(name=name):
                getattr(self.library, name).return_value = -1
                with self.assertRaises(RuntimeError):
                    MOUNT.probe_signature(self.device)
                self.library.blkid_free_probe.assert_called_with(self.handle)
                getattr(self.library, name).return_value = 0
        self.library.blkid_do_safeprobe.assert_not_called()

    def test_partition_signature_is_never_accepted_as_ext4(self):
        self.library.blkid_probe_has_value.return_value = 1
        self.assertEqual(MOUNT.probe_signature(self.device), "other")
        self.library.blkid_probe_lookup_value.assert_not_called()

    def test_unsupported_empty_or_inconsistent_type_is_not_blank(self):
        for value, size in ((b"xfs", 4), (b"", 1), (None, 0), (b"ext4", 0), (b"ext4 extra", 11)):
            with self.subTest(value=value, size=size):
                self.value, self.size = value, size
                self.assertEqual(MOUNT.probe_signature(self.device), "other")
        self.library.blkid_probe_lookup_value.side_effect = None
        self.library.blkid_probe_lookup_value.return_value = -1
        self.assertEqual(MOUNT.probe_signature(self.device), "other")


if __name__ == "__main__":
    unittest.main()
