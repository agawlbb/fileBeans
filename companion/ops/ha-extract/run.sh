#!/bin/sh
set -eu
ulimit -c 0
cd /data
export TMPDIR=/data
export GOMAXPROCS=1 GOMEMLIMIT=384MiB
exec /local_apps/filebeans_extract/worker serve
