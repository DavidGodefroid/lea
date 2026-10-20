load helpers
# The installer (install.sh) against a fake release: curl serves files from $REL, uname answers
# FAKE_UNAME_S / FAKE_UNAME_M, docker and tmux are the fakes. PATH holds only the fakes and the
# system directories, so a tool left out of $B is missing (cosign too, unless with_cosign).
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

# release VERSION: lea_<version>_linux_amd64.tar.gz (a bin/lea that logs its arguments), its
# checksums.txt and checksums.txt.sigstore.json (a stand-in bundle the fake cosign accepts).
release() {
  d="$BATS_TEST_TMPDIR/src/lea-$1"; mkdir -p "$d/bin"
  printf '#!/bin/sh\n{ printf lea; for a in "$@"; do printf " [%%s]" "$a"; done; printf "\\n"; } >>"$FAKE_LOG"\n' >"$d/bin/lea"
  chmod +x "$d/bin/lea"
  a="lea_${1#v}_linux_amd64.tar.gz"
  tar -czf "$REL/$a" -C "$BATS_TEST_TMPDIR/src" "lea-$1"
  (cd "$REL" && sha256sum "$a" >checksums.txt)
  echo '{}' >"$REL/checksums.txt.sigstore.json"
}
# with_cosign: puts the fake cosign (tests/fakes/cosign, exits FAKE_COSIGN_RC) on PATH.
with_cosign() { ln -s "$LEA_ROOT/tests/fakes/cosign" "$B/cosign"; }
# nothing_installed: no LEA_INSTALL_DIR/v1.2.3, no link, no lea init.
nothing_installed() {
  [ ! -e "$LEA_INSTALL_DIR/v1.2.3" ]
  [ ! -e "$LEA_BIN_DIR/lea" ] && [ ! -L "$LEA_BIN_DIR/lea" ]
  ! grep -q '^lea ' "$FAKE_LOG"
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

@test "--no-link unpacks the release and leaves the links as they are (the background update links it later)" {
  sh "$INSTALL" --version v1.2.3 --no-init 2>/dev/null
  release v1.3.0
  run sh "$INSTALL" --version v1.3.0 --no-init --no-link
  [ "$status" -eq 0 ]
  [ -x "$LEA_INSTALL_DIR/v1.3.0/bin/lea" ]
  [ "$(readlink "$LEA_BIN_DIR/lea")" = "$LEA_INSTALL_DIR/v1.2.3/bin/lea" ]
  [[ "$output" == *"installed lea v1.3.0 in $LEA_INSTALL_DIR/v1.3.0, not linked"* ]]
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

@test "with cosign, a valid checksums.txt signature installs, anchored to release.yml on a v* tag" {
  if command -v cosign >/dev/null; then skip "cosign is installed in the test image"; fi
  with_cosign
  run sh "$INSTALL" --version v1.2.3
  [ "$status" -eq 0 ]
  [[ "$output" == *"checksums.txt signature verified"* ]]
  [[ "$output" != *"cosign is missing"* ]]
  [ -x "$LEA_INSTALL_DIR/v1.2.3/bin/lea" ]
  [ -L "$LEA_BIN_DIR/lea" ]
  run calls
  [[ "$output" == *"curl [-fsSL] [-o] ["*"/checksums.txt.sigstore.json] [https://github.com/DavidGodefroid/lea/releases/download/v1.2.3/checksums.txt.sigstore.json]"* ]]
  line=$(grep '^cosign ' "$FAKE_LOG")
  [[ "$line" == "cosign [verify-blob] [--bundle] ["*"/checksums.txt.sigstore.json] "* ]]
  [[ "$line" == *" [--certificate-identity-regexp] [^https://github\.com/DavidGodefroid/lea/\.github/workflows/release\.yml@refs/tags/v[0-9]] "* ]]
  [[ "$line" == *" [--certificate-oidc-issuer] [https://token.actions.githubusercontent.com] "* ]]
  [[ "$line" == *"/checksums.txt]" ]]
  [[ "$output" == *$'\nlea [init]'* ]]
}

@test "with cosign, a bad checksums.txt signature is refused and nothing is installed" {
  if command -v cosign >/dev/null; then skip "cosign is installed in the test image"; fi
  with_cosign
  FAKE_COSIGN_RC=1 run sh "$INSTALL" --version v1.2.3
  [ "$status" -eq 1 ]
  [[ "$output" == *"checksums.txt does not match its Sigstore signature"* ]]
  nothing_installed
}

@test "with cosign, a signed release (v0.2.0) without its bundle is refused and nothing is installed" {
  if command -v cosign >/dev/null; then skip "cosign is installed in the test image"; fi
  with_cosign
  release v0.2.0
  rm "$REL/checksums.txt.sigstore.json"
  run sh "$INSTALL" --version v0.2.0
  [ "$status" -eq 1 ]
  [[ "$output" == *"cannot download the Sigstore bundle "*"/v0.2.0/checksums.txt.sigstore.json"* ]]
  [[ "$output" != *"pre-signing version"* ]]
  [ ! -e "$LEA_INSTALL_DIR/v0.2.0" ]
  nothing_installed
  run calls
  [[ "$output" != *"cosign "* ]]
}

# genuine_v011: a v0.1.1 release with no bundle, whose checksums.txt is the genuine one of the
# real v0.1.1 release (the SHA-256 install.sh pins); the archive is the stand-in one.
genuine_v011() {
  release v0.1.1
  rm "$REL/checksums.txt.sigstore.json"
  cat >"$REL/checksums.txt" <<'EOT'
74a57703a9c27f7a0fdffe579f8e5a2dde64c4f258fd7d106003f86efe35963c  lea_0.1.1_darwin_amd64.tar.gz
06f548f565279f837e91f866bf98e89d07d40074e6ed1de3fc7e603138e87bab  lea_0.1.1_darwin_arm64.tar.gz
16961dc2363ad3b041fd8ceb6e27afbed87758500f9c4f2cb05b6dfc2fca4a62  lea_0.1.1_linux_amd64.tar.gz
75af3ada34c489a006a5bcef16b43329de744ac2ab59fb7f1695b769aeb5c3cd  lea_0.1.1_linux_arm64.tar.gz
e5ef4d68810641275114e71d04e20133c67b358fa3021c9a3be2474dee3b5c21  install.sh
EOT
  [ "$(sha256sum "$REL/checksums.txt" | cut -d' ' -f1)" = e7fd06c427e20cd43f30b09f98cd5ac2dd23b1048256d81b73516c1b3e01398f ]
}
legacy_warning="warning: no Sigstore bundle for this release (pre-signing version), genuine checksums.txt (pinned SHA-256), checksum only"

@test "install.sh pins the SHA-256 of the genuine checksums.txt of v0.1.0 and v0.1.1" {
  grep -qx '    v0\.1\.0) pin=f1fb4cce4cdc570bd59b433b4f6b5e0f1044e48d659138013097aa3490fca2da ;;' "$INSTALL"
  grep -qx '    v0\.1\.1) pin=e7fd06c427e20cd43f30b09f98cd5ac2dd23b1048256d81b73516c1b3e01398f ;;' "$INSTALL"
}

@test "with cosign, v0.1.1 without a bundle accepts its genuine checksums.txt, with a warning" {
  if command -v cosign >/dev/null; then skip "cosign is installed in the test image"; fi
  with_cosign
  genuine_v011
  # The genuine checksums.txt passes the pin. The stand-in archive is not the genuine one, so the
  # archive checksum stops the install right after.
  run sh -c 'sh "$1" --version v0.1.1 --no-init 2>&1 >/dev/null' _ "$INSTALL"
  [ "$status" -eq 1 ]
  [[ "$output" == *"$legacy_warning"* ]]
  [[ "$output" != *"is not the genuine one"* ]]
  [[ "$output" == *"lea_0.1.1_linux_amd64.tar.gz does not match its checksum"* ]]
  [ ! -e "$LEA_INSTALL_DIR/v0.1.1" ]
  ! grep -q '^cosign ' "$FAKE_LOG"
}

@test "with cosign, v0.1.1 without a bundle installs when checksums.txt matches its pin, with a warning" {
  if command -v cosign >/dev/null; then skip "cosign is installed in the test image"; fi
  with_cosign
  release v0.1.1
  rm "$REL/checksums.txt.sigstore.json"
  # The genuine archive cannot be served here: a copy of install.sh pins this release's
  # checksums.txt for v0.1.1 instead, and the rest of the script is unchanged.
  pin=$(sha256sum "$REL/checksums.txt" | cut -d' ' -f1)
  sed "s/^\(    v0\.1\.1) pin=\)[0-9a-f]*/\1$pin/" "$INSTALL" >"$BATS_TEST_TMPDIR/install.sh"
  grep -qx "    v0\.1\.1) pin=$pin ;;" "$BATS_TEST_TMPDIR/install.sh"
  # stderr only: the warning must reach the terminal even when stdout is piped away.
  run sh -c 'sh "$1" --version v0.1.1 --no-init 2>&1 >/dev/null' _ "$BATS_TEST_TMPDIR/install.sh"
  [ "$status" -eq 0 ]
  [[ "$output" == *"$legacy_warning"* ]]
  [[ "$output" != *"signature verified"* ]]
  [ -x "$LEA_INSTALL_DIR/v0.1.1/bin/lea" ]
  [ "$(readlink "$LEA_BIN_DIR/lea")" = "$LEA_INSTALL_DIR/v0.1.1/bin/lea" ]
  ! grep -q '^cosign ' "$FAKE_LOG"
}

@test "with cosign, v0.1.1 without a bundle refuses an altered checksums.txt and installs nothing" {
  if command -v cosign >/dev/null; then skip "cosign is installed in the test image"; fi
  with_cosign
  genuine_v011
  # A forged release: the genuine checksums.txt with the linux/amd64 line rewritten to match a
  # tampered archive (the stand-in one).
  a=lea_0.1.1_linux_amd64.tar.gz
  forged=$(sha256sum "$REL/$a" | cut -d' ' -f1)
  sed "s/^[0-9a-f]*  $a\$/$forged  $a/" "$REL/checksums.txt" >"$BATS_TEST_TMPDIR/forged"
  mv "$BATS_TEST_TMPDIR/forged" "$REL/checksums.txt"
  grep -qx "$forged  $a" "$REL/checksums.txt"
  run sh "$INSTALL" --version v0.1.1
  [ "$status" -eq 1 ]
  [[ "$output" == *"checksums.txt of v0.1.1 (released before signing, no Sigstore bundle) is not the genuine one"* ]]
  [[ "$output" != *"warning: no Sigstore bundle"* ]]
  [ ! -e "$LEA_INSTALL_DIR/v0.1.1" ]
  nothing_installed
  ! grep -q '^cosign ' "$FAKE_LOG"
}

@test "with cosign, v0.1.0 without a bundle refuses a checksums.txt that is not the genuine one" {
  if command -v cosign >/dev/null; then skip "cosign is installed in the test image"; fi
  with_cosign
  release v0.1.0
  rm "$REL/checksums.txt.sigstore.json"
  run sh "$INSTALL" --version v0.1.0
  [ "$status" -eq 1 ]
  [[ "$output" == *"checksums.txt of v0.1.0 (released before signing, no Sigstore bundle) is not the genuine one"* ]]
  [ ! -e "$LEA_INSTALL_DIR/v0.1.0" ]
  nothing_installed
}

@test "LEA_REQUIRE_COSIGN=1 with cosign refuses a release without a bundle, even one before signing" {
  if command -v cosign >/dev/null; then skip "cosign is installed in the test image"; fi
  with_cosign
  rm "$REL/checksums.txt.sigstore.json"
  LEA_REQUIRE_COSIGN=1 run sh "$INSTALL" --version v1.2.3
  [ "$status" -eq 1 ]
  [[ "$output" == *"cannot download the Sigstore bundle "*"LEA_REQUIRE_COSIGN=1"* ]]
  nothing_installed
  release v0.1.1
  rm "$REL/checksums.txt.sigstore.json"
  LEA_REQUIRE_COSIGN=1 run sh "$INSTALL" --version v0.1.1
  [ "$status" -eq 1 ]
  [[ "$output" == *"LEA_REQUIRE_COSIGN=1"* ]]
  [[ "$output" != *"pre-signing version"* ]]
  [ ! -e "$LEA_INSTALL_DIR/v0.1.1" ]
  ! grep -q '^cosign ' "$FAKE_LOG"
}

@test "without cosign, the installer warns and installs on the checksum alone" {
  if command -v cosign >/dev/null; then skip "cosign is installed in the test image"; fi
  run sh "$INSTALL" --version v1.2.3
  [ "$status" -eq 0 ]
  [[ "$output" == *"warning: cosign is missing, checksums.txt signature not verified (checksum only)"* ]]
  [ -x "$LEA_INSTALL_DIR/v1.2.3/bin/lea" ]
  [ -L "$LEA_BIN_DIR/lea" ]
  run calls
  [[ "$output" != *".sigstore.json"* ]]
}

@test "without cosign, LEA_REQUIRE_COSIGN=1 is refused before anything is downloaded or installed" {
  if command -v cosign >/dev/null; then skip "cosign is installed in the test image"; fi
  LEA_REQUIRE_COSIGN=1 run sh "$INSTALL" --version v1.2.3
  [ "$status" -eq 1 ]
  [[ "$output" == *"cosign is missing and LEA_REQUIRE_COSIGN=1"* ]]
  nothing_installed
  [ ! -e "$LEA_INSTALL_DIR" ]
  run calls
  [[ "$output" != *"curl "* ]]
}

@test "LEA_REQUIRE_COSIGN=1 with cosign installs after verifying the signature" {
  if command -v cosign >/dev/null; then skip "cosign is installed in the test image"; fi
  with_cosign
  LEA_REQUIRE_COSIGN=1 run sh "$INSTALL" --version v1.2.3 --no-init
  [ "$status" -eq 0 ]
  [ -L "$LEA_BIN_DIR/lea" ]
  grep -q '^cosign \[verify-blob\]' "$FAKE_LOG"
}

@test "--help documents LEA_REQUIRE_COSIGN and stops at the header" {
  run sh "$INSTALL" --help
  [ "$status" -eq 0 ]
  [[ "$output" == *"LEA_REQUIRE_COSIGN=1"* ]]
  [[ "$output" != *"set -eu"* ]]
}

@test "an unknown option is refused" {
  run sh "$INSTALL" --gitlab
  [ "$status" -eq 2 ]
  [[ "$output" == *"unknown option --gitlab"* ]]
}
