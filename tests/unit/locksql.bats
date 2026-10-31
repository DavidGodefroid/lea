load helpers
setup() {
  setup_fakes; load_lea
  P="$BATS_TEST_TMPDIR/projects"; mkdir -p "$P"
  export LEA_PROJECTS_DIR="$P"
  locksql_on
}

@test "lea locksql starts no console: it says the console runs under lox up only" {
  run lea_locksql </dev/null
  [ "$status" -eq 0 ]
  [[ "$output" == *"locksql console: it runs under lox up only"*"lox add locksql NAME, with allowed_uids: [$(id -u)]"*"run \`lox up\`"* ]]
  [[ "$output" == *"lox attach NAME"*"lea lock open locksql"*"hub view"*"lox approve"* ]]
  run calls
  [[ "$output" != *"docker"* ]]
}

@test "lea locksql takes no console flag and no vault command any more" {
  for a in --auto "--profile uat" "vault login"; do
    # shellcheck disable=SC2086
    run lea_locksql $a </dev/null
    [ "$status" -ne 0 ]
    [[ "$output" == *"usage: lea locksql (lea never starts the console: lox up does"* ]]
  done
  run calls
  [[ "$output" != *"docker"* ]]
}

@test "lea locksql with locksql off says how to turn it on" {
  LEA_LOCKSQL=0 run lea_locksql
  [ "$status" -eq 0 ]
  [[ "$output" == *"locksql is off (LEA_LOCKSQL=0): lea init --locksql turns it on"* ]]
}

@test "lea ships no locksql command: the host's locksql is locksql's own launcher" {
  [ ! -e "$LEA_ROOT/bin/locksql" ]
}
