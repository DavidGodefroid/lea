#!/bin/sh
# lea-ctl e2e (#165): with LEA_CTL=1, lea runs in its control container and starts the agent
# container from there. Checks that lea-ctl alone has the Docker socket, that the agent container
# never sees it, and that lea-ctl sees lea's paths at their host paths. Then a full lea run there:
# the daemon, lea's tmux and the deck run in lea-ctl, `docker exec -it lea-ctl tmux attach`
# reaches them, an agent spawned by that daemon runs in the agent container, its call for the
# user rings the bell of the attached terminal, and a Lock console (a stand-in image of locksql's
# console: lea's side of it) starts in its standalone mode from lea-ctl, in the deck. Needs the
# lea, lea-proxy and lea-ctl images (make build), build/lea and script(1). Linux hosts. Creates and
# removes only lea-ctltest-* resources; the Lock console part also uses locksql's fixed names
# (locksql-console, the locksql-run and locksql-home volumes), so it runs only on CI (CI=true) or
# with LEA_CTL_E2E_LOCK=1, and only while none of them exists.
set -eu
root=$(cd "$(dirname "$0")/../.." && pwd -P)
fail() { echo "ctl e2e: $*" >&2; exit 1; }

work=$(mktemp -d) || exit 1
work=$(cd "$work" && pwd -P) || exit 1
projects="$work/projects"; mkdir -p "$projects"
export XDG_DATA_HOME="$work/data"
lockstub=lea-ctltest-lockstub:dev
cat >"$work/lea.conf" <<CONF
LEA_PREFIX=lea-ctltest
LEA_IMAGE=lea:dev
LEA_PROXY_IMAGE=lea-proxy:dev
LEA_CTL=1
LEA_CTL_IMAGE=lea-ctl:dev
LEA_LOCKSQL_IMAGE=$lockstub
LEA_AUTONAME=0
CONF
export LEA_CONFIG="$work/lea.conf" LEA_PROJECTS_DIR="$projects"
state="$XDG_DATA_HOME/lea/state"; mkdir -p "$state"; printf '{}\n' >"$state/claude.json"
id=$(printf '%s' "$projects" | sha256sum | cut -c1-10)
ctl=lea-ctltest-ctl agent=lea-ctltest-$id

# shellcheck disable=SC2317 # invoked via trap
cleanup() {
  [ -z "${attach:-}" ] || kill "$attach" 2>/dev/null || true
  if [ "${lockrun:-0}" = 1 ]; then
    docker rm -f locksql-console >/dev/null 2>&1 || true
    docker volume rm locksql-run locksql-home >/dev/null 2>&1 || true
    docker image rm "$lockstub" >/dev/null 2>&1 || true
  fi
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
# Of ~/.claude, lea-ctl mounts the projects dir's transcripts only, never all of ~/.claude or of
# its projects.
slug=$(printf '%s' "$projects" | sed 's/[^A-Za-z0-9]/-/g')
mounts=$(docker inspect -f '{{range .Mounts}}{{.Destination}} {{end}}' "$ctl")
for m in $mounts; do
  case $m in "$HOME/.claude" | "$HOME/.claude/projects") fail "lea-ctl mounts $m" ;; esac
done
case " $mounts " in *" $HOME/.claude/projects/$slug "*) ;; *) fail "lea-ctl lacks $HOME/.claude/projects/$slug: $mounts" ;; esac
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

# A full lea run in lea-ctl. lea new starts the agent container again, lea's daemon, its tmux
# server and the deck there, and an agent (Claude Code, not logged in: it waits at its first
# screen, which is all this needs).
out=$("$root/bin/lea" new e2e-dev </dev/null 2>&1) || fail "lea new failed: $out"
u="$(id -u):$(id -g)"
docker exec "$ctl" pgrep -u "$(id -u)" -f 'lea daemon' >/dev/null || fail "no lea daemon runs in $ctl: $(docker exec "$ctl" ps -ef 2>&1)"
# Its API answers the host's lea through the shared data directory.
agents=$("$root/bin/lea" agents 2>&1) || fail "lea agents failed: $agents"
case $agents in *e2e-dev*) ;; *) fail "lea agents does not list e2e-dev: $agents" ;; esac
# lea's tmux runs in lea-ctl, with the deck and the agent's pane.
panes=$(docker exec -u "$u" "$ctl" tmux -L lea list-panes -a -F '#{pane_start_command}' 2>&1) || fail "no lea tmux in $ctl: $panes"
case $panes in *"deck --container $agent"*) ;; *) fail "no deck pane in $ctl: $panes" ;; esac
# The agent runs in the agent container, not in lea-ctl.
for i in $(seq 60); do
  docker top "$agent" -eo pid,args 2>/dev/null | grep -q '[c]laude' && break
  [ "$i" -lt 60 ] || fail "the agent does not run in $agent: $(docker top "$agent" -eo pid,args 2>&1)"
  sleep 1
done
docker exec "$ctl" pgrep -f '^claude' >/dev/null && fail "Claude Code runs in $ctl"

# docker exec -it lea-ctl tmux attach, in a real terminal: the deck shows, and a call of the agent
# for the user rings that terminal's bell (lea-ctl has no sound player).
command -v script >/dev/null || fail "script(1) is missing"
log="$work/attach.log"
script -qfec "stty cols 160 rows 48; docker exec -it -u $u $ctl tmux -L lea attach" "$log" </dev/null >/dev/null 2>&1 &
attach=$!
for i in $(seq 30); do
  docker exec -u "$u" "$ctl" tmux -L lea list-clients 2>/dev/null | grep -q . && break
  [ "$i" -lt 30 ] || fail "tmux attach did not reach lea's tmux in $ctl: $(cat "$log")"
  sleep 1
done
sleep 2
grep -q 'e2e-dev' "$log" || fail "the attached terminal does not show the deck: $(tr -cd '[:print:]\n' <"$log" | tail -c 2000)"
before=$(tr -cd '\007' <"$log" | wc -c)
# The agent's PermissionRequest hook, as Claude Code runs it in the agent's pane.
hook=$(docker exec -u "$u" -e LEA_AGENT=e2e-dev "$agent" sh -c 'printf "{\"hook_event_name\":\"PermissionRequest\",\"session_id\":\"e2e\",\"tool_name\":\"Bash\"}" | timeout 5 lea hook claude PermissionRequest; echo "rc=$?"' 2>&1) || true
for i in $(seq 20); do
  [ "$(tr -cd '\007' <"$log" | wc -c)" -gt "$before" ] && break
  if [ "$i" -ge 20 ]; then
    echo "hook: $hook" >&2
    "$root/bin/lea" agents >&2 2>&1 || true
    docker exec -u "$u" "$ctl" tmux -L lea list-clients -F '#{client_control} #{client_tty} #{session_name}' >&2 || true
    tail -n 40 "$XDG_DATA_HOME/lea/host/daemon.log" >&2 2>/dev/null || true
    fail "no bell in the attached terminal"
  fi
  sleep 1
done

# A Lock console, standalone (its own TTY, no hub), opened in the deck by lea in lea-ctl.
lockrun=0
if { [ "${CI:-}" = true ] || [ "${LEA_CTL_E2E_LOCK:-0}" = 1 ]; } &&
  ! docker container inspect locksql-console >/dev/null 2>&1 &&
  ! docker volume inspect locksql-run >/dev/null 2>&1 && ! docker volume inspect locksql-home >/dev/null 2>&1; then
  lockrun=1
  # A stand-in of locksql's console image: its account, a console that waits at its terminal.
  printf 'FROM lea-ctl:dev\nRUN useradd -u 10001 -U locksql && mkdir -p /run/locksql /home/locksql && chown locksql /run/locksql /home/locksql\nENTRYPOINT ["sh", "-c", "echo LOCK-CONSOLE-UP; exec sleep 600", "console"]\n' |
    docker build -q -t "$lockstub" - >/dev/null || fail "could not build the stand-in console"
  out=$("$root/bin/lea" lock open locksql </dev/null 2>&1) || fail "lea lock open failed: $out"
  for i in $(seq 30); do
    [ "$(docker inspect -f '{{.State.Running}}' locksql-console 2>/dev/null)" = true ] && break
    [ "$i" -lt 30 ] || fail "the console did not start: $out"
    sleep 1
  done
  [ "$(docker inspect -f '{{index .Config.Labels "lea.lock-for"}}' locksql-console)" = "$agent" ] || fail "the console is not the deck's of $agent"
  mounts=$(docker inspect -f '{{range .Mounts}}{{.Name}}{{.Source}}->{{.Destination}} {{end}}' locksql-console)
  case $mounts in *"locksql-run"*"->/run/locksql "*) ;; *) fail "the console lacks its socket volume: $mounts" ;; esac
  case $mounts in *docker.sock*) fail "the console mounts the Docker socket: $mounts" ;; esac
  docker exec locksql-console sh -c '[ ! -e /var/run/docker.sock ] && [ ! -e /run/docker.sock ]' || fail "the console sees the Docker socket"
  panes=$(docker exec -u "$u" "$ctl" tmux -L lea list-panes -a -F '#{pane_start_command}')
  case $panes in *"--name locksql-console"*) ;; *) fail "no console pane in $ctl: $panes" ;; esac
  # The agents get its socket volume, never anything of its home.
  docker inspect -f '{{range .Mounts}}{{.Name}} {{end}}' "$agent" | grep -q 'locksql-home' && fail "$agent mounts locksql-home"
  "$root/bin/lea" lock close locksql </dev/null >/dev/null 2>&1 || fail "lea lock close failed"
else
  echo "ctl e2e: the Lock console part is skipped (not on CI, or locksql's console names are in use)" >&2
fi

# Every container but lea-ctl, now with the agent running: no Docker socket mounted, none seen.
for c in $(docker ps -q --filter label=lea.prefix=lea-ctltest); do
  name=$(docker inspect -f '{{.Name}}' "$c"); name=${name#/}
  [ "$name" = "$ctl" ] && continue
  docker inspect -f '{{range .Mounts}}{{.Source}} {{end}}' "$c" | grep -q 'docker.sock' && fail "$name mounts the Docker socket"
done
docker exec "$agent" sh -c '[ ! -e /var/run/docker.sock ] && [ ! -e /run/docker.sock ]' || fail "$agent sees the Docker socket"

# lea stop, through lea-ctl: the agent container goes, its agent is recorded stopped.
kill "$attach" 2>/dev/null || true; attach=
"$root/bin/lea" stop </dev/null >/dev/null 2>&1 || fail "lea stop failed"
for i in $(seq 30); do
  [ "$(docker inspect -f '{{.State.Running}}' "$agent" 2>/dev/null)" = true ] || break
  [ "$i" -lt 30 ] || fail "$agent still runs after lea stop"
  sleep 1
done
agents=$("$root/bin/lea" agents 2>&1) || true
case $agents in *e2e-dev*stopped*) ;; *) fail "e2e-dev is not stopped: $agents" ;; esac
echo "ctl e2e: ok"
