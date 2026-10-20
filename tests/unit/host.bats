load helpers
# libexec/lib/host.sh: the helpers init, update and doctor still call, kept from the retired shell
# launcher (its tmux resolution and projects-dir guard tests moved here from deck.bats and open.bats).
setup() {
  setup_fakes; load_lea
  OSR="$BATS_TEST_TMPDIR/os-release"
  export LEA_OS_RELEASE="$OSR"
}

@test "the tmux install hint follows os-release" {
  hint() { printf '%b' "$1" >"$OSR"; [ "$(lea_tmux_install_hint)" = "$2" ]; }
  hint 'ID=ubuntu\nID_LIKE=debian\n' "sudo apt install tmux"
  hint 'ID=pop\nID_LIKE="ubuntu debian"\n' "sudo apt install tmux"
  hint 'ID=fedora\n' "sudo dnf install tmux"
  hint 'ID=arch\n' "sudo pacman -S tmux"
  hint 'ID="opensuse-tumbleweed"\nID_LIKE="opensuse suse"\n' "sudo zypper install tmux"
  hint 'ID=alpine\n' "sudo apk add tmux"
  hint 'ID=nixos\n' "install tmux 3.2 or later with your package manager"
  rm -f "$OSR"
  [ "$(lea_tmux_install_hint)" = "install tmux 3.2 or later with your package manager" ]
  mkdir -p "$BATS_TEST_TMPDIR/ubin"; printf '#!/bin/sh\necho Darwin\n' >"$BATS_TEST_TMPDIR/ubin/uname"
  chmod +x "$BATS_TEST_TMPDIR/ubin/uname"
  PATH="$BATS_TEST_TMPDIR/ubin:$PATH" run lea_tmux_install_hint
  [ "$output" = "brew install tmux" ]
}

@test "tmux versions: 3.2 and later are accepted" {
  for v in "tmux 3.2" "tmux 3.2a" "tmux 3.4" "tmux 3.10" "tmux 4.0" "tmux next-3.6"; do lea_tmux_version_ok "$v"; done
  for v in "tmux 3.1c" "tmux 2.9" "tmux" "" "garbage 9.9"; do ! lea_tmux_version_ok "$v"; done
}

@test "lea_tmux_bin honours LEA_TMUX_BIN and fails when it is not executable" {
  fake_tmux
  [ "$(lea_tmux_bin)" = "$BATS_TEST_TMPDIR/tbin/tmux" ]
  LEA_TMUX_BIN="$BATS_TEST_TMPDIR/none"
  ! lea_tmux_bin
  LEA_TMUX_BIN="$BATS_TEST_TMPDIR/tbin/tmux"
  [ "$(lea_tmux_bin)" = "$LEA_TMUX_BIN" ]
}

# built_tmux [VERSION]: a lea-built tmux at $LEA_TMUX_BUILT.
built_tmux() {
  mkdir -p "$BATS_TEST_TMPDIR/b"; LEA_TMUX_BUILT="$BATS_TEST_TMPDIR/b/tmux"; export LEA_TMUX_BUILT
  printf '#!/bin/sh\necho "%s"\n' "${1:-tmux 3.5a}" >"$LEA_TMUX_BUILT"; chmod +x "$LEA_TMUX_BUILT"
}

@test "tmux resolution: a host tmux 3.2 or later wins over the lea-built one" {
  fake_tmux "tmux 3.4"; built_tmux
  [ "$(lea_tmux_bin)" = "$BATS_TEST_TMPDIR/tbin/tmux" ]
  [ "$(lea_tmux_source "$(lea_tmux_bin)")" = host ]
}

@test "tmux resolution: an old host tmux falls back to the lea-built one" {
  fake_tmux "tmux 3.1c"; built_tmux
  [ "$(lea_tmux_bin)" = "$LEA_TMUX_BUILT" ]
  [ "$(lea_tmux_source "$LEA_TMUX_BUILT")" = lea-built ]
}

@test "tmux resolution: no host tmux and a lea-built one is used" {
  no_tmux; built_tmux
  [ "$(lea_tmux_bin)" = "$LEA_TMUX_BUILT" ]
}

@test "tmux resolution: nothing usable names the install command and make tmux-build, never sudo" {
  no_tmux
  printf 'ID=ubuntu\n' >"$OSR"
  ! lea_tmux_bin
  run lea_tmux_missing
  [ "$output" = "tmux is not installed. Install it with: sudo apt install tmux, or build a static one with: make -C \"$LEA_ROOT\" tmux-build" ]
  run calls
  [[ "$output" != *"sudo"* ]]
}

@test "a LEA_TMUX_BIN that is a directory is named in the message" {
  export LEA_TMUX_BIN="$BATS_TEST_TMPDIR"
  ! lea_tmux_bin
  run lea_tmux_missing
  [[ "$output" == *"LEA_TMUX_BIN=$BATS_TEST_TMPDIR is not an executable file"* ]]
  [[ "$output" != *"is not installed"* ]]
}

@test "without LEA_PROJECTS_DIR, or with one that is not a directory, the projects dir is refused" {
  export LEA_PROJECTS_DIR=
  run lea_projects_dir
  [ "$status" -eq 1 ]
  [[ "$output" == *"no projects directory"*"lea init"*"LEA_PROJECTS_DIR"* ]]
  export LEA_PROJECTS_DIR="$BATS_TEST_TMPDIR/nope"
  run lea_projects_dir
  [ "$status" -eq 1 ]
  [[ "$output" == *"LEA_PROJECTS_DIR=$BATS_TEST_TMPDIR/nope is not a directory"* ]]
}

# guarded DIR: DIR is refused as a projects dir (lea init and lea doctor run this guard).
guarded() {
  run lea__guard_projects "$(lea__phys "$1")"
  [ "$status" -eq 1 ]
  [[ "$output" == *"cannot be a projects directory"* ]]
}

@test "guard: /, \$HOME, its ancestors and a symlink to it are refused" {
  guarded /
  guarded "$HOME"
  guarded "$BATS_TEST_TMPDIR"
  ln -s "$HOME" "$BATS_TEST_TMPDIR/homelink"
  guarded "$BATS_TEST_TMPDIR/homelink"
}

@test "guard: ~/.claude, the lea data dir and the lea installation, inside or around, are refused" {
  mkdir -p "$HOME/.claude/plugins"
  guarded "$HOME/.claude"
  guarded "$HOME/.claude/plugins"
  export XDG_DATA_HOME="$BATS_TEST_TMPDIR/elsewhere/data"
  mkdir -p "$XDG_DATA_HOME/lea"
  guarded "$BATS_TEST_TMPDIR/elsewhere"
  guarded "$XDG_DATA_HOME/lea"
  guarded "$LEA_ROOT"
  guarded "$LEA_ROOT/libexec"
  guarded "$(dirname "$LEA_ROOT")"
}

@test "guard: credential directories, inside or around, are refused" {
  for d in .ssh .gnupg .aws .config/gh .kube .docker; do
    mkdir -p "$HOME/$d/sub"
    guarded "$HOME/$d"
    guarded "$HOME/$d/sub"
  done
  rm -rf "$HOME/.ssh"
  mkdir -p "$BATS_TEST_TMPDIR/elsewhere/ssh"
  ln -s "$BATS_TEST_TMPDIR/elsewhere/ssh" "$HOME/.ssh"
  guarded "$BATS_TEST_TMPDIR/elsewhere"
  guarded "$BATS_TEST_TMPDIR/elsewhere/ssh"
  mkdir -p "$HOME/.sshx" "$HOME/projects"
  run lea__guard_projects "$(lea__phys "$HOME/.sshx")"
  [ "$status" -eq 0 ]
  run lea__guard_projects "$(lea__phys "$HOME/projects")"
  [ "$status" -eq 0 ]
}

@test "guard: a sibling of \$HOME with a common prefix is accepted" {
  mkdir -p "${HOME}x"
  run lea__guard_projects "$(lea__phys "${HOME}x")"
  [ "$status" -eq 0 ]
}

@test "lea_container_running asks Docker for the container's state" {
  export FAKE_DOCKER_OUT_INSPECT__F=true
  lea_container_running lea-x
  export FAKE_DOCKER_OUT_INSPECT__F=false
  ! lea_container_running lea-x
  run calls
  [[ "$output" == *"docker [inspect] [-f] [{{.State.Running}}] [lea-x]"* ]]
}
