load helpers
# image/lea-agent: deck agents inside lea, with a fake lea-session.
setup() {
  setup_fakes
  A="$LEA_ROOT/image/lea-agent"
  export LEA_AGENT_RUN_DIR="$BATS_TEST_TMPDIR/agents"
  F="$BATS_TEST_TMPDIR/fbin"; mkdir -p "$F"
  cat >"$F/lea-session" <<'EOT'
#!/bin/sh
{ printf 'lea-session pid=%s agent=%s' "$$" "$LEA_AGENT"; for a in "$@"; do printf ' [%s]' "$a"; done; printf '\n'; } >>"$FAKE_LOG"
printf 'DOCKER_HOST=%s\nCONTAINERS_CONF_OVERRIDE=%s\nHTTPS_PROXY=%s\nhttp_proxy=%s\nLEA_PROXY_TOKEN=%s\ncpus=%s\n' "${DOCKER_HOST:-}" "${CONTAINERS_CONF_OVERRIDE:-}" \
  "${HTTPS_PROXY:-}" "${http_proxy:-}" "${LEA_PROXY_TOKEN-unset}" "$(awk '/^Cpus_allowed_list/ { print $2 }' /proc/self/status)" >"$(dirname "$FAKE_LOG")/session.env"
EOT
  chmod +x "$F/lea-session"
  export PATH="$F:$PATH"
  # No Podman unless a test brings the fake one (fake_podman).
  export LEA_AGENT_PODMAN="$F/no-podman" XDG_RUNTIME_DIR="$BATS_TEST_TMPDIR/xdg"
  unset DOCKER_HOST CONTAINERS_CONF_OVERRIDE
  PIDS=
}
teardown() {
  # shellcheck disable=SC2086
  [ -z "$PIDS" ] || kill -9 $PIDS 2>/dev/null || true
}
# wait_env PID NAME: waits until PID has exec'd with LEA_AGENT=NAME (right after the fork, the
# child still has the shell's environment).
wait_env() {
  i=0
  until tr '\0' '\n' <"/proc/$1/environ" 2>/dev/null | grep -qx "LEA_AGENT=$2"; do
    i=$((i + 1)); [ "$i" -lt 50 ] || return 1
    sleep 0.05
  done
}
# agent NAME [ENV_NAME]: a background process whose environment says LEA_AGENT=ENV_NAME.
agent() {
  env LEA_AGENT="${2:-$1}" sleep 60 &
  _ap=$!; PIDS="$PIDS $_ap"
  wait_env "$_ap" "${2:-$1}"
  mkdir -p "$LEA_AGENT_RUN_DIR"; echo "$_ap" >"$LEA_AGENT_RUN_DIR/$1.pid"
}

# fake_podman: a fake Podman in $F/podman. Its calls go to $PODMAN_LOG; its containers are the
# lines "id|pod|agent|infra" of $PODMAN_DB; system service listens on its socket, as the real one.
fake_podman() {
  export PODMAN_LOG="$BATS_TEST_TMPDIR/podman.log" PODMAN_DB="$BATS_TEST_TMPDIR/podman.db" LEA_AGENT_PODMAN="$F/podman"
  : >"$PODMAN_LOG"; : >"$PODMAN_DB"
  cat >"$F/podman" <<'EOT'
#!/bin/sh
{ printf 'podman'; for a in "$@"; do printf ' [%s]' "$a"; done; printf '\n'; } >>"$PODMAN_LOG"
case "$1 $2" in
  "system service")
    [ -z "${CONTAINERS_CONF_OVERRIDE:-}" ] || sed 's/^/conf: /' "$CONTAINERS_CONF_OVERRIDE" >>"$PODMAN_LOG"
    exec python3 -c 'import socket, sys, time
s = socket.socket(socket.AF_UNIX); s.bind(sys.argv[1][len("unix://"):]); s.listen(); time.sleep(600)' "$4" ;;
  "ps -aq") cut -d'|' -f1 "$PODMAN_DB" ;;
  "ps -q") cat "$PODMAN_RUNNING" 2>/dev/null || true ;;
  "container inspect")
    case $4 in
      *State.Pid*) shift 4; for i in "$@"; do awk -v i="$i" '$1 == i { print $2 }' "$PODMAN_PIDS" 2>/dev/null; done ;;
      *) cat "$PODMAN_DB" ;;
    esac ;;
esac
EOT
  chmod +x "$F/podman"
}
# service PID: whether PID is a running Podman service.
service() { tr '\0' ' ' <"/proc/$1/cmdline" 2>/dev/null | grep -q 'unix://'; }

@test "run starts the agent's own Podman service, whose containers carry lea.agent=NAME (#97)" {
  fake_podman
  run "$A" run dev-1 -- x
  [ "$status" -eq 0 ]
  sock="$XDG_RUNTIME_DIR/podman/agent-dev-1.sock"
  [ -S "$sock" ]
  svc=$(cat "$LEA_AGENT_RUN_DIR/dev-1.podman.pid"); PIDS="$PIDS $svc"
  service "$svc"
  grep -qxF "podman [system] [service] [--time=0] [unix://$sock]" "$PODMAN_LOG"
  grep -qxF 'conf: annotations = ["lea.agent=dev-1"]' "$PODMAN_LOG"
  # docker and docker compose reach that service; podman itself reads the same annotation.
  grep -qxF "DOCKER_HOST=unix://$sock" "$BATS_TEST_TMPDIR/session.env"
  grep -qxF "CONTAINERS_CONF_OVERRIDE=$LEA_AGENT_RUN_DIR/dev-1.containers.conf" "$BATS_TEST_TMPDIR/session.env"
  # The service is not the agent's child: pausing the agent never freezes it.
  ! grep -qw "$svc" /proc/"$(cat "$LEA_AGENT_RUN_DIR/dev-1.pid")"/task/*/children 2>/dev/null
}

# Each agent gets a window of LEA_AGENT_CPUS CPUs, the least shared among the live agents' (#132):
# it and all it starts run there, so tools that size themselves from the CPUs they may use see it.
@test "run gives the agent a CPU window, the CPUs fewest live agents share" {
  export LEA_AGENT_CPU_LIST=0-7 LEA_AGENT_CPUS=2
  agent dev-1
  echo 0,1 >"$LEA_AGENT_RUN_DIR/dev-1.cpus"
  # A window left by an agent that is gone counts for nothing.
  echo 2,3 >"$LEA_AGENT_RUN_DIR/dev-9.cpus"
  run "$A" run dev-2 -- x
  [ "$status" -eq 0 ]
  [ "$(cat "$LEA_AGENT_RUN_DIR/dev-2.cpus")" = 2,3 ]
  agent dev-2 dev-2
  run "$A" run dev-3 -- x
  [ "$(cat "$LEA_AGENT_RUN_DIR/dev-3.cpus")" = 4,5 ]
  # LEA_AGENT_CPUS=0, or a window as large as the CPUs: no window.
  LEA_AGENT_CPUS=0 run "$A" run dev-4 -- x
  [ ! -e "$LEA_AGENT_RUN_DIR/dev-4.cpus" ]
  LEA_AGENT_CPUS=8 run "$A" run dev-5 -- x
  [ ! -e "$LEA_AGENT_RUN_DIR/dev-5.cpus" ]
}

@test "windows lists the live agents' CPU windows" {
  agent dev-1
  echo 0-3 >"$LEA_AGENT_RUN_DIR/dev-1.cpus"
  echo 4-7 >"$LEA_AGENT_RUN_DIR/dev-9.cpus"
  run "$A" windows
  [ "$status" -eq 0 ]
  [ "$output" = "dev-1 0-3" ]
}

@test "run pins the agent to its window" {
  # Pretend there are two CPUs; only CPU 0 surely exists where the test runs.
  export LEA_AGENT_CPU_LIST=0-1 LEA_AGENT_CPUS=1
  run "$A" run dev-1 -- x
  [ "$status" -eq 0 ]
  [ "$(cat "$LEA_AGENT_RUN_DIR/dev-1.cpus")" = 0 ]
  grep -qx 'cpus=0' "$BATS_TEST_TMPDIR/session.env"
}

@test "run under LockHTTP gives the agent its own proxy password, and drops it from the environment" {
  LEA_LOCKHTTP=1 LEA_PROXY_IP=10.89.0.2 LEA_PROXY_TOKEN=0123abcd HTTPS_PROXY=http://shell:x@10.89.0.2:3130 run "$A" run dev-1 -- x
  [ "$status" -eq 0 ]
  grep -qx 'HTTPS_PROXY=http://dev-1:0123abcd@10.89.0.2:3130' "$BATS_TEST_TMPDIR/session.env"
  grep -qx 'http_proxy=http://dev-1:0123abcd@10.89.0.2:3130' "$BATS_TEST_TMPDIR/session.env"
  grep -qx 'LEA_PROXY_TOKEN=unset' "$BATS_TEST_TMPDIR/session.env"
}

@test "run under LockHTTP without a token keeps the container's proxy; a bad token is refused" {
  LEA_LOCKHTTP=1 LEA_PROXY_IP=10.89.0.2 HTTPS_PROXY=http://shell:x@10.89.0.2:3130 run "$A" run dev-1 -- x
  [ "$status" -eq 0 ]
  grep -qx 'HTTPS_PROXY=http://shell:x@10.89.0.2:3130' "$BATS_TEST_TMPDIR/session.env"
  LEA_LOCKHTTP=1 LEA_PROXY_IP=10.89.0.2 LEA_PROXY_TOKEN='x@evil' run "$A" run dev-2 -- x
  [ "$status" -eq 2 ]
  [[ "$output" == *"invalid LEA_PROXY_TOKEN"* ]]
}

@test "run without Podman keeps the shared Podman service" {
  run "$A" run dev-1 -- x
  [ "$status" -eq 0 ]
  grep -qx 'DOCKER_HOST=' "$BATS_TEST_TMPDIR/session.env"
  [ ! -e "$LEA_AGENT_RUN_DIR/dev-1.podman.pid" ]
}

@test "teardown stops the agent's service and removes the pods and containers it started, only those" {
  fake_podman
  run "$A" run dev-1 -- x
  svc=$(cat "$LEA_AGENT_RUN_DIR/dev-1.podman.pid"); PIDS="$PIDS $svc"
  printf '%s\n' 'c1|p1|dev-1' 'c2|p1|dev-1' 'c3||dev-1' 'c4|p2|dev-2' 'c5||' >"$PODMAN_DB"
  : >"$PODMAN_LOG"
  run "$A" teardown dev-1
  [ "$status" -eq 0 ]
  [ "$output" = "dev-1: removed 3 containers and 1 pod" ]
  grep -qxF 'podman [pod] [rm] [-f] [p1]' "$PODMAN_LOG"
  grep -qxF 'podman [rm] [-f] [-v] [c1] [c2] [c3]' "$PODMAN_LOG"
  i=0; while kill -0 "$svc" 2>/dev/null && [ "$i" -lt 20 ]; do sleep 0.1; i=$((i + 1)); done
  ! kill -0 "$svc" 2>/dev/null || false
  [ ! -e "$XDG_RUNTIME_DIR/podman/agent-dev-1.sock" ]
  [ ! -e "$LEA_AGENT_RUN_DIR/dev-1.podman.pid" ]
  # Nothing of its own: nothing removed, nothing said.
  : >"$PODMAN_LOG"
  run "$A" teardown dev-3
  [ "$status" -eq 0 ]
  [ -z "$output" ]
  ! grep -q '\[rm\]' "$PODMAN_LOG"
}

@test "teardown keeps a pod that holds containers of others, and removes only the agent's own" {
  fake_podman
  # p1: made by a shell (its infra has no lea.agent), dev-1 ran c1 in it; p2: dev-1's own pod,
  # with its infra container; p3: dev-1's pod that dev-2 joined.
  printf '%s\n' 'i1|p1||true' 'c1|p1|dev-1|false' 'i2|p2|dev-1|true' 'c2|p2|dev-1|false' \
    'i3|p3|dev-1|true' 'c3|p3|dev-1|false' 'c4|p3|dev-2|false' >"$PODMAN_DB"
  run "$A" teardown dev-1
  [ "$status" -eq 0 ]
  [ "$output" = "dev-1: removed 3 containers and 1 pod" ]
  grep -qxF 'podman [pod] [rm] [-f] [p2]' "$PODMAN_LOG"
  grep -qxF 'podman [rm] [-f] [-v] [c1] [c2] [c3]' "$PODMAN_LOG"
  [ "$(grep -c '\[pod\] \[rm\]' "$PODMAN_LOG")" -eq 1 ]
}

@test "sweep removes a pod only when all its containers are of ended agents" {
  fake_podman
  printf '%s\n' 'i1|p1||true' 'c1|p1|dev-2|false' 'i2|p2|dev-2|true' 'c2|p2|dev-3|false' \
    'i3|p3|dev-2|true' 'c3|p3|dev-1|false' >"$PODMAN_DB"
  run "$A" sweep dev-1
  [ "$status" -eq 0 ]
  grep -qxF 'podman [pod] [rm] [-f] [p2]' "$PODMAN_LOG"
  [ "$(grep -c '\[pod\] \[rm\]' "$PODMAN_LOG")" -eq 1 ]
  grep -qxF 'podman [rm] [-f] [-v] [c1] [c2]' "$PODMAN_LOG"
  [[ "$output" == *"removed 2 containers and 1 pod of ended agents (dev-2 dev-3)"* ]]
}

@test "teardown spares a reused pid in the service's pid file" {
  fake_podman
  sleep 60 & p=$!; PIDS="$PIDS $p"
  mkdir -p "$LEA_AGENT_RUN_DIR"; echo "$p" >"$LEA_AGENT_RUN_DIR/dev-1.podman.pid"
  run "$A" teardown dev-1
  [ "$status" -eq 0 ]
  kill -0 "$p"
}

@test "sweep removes the stacks of agents not kept and reports the containers no agent started" {
  fake_podman
  printf '%s\n' 'c1|p1|dev-1' 'c2||dev-2' 'c3|p3|dev-3' 'c4|p9|' 'c5||' >"$PODMAN_DB"
  run "$A" sweep dev-1
  [ "$status" -eq 0 ]
  grep -qxF 'podman [pod] [rm] [-f] [p3]' "$PODMAN_LOG"
  grep -qxF 'podman [rm] [-f] [-v] [c2] [c3]' "$PODMAN_LOG"
  [[ "$output" == *"removed 2 containers and 1 pod of ended agents (dev-2 dev-3)"* ]]
  [[ "$output" == *"2 containers that no agent started"* ]]
  # Nothing to remove, nothing unowned: silent.
  printf '%s\n' 'c1|p1|dev-1' >"$PODMAN_DB"; : >"$PODMAN_LOG"
  run "$A" sweep dev-1
  [ "$status" -eq 0 ]
  [ -z "$output" ]
  ! grep -q '\[rm\]' "$PODMAN_LOG"
}

# Idle agents' stacks are stopped, not removed (#132): the agent starts them again when it needs
# them; a stopped container holds no memory and no CPU.
@test "park stops the running containers the agent started and its own pods, and says what it freed" {
  fake_podman
  export PODMAN_RUNNING="$BATS_TEST_TMPDIR/running" PODMAN_PIDS="$BATS_TEST_TMPDIR/pids"
  # p1: dev-1's pod (infra i1, c1 c2); c3 dev-1 alone, stopped already; p2: a shell's pod where
  # dev-1 ran c4; c5 is dev-2's.
  printf '%s\n' 'i1|p1|dev-1|true' 'c1|p1|dev-1|false' 'c2|p1|dev-1|false' 'c3||dev-1|false' \
    'i2|p2||true' 'c4|p2|dev-1|false' 'c5||dev-2|false' >"$PODMAN_DB"
  printf '%s\n' i1 c1 c2 i2 c4 c5 >"$PODMAN_RUNNING"
  sleep 60 & p1=$!; sleep 60 & p2=$!; PIDS="$PIDS $p1 $p2"
  printf 'c1 %s\nc2 %s\nc4 0\n' "$p1" "$p2" >"$PODMAN_PIDS"
  export LEA_AGENT_PARK_SAMPLE=0.3
  run "$A" park dev-1
  [ "$status" -eq 0 ]
  [[ "$output" =~ ^dev-1:\ stopped\ 3\ containers\ and\ 1\ pod\ \(freed\ [0-9]+\ MiB\)$ ]]
  grep -qxF 'podman [pod] [stop] [-t] [10] [p1]' "$PODMAN_LOG"
  grep -qxF 'podman [stop] [-t] [10] [c1] [c2] [c4]' "$PODMAN_LOG"
  ! grep -q '\[rm\]' "$PODMAN_LOG" || false
  # unpark starts again what park stopped, once.
  : >"$PODMAN_LOG"
  run "$A" unpark dev-1
  [ "$status" -eq 0 ]
  [ "$output" = "dev-1: started 3 containers and 1 pod again" ]
  grep -qxF 'podman [pod] [start] [p1]' "$PODMAN_LOG"
  grep -qxF 'podman [start] [c1] [c2] [c4]' "$PODMAN_LOG"
  run "$A" unpark dev-1
  [ "$status" -eq 0 ]
  [ -z "$output" ]
  # Nothing of its own runs: nothing stopped, nothing said.
  printf '%s\n' c5 >"$PODMAN_RUNNING"; : >"$PODMAN_LOG"
  run "$A" park dev-1
  [ "$status" -eq 0 ]
  [ -z "$output" ]
  ! grep -q '\[stop\]' "$PODMAN_LOG"
}

@test "park leaves a stack that is still at work running (a background job of the agent)" {
  fake_podman
  export PODMAN_RUNNING="$BATS_TEST_TMPDIR/running" PODMAN_PIDS="$BATS_TEST_TMPDIR/pids" LEA_AGENT_PARK_SAMPLE=0.5
  printf '%s\n' 'c1||dev-1|false' >"$PODMAN_DB"
  printf '%s\n' c1 >"$PODMAN_RUNNING"
  sh -c 'while :; do :; done' & p1=$!; PIDS="$PIDS $p1"
  printf 'c1 %s\n' "$p1" >"$PODMAN_PIDS"
  run "$A" park dev-1
  [ "$status" -eq 3 ]
  [[ "$output" == *"dev-1: stack busy ("*"% of a CPU), not stopped"* ]]
  ! grep -q '\[stop\]' "$PODMAN_LOG" || false
  [ ! -e "$LEA_AGENT_RUN_DIR/dev-1.parked" ]
}

@test "park without Podman says nothing" {
  run "$A" park dev-1
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "teardown and sweep check the names" {
  fake_podman
  for n in "" Dev-1 "a b" "../x"; do
    run "$A" teardown "$n"
    [ "$status" -eq 2 ]
    run "$A" park "$n"
    [ "$status" -eq 2 ]
    run "$A" unpark "$n"
    [ "$status" -eq 2 ]
    run "$A" sweep dev-1 "$n"
    [ "$status" -eq 2 ]
  done
  [ ! -s "$PODMAN_LOG" ]
}

@test "run records its own pid, sets LEA_AGENT and becomes lea-session --claude" {
  run "$A" run claude-1 -- --resume "two words"
  [ "$status" -eq 0 ]
  pid=$(cat "$LEA_AGENT_RUN_DIR/claude-1.pid")
  [ "$(cat "$FAKE_LOG")" = "lea-session pid=$pid agent=claude-1 [--claude] [--resume] [two words]" ]
}

@test "run without arguments starts Claude bare" {
  run "$A" run claude-1
  [ "$status" -eq 0 ]
  [[ "$(cat "$FAKE_LOG")" == *"agent=claude-1 [--claude]" ]]
}

@test "run --cli codex becomes lea-session --codex" {
  run "$A" run dev-1 --cli codex -- resume 0e5f7c2a-1b2c-4d5e-8f90-1234567890ab
  [ "$status" -eq 0 ]
  [[ "$(cat "$FAKE_LOG")" == *"agent=dev-1 [--codex] [resume] [0e5f7c2a-1b2c-4d5e-8f90-1234567890ab]" ]]
}

@test "run refuses an unknown CLI" {
  for c in "" bash "--shell" "claude;sh"; do
    run "$A" run dev-1 --cli "$c" -- x
    [ "$status" -eq 2 ]
  done
  [ ! -s "$FAKE_LOG" ]
}

@test "invalid names are refused" {
  for n in "" Claude-1 1abc "a b" "a/b" "../x" abcdefghijklmnopqrstuvwxy; do
    run "$A" run "$n"
    [ "$status" -eq 2 ]
    run "$A" stop "$n"
    [ "$status" -eq 2 ]
  done
  [ ! -s "$FAKE_LOG" ]
}

@test "ls prints live agents only and removes stale pid files" {
  agent claude-1
  agent claude-2 someone-else      # a reused pid: its process is not that agent
  mkdir -p "$LEA_AGENT_RUN_DIR"; echo 999999 >"$LEA_AGENT_RUN_DIR/claude-3.pid"   # ended
  run "$A" ls
  [ "$status" -eq 0 ]
  [ "$output" = claude-1 ]
  [ -f "$LEA_AGENT_RUN_DIR/claude-1.pid" ]
  [ ! -e "$LEA_AGENT_RUN_DIR/claude-2.pid" ]
  [ ! -e "$LEA_AGENT_RUN_DIR/claude-3.pid" ]
}

@test "ls with no run directory prints nothing" {
  run "$A" ls
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "stop ends the agent and removes its pid file" {
  agent claude-1
  pid=$(cat "$LEA_AGENT_RUN_DIR/claude-1.pid")
  run "$A" stop claude-1
  [ "$status" -eq 0 ]
  i=0; while kill -0 "$pid" 2>/dev/null && [ "$i" -lt 20 ]; do sleep 0.1; i=$((i + 1)); done
  ! kill -0 "$pid" 2>/dev/null || false
  [ ! -e "$LEA_AGENT_RUN_DIR/claude-1.pid" ]
}

@test "stop kills an agent that ignores SIGTERM after the wait" {
  env LEA_AGENT=claude-1 sh -c 'trap "" TERM; while :; do sleep 1; done' &
  pid=$!; PIDS="$PIDS $pid"
  wait_env "$pid" claude-1
  mkdir -p "$LEA_AGENT_RUN_DIR"; echo "$pid" >"$LEA_AGENT_RUN_DIR/claude-1.pid"
  LEA_AGENT_STOP_WAIT=1 run "$A" stop claude-1
  [ "$status" -eq 0 ]
  i=0; while kill -0 "$pid" 2>/dev/null && [ "$i" -lt 20 ]; do sleep 0.1; i=$((i + 1)); done
  ! kill -0 "$pid" 2>/dev/null
}

@test "stop of an agent that is not running succeeds and spares a reused pid" {
  agent claude-1 someone-else
  pid=$(cat "$LEA_AGENT_RUN_DIR/claude-1.pid")
  run "$A" stop claude-1
  [ "$status" -eq 0 ]
  kill -0 "$pid"
  run "$A" stop claude-9
  [ "$status" -eq 0 ]
}

# pstate PID: the process state letter (T when stopped).
pstate() { sed 's/.*) //' "/proc/$1/stat" | cut -d' ' -f1; }
# kid PID: the first child of PID.
kid() {
  i=0
  until c=$(cat /proc/"$1"/task/*/children 2>/dev/null | tr ' ' '\n' | grep -m1 .); do
    i=$((i + 1)); [ "$i" -lt 50 ] || return 1
    sleep 0.05
  done
  printf '%s\n' "$c"
}
# family NAME: an agent with a child, as Claude with a tool call in flight.
family() {
  env LEA_AGENT="$1" sh -c 'sleep 60 & wait' &
  _fp=$!; PIDS="$PIDS $_fp"
  wait_env "$_fp" "$1"
  _fk=$(kid "$_fp"); PIDS="$PIDS $_fk"
  mkdir -p "$LEA_AGENT_RUN_DIR"; echo "$_fp" >"$LEA_AGENT_RUN_DIR/$1.pid"
}

@test "pause freezes the agent and its children; resume lets them continue" {
  family claude-1
  run "$A" pause claude-1
  [ "$status" -eq 0 ]
  [ "$(pstate "$_fp")" = T ]
  [ "$(pstate "$_fk")" = T ]
  run "$A" ls
  [ "$output" = claude-1 ]   # a paused agent still runs
  run "$A" resume claude-1
  [ "$status" -eq 0 ]
  [ "$(pstate "$_fp")" != T ]
  [ "$(pstate "$_fk")" != T ]
}

@test "pause also freezes the running containers the agent started, only those; resume thaws them (#132)" {
  fake_podman
  export PODMAN_RUNNING="$BATS_TEST_TMPDIR/running" PODMAN_PIDS="$BATS_TEST_TMPDIR/pids"
  family claude-1
  printf '%s\n' 'c1||claude-1|false' 'c2||claude-1|false' 'c5||dev-2|false' >"$PODMAN_DB"
  printf '%s\n' c1 c5 >"$PODMAN_RUNNING"
  sh -c 'sleep 60 & wait' & p1=$!; sleep 60 & p5=$!; PIDS="$PIDS $p1 $p5"
  k1=$(kid "$p1"); PIDS="$PIDS $k1"
  printf 'c1 %s\nc5 %s\n' "$p1" "$p5" >"$PODMAN_PIDS"
  run "$A" pause claude-1
  [ "$status" -eq 0 ]
  [ "$(pstate "$_fp")" = T ]
  [ "$(pstate "$p1")" = T ]
  [ "$(pstate "$k1")" = T ]
  [ "$(pstate "$p5")" != T ]
  run "$A" resume claude-1
  [ "$status" -eq 0 ]
  [ "$(pstate "$_fp")" != T ]
  [ "$(pstate "$p1")" != T ]
  [ "$(pstate "$k1")" != T ]
}

@test "stop ends a paused agent at once and leaves no frozen child" {
  family claude-1
  "$A" pause claude-1
  t0=$(date +%s%N)
  LEA_AGENT_STOP_WAIT=5 run "$A" stop claude-1
  t1=$(date +%s%N)
  [ "$status" -eq 0 ]
  ! kill -0 "$_fp" 2>/dev/null || false
  [ $(((t1 - t0) / 1000000)) -lt 2000 ]
  [ "$(pstate "$_fk")" != T ]
}

@test "pause and resume fail for an agent that is not running and spare a reused pid" {
  agent claude-1 someone-else
  pid=$(cat "$LEA_AGENT_RUN_DIR/claude-1.pid")
  for v in pause resume; do
    run "$A" $v claude-1
    [ "$status" -eq 1 ]
    run "$A" $v claude-9
    [ "$status" -eq 1 ]
    run "$A" $v "../x"
    [ "$status" -eq 2 ]
  done
  [ "$(pstate "$pid")" != T ]
}

# shell_proc NAME [ENV_NAME]: a background process whose environment says LEA_SHELL=ENV_NAME.
shell_proc() {
  env LEA_SHELL="${2:-$1}" sleep 60 &
  _sp=$!; PIDS="$PIDS $_sp"
  i=0
  until tr '\0' '\n' <"/proc/$_sp/environ" 2>/dev/null | grep -qx "LEA_SHELL=${2:-$1}"; do
    i=$((i + 1)); [ "$i" -lt 50 ] || return 1
    sleep 0.05
  done
  mkdir -p "$LEA_SHELL_RUN_DIR"; echo "$_sp" >"$LEA_SHELL_RUN_DIR/$1.pid"
}

@test "shell records its pid and runs the user's login shell without LEA_AGENT" {
  export LEA_SHELL_RUN_DIR="$BATS_TEST_TMPDIR/shells"
  cat >"$F/fakesh" <<'EOT'
#!/bin/sh
printf 'fakesh pid=%s shell=%s agent=%s args=%s\n' "$$" "$LEA_SHELL" "${LEA_AGENT-unset}" "$*" >>"$FAKE_LOG"
EOT
  chmod +x "$F/fakesh"
  LEA_AGENT=claude-1 LEA_AGENT_SHELL="$F/fakesh" run "$A" shell sh-1
  [ "$status" -eq 0 ]
  pid=$(cat "$LEA_SHELL_RUN_DIR/sh-1.pid")
  [ "$(cat "$FAKE_LOG")" = "fakesh pid=$pid shell=sh-1 agent=unset args=-l" ]
  [ ! -e "$LEA_AGENT_RUN_DIR/sh-1.pid" ]
}

@test "shells lists live shells only, never agents; ls never lists shells" {
  export LEA_SHELL_RUN_DIR="$BATS_TEST_TMPDIR/shells"
  agent claude-1
  shell_proc sh-1
  shell_proc sh-2 sh-9                 # a reused pid
  run "$A" shells
  [ "$status" -eq 0 ]
  [ "$output" = sh-1 ]
  [ ! -e "$LEA_SHELL_RUN_DIR/sh-2.pid" ]
  run "$A" ls
  [ "$output" = claude-1 ]
}

@test "stop-shell ends the shell and removes its pid file" {
  export LEA_SHELL_RUN_DIR="$BATS_TEST_TMPDIR/shells"
  shell_proc sh-1
  pid=$(cat "$LEA_SHELL_RUN_DIR/sh-1.pid")
  run "$A" stop-shell sh-1
  [ "$status" -eq 0 ]
  i=0; while kill -0 "$pid" 2>/dev/null && [ "$i" -lt 20 ]; do sleep 0.1; i=$((i + 1)); done
  ! kill -0 "$pid" 2>/dev/null || false
  [ ! -e "$LEA_SHELL_RUN_DIR/sh-1.pid" ]
  run "$A" stop-shell sh-1
  [ "$status" -eq 0 ]
}

@test "stop-shell ends an interactive shell, which ignores TERM, at once with a hangup" {
  export LEA_SHELL_RUN_DIR="$BATS_TEST_TMPDIR/shells"
  # An interactive shell ignores TERM and exits on HUP, as when its terminal closes.
  env LEA_SHELL=sh-1 sh -c 'trap "" TERM; trap "exit 0" HUP; while :; do sleep 0.1; done' &
  pid=$!; PIDS="$PIDS $pid"
  i=0
  until tr '\0' '\n' <"/proc/$pid/environ" 2>/dev/null | grep -qx "LEA_SHELL=sh-1"; do
    i=$((i + 1)); [ "$i" -lt 50 ] || return 1
    sleep 0.05
  done
  mkdir -p "$LEA_SHELL_RUN_DIR"; echo "$pid" >"$LEA_SHELL_RUN_DIR/sh-1.pid"
  t0=$(date +%s%N)
  LEA_AGENT_STOP_WAIT=5 run "$A" stop-shell sh-1
  t1=$(date +%s%N)
  [ "$status" -eq 0 ]
  ! kill -0 "$pid" 2>/dev/null || false
  [ $(((t1 - t0) / 1000000)) -lt 2000 ]
}

@test "shell names are checked" {
  export LEA_SHELL_RUN_DIR="$BATS_TEST_TMPDIR/shells"
  for n in "" sh-0 sh-x claude-1 "sh-1 x" "../sh-1" sh-1234; do
    run "$A" shell "$n"
    [ "$status" -eq 2 ]
    run "$A" stop-shell "$n"
    [ "$status" -eq 2 ]
  done
  [ ! -s "$FAKE_LOG" ]
}
