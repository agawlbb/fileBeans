#!/bin/sh
set -eu
# Administrator-owned allowlist; never accept arbitrary unit names from the API.
case "${1-}" in archive) ;; *) exit 64 ;; esac
/usr/bin/mountpoint -q "/srv/filebeans-vaults/$1"
/usr/bin/systemctl start "filebeans-vault-quantum@$1.service"
attempt=0
while [ "$attempt" -lt 8 ]; do
    if /usr/bin/curl --silent --fail --max-time 1 http://127.0.0.1:8096/health >/dev/null; then
        exit 0
    fi
    attempt=$((attempt + 1))
    sleep 1
done
/usr/bin/systemctl stop "filebeans-vault-quantum@$1.service"
exit 1
