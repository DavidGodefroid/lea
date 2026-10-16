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
    LEA_GIT_SSH LEA_GIT_KNOWN_HOSTS LEA_GITLAB_SSH LEA_GITLAB_KNOWN_HOSTS LEA_LOCKSQL
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
