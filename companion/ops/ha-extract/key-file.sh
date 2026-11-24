#!/bin/bash
set -euo pipefail
set +x
ulimit -c 0
job=${1:?Usage: key-file.sh job.json key-file}
key_file=${2:?Usage: key-file.sh job.json key-file}
[ -f "$key_file" ]
size=$(stat -c %s -- "$key_file")
[ "$size" -gt 0 ] && [ "$size" -le 256 ]
# The existing key is read on Pi; no new key file is created or transferred.
awk -f /local_apps/filebeans_extract/normalize-key.awk "$key_file" |
 /local_apps/filebeans_extract/worker client "$job"
