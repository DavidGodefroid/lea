load helpers
# image/lea-cli-update with a fake npm: the agents' CLIs update themselves beside the image's
# pinned ones, an agent starts the newest one, and no update reaches a running agent.
setup() {
  setup_fakes
  U="$LEA_ROOT/image/lea-cli-update"
  export LEA_CLI_DIR="$BATS_TEST_TMPDIR/cli" LEA_CLI_PINS="$BATS_TEST_TMPDIR/pins"
  LEA_CLI_OWNER=$(id -u); export LEA_CLI_OWNER
  export LEA_CLI_PROC="$BATS_TEST_TMPDIR/proc"
  mkdir -p "$LEA_CLI_PINS" "$LEA_CLI_PROC"
  echo 2.1.296 >"$LEA_CLI_PINS/claude-code-version"
  echo 0.163.0 >"$LEA_CLI_PINS/codex-version"
  F="$BATS_TEST_TMPDIR/fbin"; mkdir -p "$F"
  # npm view PKG version: FAKE_LATEST_<claude|codex>; npm install -g --prefix P PKG@V: a CLI in
  # P/bin that prints its version as the real one does (FAKE_REPORTS overrides it).
  cat >"$F/npm" <<'EOT'
#!/bin/sh
{ printf 'npm'; for a in "$@"; do printf ' [%s]' "$a"; done; printf '\n'; } >>"$FAKE_LOG"
cli() { case $1 in @anthropic-ai/claude-code*) echo claude ;; @openai/codex*) echo codex ;; esac; }
case $1 in
  view)
    for a in "$@"; do case $a in @*) c=$(cli "$a") ;; esac; done
    [ "${FAKE_REGISTRY_DOWN:-0}" = 0 ] || exit 1
    eval "echo \${FAKE_LATEST_$c:-}" ;;
  install)
    [ "${FAKE_INSTALL_FAILS:-0}" = 0 ] || exit 1
    while [ $# -gt 0 ]; do case $1 in --prefix) p=$2; shift ;; @*) spec=$1 ;; esac; shift; done
    c=$(cli "$spec") v=${spec##*@}
    mkdir -p "$p/bin"
    if [ "$c" = claude ]; then r="$v (Claude Code)"; else r="codex-cli $v"; fi
    printf '#!/bin/sh\necho "%s"\n' "${FAKE_REPORTS:-$r}" >"$p/bin/$c"
    chmod 0755 "$p/bin/$c" ;;
esac
EOT
  chmod +x "$F/npm"
  export PATH="$F:$PATH"
}

@test "without an update an agent runs the image's CLI" {
  run "$U" path claude
  [ "$output" = claude ]
  run "$U" path codex
  [ "$output" = codex ]
}

@test "a newer release is installed in a directory of its own and the next agent runs it" {
  export FAKE_LATEST_claude=2.1.323 FAKE_LATEST_codex=0.169.0
  run "$U" check
  [ "$status" -eq 0 ]
  [[ "$output" == *"claude 2.1.296 -> 2.1.323 (new agents run it)"* ]]
  [[ "$output" == *"codex 0.163.0 -> 0.169.0 (new agents run it)"* ]]
  [ "$(readlink "$LEA_CLI_DIR/claude/current")" = 2.1.323 ]
  run "$U" path claude
  [ "$output" = "$LEA_CLI_DIR/claude/2.1.323/bin/claude" ]
  run "$U" path codex
  [ "$output" = "$LEA_CLI_DIR/codex/0.169.0/bin/codex" ]
  run "$U" status
  [[ "$output" == *"claude 2.1.323 (updated; image 2.1.296)"* ]]
  [[ "$output" == *"codex 0.169.0 (updated; image 0.163.0)"* ]]
  [ ! -e "$LEA_CLI_DIR/.npm" ]
}

@test "the latest release already run is not installed again" {
  export FAKE_LATEST_claude=2.1.296
  run "$U" check claude
  [ "$status" -eq 0 ]
  [[ "$output" == *"claude 2.1.296 is the latest"* ]]
  run calls
  [[ "$output" != *install* ]]
}

@test "a release older than the image's pin is never installed" {
  export FAKE_LATEST_claude=2.1.200
  run "$U" check claude
  [ "$status" -eq 0 ]
  [ ! -e "$LEA_CLI_DIR/claude/current" ]
  run "$U" path claude
  [ "$output" = claude ]
}

@test "a registry that answers no release version changes nothing" {
  for v in '' latest '2.1' '2.1.3-beta.1' '1.2.3; rm -rf /'; do
    : >"$FAKE_LOG"
    FAKE_LATEST_claude=$v run "$U" check claude
    [ "$status" -eq 1 ]
    [[ "$output" == *"claude: no release version from the registry"* ]]
    run calls
    [[ "$output" != *install* ]]
  done
  FAKE_REGISTRY_DOWN=1 run "$U" check claude
  [ "$status" -eq 1 ]
  [ ! -e "$LEA_CLI_DIR/claude/current" ]
}

@test "a failed install or a program that does not report its version keeps the current one" {
  export FAKE_LATEST_claude=2.1.323
  FAKE_INSTALL_FAILS=1 run "$U" check claude
  [ "$status" -eq 1 ]
  [[ "$output" == *"claude 2.1.323: npm install failed"* ]]
  FAKE_REPORTS="2.1.3230 (Claude Code)" run "$U" check claude
  [ "$status" -eq 1 ]
  [[ "$output" == *"the installed program reports '2.1.3230 (Claude Code)', kept 2.1.296"* ]]
  [ ! -e "$LEA_CLI_DIR/claude/current" ]
  [ ! -e "$LEA_CLI_DIR/claude/2.1.323" ]
  [ -z "$(ls -A "$LEA_CLI_DIR/claude" | grep '^\.new' || true)" ]
}

@test "an update keeps the version before it and any version a process still runs" {
  # A running agent of 2.1.300 (its command line names its version's path).
  mkdir -p "$LEA_CLI_PROC/42"; printf '%s\0--resume\0' "$LEA_CLI_DIR/claude/2.1.300/bin/claude" >"$LEA_CLI_PROC/42/cmdline"
  for v in 2.1.300 2.1.310 2.1.320; do FAKE_LATEST_claude=$v "$U" check claude >/dev/null; done
  FAKE_LATEST_claude=2.1.330 run "$U" check claude
  [ "$status" -eq 0 ]
  [ -d "$LEA_CLI_DIR/claude/2.1.330" ]
  [ -d "$LEA_CLI_DIR/claude/2.1.320" ]
  [ -d "$LEA_CLI_DIR/claude/2.1.300" ]
  [ ! -e "$LEA_CLI_DIR/claude/2.1.310" ]
}

# The agents' user cannot write /lea/cli: a CLI there it could have changed is never run.
@test "a store lea-cli does not own, or that others can write, is ignored" {
  FAKE_LATEST_claude=2.1.323 "$U" check claude >/dev/null
  LEA_CLI_OWNER=12345 run "$U" path claude
  [ "$output" = claude ]
  chmod g+w "$LEA_CLI_DIR/claude/2.1.323"
  run "$U" path claude
  [ "$output" = claude ]
  chmod g-w "$LEA_CLI_DIR/claude/2.1.323"
  run "$U" path claude
  [ "$output" = "$LEA_CLI_DIR/claude/2.1.323/bin/claude" ]
  # A current link to a version that is not a release, or not newer than the pin.
  ln -sfn ../../x "$LEA_CLI_DIR/claude/current"
  run "$U" path claude
  [ "$output" = claude ]
}

@test "LEA_CLI_AUTOUPDATE=0: the agents run the image's CLIs" {
  FAKE_LATEST_claude=2.1.323 "$U" check claude >/dev/null
  LEA_CLI_AUTOUPDATE=0 run "$U" path claude
  [ "$output" = claude ]
}

@test "lea-session starts the updated CLI of each provider" {
  FAKE_LATEST_claude=2.1.323 FAKE_LATEST_codex=0.169.0 "$U" check >/dev/null
  export LEA_CLI_UPDATE="$U" LEA_CLAUDE_SETTINGS=/nonexistent LEA_CLAUDE_PLUGIN=/nonexistent
  run "$LEA_ROOT/image/lea-session" --claude --version
  [ "$output" = "2.1.323 (Claude Code)" ]
  run "$LEA_ROOT/image/lea-session" --codex --version
  [ "$output" = "codex-cli 0.169.0" ]
}

@test "one check at a time" {
  mkdir -p "$LEA_CLI_DIR"
  exec 8>"$LEA_CLI_DIR/.lock"; flock -n 8
  FAKE_LATEST_claude=2.1.323 run "$U" check claude
  [ "$status" -eq 0 ]
  [[ "$output" == *"a check is already running"* ]]
  exec 8>&-
  [ ! -e "$LEA_CLI_DIR/claude/current" ]
}

@test "the image runs the update loop as lea-cli, never as root or the developer" {
  I="$LEA_ROOT/image"
  grep -q 'groupadd -g 10090 lea-cli && useradd -r -u 10090 -g 10090 -M -d /lea/cli -s /sbin/nologin lea-cli' "$I/Containerfile"
  grep -q 'image/lea-cli-update' "$I/Containerfile"
  grep -q 'echo "${CODEX_VERSION}" >/usr/local/share/lea/codex-version' "$I/Containerfile"
  grep -q "setpriv --reuid 10090 --regid 10090 --clear-groups --no-new-privs" "$I/entrypoint"
  grep -q "exec /usr/local/bin/lea-cli-update loop" "$I/entrypoint"
  grep -q 'for id in .* 10090; do' "$I/entrypoint"
  # Claude Code's own updater stays off: updates come through lea-cli-update only.
  [ "$(jq -r .env.DISABLE_UPDATES "$I/managed-settings.json")" = 1 ]
}
