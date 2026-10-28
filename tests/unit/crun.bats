load helpers
# image/lea-crun: the nested Podman's crun, which pins an agent's containers to its CPU window
# (#132), with a fake crun that records its calls.
setup() {
  setup_fakes
  W="$LEA_ROOT/image/lea-crun"
  export LEA_AGENT_RUN_DIR="$BATS_TEST_TMPDIR/agents"; mkdir -p "$LEA_AGENT_RUN_DIR"
  F="$BATS_TEST_TMPDIR/fbin"; mkdir -p "$F"
  export LEA_CRUN="$F/crun" CRUN_LOG="$BATS_TEST_TMPDIR/crun.log"
  # The fake crun: create starts a process and writes its pid; state names the bundle.
  cat >"$F/crun" <<'EOT'
#!/bin/sh
{ printf 'crun'; for a in "$@"; do printf ' [%s]' "$a"; done; printf '\n'; } >>"$CRUN_LOG"
pidfile= prev=
for a in "$@"; do [ "$prev" = --pid-file ] && pidfile=$a; prev=$a; done
case " $* " in
  *" --version "*) echo "crun version ${FAKE_CRUN_VERSION:-1.28}" ;;
  *" create "*) setsid sleep 60 </dev/null >/dev/null 2>&1 & echo "$!" >"$pidfile"; echo "$!" >"$CRUN_LOG.pid" ;;
  *" state "*) printf '{"id": "c1", "bundle": "%s"}\n' "$BUNDLE" ;;
esac
exit "${FAKE_CRUN_RC:-0}"
EOT
  chmod +x "$F/crun"
  export BUNDLE="$BATS_TEST_TMPDIR/bundle"; mkdir -p "$BUNDLE"
  PIDS=
}
teardown() {
  [ ! -f "$CRUN_LOG.pid" ] || kill -9 "$(cat "$CRUN_LOG.pid")" 2>/dev/null || true
}
bundle() { printf '{"annotations": {%s}, "process": {"args": ["sh"]}}\n' "$1" >"$BUNDLE/config.json"; }
cpus() { grep Cpus_allowed_list "/proc/$1/status" | awk '{ print $2 }'; }

@test "create pins an agent's container to its CPU window, and passes everything through" {
  bundle '"lea.agent": "dev-1"'
  echo 0 >"$LEA_AGENT_RUN_DIR/dev-1.cpus"
  run "$W" --log-format=json --cgroup-manager disabled create --bundle "$BUNDLE" --pid-file "$BATS_TEST_TMPDIR/pid" c1
  [ "$status" -eq 0 ]
  grep -qxF "crun [--log-format=json] [--cgroup-manager] [disabled] [create] [--bundle] [$BUNDLE] [--pid-file] [$BATS_TEST_TMPDIR/pid] [c1]" "$CRUN_LOG"
  [ "$(cpus "$(cat "$BATS_TEST_TMPDIR/pid")")" = 0 ]
}

@test "create leaves a container no agent started, or an agent without a window, as it is" {
  for b in '"other": "x"' '"lea.agent": "dev-2"'; do
    bundle "$b"
    run "$W" create --bundle "$BUNDLE" --pid-file "$BATS_TEST_TMPDIR/pid" c1
    [ "$status" -eq 0 ]
    [ "$(cpus "$(cat "$BATS_TEST_TMPDIR/pid")")" = "$(cpus $$)" ]
    kill -9 "$(cat "$CRUN_LOG.pid")"
  done
}

@test "exec gives the process the agent's window (execCPUAffinity)" {
  bundle '"lea.agent": "dev-1"'
  echo 0,2 >"$LEA_AGENT_RUN_DIR/dev-1.cpus"
  printf '{"args": ["phpstan"], "cwd": "/"}\n' >"$BATS_TEST_TMPDIR/process.json"
  run "$W" --cgroup-manager disabled exec --detach --process "$BATS_TEST_TMPDIR/process.json" c1
  [ "$status" -eq 0 ]
  [ "$(jq -c .execCPUAffinity "$BATS_TEST_TMPDIR/process.json")" = '{"initial":"0,2","final":"0,2"}' ]
  [ "$(jq -c .args "$BATS_TEST_TMPDIR/process.json")" = '["phpstan"]' ]
  grep -q '\[exec\] \[--detach\] \[--process\]' "$CRUN_LOG"
}

@test "exec on a crun without execCPUAffinity (before 1.19) leaves the process as it is" {
  bundle '"lea.agent": "dev-1"'
  echo 0 >"$LEA_AGENT_RUN_DIR/dev-1.cpus"
  printf '{"args": ["x"]}\n' >"$BATS_TEST_TMPDIR/process.json"
  FAKE_CRUN_VERSION=1.18 run "$W" exec --process "$BATS_TEST_TMPDIR/process.json" c1
  [ "$status" -eq 0 ]
  [ "$(jq -c . "$BATS_TEST_TMPDIR/process.json")" = '{"args":["x"]}' ]
}

@test "crun's status is the wrapper's, and anything it cannot read goes through untouched" {
  FAKE_CRUN_RC=7 run "$W" delete --force c1
  [ "$status" -eq 7 ]
  grep -qxF 'crun [delete] [--force] [c1]' "$CRUN_LOG"
  # A bundle without config.json, a window that is not a CPU list: the container runs anyway.
  echo 'x;y' >"$LEA_AGENT_RUN_DIR/dev-1.cpus"
  run "$W" create --bundle "$BATS_TEST_TMPDIR/nowhere" --pid-file "$BATS_TEST_TMPDIR/pid" c1
  [ "$status" -eq 0 ]
  bundle '"lea.agent": "dev-1"'
  run "$W" create --bundle "$BUNDLE" --pid-file "$BATS_TEST_TMPDIR/pid" c1
  [ "$status" -eq 0 ]
  [ "$(cpus "$(cat "$BATS_TEST_TMPDIR/pid")")" = "$(cpus $$)" ]
}
