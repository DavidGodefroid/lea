#!/bin/sh
# Isolation tests: starts lea under the lea-test prefix and a decoy published on the host,
# then checks from inside lea that neither the decoy nor the host can be reached.
# Linux hosts. Creates and removes only lea-test-*, lea-canary* resources.
set -eu
root=$(cd "$(dirname "$0")/../.." && pwd -P)

# On hosts where Docker uses AppArmor, lea starts containers with apparmor=lea: it must be loaded.
if docker info --format '{{json .SecurityOptions}}' 2>/dev/null | grep -q 'name=apparmor'; then
  if ! docker run --rm --pull never --security-opt apparmor=lea --entrypoint true lea:dev >/dev/null 2>&1; then
    echo "isolation: the AppArmor profile 'lea' is not usable (or lea:dev is not built): run 'lea init' (AppArmor) and 'make build' first" >&2
    exit 1
  fi
fi

work=$(mktemp -d) || exit 1
work=$(cd "$work" && pwd -P) || exit 1
projects="$work/projects"; mkdir -p "$projects"
# A second projects dir: its lea container is the sibling that the first one must not reach.
sibling="$work/sibling"; mkdir -p "$sibling"
export XDG_DATA_HOME="$work/data"
cat >"$work/lea.conf" <<'CONF'
LEA_PREFIX=lea-test
LEA_IMAGE=lea:dev
LEA_PROXY_IMAGE=lea-proxy:dev
CONF
export LEA_CONFIG="$work/lea.conf"
state="$XDG_DATA_HOME/lea/state"; mkdir -p "$state"; printf '{}\n' >"$state/claude.json"
slug=$(printf '%s' "$projects" | sed 's/[^A-Za-z0-9]/-/g')
id=$(printf '%s' "$projects" | sha256sum | cut -c1-10)
sid=$(printf '%s' "$sibling" | sha256sum | cut -c1-10)
holders=
# A stand-in for the Lock Family hub's facade socket (I11): the host daemon is pointed at it, and
# no agent may see it (lea follows the hub by default). Outside the projects dir, as lockhub's is.
facade_dir="$work/run/lockhub"; facade="$facade_dir/facade.sock"; facade_pid=
export LEA_HUB_SOCKET="$facade"

# hold DIR: a background lea session on DIR, serving HTTP on port 8765 inside its container,
# that ends when DIR/.lea-release exists.
hold() {
  # shellcheck disable=SC2016 # expanded by the inner shell
  LEA_PROJECTS_DIR="$1" "$root/bin/lea" shell -- sh -c 'python3 -m http.server 8765 >/dev/null 2>&1 & p=$!
    while [ ! -e "$1" ]; do sleep 1; done; kill "$p"' sh "$1/.lea-release" >/dev/null 2>&1 &
  holders="$holders $!"
}
# shellcheck disable=SC2317 # invoked from the cleanup trap
release_holders() {
  : >"$projects/.lea-release"; : >"$sibling/.lea-release"
  for h in $holders; do
    i=0
    while kill -0 "$h" 2>/dev/null && [ "$i" -lt 60 ]; do i=$((i + 1)); sleep 1; done
  done
  holders=
}
# wait_for WHAT CMD...: retries CMD (printing a non-empty value) for up to 180 s.
wait_for() {
  what=$1; shift; i=0
  until v=$("$@" 2>/dev/null) && [ -n "$v" ] && [ "$v" != "<no value>" ]; do
    i=$((i + 1))
    if [ "$i" -ge 180 ]; then echo "isolation: $what did not appear" >&2; exit 1; fi
    sleep 1
  done
  printf '%s\n' "$v"
}

# shellcheck disable=SC2317 # invoked via trap
cleanup() {
  release_holders
  if [ -n "$facade_pid" ]; then kill "$facade_pid" 2>/dev/null || true; fi
  docker rm -f lea-canary >/dev/null 2>&1 || true
  docker network rm lea-canary-net >/dev/null 2>&1 || true
  "$root/bin/lea" stop --all >/dev/null 2>&1 || true
  docker rm -f "lea-test-$id" "lea-test-$sid" lea-test-proxy >/dev/null 2>&1 || true
  docker network rm "lea-test-internal-$id" "lea-test-internal-$sid" lea-test-egress >/dev/null 2>&1 || true
  docker volume rm "lea-test-podman-$id" "lea-test-podman-$sid" >/dev/null 2>&1 || true
  # The session created this per-run projects directory under the real ~/.claude.
  if [ -n "$slug" ]; then rm -r "${HOME:?}/.claude/projects/$slug" 2>/dev/null || true; fi
  sslug=$(printf '%s' "$sibling" | sed 's/[^A-Za-z0-9]/-/g')
  if [ -n "$sslug" ]; then rm -r "${HOME:?}/.claude/projects/$sslug" 2>/dev/null || true; fi
  rm -rf "${work:?}"
}
trap cleanup EXIT

docker network create lea-canary-net >/dev/null
docker run -d --name lea-canary --network lea-canary-net -p 18080:80 busybox:1.37 \
  sh -c 'mkdir -p /w && echo canary >/w/index.html && httpd -f -p 80 -h /w' >/dev/null
canary_ip=$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' lea-canary)
# Positive control: the decoy must answer from the host, otherwise the "unreachable" checks prove nothing.
if [ -z "$canary_ip" ]; then echo "isolation: the decoy has no IP address" >&2; exit 1; fi
i=0
until curl -sf --max-time 2 http://127.0.0.1:18080/ 2>/dev/null | grep -qx canary; do
  i=$((i + 1))
  if [ "$i" -ge 10 ]; then echo "isolation: the decoy does not answer on 127.0.0.1:18080 (positive control failed)" >&2; exit 1; fi
  sleep 1
done
# Local routing table lookup: no packet is sent.
host_ip=$(ip -4 route get 1.1.1.1 2>/dev/null | sed -n 's/.* src \([0-9.]*\).*/\1/p')
# Hashes only: host file contents are never passed into lea.
cred_sha=; hist_sha=
if [ -f "$HOME/.claude/.credentials.json" ]; then cred_sha=$(sha256sum <"$HOME/.claude/.credentials.json" | cut -d' ' -f1); fi
if [ -f "$HOME/.claude/history.jsonl" ]; then hist_sha=$(sha256sum <"$HOME/.claude/history.jsonl" | cut -d' ' -f1); fi
mkdir -p "$facade_dir"
python3 -I -c 'import socket, sys
s = socket.socket(socket.AF_UNIX); s.bind(sys.argv[1]); s.listen(8)
while True: s.accept()[0].close()' "$facade" &
facade_pid=$!
# Positive control: the stand-in facade socket answers on the host.
i=0
until python3 -I -c 'import socket, sys; socket.socket(socket.AF_UNIX).connect(sys.argv[1])' "$facade" 2>/dev/null; do
  i=$((i + 1))
  if [ "$i" -ge 10 ]; then echo "isolation: the stand-in facade socket does not answer (positive control failed)" >&2; exit 1; fi
  sleep 1
done
cp "$root/tests/integration/inside-isolation.sh" "$projects/"
# The internal networks have Docker-allocated subnets: hold both sessions open to read the
# gateway of this one and the address of the sibling.
hold "$projects"
hold "$sibling"
gw_ip=$(wait_for "the gateway of lea-test-internal-$id" \
  docker network inspect -f '{{range .IPAM.Config}}{{.Gateway}}{{end}}' "lea-test-internal-$id")
sibling_ip=$(wait_for "the address of lea-test-$sid" \
  docker container inspect -f "{{with index .NetworkSettings.Networks \"lea-test-internal-$sid\"}}{{.IPAddress}}{{end}}" "lea-test-$sid")
wait_for "lea-test-$id running" sh -c "docker container inspect -f '{{.State.Running}}' lea-test-$id | grep -x true" >/dev/null
# Positive control: the sibling's listener answers from inside the sibling.
i=0
until LEA_PROJECTS_DIR="$sibling" "$root/bin/lea" shell -- curl -s -o /dev/null --max-time 2 --noproxy '*' "http://$sibling_ip:8765/" >/dev/null 2>&1; do
  i=$((i + 1))
  if [ "$i" -ge 30 ]; then echo "isolation: the sibling's listener does not answer (positive control failed)" >&2; exit 1; fi
  sleep 1
done
rc=0
LEA_PROJECTS_DIR="$projects" "$root/bin/lea" shell -- env CANARY_IP="$canary_ip" GW_IP="$gw_ip" HOST_IP="$host_ip" \
  SIBLING_IP="$sibling_ip" HOST_FACADE="$facade" HOST_CRED_SHA="$cred_sha" HOST_HIST_SHA="$hist_sha" sh "$projects/inside-isolation.sh" || rc=$?
exit "$rc"
