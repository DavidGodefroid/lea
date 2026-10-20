load helpers
# lea rollback (lea_rollback): a release install of v1.2.3 at $INST/v1.2.3, linked as
# $LEA_BIN_DIR/lea and locksql, with older releases beside it.
setup() {
  setup_fakes
  INST="$BATS_TEST_TMPDIR/inst"; R="$INST/v1.2.3"
  for v in v1.2.3 v1.2.2; do mkdir -p "$INST/$v/bin"; : >"$INST/$v/bin/lea"; : >"$INST/$v/bin/locksql"; done
  mkdir -p "$R/config"; cp "$LEA_ROOT/config/lea.conf" "$R/config/"
  export LEA_BIN_DIR="$BATS_TEST_TMPDIR/bin"; mkdir -p "$LEA_BIN_DIR"
  ln -s "$R/bin/lea" "$LEA_BIN_DIR/lea"; ln -s "$R/bin/locksql" "$LEA_BIN_DIR/locksql"
  U="$HOME/.config/lea/lea.conf"; mkdir -p "${U%/*}"; printf "LEA_PROJECTS_DIR='/p'\n" >"$U"
  load_lea
  LEA_ROOT=$R
  NOTICE="$XDG_DATA_HOME/lea/host/update-notice"
}
linked() { [ "$(readlink "$LEA_BIN_DIR/lea")" = "$1/bin/lea" ]; }

@test "rollback links the previous version and skips the current one" {
  mkdir -p "$INST/v1.1.0/bin"; : >"$INST/v1.1.0/bin/lea"
  mkdir -p "${NOTICE%/*}"; echo "v1.2.3 installed" >"$NOTICE"
  run lea_rollback
  [ "$status" -eq 0 ]
  linked "$INST/v1.2.2"
  [ "$(readlink "$LEA_BIN_DIR/locksql")" = "$INST/v1.2.2/bin/locksql" ]
  grep -qx "LEA_AUTOUPDATE_SKIP='v1.2.3'" "$U"
  grep -qx "LEA_PROJECTS_DIR='/p'" "$U"
  [ ! -e "$NOTICE" ]
  [ -d "$R" ]
  [[ "$output" == *"rolled back to lea v1.2.2, active at the next lea"* ]]
}

@test "without a previous version rollback fails and changes nothing" {
  rm -rf "$INST/v1.2.2"
  mkdir -p "$INST/v1.3.0/bin"; : >"$INST/v1.3.0/bin/lea"
  run lea_rollback
  [ "$status" -eq 1 ]
  [[ "$output" == *"no previous version of lea in $INST to roll back to"* ]]
  linked "$R"
  ! grep -q LEA_AUTOUPDATE_SKIP "$U"
}

@test "rollback refuses a git checkout and a link that leads elsewhere" {
  ln -sfn "$BATS_TEST_TMPDIR/elsewhere/bin/lea" "$LEA_BIN_DIR/lea"
  mkdir -p "$BATS_TEST_TMPDIR/elsewhere/bin"; : >"$BATS_TEST_TMPDIR/elsewhere/bin/lea"
  run lea_rollback
  [ "$status" -eq 1 ]
  [[ "$output" == *"does not lead to $R"* ]]
  ln -sfn "$R/bin/lea" "$LEA_BIN_DIR/lea"
  mkdir "$R/.git"
  run lea_rollback
  [ "$status" -eq 1 ]
  [[ "$output" == *"lea rollback is for a release install"* ]]
  linked "$R"
  ! grep -q LEA_AUTOUPDATE_SKIP "$U"
}

@test "lea-sh accepts rollback without arguments only" {
  run "$BATS_TEST_DIRNAME/../../libexec/lea-sh" rollback now
  [ "$status" -eq 2 ]
}
