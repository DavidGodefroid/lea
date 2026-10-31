load helpers
# libexec/lib/lockhttp.sh: lea doctor's LockHTTP checks.
setup() {
  setup_fakes; load_lea
  R="$BATS_TEST_TMPDIR/host"; mkdir -p "$R/usr/local/bin"
  OWNER=$(id -u)
  lea__lockhost_root() { printf '%s' "$R"; }
  lea__lockhost_owner() { printf '%s' "$OWNER"; }
  D=sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
  launcher 0.5.1 "$D"
  export LEA_LOCKHTTP=1 LEA_GIT_SSH= LEA_PREFIX=lea
  export FAKE_DOCKER_OUT_INSPECT__F=true
  export FAKE_DOCKER_OUT_INSPECT__F_AT_LEA_LOCKHTTP_GATE="true|ghcr.io/davidgodefroid/lockhttp@$D|gate|lockfamily-1a2b3c4d_web-run/var/lib/docker/volumes/lockfamily-1a2b3c4d_web-run/_data>/run/lockhttp;lockfamily-1a2b3c4d_web-gate/var/lib/docker/volumes/lockfamily-1a2b3c4d_web-gate/_data>/lockhttp-home;/home/u/.local/share/lea/lockhttp/gate>/etc/lockhttp-gate;"
  export FAKE_DOCKER_OUT_EXEC_LEA_PROXY_AT__LTN='tcp        0      0 127.0.0.1:3128          0.0.0.0:*               LISTEN
tcp        0      0 :::3130                 :::*                    LISTEN'
  S=$(lea_data_dir)/lockhttp/status; mkdir -p "$S"
  printf '{"version":1,"console":"up","pending":0}\n' >"$S/status.json"
}

# launcher VERSION DIGEST: lockhttp's launcher as install.sh writes it.
launcher() {
  printf '#!/bin/sh\nset -eu\n\nlauncher_version=%s\nimage_digest=%s\n' "$1" "$2" >"$R/usr/local/bin/lockhttp"
  chmod 0755 "$R/usr/local/bin/lockhttp"
}

@test "a launcher older than the release lea pins is a warning, not a failure; the pin or later, or a clone's, says nothing" {
  [ "$LEA_LOCKHTTP_RELEASE" = v0.5.1 ]
  for v in 0.4.0 0.2.1 0.5.0 0.5.1-rc.1 garbage; do
    launcher "$v" "$D"
    run lea__doctor_lockhttp
    [ "$status" -eq 0 ]
    [[ "$output" == *"warn  lockhttp launcher: v$v, older than v0.5.1, the release lea pins"*"LOCKHTTP_VERSION=v0.5.1 sh install.sh"* ]]
    [[ "$output" != *FAIL* ]]
  done
  for v in 0.5.1 0.5.2 1.0.0 0.6.0-rc.1 0.5.1+build v0.5.1; do
    launcher "$v" "$D"
    run lea__doctor_lockhttp
    [ "$status" -eq 0 ]
    [[ "$output" != *"older than"* ]]
  done
  # A clone's launcher runs the local image (not the gate's here: that is another failure).
  launcher dev ""
  run lea__doctor_lockhttp
  [[ "$output" != *"older than"* ]]
}

@test "off: no LockHTTP check" {
  LEA_LOCKHTTP=0 run lea__doctor_lockhttp
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "a healthy setup: launcher, gate container (image, user, mounts, answers), loopback squid, console" {
  run lea__doctor_lockhttp
  [ "$status" -eq 0 ]
  [[ "$output" != *FAIL* ]]
  [[ "$output" == *"ok    lockhttp launcher (gate image ghcr.io/davidgodefroid/lockhttp@$D)"* ]]
  [[ "$output" == *"ok    LockHTTP gate runs the pinned image"* ]]
  [[ "$output" == *"ok    LockHTTP gate runs as gate"* ]]
  [[ "$output" == *"ok    LockHTTP gate has the console's socket volume only"* ]]
  [[ "$output" == *"ok    LockHTTP gate answers"* ]]
  [[ "$output" == *"ok    LockHTTP gate in the proxy's network namespace"* ]]
  grep -qF 'docker [exec] [lea-proxy] [nc] [-z] [127.0.0.1] [3130]' "$FAKE_LOG"
  [[ "$output" == *"ok    squid listens on 127.0.0.1 only"* ]]
  [[ "$output" == *"ok    lockhttp console up"* ]]
  grep -qF 'docker [exec] [lea-lockhttp-gate] [lockhttp] [gate] [--check] [--listen] [127.0.0.1:3130]' "$FAKE_LOG"
}

@test "a launcher run from a clone pins the local image lockhttp" {
  launcher dev ""
  export FAKE_DOCKER_OUT_INSPECT__F_AT_LEA_LOCKHTTP_GATE="true|lockhttp|gate|lockfamily-1a2b3c4d_web-run/x>/run/lockhttp;"
  run lea__doctor_lockhttp
  [ "$status" -eq 0 ]
  [[ "$output" == *"ok    lockhttp launcher (gate image lockhttp)"* ]]
}

@test "a missing, unpinned or old launcher is a failure" {
  launcher 0.2.0 ""
  run lea__doctor_lockhttp
  [ "$status" -eq 1 ]
  [[ "$output" == *"FAIL  lockhttp launcher: "*"pins no image digest"* ]]
  launcher 0.2.0 'sha256:$(id)'
  run lea__doctor_lockhttp
  [[ "$output" == *"FAIL  lockhttp launcher: "*"invalid image digest"* ]]
  printf '\177ELF a host binary\n' >"$R/usr/local/bin/lockhttp"
  run lea__doctor_lockhttp
  [[ "$output" == *"FAIL  lockhttp launcher: "*"is not lockhttp's launcher"* ]]
  chmod 0777 "$R/usr/local/bin/lockhttp"
  run lea__doctor_lockhttp
  [[ "$output" == *"FAIL  lockhttp launcher: no root-owned launcher"* ]]
}

@test "a gate of another image, user or mounts, a silent gate, squid on every address and a down console are reported" {
  export FAKE_DOCKER_OUT_INSPECT__F_AT_LEA_LOCKHTTP_GATE="true|ghcr.io/davidgodefroid/lockhttp:0.2.0|root|lockfamily-1a2b3c4d_web-home/x>/home/lockhttp;lockfamily-1a2b3c4d_web-run/x>/run/lockhttp;"
  export FAKE_DOCKER_OUT_EXEC_LEA_PROXY_AT__LTN='tcp        0      0 :::3128                 :::*                    LISTEN'
  export FAKE_DOCKER_RC_EXEC_LEA_LOCKHTTP_GATE=1
  printf '{"version":1,"console":"down"}\n' >"$S/status.json"
  run lea__doctor_lockhttp
  [ "$status" -eq 1 ]
  [[ "$output" == *"FAIL  LockHTTP gate image: ghcr.io/davidgodefroid/lockhttp:0.2.0, not the launcher's"* ]]
  [[ "$output" == *"FAIL  LockHTTP gate user: 'root', not gate"* ]]
  [[ "$output" == *"FAIL  LockHTTP gate mounts: the console's home"* ]]
  [[ "$output" == *"FAIL  LockHTTP gate: does not answer"* ]]
  [[ "$output" == *"FAIL  squid: listens on ':::3128'"* ]]
  [[ "$output" == *"warn  lockhttp console: down"*"run \`lox up\`"* ]]
}

@test "a gate left in the network namespace of a proxy started again is a failure" {
  export FAKE_DOCKER_RC_EXEC_LEA_PROXY_AT_3130=1
  run lea__doctor_lockhttp
  [ "$status" -eq 1 ]
  [[ "$output" == *"ok    LockHTTP gate answers"* ]]
  [[ "$output" == *"FAIL  LockHTTP gate: not in the proxy's network namespace"* ]]
}

@test "a gate without the socket volume, or not running, is a failure" {
  export FAKE_DOCKER_OUT_INSPECT__F_AT_LEA_LOCKHTTP_GATE="true|ghcr.io/davidgodefroid/lockhttp@$D|gate|/tmp/run>/run/lockhttp;"
  run lea__doctor_lockhttp
  [ "$status" -eq 1 ]
  [[ "$output" == *"FAIL  LockHTTP gate mounts: no socket volume of lox up's lockhttp lock"*"run \`lox up\`"* ]]
  # The fixed volume of a console of lockhttp's own launcher is not lox up's.
  export FAKE_DOCKER_OUT_INSPECT__F_AT_LEA_LOCKHTTP_GATE="true|ghcr.io/davidgodefroid/lockhttp@$D|gate|lockhttp-run/x>/run/lockhttp;"
  run lea__doctor_lockhttp
  [[ "$output" == *"FAIL  LockHTTP gate mounts: no socket volume of lox up's lockhttp lock"* ]]
  export FAKE_DOCKER_OUT_INSPECT__F_AT_LEA_LOCKHTTP_GATE=
  export FAKE_DOCKER_RC_INSPECT__F_AT_LEA_LOCKHTTP_GATE=1
  run lea__doctor_lockhttp
  [[ "$output" == *"FAIL  LockHTTP gate: lea-lockhttp-gate is not running"* ]]
}

@test "LEA_GIT_SSH and LEA_OTEL_UPSTREAM set are failures" {
  LEA_GIT_SSH=github.com:22 LEA_OTEL_UPSTREAM=otel.example:4318 run lea__doctor_lockhttp
  [ "$status" -eq 1 ]
  [[ "$output" == *"FAIL  LockHTTP: LEA_GIT_SSH is set"* ]]
  [[ "$output" == *"FAIL  LockHTTP: LEA_OTEL_UPSTREAM is set"* ]]
}

@test "the proxy entrypoint refuses LEA_GIT_SSH and LEA_OTEL_UPSTREAM under LockHTTP, before any forwarder" {
  for v in "LEA_GIT_SSH=github.com:22" "LEA_OTEL_UPSTREAM=otel.example:4318"; do
    run env LEA_LOCKHTTP=1 "$v" sh "$LEA_ROOT/proxy/entrypoint.sh"
    [ "$status" -eq 1 ]
    [[ "$output" == *"LEA_LOCKHTTP=1 refuses ${v%%=*}"* ]]
  done
  ! grep -q socat "$FAKE_LOG"
}

@test "the configuration refuses LEA_LOCKHTTP=1 with LEA_GIT_SSH, and a bad value" {
  U="$HOME/.config/lea/lea.conf"; mkdir -p "${U%/*}"
  printf 'LEA_LOCKHTTP=1\nLEA_GIT_SSH=github.com:22\n' >"$U"
  run lea_load_config
  [ "$status" -ne 0 ]
  [[ "$output" == *"LEA_LOCKHTTP=1 refuses LEA_GIT_SSH"* ]]
  printf 'LEA_LOCKHTTP=1\nLEA_OTEL_UPSTREAM=otel.example:4318\n' >"$U"
  run lea_load_config
  [ "$status" -ne 0 ]
  [[ "$output" == *"LEA_LOCKHTTP=1 refuses LEA_OTEL_UPSTREAM"* ]]
  printf 'LEA_LOCKHTTP=yes\n' >"$U"
  run lea_load_config
  [ "$status" -ne 0 ]
  [[ "$output" == *"LEA_LOCKHTTP must be 1"* ]]
  printf 'LEA_LOCKHTTP=1\n' >"$U"
  lea_load_config
  [ "$LEA_LOCKHTTP" = 1 ]
}

@test "MITM: the CA's expiry, the signer and where the CA key is are checked" {
  export LEA_LOCKHTTP_MITM=1
  export FAKE_DOCKER_OUT_INSPECT__F_AT_LEA_LOCKHTTP_SIGNER='true|signer|none'
  run lea__doctor_lockhttp
  [[ "$output" == *"warn  LockHTTP CA: none yet"* ]]
  [[ "$output" == *"ok    LockHTTP signer listening"* ]]
  [[ "$output" == *"ok    LockHTTP CA key in the signer's container only"* ]]
  grep -qF 'docker [exec] [lea-lockhttp-signer] [test] [-S] [/run/lockhttp-signer/signer.sock]' "$FAKE_LOG"
  export FAKE_DOCKER_OUT_CONTAINER_INSPECT='lea-lockhttp-ca/x;'
  export FAKE_DOCKER_OUT_INSPECT__F_AT_LEA_LOCKHTTP_SIGNER='true|signer|bridge'
  run lea__doctor_lockhttp
  [ "$status" -eq 1 ]
  [[ "$output" == *"FAIL  LockHTTP signer: lea-lockhttp-signer is not running as signer without a network ('true|signer|bridge')"* ]]
  [[ "$output" == *"FAIL  LockHTTP CA key: mounted in the gate or the proxy"* ]]
  unset FAKE_DOCKER_OUT_CONTAINER_INSPECT
  export FAKE_DOCKER_OUT_INSPECT__F_AT_LEA_LOCKHTTP_SIGNER='true|signer|none'
  if ! command -v openssl >/dev/null; then skip "no openssl"; fi
  ca=$(lea_data_dir)/lockhttp/ca; mkdir -p "$ca"
  openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -days 3 -subj /CN=t -keyout "$ca/ca-key.pem" -out "$ca/ca.pem" 2>/dev/null
  export FAKE_DOCKER_RC_EXEC_LEA_LOCKHTTP_SIGNER_AT__RUN_LOCKHTTP_SIGNER_SIGNER_SOCK=1
  run lea__doctor_lockhttp
  [ "$status" -eq 1 ]
  [[ "$output" == *"FAIL  LockHTTP CA: expires within 7 days"* ]]
  [[ "$output" == *"FAIL  LockHTTP signer: no socket"* ]]
  openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -days 90 -subj /CN=t -keyout "$ca/ca-key.pem" -out "$ca/ca.pem" 2>/dev/null
  run lea__doctor_lockhttp
  [[ "$output" == *"ok    LockHTTP CA valid for more than 7 days"* ]]
}
