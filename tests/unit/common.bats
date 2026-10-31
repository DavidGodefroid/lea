load helpers
setup() { setup_fakes; load_lea; }

@test "slug matches Claude Code project directory names" {
  [ "$(lea_slug /home/alice/src)" = "-home-alice-src" ]
  [ "$(lea_slug /home/alice/work/api-mr-12)" = "-home-alice-work-api-mr-12" ]
  [ "$(lea_slug /home/alice/.cache/x)" = "-home-alice--cache-x" ]
}

@test "id is 10 hex chars and stable" {
  a=$(lea_id /some/dir); b=$(lea_id /some/dir)
  [ "$a" = "$b" ]
  [[ "$a" =~ ^[0-9a-f]{10}$ ]]
  [ "$(lea_id /other)" != "$a" ]
}

@test "abspath resolves symlinks and refuses files" {
  mkdir -p "$BATS_TEST_TMPDIR/real dir"
  ln -s "$BATS_TEST_TMPDIR/real dir" "$BATS_TEST_TMPDIR/link"
  [ "$(lea_abspath "$BATS_TEST_TMPDIR/link")" = "$(cd "$BATS_TEST_TMPDIR/real dir" && pwd -P)" ]
  touch "$BATS_TEST_TMPDIR/f"
  run lea_abspath "$BATS_TEST_TMPDIR/f"
  [ "$status" -eq 1 ]
  [[ "$output" == *"not a directory"* ]]
}

@test "state dir follows XDG_DATA_HOME" {
  [ "$(lea_state_dir)" = "$XDG_DATA_HOME/lea/state" ]
}

@test "host-only dir sits next to the state dir" {
  [ "$(lea_data_dir)" = "$XDG_DATA_HOME/lea" ]
  [ "$(lea_host_dir)" = "$XDG_DATA_HOME/lea/host" ]
}

@test "safe_path accepts real paths, also missing ones" {
  B="$BATS_TEST_TMPDIR/base"; mkdir -p "$B/a"
  run lea_safe_path "$B" "$B/a/b/c"
  [ "$status" -eq 0 ]
}

@test "safe_path refuses a symlinked final component" {
  B="$BATS_TEST_TMPDIR/base"; mkdir -p "$B" "$BATS_TEST_TMPDIR/out"
  ln -s "$BATS_TEST_TMPDIR/out" "$B/link"
  run lea_safe_path "$B" "$B/link"
  [ "$status" -eq 1 ]
  [[ "$output" == *"$B/link is a symbolic link"* ]]
}

@test "safe_path refuses a symlinked intermediate component" {
  B="$BATS_TEST_TMPDIR/base"; mkdir -p "$B" "$BATS_TEST_TMPDIR/out"
  ln -s "$BATS_TEST_TMPDIR/out" "$B/link"
  run lea_safe_path "$B" "$B/link/x/y"
  [ "$status" -eq 1 ]
  [[ "$output" == *"symbolic link"* ]]
}

@test "safe_path refuses a dangling symlink" {
  B="$BATS_TEST_TMPDIR/base"; mkdir -p "$B"
  ln -s "$BATS_TEST_TMPDIR/nowhere" "$B/link"
  run lea_safe_path "$B" "$B/link"
  [ "$status" -eq 1 ]
}

@test "safe_path refuses a path outside its base or with .." {
  B="$BATS_TEST_TMPDIR/base"; mkdir -p "$B"
  run lea_safe_path "$B" "$BATS_TEST_TMPDIR/other"
  [ "$status" -eq 1 ]
  run lea_safe_path "$B" "$B/../other"
  [ "$status" -eq 1 ]
}

@test "write_file replaces a file through a temp file" {
  B="$BATS_TEST_TMPDIR/base"; mkdir -p "$B"
  printf 'new\n' | lea_write_file "$B" "$B/f"
  [ "$(cat "$B/f")" = new ]
  [ "$(ls -A "$B" | wc -l)" -eq 1 ]
}

@test "write_file never writes through a symlink" {
  B="$BATS_TEST_TMPDIR/base"; mkdir -p "$B"
  echo keep >"$BATS_TEST_TMPDIR/target"
  ln -s "$BATS_TEST_TMPDIR/target" "$B/f"
  run sh -c '. "$LEA_ROOT/libexec/lib/common.sh"; printf x | lea_write_file "$1" "$1/f"' sh "$B"
  [ "$status" -eq 1 ]
  [ "$(cat "$BATS_TEST_TMPDIR/target")" = keep ]
}

@test "abspath ignores CDPATH" {
  mkdir -p "$BATS_TEST_TMPDIR/cdp/sub" "$BATS_TEST_TMPDIR/w/sub"
  cd "$BATS_TEST_TMPDIR/w"
  [ "$(CDPATH="$BATS_TEST_TMPDIR/cdp" lea_abspath sub)" = "$(cd "$BATS_TEST_TMPDIR/w/sub" && pwd -P)" ]
}

@test "user name falls back to the uid" {
  run lea_user_name
  [ "$status" -eq 0 ]
  [ -n "$output" ]
}

@test "config loads defaults and accepts overrides" {
  [ "$LEA_PREFIX" = "lea" ]
  [ -z "$LEA_GIT_SSH" ]
  [ "$LEA_GIT_KNOWN_HOSTS" = "$HOME/.config/lea/git_known_hosts" ]
  [ "$LEA_LOCKSQL" = 0 ]
  printf 'LEA_IMAGE=lea:test\n' >"$BATS_TEST_TMPDIR/t.conf"
  LEA_CONFIG="$BATS_TEST_TMPDIR/t.conf" lea_load_config
  [ "$LEA_IMAGE" = "lea:test" ]
  [ "$LEA_PREFIX" = "lea" ]
}

@test "config has no fixed subnet or proxy address" {
  run grep -E 'LEA_SUBNET|LEA_PROXY_IP' "$LEA_ROOT/config/lea.conf" "$LEA_ROOT/libexec/lib/config.sh"
  [ "$status" -eq 1 ]
}
