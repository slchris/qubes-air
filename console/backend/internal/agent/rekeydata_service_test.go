package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// rekeyReply mirrors the JSON contract of qubesair.RekeyData.
type rekeyReply struct {
	Rekeyed       bool   `json:"rekeyed"`
	OldKeyRemoved bool   `json:"old_key_removed"`
	Reason        string `json:"reason"`
	Detail        string `json:"detail"`
}

// rekeyFixture runs the real service script against a stub cryptsetup whose
// keyslots live in one state file: old key, old-valid, new key, new-valid.
type rekeyFixture struct {
	t      *testing.T
	binDir string
	state  string
	disk   string
	script string
	oldKey string
	newKey string
	env    []string
}

const (
	testOldKey = "b2xkLWtleS1vbGQtbWFzdGVyLWRlcml2ZWQta2V5LW1hdGVyaWFs" // 52 chars
	testNewKey = "bmV3LWtleS1wZXItcXVicy1yYW5kb20tZGVrLW1hdGVyaWFs"     // 52 chars
)

func newRekeyFixture(t *testing.T, oldValid, newValid bool) *rekeyFixture {
	t.Helper()
	dir := t.TempDir()

	script, err := os.ReadFile("../../../../remote/qubes-rpc/qubesair.RekeyData")
	require.NoError(t, err)
	path := filepath.Join(dir, "RekeyData")
	require.NoError(t, os.WriteFile(path, script, 0o700))

	state := filepath.Join(dir, "state")
	writeState(t, state, testOldKey, oldValid, testNewKey, newValid)

	disk := filepath.Join(dir, "disk.img")
	require.NoError(t, os.WriteFile(disk, make([]byte, 4096), 0o600))

	binDir := filepath.Join(dir, "bin")
	require.NoError(t, os.Mkdir(binDir, 0o700))
	writeStub(t, filepath.Join(binDir, "cryptsetup"), cryptsetupStub)
	writeStub(t, filepath.Join(binDir, "systemd-run"), systemdRunStub)

	return &rekeyFixture{
		t:      t,
		binDir: binDir,
		state:  state,
		disk:   disk,
		script: path,
		oldKey: testOldKey,
		newKey: testNewKey,
		env: []string{
			"PATH=" + binDir + ":/usr/bin:/bin",
			"QUBESAIR_TEST_STATE=" + state,
			"QUBESAIR_DATA_DISK=" + disk,
		},
	}
}

// withEnv adds an environment override for one run (the stub reads it).
func (f *rekeyFixture) withEnv(kv string) {
	f.env = append(f.env, kv)
}

func (f *rekeyFixture) stateLine(n int) string {
	f.t.Helper()
	content, err := os.ReadFile(f.state)
	require.NoError(f.t, err)
	lines := strings.Split(strings.TrimRight(string(content), "\n"), "\n")
	require.GreaterOrEqual(f.t, len(lines), n)
	return lines[n-1]
}

func (f *rekeyFixture) run(input string) rekeyReply {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, f.script)
	cmd.Env = f.env
	cmd.Stdin = strings.NewReader(input)
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	err := cmd.Run()
	require.NoError(f.t, err, "script must always exit 0; stdout=%q stderr=%q", out.String(), stderr.String())

	var reply rekeyReply
	require.NoError(f.t, json.Unmarshal([]byte(strings.TrimSpace(out.String())), &reply),
		"stdout=%q stderr=%q", out.String(), stderr.String())
	return reply
}

func (f *rekeyFixture) request() string {
	return `{"old":"` + f.oldKey + `","new":"` + f.newKey + `"}`
}

func writeState(t *testing.T, path, oldKey string, oldValid bool, newKey string, newValid bool) {
	t.Helper()
	flag := func(ok bool) string {
		if ok {
			return "1"
		}
		return "0"
	}
	require.NoError(t, os.WriteFile(path, []byte(strings.Join([]string{
		oldKey, flag(oldValid), newKey, flag(newValid), "",
	}, "\n")), 0o600))
}

func writeStub(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte(content), 0o700))
}

// cryptsetupStub models a LUKS container with exactly the two keyslots the
// state file describes. It never logs key material.
const cryptsetupStub = `#!/bin/bash
state="${QUBESAIR_TEST_STATE:?}"
old="$(sed -n 1p "$state")"
oldok="$(sed -n 2p "$state")"
new="$(sed -n 3p "$state")"
newok="$(sed -n 4p "$state")"

cmd=""
keyfile=""
newfile=""
dev=""
for a in "$@"; do
  case "$a" in
    isLuks|open|luksChangeKey|luksAddKey|luksRemoveKey) cmd="$a" ;;
    --key-file=*) keyfile="${a#--key-file=}" ;;
    --*) ;;
    *) if [ -z "$dev" ]; then dev="$a"; elif [ -z "$newfile" ]; then newfile="$a"; fi ;;
  esac
done
key="$(cat "$keyfile" 2>/dev/null)"

rewrite() { printf '%s\n%s\n%s\n%s\n' "$1" "$2" "$3" "$4" > "$state"; }

case "$cmd" in
  isLuks)
    [ "${QUBESAIR_TEST_NOT_LUKS:-}" = "1" ] && exit 1
    exit 0 ;;
  open)
    [ "$key" = "$old" ] && [ "$oldok" = "1" ] && exit 0
    [ "$key" = "$new" ] && [ "$newok" = "1" ] && exit 0
    exit 1 ;;
  luksChangeKey)
    [ "${QUBESAIR_TEST_FAIL_CHANGE:-}" = "1" ] && exit 1
    [ "$key" = "$old" ] && [ "$oldok" = "1" ] || exit 1
    rewrite "$old" 0 "$new" 1
    exit 0 ;;
  luksAddKey)
    [ "${QUBESAIR_TEST_FAIL_ADD:-}" = "1" ] && exit 1
    [ "$key" = "$old" ] && [ "$oldok" = "1" ] || exit 1
    rewrite "$old" "$oldok" "$new" 1
    exit 0 ;;
  luksRemoveKey)
    [ "${QUBESAIR_TEST_FAIL_REMOVE:-}" = "1" ] && exit 1
    [ "$key" = "$old" ] && [ "$oldok" = "1" ] || exit 1
    rewrite "$old" 0 "$new" "$newok"
    exit 0 ;;
esac
exit 1
`

// systemdRunStub drops the unit options and executes the service directly, so
// the test exercises the same inner half without a running systemd.
const systemdRunStub = `#!/bin/bash
while [ $# -gt 0 ]; do
  case "$1" in
    --pipe|--wait|--collect|--quiet) shift ;;
    --) shift; break ;;
    *) break ;;
  esac
done
exec "$@"
`

func TestRekeyDataMigratesLegacyContainer(t *testing.T) {
	f := newRekeyFixture(t, true, false)
	reply := f.run(f.request())

	require.True(t, reply.Rekeyed)
	require.True(t, reply.OldKeyRemoved)
	// The container must now answer to the new key only.
	require.Equal(t, "0", f.stateLine(2), "the legacy keyslot must be gone")
	require.Equal(t, "1", f.stateLine(4), "the new key must open the container")
}

func TestRekeyDataIsIdempotentWhenAlreadyRekeyed(t *testing.T) {
	f := newRekeyFixture(t, false, true)
	reply := f.run(f.request())

	require.True(t, reply.Rekeyed)
	require.True(t, reply.OldKeyRemoved)
	require.Equal(t, "already", reply.Reason)
}

func TestRekeyDataRemovesLeftoverLegacySlot(t *testing.T) {
	// Both keys open: a previous attempt added the new key but never removed
	// the old one. Removal must succeed and be reported.
	f := newRekeyFixture(t, true, true)
	reply := f.run(f.request())

	require.True(t, reply.Rekeyed)
	require.True(t, reply.OldKeyRemoved)
	require.Equal(t, "0", f.stateLine(2))
}

func TestRekeyDataRefusesWhenNeitherKeyOpens(t *testing.T) {
	f := newRekeyFixture(t, false, false)
	reply := f.run(f.request())

	require.False(t, reply.Rekeyed)
	require.Equal(t, "no_key", reply.Reason)
}

func TestRekeyDataRefusesNonLuksDisk(t *testing.T) {
	f := newRekeyFixture(t, true, false)
	f.withEnv("QUBESAIR_TEST_NOT_LUKS=1")
	reply := f.run(f.request())

	require.False(t, reply.Rekeyed)
	require.Equal(t, "not_luks", reply.Reason)
	require.Equal(t, "1", f.stateLine(2), "a refused rekey must not touch the container")
}

func TestRekeyDataRefusesMissingDisk(t *testing.T) {
	f := newRekeyFixture(t, true, false)
	f.env = append(f.env[:len(f.env):len(f.env)], "QUBESAIR_DATA_DISK=/nonexistent/qubesair-data")
	reply := f.run(f.request())

	require.False(t, reply.Rekeyed)
	require.Equal(t, "no_disk", reply.Reason)
}

func TestRekeyDataKeepsOldKeyWhenChangeFails(t *testing.T) {
	// luksChangeKey fails and the add+remove fallback cannot add either: the
	// old key must remain the only valid one, and the console must see failure.
	f := newRekeyFixture(t, true, false)
	f.withEnv("QUBESAIR_TEST_FAIL_CHANGE=1")
	f.withEnv("QUBESAIR_TEST_FAIL_ADD=1")
	reply := f.run(f.request())

	require.False(t, reply.Rekeyed)
	require.Equal(t, "change_failed", reply.Reason)
	require.Equal(t, "1", f.stateLine(2), "the old key must stay valid")
	require.Equal(t, "0", f.stateLine(4), "no unreachable key may be introduced")
}

func TestRekeyDataReportsRemoveFailureAfterFallbackAdd(t *testing.T) {
	// The fallback adds the new key but cannot remove the old one: the data is
	// reachable with both keys, and the reply must say the old slot survived so
	// the console keeps its migration marker.
	f := newRekeyFixture(t, true, false)
	f.withEnv("QUBESAIR_TEST_FAIL_CHANGE=1")
	f.withEnv("QUBESAIR_TEST_FAIL_REMOVE=1")
	reply := f.run(f.request())

	require.True(t, reply.Rekeyed)
	require.False(t, reply.OldKeyRemoved)
	require.Equal(t, "remove_failed", reply.Reason)
	require.Equal(t, "1", f.stateLine(4), "the new key must remain usable")
}

func TestRekeyDataRejectsMalformedRequests(t *testing.T) {
	cases := map[string]string{
		"empty":         "",
		"not json":      "definitely not json",
		"wrong types":   `{"old":1,"new":true}`,
		"missing new":   `{"old":"` + testOldKey + `"}`,
		"bad key shape": `{"old":"old","new":"new"}`,
		"same key":      `{"old":"` + testOldKey + `","new":"` + testOldKey + `"}`,
		"oversize":      `{"old":"` + testOldKey + `","new":"` + strings.Repeat("A", 9000) + `"}`,
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			f := newRekeyFixture(t, true, false)
			reply := f.run(input)
			require.False(t, reply.Rekeyed)
			require.NotEmpty(t, reply.Reason)
			require.Equal(t, "1", f.stateLine(2), "a rejected request must not touch the container")
		})
	}
}
