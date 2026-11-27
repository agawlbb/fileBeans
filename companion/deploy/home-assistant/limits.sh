#!/bin/sh
set -eu
umask 077
# This mount is confined to the HA app's cgroup namespace, never the host tree.
[ "$(cat /proc/self/cgroup)" = '0::/' ]
mkdir -p /run/filebeans-limits
mount -t cgroup2 -o nosuid,nodev,noexec cgroup2 /run/filebeans-limits
root=/run/filebeans-limits
mkdir "$root/work"
while read -r process; do
 printf '%s\n' "$process" > "$root/work/cgroup.procs"
done < "$root/cgroup.procs"
printf '+cpu +memory +pids\n' > "$root/cgroup.subtree_control"
printf '536870912\n' > "$root/work/memory.max"
printf '0\n' > "$root/work/memory.swap.max"
printf '1\n' > "$root/work/memory.oom.group"
printf '128\n' > "$root/work/pids.max"
printf '100000 100000\n' > "$root/work/cpu.max"
printf '10\n' > "$root/work/cpu.weight"
[ "$(cat "$root/work/memory.max")" = 536870912 ]
[ "$(cat "$root/work/memory.swap.max")" = 0 ]
[ "$(cat "$root/work/memory.oom.group")" = 1 ]
[ "$(cat "$root/work/pids.max")" = 128 ]
[ "$(cat "$root/work/cpu.max")" = '100000 100000' ]
mkdir -p /data/api /data/broker /run/filebeans-vaults
chown 1000:1000 /data/api
chmod 0700 /data/api /data/broker
printf 'FileBeans limits: 512 MiB, no swap, one CPU, 128 processes\n'
