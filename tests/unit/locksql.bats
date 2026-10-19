load helpers
setup() {
  setup_fakes; load_lea
  P="$BATS_TEST_TMPDIR/projects"; mkdir -p "$P"
  export LEA_PROJECTS_DIR="$P"
}

@test "lea locksql starts the console of the projects directory in its container" {
  run lea_locksql --profile uat </dev/null
  [ "$status" -eq 0 ]
  run calls
  [[ "$output" == *"docker [run] [-i] [--rm] [--user] [locksql] [-v] [locksql-home:/home/locksql] [--name] [locksql-console] [--add-host=host.docker.internal:host-gateway] [-v] [$P:$P] [-v] [locksql-run:/run/locksql] [locksql:0.7.0] [console] [--project] [$P] [--profile] [uat]"* ]]
  [[ "$output" != *"docker [rm]"* ]]
}

@test "lea locksql removes a stopped console left behind, passes the Vault address" {
  export FAKE_DOCKER_OUT_INSPECT__F_AT_LOCKSQL_CONSOLE=false LEA_LOCKSQL_VAULT_ADDR=https://vault.example.com
  lea_locksql </dev/null
  run calls
  [[ "$output" == *"docker [rm] [locksql-console]"*"[-e] [VAULT_ADDR=https://vault.example.com] [--name] [locksql-console]"* ]]
}

@test "lea locksql vault runs the vault CLI as the console account" {
  lea_locksql vault login -method=oidc </dev/null
  run calls
  [[ "$output" == *"docker [run] [-i] [--rm] [--user] [locksql] [-v] [locksql-home:/home/locksql] [--entrypoint] [vault] [locksql:0.7.0] [login] [-method=oidc]"* ]]
}

@test "lea locksql refuses without locksql, a projects directory or the image" {
  LEA_LOCKSQL=0 run lea_locksql
  [[ "$output" == *"locksql is off (LEA_LOCKSQL=0)"* ]]
  LEA_PROJECTS_DIR= run lea_locksql
  [[ "$output" == *"no projects directory: run lea init"* ]]
  FAKE_DOCKER_RC_IMAGE_INSPECT_AT_LOCKSQL_0_7_0=1 run lea_locksql
  [[ "$output" == *"no locksql console image locksql:0.7.0: run lea init"* ]]
}

@test "bin/locksql runs lea locksql through the link lea init makes" {
  d="$BATS_TEST_TMPDIR/bin"; mkdir -p "$d"
  ln -s "$LEA_ROOT/bin/locksql" "$d/locksql"
  grep -q 'exec "$(cd "$(dirname "$self")" && pwd -P)/lea" locksql "$@"' "$LEA_ROOT/bin/locksql"
  [ -x "$d/locksql" ]
}
