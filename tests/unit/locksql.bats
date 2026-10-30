load helpers
setup() {
  setup_fakes; load_lea
  P="$BATS_TEST_TMPDIR/projects"; mkdir -p "$P"
  export LEA_PROJECTS_DIR="$P"
  fake_launcher
}

@test "lea locksql hands the console of the projects directory to locksql's launcher" {
  run lea_locksql --profile uat </dev/null
  [ "$status" -eq 0 ]
  r=$(cd "$P" && pwd -P)
  run calls
  [[ "$output" == *"launcher pwd=$r image=locksql:0.9.0 ro=1 name=locksql-console vault= [console] [--project] [$P] [--profile] [uat]"* ]]
  [[ "$output" != *"docker [run]"* && "$output" != *"docker [rm]"* ]]
  [ -d "$P/.locksql" ]
  [ ! -L "$P/.locksql" ]
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
  # An agent of the directory above the projects one: the launcher may not sit in it.
  lb=$(mktemp -d "$BATS_RUN_TMPDIR/lbin.XXXXXX"); mv "$BATS_TEST_TMPDIR/lbin/locksql" "$lb/"
  export PATH="$lb:$PATH"
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
  [[ "$output" == *"launcher pwd="*"[console]"* ]]
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

@test "lea locksql vault runs the vault CLI as the console account" {
  lea_locksql vault login -method=oidc </dev/null
  run calls
  [[ "$output" == *"docker [run] [-i] [--rm] [--log-driver] [none] [--user] [locksql] [--cap-drop] [ALL] [--security-opt] [no-new-privileges] [--read-only] [--tmpfs] [/tmp:rw,nosuid,nodev,noexec,size=64m] [--pids-limit] [256] [--memory] [1g] [-v] [locksql-home:/home/locksql] [--entrypoint] [vault] [locksql:0.9.0] [login] [-method=oidc]"* ]]
}

@test "lea locksql refuses without locksql, a projects directory or the image" {
  LEA_LOCKSQL=0 run lea_locksql
  [[ "$output" == *"locksql is off (LEA_LOCKSQL=0)"* ]]
  LEA_PROJECTS_DIR= run lea_locksql
  [[ "$output" == *"no projects directory: run lea init"* ]]
  FAKE_DOCKER_RC_IMAGE_INSPECT_AT_LOCKSQL_0_9_0=1 run lea_locksql
  [[ "$output" == *"no locksql console image locksql:0.9.0: run lea init"* ]]
  # Without locksql's launcher there is no console: lea init installs it.
  rm "$BATS_TEST_TMPDIR/lbin/locksql"
  run lea_locksql </dev/null
  [ "$status" -ne 0 ]
  [[ "$output" == *"no trusted locksql launcher on PATH"*"lea init"* ]]
  run calls
  [[ "$output" != *"docker [run]"* ]]
}

@test "lea ships no locksql command: the host's locksql is locksql's own launcher" {
  [ ! -e "$LEA_ROOT/bin/locksql" ]
}

# fake_launcher: locksql's launcher (scripts/locksql of locksql), first on PATH: it logs its
# directory, the environment lea gives it and its arguments.
fake_launcher() {
  mkdir -p "$BATS_TEST_TMPDIR/lbin"
  cat >"$BATS_TEST_TMPDIR/lbin/locksql" <<'EOT'
#!/bin/sh
# locksql: the launcher. Its console: --cap-drop ALL --security-opt no-new-privileges --read-only,
# LOCKSQL_READONLY_PROJECT, LOCKSQL_CONSOLE_NAME.
launcher_version=1.0.0
{ printf 'launcher pwd=%s image=%s ro=%s name=%s vault=%s' "$(pwd -P)" "${LOCKSQL_IMAGE:-}" \
    "${LOCKSQL_READONLY_PROJECT:-}" "${LOCKSQL_CONSOLE_NAME:-}" "${VAULT_ADDR:-}"
  for a in "$@"; do printf ' [%s]' "$a"; done; printf '\n'; } >>"$FAKE_LOG"
EOT
  chmod +x "$BATS_TEST_TMPDIR/lbin/locksql"
  export PATH="$BATS_TEST_TMPDIR/lbin:$PATH"
}

@test "lea locksql delegates the console to locksql's launcher, after its own checks" {
  export LEA_LOCKSQL_VAULT_ADDR=https://vault.example.com
  run lea_locksql --profile uat </dev/null
  [ "$status" -eq 0 ]
  r=$(cd "$P" && pwd -P)
  run calls
  [[ "$output" == *"docker [ps] [--filter] [label=lea.role=session]"* ]]
  [[ "$output" == *"launcher pwd=$r image=locksql:0.9.0 ro=1 name=locksql-console vault=https://vault.example.com [console] [--project] [$P] [--profile] [uat]"* ]]
  [[ "$output" != *"docker [run]"* ]]
  # Its checks still stop it first.
  : >"$FAKE_LOG"; ln -s "$HOME/.ssh" "$P/.locksql/k"
  run lea_locksql </dev/null
  [ "$status" -ne 0 ]
  run calls
  [[ "$output" != *"launcher"* ]]
}

# launcher_refused: no console (no launcher call, no docker run of lea's), and lea says why.
launcher_refused() {
  : >"$FAKE_LOG"
  run lea_locksql </dev/null
  [ "$status" -ne 0 ]
  [[ "$output" == *"no trusted locksql launcher on PATH"* ]]
  run calls
  [[ "$output" != *"launcher pwd"* && "$output" != *"docker [run]"* ]]
}

@test "lea locksql opens no console with a launcher that cannot confine it" {
  sed -i '/--cap-drop ALL/d' "$BATS_TEST_TMPDIR/lbin/locksql"
  launcher_refused
}

@test "lea locksql trusts no launcher an agent or another account could have written" {
  l="$BATS_TEST_TMPDIR/lbin/locksql"
  # Writable by its group or by others, or in such a directory.
  chmod 0775 "$l"; launcher_refused
  chmod 0757 "$l"; launcher_refused
  chmod 0755 "$l"; chmod 0777 "$BATS_TEST_TMPDIR/lbin"; launcher_refused
  chmod 0755 "$BATS_TEST_TMPDIR/lbin"
  # Owned by another account than this one or root (here: this one is someone else). Run as root,
  # the file is root's, which is trusted: nothing to check then.
  mkdir -p "$BATS_TEST_TMPDIR/idbin"
  printf '#!/bin/sh\n[ "$1" = -u ] && echo 4242 && exit 0\nexec /usr/bin/id "$@"\n' >"$BATS_TEST_TMPDIR/idbin/id"
  chmod +x "$BATS_TEST_TMPDIR/idbin/id"
  if [ "$(id -u)" != 0 ]; then PATH="$BATS_TEST_TMPDIR/idbin:$PATH" launcher_refused; fi
  # In the projects directory (the agents write it), or in lea's data directory, even through a
  # link from a trusted directory.
  mkdir -p "$P/bin"; cp -p "$l" "$P/bin/locksql"; rm "$l"; ln -s "$P/bin/locksql" "$l"
  launcher_refused
  rm "$l"; d=$(lea_data_dir)/bin; mkdir -p "$d"; cp -p "$P/bin/locksql" "$d/locksql"
  export PATH="$d:$PATH"
  launcher_refused
  # In the projects directory of another running lea agent (its lea.projects label).
  o="$BATS_TEST_TMPDIR/other-projects"; mkdir -p "$o/bin"; cp -p "$d/locksql" "$o/bin/locksql"
  export PATH="${PATH#"$d:"}"
  export PATH="$o/bin:$PATH" FAKE_DOCKER_OUT_PS___FILTER="c9 $o"
  launcher_refused
  export PATH="${PATH#"$o/bin:"}"; unset FAKE_DOCKER_OUT_PS___FILTER
  # Under a directory, any one up to /, that its group or others may write (a sticky one, such as
  # /tmp, is fine: nobody else can move an entry of this account there).
  g="$BATS_TEST_TMPDIR/g"; mkdir -p "$g/sub/bin"; cp -p "$d/locksql" "$g/sub/bin/locksql"; chmod 0775 "$g"
  export PATH="$g/sub/bin:$PATH"
  launcher_refused
  export PATH="${PATH#"$g/sub/bin:"}"
  # The same launcher elsewhere, owned by this account, mode 0755: it gets the console.
  cp -p "$d/locksql" "$l"
  : >"$FAKE_LOG"
  run lea_locksql </dev/null
  [ "$status" -eq 0 ]
  run calls
  [[ "$output" == *"launcher pwd="* ]]
}

@test "lea locksql gives up on a launcher behind a cycle of links" {
  mkdir -p "$BATS_TEST_TMPDIR/cbin"
  ln -s "$BATS_TEST_TMPDIR/cbin/b" "$BATS_TEST_TMPDIR/cbin/locksql"
  ln -s "$BATS_TEST_TMPDIR/cbin/locksql" "$BATS_TEST_TMPDIR/cbin/b"
  export PATH="$BATS_TEST_TMPDIR/cbin:$PATH"
  # command -v finds no such locksql: it could only meet the cycle after a link changed under it.
  run timeout 20 sh -c '. "$1"/libexec/lib/common.sh; . "$1"/libexec/lib/locksql.sh
    cyc=$2/locksql; command() { printf "%s\n" "$cyc"; }
    lea__locksql_launcher; echo "end:$?"' _ "$LEA_ROOT" "$BATS_TEST_TMPDIR/cbin"
  [ "$status" -eq 0 ]
  [ "$output" = "end:0" ]
}

@test "lea locksql never takes another locksql on PATH for the launcher" {
  mkdir -p "$BATS_TEST_TMPDIR/obin"
  printf '#!/bin/sh\necho other >>"$FAKE_LOG"\n' >"$BATS_TEST_TMPDIR/obin/locksql"
  chmod +x "$BATS_TEST_TMPDIR/obin/locksql"
  export PATH="$BATS_TEST_TMPDIR/obin:$PATH"
  launcher_refused
  run calls
  [[ "$output" != *"other"* ]]
}
