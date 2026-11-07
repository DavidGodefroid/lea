load helpers
setup() {
  setup_fakes; load_lea
  P="$BATS_TEST_TMPDIR/projects"; mkdir -p "$P"
  export LEA_PROJECTS_DIR="$P"
  export LEA_CONFIG="$BATS_TEST_TMPDIR/user.conf"
  LEA_LOCKSQL=1 LEA_LOCKMQ=1 LEA_LOCKFILE=1 LEA_LOCKLOG=1
}

# lea#134: the Locks' clients come with lea's image, as one set (config/locks.lock): lea update
# says which, installs, builds and fetches no Lock, whatever --deps says.
@test "lea update names the set of Lock clients and installs, builds or fetches nothing" {
  for m in ask all none; do
    : >"$FAKE_LOG"
    run lea_update_deps "$m"
    [ "$status" -eq 0 ]
    [[ "$output" == *"Lock clients in lea's image: lockkit-v"*": locksql v"*", lockmq v"*", lockfile v"*", locklog v"*", lockhttp v"*", lox v"*" (config/locks.lock, one set: lea update moves them together)"* ]]
    [[ "$output" == *"the locks themselves are lox's: brew upgrade lox, then lox add TYPE NAME --version vX.Y.Z"* ]]
    [ -z "$(calls)" ]
  done
  [ ! -e "$LEA_CONFIG" ]
}

@test "lea update warns about a dev override of the set" {
  LEA_LOCKMQ_REF=v0.7.0 run lea_update_deps all
  [ "$status" -eq 0 ]
  [[ "$output" == *"warning: LEA_LOCKMQ_REF=v0.7.0 overrides the set's lockmq $(lea__lock_version lockmq) (a dev override, for this release only): remove it from $LEA_CONFIG"* ]]
  [[ "$output" != *"LEA_LOCKSQL_REF"* ]]
}

@test "the per-Lock refs are no longer pinned by lea: empty by default, a dev override when set" {
  : >"$LEA_CONFIG"
  lea_load_config
  [ -z "$LEA_LOCKSQL_REF" ] && [ -z "$LEA_LOCKMQ_REF" ] && [ -z "$LEA_LOCKFILE_REF" ] && [ -z "$LEA_LOCKLOG_REF" ]
  ! grep -q '^LEA_LOCK[A-Z]*_REF=\|^LEA_LOCKSQL_IMAGE=' "$LEA_ROOT/config/lea.conf"
  # A recorded release is kept as it is (no pin raises it any more), a locksql checkout too.
  printf "LEA_LOCKSQL_REF='v0.7.0'\nLEA_LOCKMQ_REF='v1.0.0'\n" >"$LEA_CONFIG"
  lea_load_config
  [ "$LEA_LOCKSQL_REF" = v0.7.0 ] && [ "$LEA_LOCKMQ_REF" = v1.0.0 ]
  printf "LEA_LOCKSQL_REF='%s'\n" "$P" >"$LEA_CONFIG"
  lea_load_config
  [ "$LEA_LOCKSQL_REF" = "$P" ]
  for c in LEA_LOCKSQL_REF=main LEA_LOCKSQL_REF=rel/x LEA_LOCKMQ_REF=latest; do
    echo "$c" >"$LEA_CONFIG"
    run lea_load_config
    [ "$status" -ne 0 ]
  done
}

@test "the set lea reads is config/locks.lock's" {
  [ "$(lea__lockset LOCKS_SET)" = "$(sed -n 's/^LOCKS_SET=//p' "$LEA_ROOT/config/locks.lock")" ]
  for t in locksql lockmq lockfile locklog lockhttp; do lea__semver "$(lea__lock_version "$t")" >/dev/null; done
  [ -z "$(lea__lockset LOCKS_NOPE)" ]
}

@test "lea update refuses an unknown --deps mode" {
  run lea_update_deps maybe
  [ "$status" -ne 0 ]
  run lea_update --deps maybe
  [ "$status" -ne 0 ]
  [[ "$output" == *"usage: lea update [--deps ask|all|none]"* ]]
  run "$LEA_ROOT/libexec/lea-sh" update --deps
  [ "$status" -eq 2 ]
  run "$LEA_ROOT/libexec/lea-sh" update --deps sometimes
  [ "$status" -eq 2 ]
  run "$LEA_ROOT/libexec/lea-sh" update-deps
  [ "$status" -eq 2 ]
  run "$LEA_ROOT/libexec/lea-sh" update-deps all extra
  [ "$status" -eq 2 ]
  [ -z "$(calls)" ]
}

@test "the background update never touches a dependency" {
  ! grep -q 'update-deps\|lea_update_deps' <(sed -n '/^lea_update_background()/,/^}/p;/^lea__update_background()/,/^}/p' "$LEA_ROOT/libexec/lib/host.sh") || false
  ! grep -q 'update-deps\|lea_update_deps' "$LEA_ROOT/internal/launcher/autoupdate.go"
}

@test "the user config takes several keys in one write, and keeps the other lines" {
  printf "LEA_SOUND='1'\nLEA_LOCKSQL_REF='v0.1.0'\n" >"$LEA_CONFIG"
  lea_user_config_set LEA_LOCKSQL_REF v0.17.0 LEA_LOCKSQL_IMAGE "locksql:0.17.0"
  [ "$(cat "$LEA_CONFIG")" = "$(printf "LEA_SOUND='1'\nLEA_LOCKSQL_REF='v0.17.0'\nLEA_LOCKSQL_IMAGE='locksql:0.17.0'")" ]
  [ "$(stat -c %a "$LEA_CONFIG")" = 600 ]
  [ -z "$(ls "$BATS_TEST_TMPDIR" | grep 'user.conf.')" ]
}

@test "a release version is one line of vX.Y.Z without leading zeros" {
  for v in v0.1.0 v1.20.3 v10.0.0; do lea__semver "$v" >/dev/null; done
  [ "$(lea__semver v1.20.3)" = "1 20 3" ]
  for v in v01.2.3 v1.02.3 v1.2.03 v00.1.1 "$(printf 'v1.2.3\nv9.9.9')" "$(printf 'x\nv1.2.3')" v1.2 1.2.3 v1.2.3-rc.1 v1234567890.0.0 ""; do
    if lea__semver "$v" >/dev/null; then echo "accepted: $v"; false; fi
  done
}
