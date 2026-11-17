#!/bin/sh
set -eu
case "${1-}" in archive) ;; *) exit 64 ;; esac
# stop waits for the complete unit/cgroup, including streams and index workers.
exec /usr/bin/systemctl stop "filebeans-vault-quantum@$1.service"
