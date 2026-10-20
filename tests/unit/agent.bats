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
EOT
  chmod +x "$F/lea-session"
  export PATH="$F:$PATH"
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
  ! kill -0 "$pid" 2>/dev/null
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

@test "stop ends a paused agent at once and leaves no frozen child" {
  family claude-1
  "$A" pause claude-1
  t0=$(date +%s%N)
  LEA_AGENT_STOP_WAIT=5 run "$A" stop claude-1
  t1=$(date +%s%N)
  [ "$status" -eq 0 ]
  ! kill -0 "$_fp" 2>/dev/null
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
  ! kill -0 "$pid" 2>/dev/null
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
  ! kill -0 "$pid" 2>/dev/null
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
