#!/bin/bash
set -euo pipefail
set +x
ulimit -c 0
job=${1:?Usage: key-entry.sh job.json}
# Read only from the user's terminal. Never use command history, argv, or env.
code=
trap 'unset code' EXIT
IFS= read -r -s -p 'FileBeans recovery code (hidden): ' code </dev/tty
printf '\n' >/dev/tty
printf '%s\n' "$code" |
 awk -f /local_apps/filebeans_extract/normalize-key.awk |
 /local_apps/filebeans_extract/worker client "$job"
unset code
