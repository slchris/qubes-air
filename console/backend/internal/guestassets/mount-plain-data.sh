#!/bin/sh
# Managed by the Qubes Air console and delivered before agent installation.
set -eu

check_data() {
    python3 -I /usr/local/lib/qubes-air/data-mount.py "$1"
}

# Only an absent disk is retried. Ambiguous devices and unsafe mounts fail now.
i=0
while :; do
    state="$(check_data plain-preflight)"
    [ "$state" = absent ] || break
    if [ "$i" -ge 15 ]; then
        echo "qubes-air-mount-data: required data disk (scsi1) is missing; refusing startup" >&2
        exit 1
    fi
    i=$((i + 1))
    sleep 1
done
if [ "$state" = mounted ]; then
    echo "qubes-air-mount-data: verified /data is already mounted" >&2
    exit 0
fi
dev="$state"

require_unmounted() {
    current="$(check_data plain-unmounted)"
    if [ "$current" != "$dev" ]; then
        echo "qubes-air-mount-data: data device changed; refusing initialization or mount" >&2
        exit 1
    fi
}

# Exit 2 can mean a read error. It never authorizes formatting by itself.
# Existing whole-disk ext4 skips the full scan; other signatures are rejected.
probe_status=0
fstype="$(blkid -p -o value -s TYPE -s PTTYPE "$dev" 2>/dev/null)" || probe_status=$?
case "$probe_status:$fstype" in
    0:ext4) ;;
    2:)
        bytes="$(blockdev --getsize64 "$dev")"
        case "$bytes" in
            ''|*[!0-9]*|0*)
                echo "qubes-air-mount-data: cannot verify data disk size" >&2
                exit 1 ;;
        esac
        if ! cmp -s -n "$bytes" "$dev" /dev/zero; then
            echo "qubes-air-mount-data: data disk is not readable as all zeros; refusing initialization" >&2
            exit 1
        fi
        require_unmounted
        echo "qubes-air-mount-data: verified zero disk; creating ext4 (first boot)" >&2
        mkfs.ext4 -q -L qubesair-data "$dev"
        ;;
    *)
        echo "qubes-air-mount-data: unsupported signature or failed probe; refusing initialization" >&2
        exit 1
        ;;
esac

require_unmounted
mkdir -p /data
mount -t ext4 -o rw -- "$dev" /data
check_data plain-mounted
echo "qubes-air-mount-data: verified $dev mounted at /data" >&2
