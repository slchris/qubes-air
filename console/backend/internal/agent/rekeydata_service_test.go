package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
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

// withoutSystemd points PATH at a directory holding only the tools the script and
// the cryptsetup stub use. A CI runner's /usr/bin carries a real systemd-run, so
// deleting the stub alone would not reach the script's no-systemd branch.
func (f *rekeyFixture) withoutSystemd() {
	f.t.Helper()
	jail := filepath.Join(f.t.TempDir(), "jail")
	require.NoError(f.t, os.Mkdir(jail, 0o700))
	require.NoError(f.t, os.Symlink(filepath.Join(f.binDir, "cryptsetup"), filepath.Join(jail, "cryptsetup")))
	for _, tool := range []string{"cat", "grep", "head", "python3", "readlink", "sed"} {
		path, err := exec.LookPath(tool)
		require.NoError(f.t, err)
		require.NoError(f.t, os.Symlink(path, filepath.Join(jail, tool)))
	}
	require.True(f.t, strings.HasPrefix(f.env[0], "PATH="), "the fixture's first variable is PATH")
	f.env[0] = "PATH=" + jail
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
	return f.runArgs(input)
}

// runArgs runs the script with qrexec service arguments, as the agent does for
// a "qubesair.RekeyData+<arg>" request.
func (f *rekeyFixture) runArgs(input string, args ...string) rekeyReply {
	f.t.Helper()
	return f.runStdin(strings.NewReader(input), args...)
}

// runStdin runs the script with any stdin, such as a writer that stalls.
func (f *rekeyFixture) runStdin(stdin io.Reader, args ...string) rekeyReply {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, f.script, args...)
	cmd.Env = f.env
	cmd.Stdin = stdin
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
// the test exercises the same inner half without a running systemd. --setenv is
// honored: it is how the outer half hands the inner half its marker.
const systemdRunStub = `#!/bin/bash
while [ $# -gt 0 ]; do
  case "$1" in
    --pipe|--wait|--collect|--quiet) shift ;;
    --setenv=*) export "${1#--setenv=}"; shift ;;
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

// With no systemd-run on PATH the outer half enters the inner half directly, and
// it can only do so by setting the marker itself on that call.
func TestRekeyDataFallbackWithoutSystemd(t *testing.T) {
	f := newRekeyFixture(t, true, false)
	f.withoutSystemd()
	reply := f.run(f.request())

	require.True(t, reply.Rekeyed)
	require.True(t, reply.OldKeyRemoved)
	require.Equal(t, "0", f.stateLine(2), "the legacy keyslot must be gone")
	require.Equal(t, "1", f.stateLine(4), "the new key must open the container")
}

// The console never sends a service argument. "qubesair.RekeyData+__rekey" used to
// select the privileged half directly, skipping the outer validation; with the
// same key as old and new it then removed the only keyslot.
func TestRekeyDataRefusesServiceArguments(t *testing.T) {
	for _, arg := range []string{"__rekey", "anything"} {
		t.Run(arg, func(t *testing.T) {
			f := newRekeyFixture(t, true, false)
			reply := f.runArgs(f.oldKey+"\n"+f.oldKey+"\n", arg)

			require.False(t, reply.Rekeyed)
			require.Equal(t, "bad_argument", reply.Reason)
			require.Equal(t, "1", f.stateLine(2), "the only keyslot must survive")
		})
	}
}

// The 8192-byte cap counts raw stdin bytes. A command substitution used to strip
// trailing newlines before the count and bash drops NUL bytes, so an oversized
// or NUL-carrying request could shrink under the cap and be used altered.
func TestRekeyDataCapCountsRawBytes(t *testing.T) {
	request := `{"old":"` + testOldKey + `","new":"` + testNewKey + `"}`
	cases := []struct{ name, input, reason string }{
		{"newline padding past the cap", request + strings.Repeat("\n", 8200) + "x", "oversize"},
		{"newlines alone past the cap", request + strings.Repeat("\n", 8192), "oversize"},
		{"NUL inside the old key", `{"old":"` + testOldKey + "\x00" + `","new":"` + testNewKey + `"}`, "malformed"},
		{"NUL after the request", request + "\x00", "malformed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newRekeyFixture(t, true, false)
			reply := f.run(tc.input)
			require.False(t, reply.Rekeyed)
			require.Equal(t, tc.reason, reply.Reason)
			require.Equal(t, "1", f.stateLine(2), "a rejected request must not touch the container")
		})
	}
}

// The cap counts bytes even under a UTF-8 locale: the pad is 2800 characters of
// U+4F60 but 8400 bytes. A character count would let the request through.
func TestRekeyDataCapCountsBytesUnderUTF8Locale(t *testing.T) {
	f := newRekeyFixture(t, true, false)
	f.withEnv("LC_ALL=C.UTF-8")
	reply := f.run(`{"old":"` + f.oldKey + `","new":"` + f.newKey + `","pad":"` +
		strings.Repeat("\u4f60", 2800) + `"}`)

	require.False(t, reply.Rekeyed)
	require.Equal(t, "oversize", reply.Reason)
	require.Equal(t, "1", f.stateLine(2), "a rejected request must not touch the container")
}

// stalledRequest delivers the first 20 bytes, pauses 2 s, then the rest.
func stalledRequest(request string) io.Reader {
	r, w := io.Pipe()
	go func() {
		_, _ = w.Write([]byte(request[:20]))
		time.Sleep(2 * time.Second)
		_, _ = w.Write([]byte(request[20:]))
		_ = w.Close()
	}()
	return r
}

// TMOUT is bash's default timeout for read. A caller-side TMOUT must not end the
// read of a slowly arriving request early: the whole request is used.
func TestRekeyDataReadIsNotCutShortByTMOUT(t *testing.T) {
	f := newRekeyFixture(t, true, false)
	f.withEnv("TMOUT=1")
	reply := f.runStdin(stalledRequest(f.request()))

	require.True(t, reply.Rekeyed, "reply=%+v", reply)
	require.True(t, reply.OldKeyRemoved)
	require.Equal(t, "0", f.stateLine(2))
	require.Equal(t, "1", f.stateLine(4))
}

// With the TMOUT reset taken out of a copy the read does time out. bash 5 returns
// >128 and keeps the partial input, which the status check must refuse; bash 3.2
// returns 1 and discards it, which the JSON parser refuses. Either way nothing
// touches the container.
func TestRekeyDataRefusesATimedOutRead(t *testing.T) {
	f := newRekeyFixture(t, true, false)
	script, err := os.ReadFile(f.script)
	require.NoError(t, err)
	require.True(t, strings.Contains(string(script), "\nunset TMOUT\n"), "the script no longer resets TMOUT; update this test")
	require.NoError(t, os.WriteFile(f.script, []byte(strings.Replace(string(script), "\nunset TMOUT\n", "\n:\n", 1)), 0o700))
	f.withEnv("TMOUT=1")
	reply := f.runStdin(stalledRequest(f.request()))

	require.False(t, reply.Rekeyed)
	require.Equal(t, "malformed", reply.Reason)
	require.Equal(t, "1", f.stateLine(2), "a refused request must not touch the container")
	major, err := exec.Command("/bin/bash", "-c", `printf %s "${BASH_VERSINFO[0]}"`).Output()
	require.NoError(t, err)
	if string(major) >= "4" {
		require.Equal(t, "could not read the request", reply.Detail)
	}
}

func TestRekeyDataAcceptsRequestAtTheCap(t *testing.T) {
	f := newRekeyFixture(t, true, false)
	request := f.request()
	reply := f.run(request + strings.Repeat("\n", 8192-len(request)))

	require.True(t, reply.Rekeyed)
	require.True(t, reply.OldKeyRemoved)
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
