#!/bin/sh
# Runs shellcheck on every shell file and the bats unit tests. Usage: tests/check.sh [bats args]
set -eu
cd "$(dirname "$0")/.."
files="bin/lea bin/locksql libexec/lea-sh install.sh tools/dist.sh tests/check.sh tests/run.sh"
for f in libexec/lib/*.sh proxy/*.sh k8s/*.sh tests/fakes/* image/entrypoint image/lock-host image/user-init image/lea-session image/lea-agent image/wl-copy image/go-cross image/claude-plugin/hooks/tower-guard \
         tests/proxy/run.sh tests/integration/isolation.sh tests/integration/lockhost.sh tests/integration/update-deps.sh tests/integration/inside-isolation.sh tests/integration/locksql-confine.sh; do
  [ -f "$f" ] && files="$files $f"
done
# shellcheck disable=SC2086
shellcheck -x -s sh $files
if [ $# -eq 0 ]; then set -- tests/unit; fi
bats "$@"
