#!/bin/sh
set -eu
umask 077
ulimit -c 0
ulimit -f 4096
base=/local_apps/filebeans_extract
work=/data/filebeans-selftest-$$
mkdir -m 700 "$work"
cleanup() {
 set +e
 umount "$work/source" 2>/dev/null
 umount "$work/destination" 2>/dev/null
 [ -n "${src_pid:-}" ] && kill "$src_pid" 2>/dev/null
 [ -n "${dst_pid:-}" ] && kill "$dst_pid" 2>/dev/null
 wait 2>/dev/null
 # Never remove a still-mounted tree.
 if ! grep -F " $work/" /proc/mounts >/dev/null; then rm -rf "$work"; fi
}
trap cleanup EXIT
trap 'exit 1' INT TERM
mkdir "$work/source-cipher" "$work/destination-cipher" "$work/source" "$work/destination"
for name in source destination; do
 printf '%s\n' 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef |
 "$base/gocryptfs" -q -init -masterkey=stdin -extpass '/bin/echo synthetic-test-only' "$work/$name-cipher" >/dev/null 2>&1
done
printf '%s\n' 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef |
 "$base/gocryptfs" -q -fg -nosyslog -masterkey=stdin "$work/source-cipher" "$work/source" >/dev/null 2>&1 &
src_pid=$!
printf '%s\n' 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef |
 "$base/gocryptfs" -q -fg -nosyslog -masterkey=stdin "$work/destination-cipher" "$work/destination" >/dev/null 2>&1 &
dst_pid=$!
for name in source destination; do
 i=0
 until grep -F " $work/$name fuse.gocryptfs " /proc/mounts >/dev/null; do
  i=$((i+1)); [ "$i" -lt 100 ] || { echo 'FUSE mount failed'; exit 1; }
  sleep .1
 done
done
"$base/worker" selftest "$work/source" "$work/destination"
umount "$work/source"
wait "$src_pid"
src_pid=
printf '%s\n' 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef |
 "$base/gocryptfs" -q -fg -ro -nosyslog -masterkey=stdin "$work/source-cipher" "$work/source" >/dev/null 2>&1 &
src_pid=$!
i=0
until grep -F " $work/source fuse.gocryptfs " /proc/mounts >/dev/null; do
 i=$((i+1)); [ "$i" -lt 100 ] || exit 1
 sleep .1
done
[ -s "$work/source/fixture.tar.gz" ]
if touch "$work/source/should-not-write" 2>/dev/null; then echo 'Read-only mount failed'; exit 1; fi
cleanup
trap - EXIT
[ ! -e "$work" ]
echo 'Read-only source mount passed; synthetic fixtures verified absent'
