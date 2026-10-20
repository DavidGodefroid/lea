load helpers
setup() {
  setup_fakes; load_lea
  P="$BATS_TEST_TMPDIR/projects"; mkdir -p "$P"
  export LEA_PROJECTS_DIR="$P"
  export LEA_CONFIG="$BATS_TEST_TMPDIR/user.conf"
  R="$BATS_TEST_TMPDIR/host"; mkdir -p "$R/usr/local/bin" "$R/etc" "$R/run"
  export LEA_LOCKHOST_ROOT="$R" LEA_LOCKHOST_OWNER
  LEA_LOCKHOST_OWNER=$(id -u)
}

# host_bin TOOL VERSION-LINE: a TOOL in the host's /usr/local/bin that prints VERSION-LINE and logs
# its other calls.
host_bin() {
  printf '#!/bin/sh\nif [ "$1" = version ]; then echo "%s"; exit 0; fi\n{ printf "%s"; for a in "$@"; do printf " [%%s]" "$a"; done; printf "\\n"; } >>"$FAKE_LOG"\n' "$2" "$1" >"$R/usr/local/bin/$1"
  chmod 0755 "$R/usr/local/bin/$1"
}
host_setup() { mkdir -p "$R/etc/$1" "$R/run/$1"; : >"$R/etc/$1/system.toml"; }

@test "lockfile below v0.1.1 is never safe (v0.1.0: plugin sandbox escape)" {
  for v in v0.1.0 v0.0.9 v0.1 latest ''; do if lea__lockfile_safe "$v"; then false; fi; done
  for v in v0.1.1 v0.2.0 v0.10.0 v1.0.0; do lea__lockfile_safe "$v"; done
}

@test "lockmq and lockfile are off by default, pinned to releases" {
  [ "$LEA_LOCKMQ" = 0 ] && [ "$LEA_LOCKFILE" = 0 ]
  [ "$LEA_LOCKMQ_REF" = v0.1.0 ] && [ "$LEA_LOCKFILE_REF" = v0.2.0 ]
}

@test "the configuration refuses a bad switch, an unpinned ref and lockfile v0.1.0" {
  for c in LEA_LOCKMQ=yes LEA_LOCKFILE=2 LEA_LOCKMQ_REF=latest LEA_LOCKFILE_REF=main LEA_LOCKFILE_REF=v0.1.0; do
    echo "$c" >"$LEA_CONFIG"
    run lea_load_config
    [ "$status" -ne 0 ]
  done
  [[ "$output" == *"lockfile v0.1.0 lets a plugin escape its sandbox"* ]]
}

@test "the image's clients are the releases lea pins" {
  c="$LEA_ROOT/image/Containerfile"
  grep -q "MQ=${LEA_LOCKMQ_REF#v} LF=${LEA_LOCKFILE_REF#v}" "$c"
  grep -q 'echo "$mq  lockmq_' "$c" && grep -q 'echo "$lf  lockfile_' "$c"
  grep -q 'image/lock-host /usr/local/libexec/lea/' "$c"
  grep -q '^\. /usr/local/libexec/lea/lock-host$' "$LEA_ROOT/image/entrypoint"
}

@test "lea lockmq: off, it says how to turn it on; it never starts anything" {
  run lea_lockhost lockmq
  [ "$status" -eq 0 ]
  [[ "$output" == *"lockmq is off (LEA_LOCKMQ=0): lea init --lockmq turns it on"* ]]
  [ -z "$(calls)" ]
}

@test "lea lockfile: on, it reports the host setup and how to open the console in its account" {
  LEA_LOCKFILE=1
  run lea_lockhost lockfile
  [[ "$output" == *"lockfile: not installed in /usr/local/bin (lea init installs v0.2.0)"* ]]
  [[ "$output" == *"no separated setup: sudo lockfile install --client"* ]]
  [[ "$output" == *"open a session of the lockfile account"*"lockfile console --project $P --profile PROFILE"* ]]
  host_bin lockfile "lockfile v0.2.0"; host_setup lockfile
  run lea_lockhost lockfile
  [[ "$output" == *"lockfile on this host: v0.2.0 (lea pins v0.2.0)"* ]]
  [[ "$output" == *"separated setup present"* ]]
  [ -z "$(calls)" ]
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
  [ "$LEA_LOCKMQ" = 0 ] && [ "$LEA_LOCKFILE" = 0 ]
  [ -z "$(calls)" ]
}

@test "lea init asks once, no by default, and remembers the answer" {
  lea__init_tty() { true; }
  run lea__init_lockhost lockmq <<<""
  [[ "$output" == *"through a lockmq console on this host"*"[y/N]"* ]]
  grep -qx "LEA_LOCKMQ='0'" "$LEA_CONFIG"
  run lea__init_lockhost lockmq <<<"y"
  [[ "$output" != *"[y/N]"* ]]
  [ -z "$(calls)" ]
}

@test "lea init --lockmq: the release is offered, its absence is never fatal" {
  lea__init_tty() { true; }
  lea__lockhost_set lockmq 1
  run lea__init_lockhost lockmq <<<"n"
  [ "$status" -eq 0 ]
  [[ "$output" == *"lockmq: lea pins v0.1.0; this host has none"* ]]
  [[ "$output" == *"Install lockmq v0.1.0 there"* ]]
  [[ "$output" == *"not installed; lea's agents cannot reach a lockmq console yet"* ]]
  [ -z "$(calls)" ]
}

@test "lea init: a lockfile below v0.1.1 on the host is not wired" {
  lea__lockhost_set lockfile 1
  host_bin lockfile "lockfile v0.1.0"
  LEA_LOCKFILE_REF=v0.1.0 LEA_INIT_NONINTERACTIVE=1 run lea__init_lockhost lockfile </dev/null
  [[ "$output" == *"lockfile v0.1.0 lets a plugin escape its sandbox: lea does not wire it"* ]]
}

@test "lea init with lockmq set up offers the profile wizard as you, then the console hint" {
  lea__init_tty() { true; }
  lea__lockhost_set lockmq 1
  host_bin lockmq "lockmq 0.1.0"; host_setup lockmq
  run lea__init_lockhost lockmq <<<"y"
  [[ "$output" == *"lockmq has no broker profile for $P yet"* ]]
  [[ "$output" == *"lockmq console --project $P --profile PROFILE"* ]]
  run calls
  [ "$output" = "lockmq [setup] [--project] [$P]" ]
}

@test "lea init offers the separated setup, with the tool's own root script" {
  lea__init_tty() { true; }
  lea__lockhost_set lockfile 1
  host_bin lockfile "lockfile v0.2.0"
  run lea__init_lockhost lockfile <<<"y"
  [[ "$output" == *"no separated console account yet"* ]]
  run calls
  [[ "$output" == "sudo [$R/usr/local/bin/lockfile] [install] [--client] [$(lea_user_name)]" ]]
}

# release TOOL VERSION: the release archive of a TOOL printing VERSION, and its checksums.txt.
release() {
  d="$BATS_TEST_TMPDIR/rel"; mkdir -p "$d/x"
  printf '#!/bin/sh\necho "%s %s"\n' "$1" "$2" >"$d/x/$1"; chmod 0755 "$d/x/$1"
  a=${1}_${2#v}_linux_$(lea__lockhost_arch).tar.gz
  tar -czf "$d/$a" -C "$d/x" "$1"
  printf '%s  %s\n' "$(lea__sha256 "$d/$a")" "$a" >"$d/checksums.txt"
  : >"$d/checksums.txt.sigstore.json"
  export FAKE_CURL_DIR="$d"
}

@test "the host install checks the signature against the tool's release workflow at that tag" {
  release lockmq v0.1.0
  lea__lockhost_install lockmq v0.1.0
  run calls
  [[ "$output" == *"cosign [verify-blob] [--bundle] "*"[--certificate-identity-regexp] [^https://github\\.com/DavidGodefroid/lockmq/\\.github/workflows/release\\.yml@refs/tags/v0\\.1\\.0\$] [--certificate-oidc-issuer] [https://token.actions.githubusercontent.com]"* ]]
  [[ "$output" == *"sudo [install] [-m] [0755] [-o] [root] [-g] [root] "*"[$R/usr/local/bin/lockmq]"* ]]
  [[ "$output" == *"curl "*"[https://github.com/DavidGodefroid/lockmq/releases/download/v0.1.0/lockmq_0.1.0_linux_"* ]]
}

@test "nothing is installed when the signature, the checksum or the version is wrong" {
  release lockmq v0.1.0
  FAKE_COSIGN_RC=1 run lea__lockhost_install lockmq v0.1.0
  [ "$status" -ne 0 ]
  [[ "$output" == *"is not signed by lockmq's release workflow; nothing installed"* ]]
  if calls | grep -q '^sudo'; then false; fi
  release lockmq v0.1.0
  printf '0000  %s\n' "lockmq_0.1.0_linux_$(lea__lockhost_arch).tar.gz" >"$FAKE_CURL_DIR/checksums.txt"
  run lea__lockhost_install lockmq v0.1.0
  [[ "$output" == *"does not match the signed checksums.txt; nothing installed"* ]]
  if calls | grep -q '^sudo'; then false; fi
  release lockmq v0.0.1
  mv "$FAKE_CURL_DIR/lockmq_0.0.1_linux_$(lea__lockhost_arch).tar.gz" "$FAKE_CURL_DIR/lockmq_0.1.0_linux_$(lea__lockhost_arch).tar.gz"
  sed -i 's/0\.0\.1/0.1.0/' "$FAKE_CURL_DIR/checksums.txt"
  run lea__lockhost_install lockmq v0.1.0
  [[ "$output" == *"holds no lockmq v0.1.0; nothing installed"* ]]
  if calls | grep -q '^sudo'; then false; fi
}

@test "a binary that is a symlink, not root's, or writable by others (or in such a dir) is not the host's tool" {
  host_bin lockmq "lockmq 0.1.0"
  lea__lockhost_bin lockmq
  LEA_LOCKHOST_OWNER=12345 run lea__lockhost_bin lockmq
  [ "$status" -ne 0 ]
  chmod 0775 "$R/usr/local/bin/lockmq"
  run lea__lockhost_bin lockmq
  [ "$status" -ne 0 ]
  chmod 0757 "$R/usr/local/bin/lockmq"
  run lea__lockhost_bin lockmq
  [ "$status" -ne 0 ]
  chmod 0755 "$R/usr/local/bin/lockmq"; chmod 0777 "$R/usr/local/bin"
  run lea__lockhost_bin lockmq
  [ "$status" -ne 0 ]
  chmod 0755 "$R/usr/local/bin"
  lea__lockhost_bin lockmq
  mv "$R/usr/local/bin/lockmq" "$R/usr/local/bin/real"; ln -s real "$R/usr/local/bin/lockmq"
  run lea__lockhost_bin lockmq
  [ "$status" -ne 0 ]
}

@test "lea doctor reports the tools that are on, and only those" {
  run lea__doctor_lockhost lockmq
  [ -z "$output" ]
  LEA_LOCKFILE=1
  host_bin lockfile "lockfile v0.1.0"
  run lea__doctor_lockhost lockfile
  [[ "$output" == *"warn  lockfile binary: v0.1.0: below v0.1.1"* ]]
  [[ "$output" == *"warn  lockfile separated setup: none"* ]]
  host_bin lockfile "lockfile v0.2.0"; host_setup lockfile
  run lea__doctor_lockhost lockfile
  [[ "$output" == *"ok    lockfile binary v0.2.0"*"ok    lockfile separated setup"*"ok    lockfile socket directory /run/lockfile"* ]]
}
