setup_fakes() {
  LEA_ROOT=$(cd "$BATS_TEST_DIRNAME/../.." && pwd -P)
  export LEA_ROOT
  export PATH="$LEA_ROOT/tests/fakes:$PATH"
  export FAKE_LOG="$BATS_TEST_TMPDIR/calls.log"
  : >"$FAKE_LOG"
  export HOME="$BATS_TEST_TMPDIR/home"
  export XDG_DATA_HOME="$BATS_TEST_TMPDIR/data"
  mkdir -p "$HOME" "$XDG_DATA_HOME"
  export LEA_TMUX_BUILT="$BATS_TEST_TMPDIR/no-built-tmux"
  unset LEA_CONFIG LEA_PROJECTS_DIR XDG_CONFIG_HOME LEA_UI LEA_LAUNCHER LEA_TMUX_BIN \
    LEA_GIT_SSH LEA_GIT_KNOWN_HOSTS LEA_GITLAB_SSH LEA_GITLAB_KNOWN_HOSTS LEA_LOCKSQL LEA_LOCKMQ LEA_LOCKFILE
}
calls() { cat "$FAKE_LOG"; }
# load_lea: the shell parts of lea (libexec/lib/*.sh) and the configuration, as libexec/lea-sh
# loads them.
load_lea() {
  for f in "$LEA_ROOT"/libexec/lib/*.sh; do
    # shellcheck source=/dev/null
    . "$f"
  done
  lea_load_config
}

# fake_tmux [VERSION]: a tmux that logs its calls, first on PATH; tmux -V prints VERSION.
fake_tmux() {
  mkdir -p "$BATS_TEST_TMPDIR/tbin"
  cat >"$BATS_TEST_TMPDIR/tbin/tmux" <<'EOT'
#!/bin/sh
{ printf 'tmux'; for a in "$@"; do printf ' [%s]' "$a"; done; printf '\n'; } >>"$FAKE_LOG"
if [ "${1:-}" = -V ]; then echo "${FAKE_TMUX_VERSION:-tmux 3.4}"; fi
exit "${FAKE_TMUX_RC:-0}"
EOT
  chmod +x "$BATS_TEST_TMPDIR/tbin/tmux"
  export PATH="$BATS_TEST_TMPDIR/tbin:$PATH"
  if [ -n "${1:-}" ]; then export FAKE_TMUX_VERSION="$1"; fi
}
# no_tmux: skips the test when the test image ships a tmux (these tests need it absent).
no_tmux() { if command -v tmux >/dev/null 2>&1; then skip "tmux is installed in the test image"; fi; }

# fake_git: a git that logs its calls, first on PATH: init and clone create their destination,
# rev-parse HEAD prints $FAKE_GIT_SHA (FAKE_GIT_FETCH_RC: a failing fetch).
fake_git() {
  mkdir -p "$BATS_TEST_TMPDIR/gbin"
  cat >"$BATS_TEST_TMPDIR/gbin/git" <<'EOT'
#!/bin/sh
{ printf git; for a in "$@"; do printf ' [%s]' "$a"; done; printf '\n'; } >>"$FAKE_LOG"
for last in "$@"; do :; done
case " $* " in
  *" init "*|*" clone "*) mkdir -p "$last" ;;
  *" fetch "*) exit "${FAKE_GIT_FETCH_RC:-0}" ;;
  *" rev-parse HEAD "*) printf '%s\n' "$FAKE_GIT_SHA" ;;
esac
EOT
  chmod +x "$BATS_TEST_TMPDIR/gbin/git"
  export PATH="$BATS_TEST_TMPDIR/gbin:$PATH" FAKE_GIT_SHA=${FAKE_GIT_SHA:-0123456789abcdef0123456789abcdef01234567}
}
# locksql_release: a git (fake_git) and a locksql release as lea fetches them before a build: its
# checksums.txt and a stand-in Sigstore bundle (the fake cosign accepts it unless FAKE_COSIGN_RC),
# served by the fake curl from $FAKE_CURL_DIR.
locksql_release() {
  fake_git
  export FAKE_CURL_DIR=${FAKE_CURL_DIR:-$BATS_TEST_TMPDIR/rel}
  mkdir -p "$FAKE_CURL_DIR"
  [ -f "$FAKE_CURL_DIR/checksums.txt" ] || echo "00  locksql_0.0.0_linux_amd64.tar.gz" >"$FAKE_CURL_DIR/checksums.txt"
  [ -f "$FAKE_CURL_DIR/checksums.txt.sigstore.json" ] || echo '{}' >"$FAKE_CURL_DIR/checksums.txt.sigstore.json"
}
# fake_uname RELEASE: a uname, first on PATH, whose -r prints RELEASE.
fake_uname() {
  mkdir -p "$BATS_TEST_TMPDIR/ubin"
  printf '#!/bin/sh\nif [ "${1:-}" = -r ]; then echo %s; else echo Linux; fi\n' "$1" >"$BATS_TEST_TMPDIR/ubin/uname"
  chmod +x "$BATS_TEST_TMPDIR/ubin/uname"
  case ":$PATH:" in *":$BATS_TEST_TMPDIR/ubin:"*) ;; *) export PATH="$BATS_TEST_TMPDIR/ubin:$PATH" ;; esac
}
