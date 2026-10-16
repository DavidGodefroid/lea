#!/bin/sh
# Builds the test image and runs tests/check.sh in it: repository read-only, host uid, no network.
set -eu
root=$(cd "$(dirname "$0")/.." && pwd -P)
docker build -q -t lea-tests -f "$root/tests/Containerfile" "$root/tests" >/dev/null
tmp=$(mktemp -d)
trap 'rm -r "$tmp"' EXIT
printf 'lea:x:%s:%s::/tmp:/bin/sh\n' "$(id -u)" "$(id -g)" >"$tmp/passwd"
printf 'lea:x:%s:\n' "$(id -g)" >"$tmp/group"
docker run --rm --network none -u "$(id -u):$(id -g)" -e HOME=/tmp \
  -v "$tmp/passwd:/etc/passwd:ro" -v "$tmp/group:/etc/group:ro" \
  -v "$root:/lea:ro" -w /lea lea-tests tests/check.sh "$@"
