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
	probeStatus string
	signature   string
	sizeStatus  string
	size        string
	zeroStatus  string
	mkfsStatus  string
	diskData    []byte
	realCompare bool
	directory   bool
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
	rendered := dataDiskMountScript()
	require.Contains(t, rendered, "/dev/disk/by-path/*-scsi-0:0:0:1")
	require.Contains(t, rendered, dataMountPoint)
	script := strings.NewReplacer("/dev/disk/by-path/*-scsi-0:0:0:1", disk,
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
		"ZERO_STATUS=" + fixture.zeroStatus, "MKFS_STATUS=" + fixture.mkfsStatus}
	output, err := cmd.CombinedOutput()
	calls, readErr := os.ReadFile(filepath.Join(dir, "calls"))
	require.NoError(t, readErr)
	return string(calls), string(output), err
}

func writeDataMountStubs(t *testing.T, dir string) {
	t.Helper()
	for name, body := range map[string]string{
		"readlink":   `printf '%s\n' "$STUB_DISK"`,
		"blkid":      `printf '%s' "$SIGNATURE"; exit "${PROBE_STATUS:-0}"`,
		"blockdev":   `printf '%s' "$DISK_SIZE"; exit "${SIZE_STATUS:-0}"`,
		"cmp":        `exit "${ZERO_STATUS:-0}"`,
		"mkfs.ext4":  `exit "${MKFS_STATUS:-0}"`,
		"mountpoint": "exit 1",
		"mount":      "exit 0",
		"mkdir":      "exit 0",
	} {
		script := "#!/bin/sh\nprintf '%s\\n' '" + name + " '\"$*\" >> \"$STUB_LOG\"\n" + body + "\n"
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(script), 0o700))
	}
}
