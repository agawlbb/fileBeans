#!/bin/sh
set -eu
umask 077
# A new cgroup mount is scoped to this container's cgroup namespace.
# Fail closed unless that namespace exposes only the app's own cgroup.
[ "$(cat /proc/self/cgroup)" = '0::/' ]
mkdir -p /run/filebeans-limits
mount -t cgroup2 -o nosuid,nodev,noexec cgroup2 /run/filebeans-limits
# cgroup namespaces delegate controls to children, not the namespace root.
# Move only this app's processes into its own child; never access host cgroups.
root=/run/filebeans-limits
mkdir "$root/work"
while read -r process; do
 printf '%s\n' "$process" > "$root/work/cgroup.procs"
done < "$root/cgroup.procs"
printf '+cpu +memory +pids\n' > "$root/cgroup.subtree_control"
printf '536870912\n' > "$root/work/memory.max"
printf '0\n' > "$root/work/memory.swap.max"
printf '64\n' > "$root/work/pids.max"
printf '100000 100000\n' > "$root/work/cpu.max"
printf 'FileBeans helper limits: memory=%s swap=%s pids=%s cpu=%s\n' \
 "$(cat "$root/work/memory.max")" "$(cat "$root/work/memory.swap.max")" \
 "$(cat "$root/work/pids.max")" "$(cat "$root/work/cpu.max")"
mkdir -p /etc/services.d/filebeans-extract
cat > /etc/services.d/filebeans-extract/run <<'EOF'
#!/bin/sh
exec /bin/sh /local_apps/filebeans_extract/run.sh
EOF
chmod 700 /etc/services.d/filebeans-extract/run
