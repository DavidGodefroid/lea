#!/bin/sh
# Builds the test image and runs tests/check.sh in it: repository read-only, host uid, no network.
set -eu
root=$(cd "$(dirname "$0")/.." && pwd -P)
docker build -q -t lea-tests -f "$root/tests/Containerfile" "$root/tests" >/dev/null
tmp=$(mktemp -d)
trap 'rm -r "$tmp"' EXIT
printf 'lea:x:%s:%s::/tmp:/bin/sh\n' "$(id -u)" "$(id -g)" >"$tmp/passwd"
printf 'lea:x:%s:\n' "$(id -g)" >"$tmp/group"
# The pid-file tests compare pids with /proc. Under lea's Podman a nested container binds lea's /proc
# (Docker's masked /proc forbids mounting its own), so the suite shares lea's pid namespace to match
# it; Podman shares it only with cgroups, which lea's containers.conf disables by default.
pid=--pid=host
if docker --version 2>/dev/null | grep -qi podman; then pid="$pid --cgroups=enabled"; fi
# shellcheck disable=SC2086
docker run --rm --network none $pid -u "$(id -u):$(id -g)" -e HOME=/tmp \
  -v "$tmp/passwd:/etc/passwd:ro" -v "$tmp/group:/etc/group:ro" \
  -v "$root:/lea:ro" -w /lea lea-tests tests/check.sh "$@"
