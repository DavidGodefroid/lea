load helpers
# lea update --background (lea_update_background), started by the launcher at most once a day:
# a release install of v1.2.3 at $INST/v1.2.3, linked as $LEA_BIN_DIR/lea; the latest release
# (FAKE_CURL_EFFECTIVE) is served from $REL by the fake curl. PATH holds the fakes it needs and
# the system directories only, so cosign is missing unless with_cosign.
setup() {
  setup_fakes
  INST="$BATS_TEST_TMPDIR/inst"; R="$INST/v1.2.3"; mkdir -p "$R/config" "$R/bin" "$R/build"
  cp "$LEA_ROOT/config/lea.conf" "$R/config/"
  printf '#!/bin/sh\n[ "$1" = version ] && echo v1.2.3\n' >"$R/build/lea"; chmod +x "$R/build/lea"
  : >"$R/bin/lea"; : >"$R/bin/locksql"
  export LEA_BIN_DIR="$BATS_TEST_TMPDIR/bin"; mkdir -p "$LEA_BIN_DIR"
  ln -s "$R/bin/lea" "$LEA_BIN_DIR/lea"; ln -s "$R/bin/locksql" "$LEA_BIN_DIR/locksql"
  B="$BATS_TEST_TMPDIR/b"; mkdir -p "$B"
  ln -s "$LEA_ROOT/tests/fakes/curl" "$B/curl"; ln -s "$LEA_ROOT/tests/fakes/docker" "$B/docker"
  export PATH="$B:/usr/local/bin:/usr/bin:/bin"
  load_lea
  LEA_ROOT=$R
  NOTICE="$XDG_DATA_HOME/lea/host/update-notice"
}
with_cosign() { ln -s "$BATS_TEST_DIRNAME/../fakes/cosign" "$B/cosign"; }

# bg_release VERSION: the release VERSION. Its install.sh logs "installer ARGS" and unpacks a copy
# of this checkout's bin, libexec and config at $INST/VERSION (versioned images in its lea.conf),
# linking $LEA_BIN_DIR/lea only without --no-link; checksums.txt lists it, and a stand-in Sigstore
# bundle sits next to it.
bg_release() {
  REL="$BATS_TEST_TMPDIR/release"; NEW="$INST/$1"; mkdir -p "$REL"
  src=$(cd "$BATS_TEST_DIRNAME/../.." && pwd -P)
  cat >"$REL/install.sh" <<EOT
echo "installer \$* inst=\$LEA_INSTALL_DIR cosign=\${LEA_REQUIRE_COSIGN:-}" >>"\$FAKE_LOG"
mkdir -p "$NEW"
cp -R "$src/bin" "$src/libexec" "$src/config" "$NEW/"
sed -i -e 's|^LEA_IMAGE=.*|LEA_IMAGE=ghcr.io/davidgodefroid/lea:${1#v}|' \\
  -e 's|^LEA_PROXY_IMAGE=.*|LEA_PROXY_IMAGE=ghcr.io/davidgodefroid/lea-proxy:${1#v}|' -e 's|^LEA_LOCKSQL=.*|LEA_LOCKSQL=0|' "$NEW/config/lea.conf"
case " \$* " in *" --no-link "*) ;; *) ln -sfn "$NEW/bin/lea" "$LEA_BIN_DIR/lea" ;; esac
EOT
  (cd "$REL" && sha256sum install.sh >checksums.txt)
  echo '{}' >"$REL/checksums.txt.sigstore.json"
  export FAKE_CURL_DIR="$REL" FAKE_CURL_EFFECTIVE="https://github.com/DavidGodefroid/lea/releases/tag/$1"
}
linked() { [ "$(readlink "$LEA_BIN_DIR/lea")" = "$1/bin/lea" ]; }

@test "a newer minor release is verified, installed unlinked, its images pulled, then linked" {
  with_cosign
  bg_release v1.3.0
  run lea_update --background </dev/null
  [ "$status" -eq 0 ]
  linked "$INST/v1.3.0"
  [ "$(readlink "$LEA_BIN_DIR/locksql")" = "$INST/v1.3.0/bin/locksql" ]
  [ "$(cat "$NOTICE")" = "v1.3.0 installed" ]
  [[ "$output" == *"lea v1.3.0 installed, active at the next lea"* ]]
  run calls
  [[ "$output" == *"cosign [verify-blob] [--bundle] "*"checksums.txt.sigstore.json] [--certificate-identity-regexp] [^https://github\.com/DavidGodefroid/lea/\.github/workflows/release\.yml@refs/tags/v[0-9]]"*"installer --version v1.3.0 --no-init --no-link inst=$INST cosign=1"*"docker [pull] [ghcr.io/davidgodefroid/lea:1.3.0]"*"docker [pull] [ghcr.io/davidgodefroid/lea-proxy:1.3.0]"* ]]
  [ ! -e "$XDG_DATA_HOME/lea/host/update.lock" ]
}

@test "a newer patch release is installed too" {
  with_cosign
  bg_release v1.2.4
  run lea_update --background </dev/null
  [ "$status" -eq 0 ]
  linked "$INST/v1.2.4"
}

# at_v0: the installed release is v0.4.2 instead (a 0.x major: its minor releases may break).
at_v0() {
  R0="$INST/v0.4.2"; mkdir -p "$INST"; mv "$R" "$R0"; R=$R0
  printf '#!/bin/sh\n[ "$1" = version ] && echo v0.4.2\n' >"$R/build/lea"
  ln -sfn "$R/bin/lea" "$LEA_BIN_DIR/lea"; ln -sfn "$R/bin/locksql" "$LEA_BIN_DIR/locksql"
  LEA_ROOT=$R
}

@test "in 0.x a newer patch release is installed" {
  with_cosign
  at_v0
  bg_release v0.4.3
  run lea_update --background </dev/null
  [ "$status" -eq 0 ]
  linked "$INST/v0.4.3"
  [ "$(cat "$NOTICE")" = "v0.4.3 installed" ]
}

@test "in 0.x a newer minor release is only announced, nothing is downloaded nor installed" {
  with_cosign
  at_v0
  bg_release v0.5.0
  run lea_update --background </dev/null
  [ "$status" -eq 0 ]
  linked "$R"
  [ "$(cat "$NOTICE")" = "v0.5.0 available" ]
  [[ "$output" == *"lea v0.5.0 is a new minor version of lea 0.x: run lea update"* ]]
  run calls
  [[ "$output" != *installer* ]]
  [[ "$output" != *install.sh* ]]
}

@test "a newer major release is only announced, nothing is downloaded nor installed" {
  with_cosign
  bg_release v2.0.0
  run lea_update --background </dev/null
  [ "$status" -eq 0 ]
  linked "$R"
  [ "$(cat "$NOTICE")" = "v2.0.0 major" ]
  run calls
  [[ "$output" != *installer* ]]
  [[ "$output" != *install.sh* ]]
}

@test "the same or an older release does nothing" {
  with_cosign
  for v in v1.2.3 v1.1.9 v0.9.0; do
    : >"$FAKE_LOG"
    bg_release "$v"
    run lea_update --background </dev/null
    [ "$status" -eq 0 ]
    linked "$R"
    [ ! -e "$NOTICE" ]
    run calls
    [[ "$output" != *install.sh* ]]
  done
}

@test "a network failure is silent and changes nothing" {
  with_cosign
  bg_release v1.3.0
  export FAKE_CURL_RC=6
  run lea_update --background </dev/null
  [ "$status" -eq 0 ]
  linked "$R"
  [ ! -e "$NOTICE" ]
}

@test "without cosign nothing is installed: the release is announced for lea update" {
  bg_release v1.3.0
  run lea_update --background </dev/null
  [ "$status" -eq 0 ]
  linked "$R"
  [ "$(cat "$NOTICE")" = "v1.3.0 available" ]
  [[ "$output" == *"cosign is missing"* ]]
  run calls
  [[ "$output" != *installer* ]]
}

@test "a bad signature keeps the current link and never runs the installer" {
  with_cosign
  bg_release v1.3.0
  export FAKE_COSIGN_RC=1
  run lea_update --background </dev/null
  [ "$status" -eq 1 ]
  linked "$R"
  [ ! -e "$NOTICE" ]
  [[ "$output" == *"background update to lea v1.3.0 failed"*"lea v1.2.3 stays"* ]]
  run calls
  [[ "$output" != *installer* ]]
}

@test "an installer that does not match checksums.txt is never run" {
  with_cosign
  bg_release v1.3.0
  echo 'echo tampered >>"$FAKE_LOG"' >>"$REL/install.sh"
  run lea_update --background </dev/null
  [ "$status" -eq 1 ]
  linked "$R"
  run calls
  [[ "$output" != *installer* ]]
  [[ "$output" != *tampered* ]]
}

@test "a failing installer or image pull keeps the current link" {
  with_cosign
  bg_release v1.3.0
  export FAKE_DOCKER_RC_PULL_GHCR_IO_DAVIDGODEFROID_LEA_1_3_0=1
  run lea_update --background </dev/null
  [ "$status" -eq 1 ]
  linked "$R"
  [ ! -e "$NOTICE" ]
  bg_release v1.3.0
  echo 'exit 3' >"$REL/install.sh"; (cd "$REL" && sha256sum install.sh >checksums.txt)
  run lea_update --background </dev/null
  [ "$status" -eq 1 ]
  linked "$R"
}

@test "a link that leads to another installation is left alone" {
  with_cosign
  bg_release v1.3.0
  mkdir -p "$BATS_TEST_TMPDIR/other/bin"; : >"$BATS_TEST_TMPDIR/other/bin/lea"
  ln -sfn "$BATS_TEST_TMPDIR/other/bin/lea" "$LEA_BIN_DIR/lea"
  run lea_update --background </dev/null
  [ "$status" -eq 0 ]
  [ "$(readlink "$LEA_BIN_DIR/lea")" = "$BATS_TEST_TMPDIR/other/bin/lea" ]
  run calls
  [[ "$output" != *installer* ]]
}

@test "a running updater (its lock) makes another one stop at once" {
  with_cosign
  bg_release v1.3.0
  mkdir -p "$XDG_DATA_HOME/lea/host/update.lock"
  run lea_update --background </dev/null
  [ "$status" -eq 0 ]
  linked "$R"
  run calls
  [[ "$output" != *curl* ]]
  [ -d "$XDG_DATA_HOME/lea/host/update.lock" ]
}

@test "a lock left for more than an hour (a killed updater) is taken over" {
  with_cosign
  bg_release v1.3.0
  mkdir -p "$XDG_DATA_HOME/lea/host/update.lock"
  touch -d '2 hours ago' "$XDG_DATA_HOME/lea/host/update.lock"
  run lea_update --background </dev/null
  [ "$status" -eq 0 ]
  linked "$INST/v1.3.0"
}

@test "a git checkout and a dev build never update in the background" {
  with_cosign
  bg_release v1.3.0
  printf '#!/bin/sh\necho dev\n' >"$R/build/lea"
  run lea_update --background </dev/null
  [ "$status" -eq 0 ]
  mkdir "$R/.git"
  printf '#!/bin/sh\necho v1.2.3\n' >"$R/build/lea"
  run lea_update --background </dev/null
  [ "$status" -eq 0 ]
  linked "$R"
  run calls
  [[ "$output" != *curl* ]]
}

@test "lea-sh accepts update --background and nothing else after update" {
  run "$BATS_TEST_DIRNAME/../../libexec/lea-sh" update --now
  [ "$status" -eq 2 ]
}
