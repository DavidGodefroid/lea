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
  [[ "$output" == *"docker [run] [-i] [--rm] [--log-driver] [none] [--user] [locksql] [--cap-drop] [ALL] [--security-opt] [no-new-privileges] [--read-only] [--tmpfs] [/tmp:rw,nosuid,nodev,noexec,size=64m] [--pids-limit] [256] [--memory] [1g] [-v] [locksql-home:/home/locksql] [--name] [locksql-console] [--add-host=host.docker.internal:host-gateway] [-v] [$P:$P:ro] [-v] [$P/.locksql:$P/.locksql] [-v] [locksql-run:/run/locksql] [locksql:0.8.0] [console] [--project] [$P] [--profile] [uat]"* ]]
  [[ "$output" != *"docker [rm]"* ]]
  [ -d "$P/.locksql" ] && [ ! -L "$P/.locksql" ]
}

@test "lea locksql refuses a .locksql that is not a directory of the projects directory" {
  ln -s "$HOME" "$P/.locksql"
  run lea_locksql </dev/null
  [ "$status" -ne 0 ]
  [[ "$output" == *"$P/.locksql is not a directory"* ]]
  run calls
  [[ "$output" != *"docker [run]"* ]]
  rm "$P/.locksql"; : >"$P/.locksql"
  run lea_locksql </dev/null
  [ "$status" -ne 0 ]
  [[ "$output" == *"$P/.locksql is not a directory"* ]]
}

@test "lea locksql refuses while a running agent lacks the read-only .locksql mount" {
  r=$(cd "$P" && pwd -P)
  # Two agents of the projects directory, or of a directory above it (its mount holds .locksql):
  # one started with the mount, one before it (no mount). Those of other directories do not count.
  export FAKE_DOCKER_OUT_PS___FILTER="$(printf 'c1 %s\nc0 %s/sub\nc2 %s\nc3 /elsewhere\nc4 ' "$r" "$r" "${r%/*}")"
  export FAKE_DOCKER_OUT_INSPECT__F_AT_C2="$(printf '/lea-a false\n/lea-b ')"
  run lea_locksql </dev/null
  [ "$status" -ne 0 ]
  [[ "$output" == *"lea-b"*"without $r/.locksql read-only"*"restart"* ]]
  [[ "$output" != *"lea-a"* ]]
  run calls
  [[ "$output" == *"docker [ps] [--filter] [label=lea.role=session] [--format] [{{.ID}} {{.Label \"lea.projects\"}}]"* ]]
  [[ "$output" == *"docker [inspect] [-f]"*"[c1] [c2]"* ]]
  [[ "$output" != *"[c0]"* && "$output" != *"[c3]"* && "$output" != *"[c4]"* ]]
  [[ "$output" != *"docker [run]"* ]]
  # A read-write mount of it is no better.
  export FAKE_DOCKER_OUT_INSPECT__F_AT_C2="$(printf '/lea-a false\n/lea-b true')"
  run lea_locksql </dev/null
  [ "$status" -ne 0 ]
  [[ "$output" == *"lea-b"* ]]
  # Every agent has it: the console starts.
  export FAKE_DOCKER_OUT_INSPECT__F_AT_C2="$(printf '/lea-a false\n/lea-b false')"
  run lea_locksql </dev/null
  [ "$status" -eq 0 ]
  run calls
  [[ "$output" == *"docker [run]"*"[console]"* ]]
}

@test "lea locksql refuses when it cannot list the agents" {
  export FAKE_DOCKER_RC_PS___FILTER=1
  run lea_locksql </dev/null
  [ "$status" -ne 0 ]
  [[ "$output" == *"could not list the lea agents"* ]]
  run calls
  [[ "$output" != *"docker [run]"* ]]
}

@test "lea locksql refuses an entry of .locksql that is not a regular file or directory" {
  mkdir -p "$P/.locksql/pii/a"; : >"$P/.locksql/pii.toml"; : >"$P/.locksql/pii/a/x.toml"
  run lea_locksql </dev/null
  [ "$status" -eq 0 ]
  : >"$FAKE_LOG"
  ln -s "$HOME/.ssh" "$P/.locksql/pii/a/k"
  run lea_locksql </dev/null
  [ "$status" -ne 0 ]
  [[ "$output" == *"$P/.locksql/pii/a/k is not a regular file or directory"*"remove it"* ]]
  run calls
  [[ "$output" != *"[console]"* ]]
  rm "$P/.locksql/pii/a/k"; ln -s "$HOME" "$P/.locksql/pii.d"
  run lea_locksql </dev/null
  [ "$status" -ne 0 ]
  [[ "$output" == *"$P/.locksql/pii.d is not a regular file or directory"* ]]
  rm "$P/.locksql/pii.d"; mkfifo "$P/.locksql/f"
  run lea_locksql </dev/null
  [ "$status" -ne 0 ]
  [[ "$output" == *"$P/.locksql/f is not a regular file or directory"* ]]
  # A hard link: an agent writes the console's file through its other name, outside .locksql.
  rm "$P/.locksql/f"; ln "$P/.locksql/pii/a/x.toml" "$P/x"
  run lea_locksql </dev/null
  [ "$status" -ne 0 ]
  [[ "$output" == *"$P/.locksql/pii/a/x.toml has another name (a hard link)"*"remove it"* ]]
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
  [[ "$output" == *"docker [run] [-i] [--rm] [--log-driver] [none] [--user] [locksql] [--cap-drop] [ALL] [--security-opt] [no-new-privileges] [--read-only] [--tmpfs] [/tmp:rw,nosuid,nodev,noexec,size=64m] [--pids-limit] [256] [--memory] [1g] [-v] [locksql-home:/home/locksql] [--entrypoint] [vault] [locksql:0.8.0] [login] [-method=oidc]"* ]]
}

@test "lea locksql refuses without locksql, a projects directory or the image" {
  LEA_LOCKSQL=0 run lea_locksql
  [[ "$output" == *"locksql is off (LEA_LOCKSQL=0)"* ]]
  LEA_PROJECTS_DIR= run lea_locksql
  [[ "$output" == *"no projects directory: run lea init"* ]]
  FAKE_DOCKER_RC_IMAGE_INSPECT_AT_LOCKSQL_0_8_0=1 run lea_locksql
  [[ "$output" == *"no locksql console image locksql:0.8.0: run lea init"* ]]
}

@test "bin/locksql runs lea locksql through the link lea init makes" {
  d="$BATS_TEST_TMPDIR/bin"; mkdir -p "$d"
  ln -s "$LEA_ROOT/bin/locksql" "$d/locksql"
  grep -q 'exec "$(cd "$(dirname "$self")" && pwd -P)/lea" locksql "$@"' "$LEA_ROOT/bin/locksql"
  [ -x "$d/locksql" ]
}
