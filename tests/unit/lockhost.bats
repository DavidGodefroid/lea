load helpers
setup() {
  setup_fakes; load_lea
  P="$BATS_TEST_TMPDIR/projects"; mkdir -p "$P"
  export LEA_PROJECTS_DIR="$P"
  export LEA_CONFIG="$BATS_TEST_TMPDIR/user.conf"
  B="$BATS_TEST_TMPDIR/bin"; mkdir -p "$B"
}

# launcher TOOL: TOOL's launcher on the PATH (its install.sh put it there); it logs its calls.
launcher() {
  printf '#!/bin/sh\n{ printf "%s"; for a in "$@"; do printf " [%%s]" "$a"; done; printf "\\n"; } >>"$FAKE_LOG"\n' "$1" >"$B/$1"
  chmod 0755 "$B/$1"
  PATH="$B:$PATH"
}

@test "lockfile below v0.1.1 is never safe (v0.1.0: plugin sandbox escape)" {
  for v in v0.1.0 v0.0.9 v0.1 latest ''; do if lea__lockfile_safe "$v"; then false; fi; done
  for v in v0.1.1 v0.2.0 v0.10.0 v1.0.0; do lea__lockfile_safe "$v"; done
}

@test "lockmq and lockfile are off by default, pinned to releases" {
  [ "$LEA_LOCKMQ" = 0 ]
  [ "$LEA_LOCKFILE" = 0 ]
  lea__semver "$LEA_LOCKMQ_REF" >/dev/null
  lea__semver "$LEA_LOCKFILE_REF" >/dev/null
}

@test "the configuration refuses a bad switch, an unpinned ref and lockfile v0.1.0" {
  for c in LEA_LOCKMQ=yes LEA_LOCKFILE=2 LEA_LOCKMQ_REF=latest LEA_LOCKFILE_REF=main LEA_LOCKFILE_REF=v0.1.0; do
    echo "$c" >"$LEA_CONFIG"
    run lea_load_config
    [ "$status" -ne 0 ]
  done
  [[ "$output" == *"lockfile v0.1.0 lets a plugin escape its sandbox"* ]]
}

@test "the image's clients are the releases lea pins, with the tools' own accounts and no host account" {
  c="$LEA_ROOT/image/Containerfile"
  grep -q "MQ=${LEA_LOCKMQ_REF#v} LF=${LEA_LOCKFILE_REF#v}" "$c"
  grep -q 'groupadd -g 10010 lockmq-clients && useradd -r -u 10011 ' "$c"
  grep -q 'groupadd -g 10030 lockfile-clients && useradd -r -u 10031 ' "$c"
  grep -q 'install -d -m 2710 -o 10011 -g 10010 /run/lockmq' "$c"
  grep -q 'install -d -m 2750 -o 10031 -g 10030 /run/lockfile' "$c"
  if grep -q 'lock-host\|lock_host' "$c" "$LEA_ROOT/image/entrypoint"; then false; fi
  grep -q 'usermod -aG lockmq-clients' "$LEA_ROOT/image/entrypoint"
  grep -q 'usermod -aG lockfile-clients' "$LEA_ROOT/image/entrypoint"
}

@test "lea lockmq: off, it says how to turn it on; it never starts anything" {
  run lea_lockhost lockmq
  [ "$status" -eq 0 ]
  [[ "$output" == *"lockmq is off (LEA_LOCKMQ=0): lea init --lockmq turns it on"* ]]
  [ -z "$(calls)" ]
}

@test "lea lockfile: on, it reports the launcher and that the console runs under lox up only" {
  LEA_LOCKFILE=1
  run lea_lockhost lockfile
  [[ "$output" == *"lockfile: install its launcher"*"scripts/install.sh"* ]]
  [[ "$output" == *"lockfile console: it runs under lox up only: add a lockfile lock"*"allowed_uids: [$(id -u)]"*"run \`lox up\`"* ]]
  [[ "$output" != *"lockfile-run"* ]]
  launcher lockfile
  run lea_lockhost lockfile
  [[ "$output" == *"lockfile launcher: $B/lockfile"* ]]
  if calls | grep -q '^lockfile\|sudo\|migrate-ids\|docker'; then false; fi
}

@test "lea lockmq takes no argument: no way to pass flags to a console" {
  LEA_LOCKMQ=1
  run lea_lockhost lockmq console --auto
  [ "$status" -ne 0 ]
  [[ "$output" == *"lea never starts the console"* ]]
  [ -z "$(calls)" ]
}

@test "lea-sh lockmq refuses arguments before any lea function" {
  run "$LEA_ROOT/libexec/lea-sh" lockmq --auto
  [ "$status" -eq 2 ]
  [[ "$output" == *"usage: lea-sh"* ]]
}

@test "lea init without a terminal never asks and leaves the tools off" {
  LEA_INIT_NONINTERACTIVE=1 lea__init_lockhost lockmq </dev/null
  LEA_INIT_NONINTERACTIVE=1 lea__init_lockhost lockfile </dev/null
  [ "$LEA_LOCKMQ" = 0 ]
  [ "$LEA_LOCKFILE" = 0 ]
  [ -z "$(calls)" ]
}

@test "lea init asks once, no by default, and remembers the answer" {
  lea__init_tty() { true; }
  run lea__init_lockhost lockmq <<<""
  [[ "$output" == *"through a lockmq console"*"[y/N]"* ]]
  grep -qx "LEA_LOCKMQ='0'" "$LEA_CONFIG"
  run lea__init_lockhost lockmq <<<"y"
  [[ "$output" != *"[y/N]"* ]]
  [ -z "$(calls)" ]
}

@test "lea init --lockmq without the launcher: how to install it, never fatal, nothing installed" {
  lea__lockhost_set lockmq 1
  LEA_INIT_NONINTERACTIVE=1 run lea__init_lockhost lockmq </dev/null
  [ "$status" -eq 0 ]
  [[ "$output" == *"lockmq: install its launcher"* ]]
  [[ "$output" == *"lockmq console: it runs under lox up only"* ]]
  if calls | grep -q 'sudo\|install'; then false; fi
}

@test "lea init with lockmq offers the profile wizard as you, in lea's image with only the projects dir, then the console hint" {
  lea__init_tty() { true; }
  lea__lockhost_set lockmq 1
  launcher lockmq
  run lea__init_lockhost lockmq <<<"y"
  [[ "$output" == *"lockmq has no broker profile for $P yet"* ]]
  [[ "$output" == *"lockmq console: it runs under lox up only"* ]]
  run calls
  [ "$output" = "docker [run] [-it] [--rm] [--network] [none] [--user] [$(id -u):$(id -g)] [-e] [HOME=/tmp] [-v] [$P:$P] [-w] [$P] [--entrypoint] [lockmq] [$LEA_IMAGE] [setup] [--project] [$P]" ]
}

@test "lea init never runs lockmq setup through a symlink the agents planted" {
  lea__init_tty() { true; }
  lea__lockhost_set lockmq 1
  launcher lockmq
  ln -s "$BATS_TEST_TMPDIR/elsewhere" "$P/.lockmq"
  run lea__init_lockhost lockmq <<<"y"
  [[ "$output" == *".lockmq or its config.toml is a symbolic link: lockmq setup not offered"* ]]
  [ -z "$(calls)" ]
  rm "$P/.lockmq"; mkdir "$P/.lockmq"; ln -s "$BATS_TEST_TMPDIR/elsewhere.toml" "$P/.lockmq/config.toml"
  run lea__init_lockhost lockmq <<<"y"
  [[ "$output" == *"symbolic link: lockmq setup not offered"* ]]
  [ -z "$(calls)" ]
}

@test "lea doctor reports the tools that are on, and only those" {
  run lea__doctor_lockhost lockmq
  [ -z "$output" ]
  LEA_LOCKFILE=1
  run lea__doctor_lockhost lockfile
  [[ "$output" == *"warn  lockfile launcher: not installed"* ]]
  launcher lockfile
  run lea__doctor_lockhost lockfile
  [[ "$output" == *"ok    lockfile launcher $B/lockfile"* ]]
  [[ "$output" != *"-run volume"* ]]
}

@test "the entrypoint refuses a session whose uid or gid is a lock console's id in the image" {
  for ids in "10011 4243" "4242 10030" "10001 4243" "4242 10010" "10031 4243" "4242 10000"; do
    set -- $ids
    run env LEA_UID="$1" LEA_GID="$2" LEA_USER=u HOME="$BATS_TEST_TMPDIR" LEA_PROXY_IP=10.0.0.2 sh "$LEA_ROOT/image/entrypoint" true
    [ "$status" -ne 0 ]
    [[ "$output" == *"refusing to run the session as uid $1 / gid $2: a lock console's id"* ]]
  done
}
