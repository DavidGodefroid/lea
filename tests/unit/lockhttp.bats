load helpers
# libexec/lib/lockhttp.sh: lea doctor's LockHTTP checks.
setup() {
  setup_fakes; load_lea
  R="$BATS_TEST_TMPDIR/host"; mkdir -p "$R/usr/local/bin" "$R/etc/lockhttp" "$R/run/lockhttp"
  OWNER=$(id -u)
  lea__lockhost_root() { printf '%s' "$R"; }
  lea__lockhost_owner() { printf '%s' "$OWNER"; }
  lea__lockhttp_in_clients() { true; }
  lea__lockhttp_self() { echo 1000; }
  printf '#!/bin/sh\n' >"$R/usr/local/bin/lockhttp"; chmod 0755 "$R/usr/local/bin/lockhttp"
  printf 'service_user = "lockhttp"\nallowed_uids = [10003]\n' >"$R/etc/lockhttp/system.toml"
  export LEA_LOCKHTTP=1 LEA_GIT_SSH= LEA_PREFIX=lea
  export FAKE_DOCKER_OUT_INSPECT__F=true
  export FAKE_DOCKER_OUT_EXEC_LEA_PROXY_AT__LTN='tcp        0      0 127.0.0.1:3128          0.0.0.0:*               LISTEN
tcp        0      0 :::3130                 :::*                    LISTEN'
  S=$(lea_data_dir)/lockhttp/status; mkdir -p "$S"
  printf '{"version":1,"console":"up","pending":0}\n' >"$S/status.json"
}

@test "off: no LockHTTP check" {
  LEA_LOCKHTTP=0 run lea__doctor_lockhttp
  [ "$status" -eq 0 ] && [ -z "$output" ]
}

@test "a healthy setup: binary, allowed gate uid, socket dir, group, gate, loopback squid, console" {
  run lea__doctor_lockhttp
  [ "$status" -eq 0 ]
  [[ "$output" != *FAIL* ]]
  [[ "$output" == *"ok    lockhttp binary"* ]]
  [[ "$output" == *"ok    lockhttp allows the gate (uid 10003)"* ]]
  [[ "$output" == *"ok    LockHTTP gate answers"* ]]
  [[ "$output" == *"ok    squid listens on 127.0.0.1 only"* ]]
  [[ "$output" == *"ok    lockhttp console up"* ]]
  grep -qF 'docker [exec] [lea-proxy] [lockhttp] [gate] [--check] [--listen] [127.0.0.1:3130]' "$FAKE_LOG"
}

@test "squid on every address, a silent gate, a missing setup and a down console are reported" {
  export FAKE_DOCKER_OUT_EXEC_LEA_PROXY_AT__LTN='tcp        0      0 :::3128                 :::*                    LISTEN'
  export FAKE_DOCKER_RC_EXEC_LEA_PROXY_AT_127_0_0_1_3130=1
  rm "$R/etc/lockhttp/system.toml"
  printf '{"version":1,"console":"down"}\n' >"$S/status.json"
  run lea__doctor_lockhttp
  [ "$status" -eq 1 ]
  [[ "$output" == *"FAIL  squid: listens on ':::3128'"* ]]
  [[ "$output" == *"FAIL  LockHTTP gate: does not answer"* ]]
  [[ "$output" == *"FAIL  lockhttp separated setup: none: sudo lockhttp install --client"*"--gate-uid 10003"* ]]
  [[ "$output" == *"warn  lockhttp console: down"*"lockhttp console --profile default"* ]]
}

@test "rootless Podman: allowed_uids must hold the gate's subuid" {
  export FAKE_DOCKER_OUT___VERSION_='podman version 5.8.9'
  printf '%s:200000:65536\n' "$(lea_user_name)" >"$R/etc/subuid"
  run lea__doctor_lockhttp
  [ "$status" -eq 1 ]
  [[ "$output" == *"FAIL  lockhttp allowed_uids"*"uid 210002"*"--rootless"* ]]
  printf 'allowed_uids = [ 1000, 210002 ]\n' >"$R/etc/lockhttp/system.toml"
  run lea__doctor_lockhttp
  [[ "$output" == *"ok    lockhttp allows the gate (uid 210002)"* ]]
}

@test "the user outside lockhttp-clients and LEA_GIT_SSH set are failures" {
  lea__lockhttp_in_clients() { false; }
  LEA_GIT_SSH=github.com:22 run lea__doctor_lockhttp
  [ "$status" -eq 1 ]
  [[ "$output" == *"FAIL  lockhttp-clients: you are not in the group"* ]]
  [[ "$output" == *"FAIL  LockHTTP: LEA_GIT_SSH is set"* ]]
}

@test "the configuration refuses LEA_LOCKHTTP=1 with LEA_GIT_SSH, and a bad value" {
  U="$HOME/.config/lea/lea.conf"; mkdir -p "${U%/*}"
  printf 'LEA_LOCKHTTP=1\nLEA_GIT_SSH=github.com:22\n' >"$U"
  run lea_load_config
  [ "$status" -ne 0 ]
  [[ "$output" == *"LEA_LOCKHTTP=1 refuses LEA_GIT_SSH"* ]]
  printf 'LEA_LOCKHTTP=yes\n' >"$U"
  run lea_load_config
  [ "$status" -ne 0 ]
  [[ "$output" == *"LEA_LOCKHTTP must be 1"* ]]
  printf 'LEA_LOCKHTTP=1\n' >"$U"
  lea_load_config
  [ "$LEA_LOCKHTTP" = 1 ]
}

@test "MITM: the CA's expiry and the signer are checked" {
  export LEA_LOCKHTTP_MITM=1
  run lea__doctor_lockhttp
  [[ "$output" == *"warn  LockHTTP CA: none yet"* ]]
  [[ "$output" == *"ok    LockHTTP signer listening"* ]]
  grep -qF 'docker [exec] [lea-proxy] [test] [-S] [/run/lockhttp-signer/signer.sock]' "$FAKE_LOG"
  if ! command -v openssl >/dev/null; then skip "no openssl"; fi
  ca=$(lea_data_dir)/lockhttp/ca; mkdir -p "$ca"
  openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -days 3 -subj /CN=t -keyout "$ca/ca-key.pem" -out "$ca/ca.pem" 2>/dev/null
  export FAKE_DOCKER_RC_EXEC_LEA_PROXY_AT__RUN_LOCKHTTP_SIGNER_SIGNER_SOCK=1
  run lea__doctor_lockhttp
  [ "$status" -eq 1 ]
  [[ "$output" == *"FAIL  LockHTTP CA: expires within 7 days"* ]]
  [[ "$output" == *"FAIL  LockHTTP signer"* ]]
  openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -days 90 -subj /CN=t -keyout "$ca/ca-key.pem" -out "$ca/ca.pem" 2>/dev/null
  run lea__doctor_lockhttp
  [[ "$output" == *"ok    LockHTTP CA valid for more than 7 days"* ]]
}
