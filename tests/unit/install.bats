load helpers
# The installer (install.sh) against a fake release: curl serves files from $REL, uname answers
# FAKE_UNAME_S / FAKE_UNAME_M, docker and tmux are the fakes. PATH holds only the fakes and the
# system directories, so a tool left out of $B is missing.
setup() {
  setup_fakes
  INSTALL="$LEA_ROOT/install.sh"
  B="$BATS_TEST_TMPDIR/b"; REL="$BATS_TEST_TMPDIR/release"; mkdir -p "$B" "$REL"
  export REL
  cat >"$B/curl" <<'EOT'
#!/bin/sh
{ printf 'curl'; for a in "$@"; do printf ' [%s]' "$a"; done; printf '\n'; } >>"$FAKE_LOG"
out= url= w=
while [ $# -gt 0 ]; do
  case $1 in -o) out=$2; shift ;; -w) w=1; shift ;; -*) ;; *) url=$1 ;; esac
  shift
done
if [ -n "$w" ]; then printf 'https://github.com/DavidGodefroid/lea/releases/tag/%s' "${FAKE_LATEST:-v1.2.3}"; exit 0; fi
[ -f "$REL/${url##*/}" ] || exit 22
cp "$REL/${url##*/}" "$out"
EOT
  cat >"$B/uname" <<'EOT'
#!/bin/sh
case $1 in -s) echo "${FAKE_UNAME_S:-Linux}" ;; -m) echo "${FAKE_UNAME_M:-x86_64}" ;; esac
EOT
  chmod +x "$B/curl" "$B/uname"
  ln -s "$LEA_ROOT/tests/fakes/docker" "$B/docker"
  fake_tmux "tmux 3.4"
  export PATH="$BATS_TEST_TMPDIR/tbin:$B:/usr/local/bin:/usr/bin:/bin"
  export LEA_INSTALL_DIR="$BATS_TEST_TMPDIR/inst" LEA_BIN_DIR="$BATS_TEST_TMPDIR/bin"
  release v1.2.3
}

# release VERSION: lea_<version>_linux_amd64.tar.gz (a bin/lea that logs its arguments) and its
# checksums.txt.
release() {
  d="$BATS_TEST_TMPDIR/src/lea-$1"; mkdir -p "$d/bin"
  printf '#!/bin/sh\n{ printf lea; for a in "$@"; do printf " [%%s]" "$a"; done; printf "\\n"; } >>"$FAKE_LOG"\n' >"$d/bin/lea"
  chmod +x "$d/bin/lea"
  a="lea_${1#v}_linux_amd64.tar.gz"
  tar -czf "$REL/$a" -C "$BATS_TEST_TMPDIR/src" "lea-$1"
  (cd "$REL" && sha256sum "$a" >checksums.txt)
}

@test "an unsupported system is refused" {
  FAKE_UNAME_S=FreeBSD run sh "$INSTALL" --version v1.2.3
  [ "$status" -eq 1 ]
  [[ "$output" == *"unsupported system FreeBSD"* ]]
  FAKE_UNAME_M=riscv64 run sh "$INSTALL" --version v1.2.3
  [ "$status" -eq 1 ]
  [[ "$output" == *"unsupported architecture riscv64"* ]]
  [ ! -e "$LEA_INSTALL_DIR" ]
}

@test "a missing docker is refused, and a docker that does not answer too" {
  rm "$B/docker"
  if command -v docker >/dev/null; then skip "docker is installed in the test image"; fi
  run sh "$INSTALL" --version v1.2.3
  [ "$status" -eq 1 ]
  [[ "$output" == *"Docker is missing"* ]]
  ln -s "$LEA_ROOT/tests/fakes/docker" "$B/docker"
  FAKE_DOCKER_RC_INFO_=1 run sh "$INSTALL" --version v1.2.3
  [ "$status" -eq 1 ]
  [[ "$output" == *"Docker does not answer"* ]]
  [ ! -e "$LEA_INSTALL_DIR" ]
}

@test "an archive that does not match its checksum is refused and nothing is installed" {
  printf '%064d  lea_1.2.3_linux_amd64.tar.gz\n' 0 >"$REL/checksums.txt"
  run sh "$INSTALL" --version v1.2.3
  [ "$status" -eq 1 ]
  [[ "$output" == *"lea_1.2.3_linux_amd64.tar.gz does not match its checksum"* ]]
  [ ! -e "$LEA_INSTALL_DIR/v1.2.3" ]
  [ ! -e "$LEA_BIN_DIR/lea" ]
}

@test "an archive missing from checksums.txt is refused" {
  : >"$REL/checksums.txt"
  run sh "$INSTALL" --version v1.2.3
  [ "$status" -eq 1 ]
  [[ "$output" == *"lea_1.2.3_linux_amd64.tar.gz is not in checksums.txt"* ]]
}

@test "a good archive is unpacked into LEA_INSTALL_DIR/<version>, linked as LEA_BIN_DIR/lea, then lea init runs" {
  run sh "$INSTALL" --version v1.2.3
  [ "$status" -eq 0 ]
  [ -x "$LEA_INSTALL_DIR/v1.2.3/bin/lea" ]
  [ "$(readlink "$LEA_BIN_DIR/lea")" = "$LEA_INSTALL_DIR/v1.2.3/bin/lea" ]
  [[ "$output" == *"installed lea v1.2.3 in $LEA_INSTALL_DIR/v1.2.3, linked as $LEA_BIN_DIR/lea"* ]]
  run calls
  [[ "$output" == *"curl [-fsSL] [-o] ["*"/lea_1.2.3_linux_amd64.tar.gz] [https://github.com/DavidGodefroid/lea/releases/download/v1.2.3/lea_1.2.3_linux_amd64.tar.gz]"* ]]
  [[ "$output" == *$'\nlea [init]'* ]]
}

@test "without --version the latest release is installed, and --no-locksql reaches lea init" {
  FAKE_LATEST=v1.2.3 run sh "$INSTALL" --no-locksql
  [ "$status" -eq 0 ]
  [ -x "$LEA_INSTALL_DIR/v1.2.3/bin/lea" ]
  run calls
  [[ "$output" == *"[https://github.com/DavidGodefroid/lea/releases/latest]"* ]]
  [[ "$output" == *"lea [init] [--no-locksql]"* ]]
}

@test "--no-init installs without running lea init" {
  run sh "$INSTALL" --version v1.2.3 --no-init
  [ "$status" -eq 0 ]
  [ -L "$LEA_BIN_DIR/lea" ]
  run calls
  [[ "$output" != *"lea [init]"* ]]
}

@test "a reinstall replaces the link to the new version" {
  sh "$INSTALL" --version v1.2.3 --no-init 2>/dev/null
  release v1.3.0
  run sh "$INSTALL" --version v1.3.0 --no-init
  [ "$status" -eq 0 ]
  [ "$(readlink "$LEA_BIN_DIR/lea")" = "$LEA_INSTALL_DIR/v1.3.0/bin/lea" ]
}

@test "an existing LEA_BIN_DIR/lea that is not a link is refused and kept" {
  mkdir -p "$LEA_BIN_DIR"; echo mine >"$LEA_BIN_DIR/lea"
  run sh "$INSTALL" --version v1.2.3
  [ "$status" -eq 1 ]
  [[ "$output" == *"$LEA_BIN_DIR/lea exists and is not a link"* ]]
  [ "$(cat "$LEA_BIN_DIR/lea")" = mine ]
  run calls
  [[ "$output" != *"lea [init]"* ]]
}

@test "an old tmux is named with its install hint, without failing the install" {
  export FAKE_TMUX_VERSION="tmux 3.1"
  run sh "$INSTALL" --version v1.2.3 --no-init
  [ "$status" -eq 0 ]
  [[ "$output" == *"lea needs tmux 3.2 or later"* ]]
  export FAKE_TMUX_VERSION="tmux 3.4"
  run sh "$INSTALL" --version v1.2.3 --no-init
  [[ "$output" != *"needs tmux"* ]]
}

@test "an unknown option is refused" {
  run sh "$INSTALL" --gitlab
  [ "$status" -eq 2 ]
  [[ "$output" == *"unknown option --gitlab"* ]]
}
