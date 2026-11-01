load helpers
setup() {
  setup_fakes
  S="$XDG_DATA_HOME/lea/state"; mkdir -p "$S"; echo '{}' >"$S/claude.json"
  P="$BATS_TEST_TMPDIR/projects"; mkdir -p "$P"
  export LEA_PROJECTS_DIR="$P"
}

@test "lea-sh runs only its listed commands" {
  run "$LEA_ROOT/libexec/lea-sh" lea_die boom
  [ "$status" -eq 2 ]
  [[ "$output" == *"usage: lea-sh"* ]]
  [[ "$output" != *"boom"* ]]
  run "$LEA_ROOT/libexec/lea-sh"
  [ "$status" -eq 2 ]
}

@test "lea-sh ensure-images pulls a missing image without the first-run prefix, and takes no argument" {
  run "$LEA_ROOT/libexec/lea-sh" ensure-images extra
  [ "$status" -eq 2 ]
  mkdir -p "$HOME/.config/lea"
  printf "LEA_IMAGE=ghcr.io/davidgodefroid/lea:0.5.0\nLEA_PROXY_IMAGE=ghcr.io/davidgodefroid/lea-proxy:0.5.0\n" >"$HOME/.config/lea/lea.conf"
  FAKE_DOCKER_RC_IMAGE_INSPECT=1 run "$LEA_ROOT/libexec/lea-sh" ensure-images
  [ "$status" -eq 0 ]
  [[ "$output" == *"lea: pulling ghcr.io/davidgodefroid/lea:0.5.0"* ]]
  [[ "$output" != *"first run"* ]]
}

@test "lea-sh refresh-images takes no argument, and refreshes the images and the locksql image" {
  run "$LEA_ROOT/libexec/lea-sh" refresh-images extra
  [ "$status" -eq 2 ]
  [[ "$output" == *"usage: lea-sh"*"refresh-images"* ]]
  export FAKE_DOCKER_RC_IMAGE_INSPECT=1
  mkdir -p "$HOME/.config/lea"; echo LEA_LOCKSQL=1 >>"$HOME/.config/lea/lea.conf"
  locksql_release
  run "$LEA_ROOT/libexec/lea-sh" refresh-images
  [ "$status" -eq 0 ]
  run calls
  [[ "$output" == *"docker [build] [-t] [lea:dev]"* ]]
  [[ "$output" == *"docker [build] [-t] [lea-proxy:dev]"* ]]
  [[ "$output" == *"docker [build] [-t] [locksql:0.13.0] [--build-arg] [ALLOWED_UIDS=$(id -u)] ["*"/src]"* ]]
}

@test "lea-sh scan-begin takes exactly one directory" {
  run "$LEA_ROOT/libexec/lea-sh" scan-begin
  [ "$status" -eq 2 ]
  run "$LEA_ROOT/libexec/lea-sh" scan-begin "$P" extra
  [ "$status" -eq 2 ]
}

@test "lea-sh scan-begin writes the start snapshot; scan-finish consumes it" {
  run "$LEA_ROOT/libexec/lea-sh" scan-begin "$P"
  [ "$status" -eq 0 ]
  ID=$(printf '%s' "$P" | sha256sum | cut -c1-10)
  [ -f "$XDG_DATA_HOME/lea/host/scan/$ID.start" ]
  run "$LEA_ROOT/libexec/lea-sh" scan-finish "$P"
  [ "$status" -eq 0 ]
  [ ! -e "$XDG_DATA_HOME/lea/host/scan/$ID.start" ]
}

@test "lea-sh scan-begin and scan-finish take one absolute directory" {
  cd "$BATS_TEST_TMPDIR"
  mkdir -p rel
  for a in -print -fprint rel ./rel ''; do
    run "$LEA_ROOT/libexec/lea-sh" scan-begin "$a"
    [ "$status" -eq 2 ]
    [[ "$output" == *"usage: lea-sh"* ]]
    run "$LEA_ROOT/libexec/lea-sh" scan-finish "$a"
    [ "$status" -eq 2 ]
  done
  [ ! -e "$BATS_TEST_TMPDIR/(" ]
  [ ! -d "$XDG_DATA_HOME/lea/host/scan" ]
}

# lea-sh runs the scans with errexit off (lea_scan_begin ... || exit 1, lea_scan_finish ... ||
# true, as the retired shell launcher did): it lists and reports exactly what the scan functions
# report when called that way, an unreadable hook included.
@test "lea-sh scans with errexit off when a hook is unreadable" {
  R="$P/repo"; git init -q "$R"
  printf 'a\n' >"$R/.git/hooks/a-hook"; printf 'z\n' >"$R/.git/hooks/z-hook"
  chmod 000 "$R/.git/hooks/a-hook"
  shell_scan() { # shell_scan DATA FUNCTION OR
    XDG_DATA_HOME=$1 sh -c 'set -eu; for f in "$LEA_ROOT"/libexec/lib/*.sh; do . "$f"; done; lea_load_config
      '"$2"' "$1" || '"$3" "$LEA_ROOT/libexec/lea-sh" "$P" 2>&1 # same $0: same error prefix
  }
  GO_DATA="$XDG_DATA_HOME"; SH_DATA="$BATS_TEST_TMPDIR/shdata"; mkdir -p "$SH_DATA"
  ID=$(printf '%s' "$P" | sha256sum | cut -c1-10)
  run "$LEA_ROOT/libexec/lea-sh" scan-begin "$P"
  [ "$status" -eq 0 ]
  shell_scan "$SH_DATA" lea_scan_begin "exit 1"
  cmp "$GO_DATA/lea/host/scan/$ID.start" "$SH_DATA/lea/host/scan/$ID.start"
  grep -q "^hook $R/.git/hooks/a-hook " "$GO_DATA/lea/host/scan/$ID.start"
  grep -q "^hook $R/.git/hooks/z-hook " "$GO_DATA/lea/host/scan/$ID.start"
  printf 'b\n' >"$R/.git/hooks/b-hook"; chmod 000 "$R/.git/hooks/b-hook"
  printf 'curl evil\n' >"$R/.git/hooks/z-hook"
  run "$LEA_ROOT/libexec/lea-sh" scan-finish "$P"
  [ "$status" -eq 0 ]
  expected=$(shell_scan "$SH_DATA" lea_scan_finish true)
  [ "$output" = "$expected" ]
  [[ "$output" == *"WARNING: files that the host executes changed"* ]]
  [[ "$output" == *"+hook $R/.git/hooks/z-hook "* ]]
  [ ! -e "$GO_DATA/lea/host/scan/$ID.start" ]
}

@test "lea-sh scans never load the configuration" {
  mkdir -p "$HOME/.config/lea"
  printf 'touch "%s/sourced"\n' "$BATS_TEST_TMPDIR" >"$HOME/.config/lea/lea.conf"
  run "$LEA_ROOT/libexec/lea-sh" scan-begin "$P"
  [ "$status" -eq 0 ]
  run "$LEA_ROOT/libexec/lea-sh" scan-finish "$P"
  [ "$status" -eq 0 ]
  [ ! -e "$BATS_TEST_TMPDIR/sourced" ]
}

# fake_root: a copy of the checkout whose build/lea is a fake that logs its arguments and exits
# with FAKE_CORE_RC.
fake_root() {
  R="$BATS_TEST_TMPDIR/root"
  mkdir -p "$R/build"
  cp -r "$LEA_ROOT/bin" "$LEA_ROOT/libexec" "$LEA_ROOT/config" "$R/"
  cat >"$R/build/lea" <<'EOT'
#!/bin/sh
{ printf 'core'; for a in "$@"; do printf ' [%s]' "$a"; done; printf '\n'; } >>"$FAKE_LOG"
exit "${FAKE_CORE_RC:-0}"
EOT
  chmod +x "$R/build/lea"
}

# fake_make: a make that logs its arguments and builds the fake build/lea (FAKE_MAKE_RC fails it).
fake_make() {
  mkdir -p "$BATS_TEST_TMPDIR/mbin"
  cat >"$BATS_TEST_TMPDIR/mbin/make" <<EOT
#!/bin/sh
echo "make \$*" >>"\$FAKE_LOG"
[ "\${FAKE_MAKE_RC:-0}" = 0 ] || exit "\$FAKE_MAKE_RC"
printf '#!/bin/sh\necho "core \$*" >>"\$FAKE_LOG"\n' >"$R/build/lea"; chmod +x "$R/build/lea"
EOT
  chmod +x "$BATS_TEST_TMPDIR/mbin/make"
  PATH="$BATS_TEST_TMPDIR/mbin:$PATH"
}

@test "bin/lea hands every command, as it is, to build/lea, with its status" {
  fake_root
  run "$R/bin/lea"
  [ "$status" -eq 0 ]
  run "$R/bin/lea" new -r dev "a b" -- --model x
  run "$R/bin/lea" stop --all
  run "$R/bin/lea" init
  FAKE_CORE_RC=3 run "$R/bin/lea" shell -- false
  [ "$status" -eq 3 ]
  run calls
  [ "$output" = "core
core [new] [-r] [dev] [a b] [--] [--model] [x]
core [stop] [--all]
core [init]
core [shell] [--] [false]" ]
}

# lea init links ~/.local/bin/lea to bin/lea: that link keeps working.
@test "bin/lea run through a symlink finds its checkout" {
  fake_root
  mkdir -p "$BATS_TEST_TMPDIR/home/.local/bin"
  ln -s "$R/bin/lea" "$BATS_TEST_TMPDIR/home/.local/bin/lea"
  cd "$BATS_TEST_TMPDIR"
  run "$BATS_TEST_TMPDIR/home/.local/bin/lea" doctor
  [ "$status" -eq 0 ]
  run calls
  [ "$output" = "core [doctor]" ]
}

@test "bin/lea builds a missing build/lea first, then hands over" {
  fake_root
  rm "$R/build/lea"
  fake_make
  run "$R/bin/lea" init
  [ "$status" -eq 0 ]
  [[ "$output" == *"lea: building the lea binary (make go-build)"* ]]
  run calls
  [ "$output" = "make -C $R go-build
core init" ]
}

@test "a failing build stops bin/lea" {
  fake_root
  rm "$R/build/lea"
  fake_make
  FAKE_MAKE_RC=2 run "$R/bin/lea" stop
  [ "$status" -eq 1 ]
  [[ "$output" == *"lea: error: make go-build failed"* ]]
  run calls
  [[ "$output" != *core* ]]
}

@test "an existing build/lea is run as it is, never rebuilt" {
  fake_root
  fake_make
  run "$R/bin/lea" stop
  [ "$status" -eq 0 ]
  run calls
  [ "$output" = "core [stop]" ]
}

@test "the shell left on the host is libexec/lea-sh and libexec/lib only" {
  [ ! -e "$LEA_ROOT/lib" ]
  for f in open deck container net sessions core claude_mounts clipboard; do [ ! -e "$LEA_ROOT/libexec/lib/$f.sh" ]; done
  run grep -c . "$LEA_ROOT/bin/lea"
  [ "$output" -lt 35 ] # a shim: find the checkout, build a missing build/lea, exec it
  run grep -E '^\. |lib/' "$LEA_ROOT/bin/lea"
  [ "$status" -eq 1 ]
}
