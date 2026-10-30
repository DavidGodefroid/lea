#!/bin/sh
# lea-ctl e2e (#165): with LEA_CTL=1, lea runs in its control container and starts the agent
# container from there. Checks that lea-ctl alone has the Docker socket, that the agent container
# never sees it, and that lea-ctl sees lea's paths at their host paths. Needs the lea, lea-proxy
# and lea-ctl images (make build) and build/lea. Linux hosts. Creates and removes only
# lea-ctltest-* resources.
set -eu
root=$(cd "$(dirname "$0")/../.." && pwd -P)
fail() { echo "ctl e2e: $*" >&2; exit 1; }

work=$(mktemp -d) || exit 1
work=$(cd "$work" && pwd -P) || exit 1
projects="$work/projects"; mkdir -p "$projects"
export XDG_DATA_HOME="$work/data"
cat >"$work/lea.conf" <<'CONF'
LEA_PREFIX=lea-ctltest
LEA_IMAGE=lea:dev
LEA_PROXY_IMAGE=lea-proxy:dev
LEA_CTL=1
LEA_CTL_IMAGE=lea-ctl:dev
CONF
export LEA_CONFIG="$work/lea.conf" LEA_PROJECTS_DIR="$projects"
state="$XDG_DATA_HOME/lea/state"; mkdir -p "$state"; printf '{}\n' >"$state/claude.json"
id=$(printf '%s' "$projects" | sha256sum | cut -c1-10)
ctl=lea-ctltest-ctl agent=lea-ctltest-$id

# shellcheck disable=SC2317 # invoked via trap
cleanup() {
  docker rm -f "$agent" lea-ctltest-proxy "$ctl" >/dev/null 2>&1 || true
  docker network rm "lea-ctltest-internal-$id" lea-ctltest-egress >/dev/null 2>&1 || true
  docker volume rm "lea-ctltest-podman-$id" >/dev/null 2>&1 || true
  rm -rf "$work"
}
trap cleanup EXIT

# A command in the agent container, through the host's lea, which runs it in lea-ctl.
out=$("$root/bin/lea" shell -- sh -c 'for s in /var/run/docker.sock /run/docker.sock; do [ ! -e "$s" ] || echo "SOCK $s"; done; echo AGENT-RAN' </dev/null 2>&1) ||
  fail "lea shell failed: $out"
case $out in *AGENT-RAN*) ;; *) fail "the agent command did not run: $out" ;; esac
case $out in *"SOCK "*) fail "the agent container sees the Docker socket: $out" ;; esac

# The agent container was started by lea-ctl, and no mount of it is the socket.
[ "$(docker inspect -f '{{.State.Running}}' "$ctl")" = true ] || fail "$ctl is not running"
docker inspect -f '{{range .Mounts}}{{.Source}} {{end}}' "$agent" | grep -q 'docker.sock' &&
  fail "$agent mounts the Docker socket"
# lea-ctl has it, as the user (its group), and lea's paths at their host paths.
docker exec -u "$(id -u):$(id -g)" "$ctl" docker version --format '{{.Server.Version}}' >/dev/null ||
  fail "lea-ctl cannot reach the engine"
for p in "$root/build/lea" "$projects" "$XDG_DATA_HOME/lea/state/claude.json"; do
  docker exec "$ctl" test -e "$p" || fail "lea-ctl does not see $p"
done
# lea-ctl has a pid namespace of its own; it reads a lea container's cgroup by its ID (the
# pressure gauge) in the host's cgroup tree. The agent container stopped with the shell: the
# proxy's, still running, stands in for it.
[ "$(docker inspect -f '{{.HostConfig.PidMode}}' "$ctl")" != host ] || fail "$ctl shares the host's pid namespace"
probe=lea-ctltest-proxy
[ "$(docker inspect -f '{{.State.Running}}' "$probe")" = true ] || fail "$probe is not running"
cid=$(docker inspect -f '{{.Id}}' "$probe")
if ! docker exec "$ctl" sh -c 'd=$(find /sys/fs/cgroup -maxdepth 8 -type d \( -name "docker-$1.scope" -o -name "$1" \) | head -n1) &&
  [ -n "$d" ] && [ "$(cat "$d/memory.current")" -gt 0 ]' sh "$cid"; then
  echo "host cgroup of $probe: $(cat "/proc/$(docker inspect -f '{{.State.Pid}}' "$probe")/cgroup" 2>&1)" >&2
  docker exec "$ctl" sh -c 'cat /proc/self/cgroup; find /sys/fs/cgroup -maxdepth 3 -type d | head -n 60' >&2 || true
  fail "lea-ctl does not see the cgroup of $probe"
fi
# Every container but lea-ctl: no Docker socket mounted.
for c in $(docker ps -q --filter label=lea.prefix=lea-ctltest); do
  name=$(docker inspect -f '{{.Name}}' "$c"); name=${name#/}
  [ "$name" = "$ctl" ] && continue
  docker inspect -f '{{range .Mounts}}{{.Source}} {{end}}' "$c" | grep -q 'docker.sock' && fail "$name mounts the Docker socket"
done
echo "ctl e2e: ok"
