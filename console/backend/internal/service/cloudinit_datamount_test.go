package service

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type dataMountFixture struct {
	probeStatus       string
	signature         string
	sizeStatus        string
	size              string
	zeroStatus        string
	mkfsStatus        string
	diskData          []byte
	realCompare       bool
	directory         bool
	preflightStatus   string
	beforeMountStatus string
	mountedStatus     string
	alreadyMounted    bool
	mountStatus       string
	changedDevice     bool
	diskAbsent        bool
}

func TestPlainDataIdentityFailurePrecedesProbeAndFormat(t *testing.T) {
	for name, fixture := range map[string]dataMountFixture{
		"existing filesystem": {probeStatus: "0", signature: "ext4", preflightStatus: "69", alreadyMounted: true},
		"blank disk":          {probeStatus: "2", size: "1048576", zeroStatus: "0", preflightStatus: "69", alreadyMounted: true},
	} {
		t.Run(name, func(t *testing.T) {
			calls, output, err := runDataMountFixture(t, fixture)
			require.Error(t, err, output)
			require.NotContains(t, calls, "blkid ")
			require.NotContains(t, calls, "mkfs.ext4 ")
			require.NotContains(t, calls, "mount ")
		})
	}
}

func TestPlainDataMountRequiresFinalIdentity(t *testing.T) {
	calls, output, err := runDataMountFixture(t, dataMountFixture{
		probeStatus: "0", signature: "ext4", mountedStatus: "69",
	})
	require.Error(t, err, output)
	require.Contains(t, calls, "mount ")
}

func TestPlainDataMountRechecksBeforeMutating(t *testing.T) {
	for name, fixture := range map[string]dataMountFixture{
		"changed mount before format":  {probeStatus: "2", size: "1048576", beforeMountStatus: "69"},
		"changed mount before mount":   {probeStatus: "0", signature: "ext4", beforeMountStatus: "69"},
		"changed device before format": {probeStatus: "2", size: "1048576", changedDevice: true},
		"changed device before mount":  {probeStatus: "0", signature: "ext4", changedDevice: true},
	} {
		t.Run(name, func(t *testing.T) {
			calls, output, err := runDataMountFixture(t, fixture)
			require.Error(t, err, output)
			require.NotContains(t, calls, "mkfs.ext4 ")
			require.NotContains(t, calls, "mount ")
		})
	}
}

func TestPlainDataAlreadyVerifiedMountSkipsAllDiskWrites(t *testing.T) {
	calls, output, err := runDataMountFixture(t, dataMountFixture{alreadyMounted: true})
	require.NoError(t, err, output)
	require.NotContains(t, calls, "blkid ")
	require.NotContains(t, calls, "mkfs.ext4 ")
	require.NotContains(t, calls, "mount ")
	require.Contains(t, calls, "plain-preflight")
}

func TestPlainDataAbsentDiskWaitIsBounded(t *testing.T) {
	calls, output, err := runDataMountFixture(t, dataMountFixture{diskAbsent: true})
	require.Error(t, err, output)
	require.Equal(t, 15, strings.Count(calls, "sleep 1\n"))
	require.Equal(t, 16, strings.Count(calls, "plain-preflight\n"))
	require.NotContains(t, calls, "blkid ")
	require.NotContains(t, calls, "mount ")
}

func TestPlainDataFailedMountNeverReportsVerified(t *testing.T) {
	calls, output, err := runDataMountFixture(t, dataMountFixture{
		probeStatus: "0", signature: "ext4", mountStatus: "1",
	})
	require.Error(t, err, output)
	require.NotContains(t, calls, "plain-mounted")
	require.NotContains(t, output, "verified ")
}

func TestPlainDataInitializationPreservesUnreadableOrNonzeroDisk(t *testing.T) {
	cases := map[string]dataMountFixture{
		"probe error":           {probeStatus: "4"},
		"empty successful read": {probeStatus: "0"},
		"unreadable disk":       {probeStatus: "2", size: "1048576", zeroStatus: "2"},
		"unrecognized data":     {probeStatus: "2", size: "1048576", zeroStatus: "1"},
		"size read failed":      {probeStatus: "2", sizeStatus: "1"},
		"size zero":             {probeStatus: "2", size: "0"},
		"size absent":           {probeStatus: "2"},
		"size malformed":        {probeStatus: "2", size: "1024 extra"},
		"size octal prefix":     {probeStatus: "2", size: "01024"},
		"size negative":         {probeStatus: "2", size: "-1024"},
		"partition table":       {probeStatus: "0", signature: "gpt"},
		"partition and ext4":    {probeStatus: "0", signature: "ext4\ngpt"},
		"other filesystem":      {probeStatus: "0", signature: "xfs"},
		"encrypted disk":        {probeStatus: "0", signature: "crypto_LUKS"},
		"failed partial probe":  {probeStatus: "2", signature: "ext4"},
	}
	for name, fixture := range cases {
		t.Run(name, func(t *testing.T) {
			calls, output, err := runDataMountFixture(t, fixture)
			require.Error(t, err, output)
			require.NotContains(t, calls, "mkfs.ext4", calls)
			require.NotContains(t, calls, "mount ", calls)
		})
	}
}

func TestPlainDataInitializationRequiresCompleteZeroRead(t *testing.T) {
	calls, output, err := runDataMountFixture(t, dataMountFixture{
		probeStatus: "2", size: "1048576", zeroStatus: "0",
	})
	require.NoError(t, err, output)
	require.Contains(t, calls, "cmp -s -n 1048576 ")
	require.Less(t, strings.Index(calls, "cmp "), strings.Index(calls, "mkfs.ext4 "))
	require.Contains(t, calls, "mkfs.ext4 -q -L qubesair-data ")
	require.Contains(t, calls, "mount ")
	require.Less(t, strings.Index(calls, "plain-preflight"), strings.Index(calls, "blkid "))
	require.Less(t, strings.Index(calls, "plain-unmounted"), strings.Index(calls, "mkfs.ext4 "))
	require.Less(t, strings.Index(calls, "mount "), strings.Index(calls, "plain-mounted"))
}

func TestPlainDataResumeNeverScansOrReformatsExt4(t *testing.T) {
	calls, output, err := runDataMountFixture(t, dataMountFixture{probeStatus: "0", signature: "ext4"})
	require.NoError(t, err, output)
	require.NotContains(t, calls, "blockdev ")
	require.NotContains(t, calls, "cmp ")
	require.NotContains(t, calls, "mkfs.ext4 ")
	require.Contains(t, calls, "mount ")
}

func TestPlainDataCreationFailureStopsBeforeMount(t *testing.T) {
	calls, output, err := runDataMountFixture(t, dataMountFixture{
		probeStatus: "2", size: "1048576", zeroStatus: "0", mkfsStatus: "1",
	})
	require.Error(t, err, output)
	require.Contains(t, calls, "mkfs.ext4 ")
	require.NotContains(t, calls, "mount ")
}

func TestPlainDataGuardWithRealCompare(t *testing.T) {
	zeros := make([]byte, 32768)
	nonzero := make([]byte, len(zeros))
	nonzero[len(nonzero)-1] = 1
	for _, tc := range []struct {
		name      string
		data      []byte
		directory bool
		accepted  bool
	}{
		{name: "complete zero disk", data: zeros, accepted: true},
		{name: "last byte nonzero", data: nonzero},
		{name: "short read", data: zeros[:8192]},
		{name: "read error", directory: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls, output, err := runDataMountFixture(t, dataMountFixture{
				probeStatus: "2", size: strconv.Itoa(len(zeros)), diskData: tc.data,
				realCompare: true, directory: tc.directory,
			})
			if tc.accepted {
				require.NoError(t, err, output)
				require.Contains(t, calls, "mkfs.ext4 ")
			} else {
				require.Error(t, err, output)
				require.Contains(t, output, "refusing initialization")
				require.NotContains(t, calls, "mkfs.ext4 ")
				require.NotContains(t, calls, "mount ")
			}
		})
	}
}

func runDataMountFixture(t *testing.T, fixture dataMountFixture) (string, string, error) {
	t.Helper()
	dir := t.TempDir()
	disk := filepath.Join(dir, "disk")
	if fixture.directory {
		require.NoError(t, os.Mkdir(disk, 0o700))
	} else {
		require.NoError(t, os.WriteFile(disk, fixture.diskData, 0o600))
	}
	userData, _ := renderFixture(t)
	rendered := fileContent(t, parseConfig(t, userData), "qubes-air-mount-data")
	require.Contains(t, rendered, dataMountCheckerPath)
	require.Contains(t, rendered, dataMountPoint)
	script := strings.NewReplacer(dataMountCheckerPath, filepath.Join(dir, "checker.py"),
		dataMountPoint, filepath.Join(dir, "data")).Replace(rendered)
	path := filepath.Join(dir, "mount-data")
	require.NoError(t, os.WriteFile(path, []byte(script), 0o700))
	writeDataMountStubs(t, dir)
	if fixture.realCompare {
		compare, err := exec.LookPath("cmp")
		require.NoError(t, err)
		require.NoError(t, os.Remove(filepath.Join(dir, "cmp")))
		require.NoError(t, os.Symlink(compare, filepath.Join(dir, "cmp")))
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", path)
	// No real formatting, mounting or device tools are reachable from PATH.
	cmd.Env = []string{"PATH=" + dir, "STUB_LOG=" + filepath.Join(dir, "calls"), "STUB_DISK=" + disk,
		"PROBE_STATUS=" + fixture.probeStatus, "SIGNATURE=" + fixture.signature,
		"SIZE_STATUS=" + fixture.sizeStatus, "DISK_SIZE=" + fixture.size,
		"ZERO_STATUS=" + fixture.zeroStatus, "MKFS_STATUS=" + fixture.mkfsStatus,
		"PREFLIGHT_STATUS=" + fixture.preflightStatus, "MOUNTED_STATUS=" + fixture.mountedStatus,
		"BEFORE_MOUNT_STATUS=" + fixture.beforeMountStatus, "ALREADY_MOUNTED=" + strconv.FormatBool(fixture.alreadyMounted),
		"MOUNT_STATUS=" + fixture.mountStatus, "CHANGED_DEVICE=" + strconv.FormatBool(fixture.changedDevice),
		"DISK_ABSENT=" + strconv.FormatBool(fixture.diskAbsent)}
	output, err := cmd.CombinedOutput()
	calls, readErr := os.ReadFile(filepath.Join(dir, "calls"))
	require.NoError(t, readErr)
	return string(calls), string(output), err
}

func writeDataMountStubs(t *testing.T, dir string) {
	t.Helper()
	for name, body := range map[string]string{
		"blkid":     `printf '%s' "$SIGNATURE"; exit "${PROBE_STATUS:-0}"`,
		"blockdev":  `printf '%s' "$DISK_SIZE"; exit "${SIZE_STATUS:-0}"`,
		"cmp":       `exit "${ZERO_STATUS:-0}"`,
		"mkfs.ext4": `exit "${MKFS_STATUS:-0}"`,
		"python3": `case "$3" in
    plain-preflight)
        [ "${PREFLIGHT_STATUS:-0}" = 0 ] || exit "$PREFLIGHT_STATUS"
        if [ "$DISK_ABSENT" = true ]; then printf 'absent\n'; exit 0; fi
        if [ "$ALREADY_MOUNTED" = true ]; then printf 'mounted\n'; else printf '%s\n' "$STUB_DISK"; fi ;;
    plain-unmounted)
        if [ "$CHANGED_DEVICE" = true ]; then printf '%s.other\n' "$STUB_DISK"; else printf '%s\n' "$STUB_DISK"; fi
        exit "${BEFORE_MOUNT_STATUS:-0}" ;;
    plain-mounted) exit "${MOUNTED_STATUS:-0}" ;;
    *) exit 64 ;;
esac`,
		"mount": `exit "${MOUNT_STATUS:-0}"`,
		"mkdir": "exit 0",
		"sleep": "exit 0",
	} {
		script := "#!/bin/sh\nprintf '%s\\n' '" + name + " '\"$*\" >> \"$STUB_LOG\"\n" + body + "\n"
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(script), 0o700))
	}
}
