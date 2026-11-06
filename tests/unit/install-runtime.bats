load helpers
# tests/integration/install-runtime.sh extracts the Kata tarball into / and the gVisor tarball into
# /opt/gvisor as root: it must never do so without a pinned checksum. curl, sudo and uname are
# fakes; sudo logs and fails, so a test that reaches it (the extraction) fails loudly.
setup() {
  setup_fakes
  S="$LEA_ROOT/tests/integration/install-runtime.sh"
  B="$BATS_TEST_TMPDIR/ibin"; mkdir -p "$B"
  cat >"$B/curl" <<'EOT'
#!/bin/sh
for a in "$@"; do u=$a; done
echo "curl $u" >>"$FAKE_LOG"
printf '%s' "${FAKE_TARBALL:-tampered}" >"${u##*/}"
EOT
  cat >"$B/sudo" <<'EOT'
#!/bin/sh
echo "sudo $*" >>"$FAKE_LOG"
exit 99
EOT
  cat >"$B/uname" <<'EOT'
#!/bin/sh
echo "${FAKE_ARCH:-x86_64}"
EOT
  chmod +x "$B"/*
  export PATH="$B:$PATH"
  unset KATA_SHA256 KATA_VERSION GVISOR_SHA512 GVISOR_RELEASE
}

@test "install-runtime kata pins the sha256 of the 4.3.1 amd64 and arm64 tarballs" {
  grep -q '4.3.1-amd64) sha=1463a2b86845f4d3d8e87f2f764ec59856fc0fee3520967a79ade32762be2e97 ;;' "$S"
  grep -q '4.3.1-arm64) sha=2c5bf7cde334ad5307d482f45cceda496f91c9b6b96f5278c47f4f4eac1bbae6 ;;' "$S"
}

@test "install-runtime kata refuses a tarball whose sha256 does not match the pin" {
  run sh "$S" kata
  [ "$status" -eq 1 ]
  [[ "$output" == *"kata-static-4.3.1-amd64.tar.zst: sha256 mismatch"* ]]
  grep -q '^curl https://github.com/kata-containers/kata-containers/releases/download/4.3.1/kata-static-4.3.1-amd64.tar.zst$' "$FAKE_LOG"
  ! grep -q '^sudo' "$FAKE_LOG"
}

@test "install-runtime kata refuses an unpinned version or architecture without KATA_SHA256, before downloading" {
  KATA_VERSION=9.9.9 run sh "$S" kata
  [ "$status" -eq 1 ]
  [[ "$output" == *"no pinned sha256 for kata-static-9.9.9-amd64.tar.zst: set KATA_SHA256"* ]]
  FAKE_ARCH=riscv64 run sh "$S" kata
  [ "$status" -eq 1 ]
  [[ "$output" == *"no pinned sha256 for kata-static-4.3.1-riscv64.tar.zst"* ]]
  [ ! -s "$FAKE_LOG" ]
}

@test "install-runtime kata refuses a KATA_SHA256 that is not a sha256" {
  for bad in xyz ABCDEF "$(printf '%063d' 0)" "$(printf '%064d' 0)0"; do
    KATA_SHA256=$bad run sh "$S" kata
    [ "$status" -eq 1 ]
    [[ "$output" == *"KATA_SHA256 is not a lowercase hex sha256"* ]]
  done
  [ ! -s "$FAKE_LOG" ]
}

@test "install-runtime kata checks the download against KATA_SHA256 when it is set" {
  export FAKE_TARBALL=release KATA_VERSION=9.9.9
  # KATA_SHA256 of another content: refused, nothing extracted.
  KATA_SHA256=$(printf other | sha256sum | cut -d' ' -f1) run sh "$S" kata
  [ "$status" -eq 1 ]
  [[ "$output" == *"sha256 mismatch"* ]]
  ! grep -q '^sudo' "$FAKE_LOG"
  # The matching digest passes the check and reaches the extraction (the fake sudo).
  KATA_SHA256=$(printf release | sha256sum | cut -d' ' -f1) run sh "$S" kata
  [[ "$output" == *"kata-static-9.9.9-amd64.tar.zst: OK"* ]]
  grep -q '^sudo ' "$FAKE_LOG"
}

@test "install-runtime runsc pins the sha512 of the 20261102.0 x86_64 and aarch64 tarballs" {
  grep -q '20261102.0-x86_64) sha=2e6671bb2d033b871c5811ab57e16ac570988b0e0054c525f911cfaf191bab3ca4efb08e0a98daa4b9e7d248f9e5776ef2ef2f8a6b82d829646a42924c98bb05 ;;' "$S"
  grep -q '20261102.0-aarch64) sha=574b5775b4417618f63c8506e6bb237e370df85722832a5d0c3831382155e55e59d4cfa993a1e9bbe1b1cf04346ffd48b20aaa879c6ed9a52c680f762fd19980 ;;' "$S"
}

@test "install-runtime runsc never trusts the .sha512 published next to the tarball" {
  run sh "$S" runsc
  [ "$status" -eq 1 ]
  [[ "$output" == *"gVisor 20261102.0 x86_64: sha512 mismatch"* ]]
  grep -q '^curl https://storage.googleapis.com/gvisor/releases/release/20261102.0/x86_64/gvisor.tar.bz2$' "$FAKE_LOG"
  ! grep -q 'sha512$' "$FAKE_LOG"
  ! grep -q '^sudo' "$FAKE_LOG"
}

@test "install-runtime runsc refuses an unpinned release or architecture without GVISOR_SHA512, before downloading" {
  GVISOR_RELEASE=20990101.0 run sh "$S" runsc
  [ "$status" -eq 1 ]
  [[ "$output" == *"no pinned sha512 for gVisor 20990101.0 x86_64: set GVISOR_SHA512"* ]]
  FAKE_ARCH=riscv64 run sh "$S" runsc
  [ "$status" -eq 1 ]
  [[ "$output" == *"no pinned sha512 for gVisor 20261102.0 riscv64"* ]]
  [ ! -s "$FAKE_LOG" ]
}

@test "install-runtime runsc refuses a GVISOR_SHA512 that is not a sha512" {
  for bad in xyz ABCDEF "$(printf '%0127d' 0)" "$(printf '%0128d' 0)0"; do
    GVISOR_SHA512=$bad run sh "$S" runsc
    [ "$status" -eq 1 ]
    [[ "$output" == *"GVISOR_SHA512 is not a lowercase hex sha512"* ]]
  done
  [ ! -s "$FAKE_LOG" ]
}

@test "install-runtime runsc checks the download against GVISOR_SHA512 when it is set" {
  export FAKE_TARBALL=release GVISOR_RELEASE=20990101.0
  GVISOR_SHA512=$(printf other | sha512sum | cut -d' ' -f1) run sh "$S" runsc
  [ "$status" -eq 1 ]
  [[ "$output" == *"sha512 mismatch"* ]]
  ! grep -q '^sudo' "$FAKE_LOG"
  GVISOR_SHA512=$(printf release | sha512sum | cut -d' ' -f1) run sh "$S" runsc
  [[ "$output" == *"gvisor.tar.bz2: OK"* ]]
  grep -q '^sudo ' "$FAKE_LOG"
}
