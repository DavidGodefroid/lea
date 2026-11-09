#!/usr/bin/env bats
load helpers
# lea update on a Homebrew install (#354): lea v0.13.0 at <prefix>/Cellar/lea/0.13.0/libexec (a
# copy of this checkout's bin, libexec and config, versioned images in its lea.conf), linked as
# <prefix>/opt/lea and <prefix>/bin/lea the way the tap's formula installs it. The fake brew's
# upgrade (brew_release) unpacks the next version beside it and moves both links. PATH holds the
# fakes it needs and the system directories only, so brew is missing unless with_brew.
setup() {
  setup_fakes
  src=$LEA_ROOT
  P="$BATS_TEST_TMPDIR/brew"
  export FAKE_BREW_PREFIX=$P
  R=$(cellar_version 0.13.0)
  mkdir -p "$P/opt" "$P/bin"
  ln -s ../Cellar/lea/0.13.0 "$P/opt/lea"
  ln -s ../Cellar/lea/0.13.0/libexec/bin/lea "$P/bin/lea"
  B="$BATS_TEST_TMPDIR/b"; mkdir -p "$B"
  for f in curl docker cosign; do ln -s "$src/tests/fakes/$f" "$B/$f"; done
  export PATH="$B:/usr/local/bin:/usr/bin:/bin"
  load_lea
  LEA_ROOT=$R
  NOTICE="$XDG_DATA_HOME/lea/host/update-notice"
}
with_brew() { ln -s "$src/tests/fakes/brew" "$B/brew"; }

# cellar_version V: lea vV in the Cellar, as the formula installs it; prints its libexec.
cellar_version() {
  d="$P/Cellar/lea/$1/libexec"; mkdir -p "$d/build"
  cp -R "$src/bin" "$src/libexec" "$src/config" "$d/"
  sed -i -e "s|^LEA_IMAGE=.*|LEA_IMAGE=ghcr.io/davidgodefroid/lea:$1|" \
    -e "s|^LEA_PROXY_IMAGE=.*|LEA_PROXY_IMAGE=ghcr.io/davidgodefroid/lea-proxy:$1|" \
    -e 's|^LEA_AUTOUPDATE=1$|LEA_AUTOUPDATE=0|' "$d/config/lea.conf"
  printf '#!/bin/sh\n[ "$1" = version ] && echo v%s\n' "$1" >"$d/build/lea"; chmod +x "$d/build/lea"
  (cd "$d" && pwd -P)
}

# brew_release V: brew upgrade installs lea vV in the Cellar and moves opt/lea and bin/lea to it.
brew_release() {
  cat >"$BATS_TEST_TMPDIR/upgrade.sh" <<EOT
P=$P src=$src
$(declare -f cellar_version)
cellar_version $1 >/dev/null
ln -sfn ../Cellar/lea/$1 "$P/opt/lea"
ln -sfn ../Cellar/lea/$1/libexec/bin/lea "$P/bin/lea"
EOT
  export FAKE_BREW_UPGRADE="$BATS_TEST_TMPDIR/upgrade.sh"
}

# nothing_outside_brew: lea's own installer left no trace: no launcher link, no release install.
nothing_outside_brew() {
  [ ! -e "$HOME/.local/bin/lea" ] && [ ! -L "$HOME/.local/bin/lea" ]
  [ ! -e "$XDG_DATA_HOME/lea-install" ] && [ ! -e "$HOME/.local/share/lea-install" ]
  [ ! -e "$XDG_DATA_HOME/lea/host/update" ]
}

@test "a Homebrew install is updated by brew upgrade, never by lea's installer" {
  with_brew
  brew_release 0.13.1
  run lea_update
  [ "$status" -eq 0 ]
  [[ "$output" == *"this lea is a Homebrew install ($P/Cellar/lea/0.13.0/libexec): updating it with brew upgrade davidgodefroid/tap/lea"* ]]
  [[ "$output" == *"updated; running lea containers keep their image until they stop (lea stop --all)"* ]]
  run calls
  [[ "$output" == *"brew [update] noauto="*"brew [upgrade] [davidgodefroid/tap/lea] noauto=1"* ]]
  [[ "$output" != *curl* ]]
  [[ "$output" != *installer* ]]
  nothing_outside_brew
}

@test "after brew upgrade the new Cellar version refreshes its images and checks the dependencies" {
  with_brew
  brew_release 0.13.1
  run lea_update --deps none
  [ "$status" -eq 0 ]
  [[ "$output" == *"updated; running lea containers keep their image"*"Lock clients in lea's image: lockkit-v"* ]]
  run calls
  [[ "$output" == *"brew [upgrade] [davidgodefroid/tap/lea]"*"docker [pull] [ghcr.io/davidgodefroid/lea:0.13.1]"* ]]
  [[ "$output" == *"docker [pull] [ghcr.io/davidgodefroid/lea-proxy:0.13.1]"* ]]
  [[ "$output" != *":0.13.0]"* ]]
  # The old version stays: removing it is brew cleanup's, not lea's.
  [ -d "$P/Cellar/lea/0.13.0/libexec" ]
}

@test "already up to date: brew upgrades nothing, lea still refreshes the images and checks the dependencies" {
  with_brew
  run lea_update --deps none
  [ "$status" -eq 0 ]
  [[ "$output" == *"Lock clients in lea's image"* ]]
  run calls
  [[ "$output" == *"docker [pull] [ghcr.io/davidgodefroid/lea:0.13.0]"* ]]
  nothing_outside_brew
}

@test "a failing brew upgrade fails lea update with brew's status: no image refreshed, nothing installed" {
  with_brew
  brew_release 0.13.1
  FAKE_BREW_RC_UPGRADE=3 run lea_update
  [ "$status" -eq 3 ]
  [[ "$output" == *"brew upgrade davidgodefroid/tap/lea failed (status 3): lea is not updated"* ]]
  run calls
  [[ "$output" != *"docker [pull]"* ]]
  [[ "$output" != *curl* ]]
  [ ! -e "$P/Cellar/lea/0.13.1" ]
  nothing_outside_brew
}

@test "a failing brew update fails lea update before any upgrade" {
  with_brew
  FAKE_BREW_RC_UPDATE=1 run lea_update
  [ "$status" -eq 1 ]
  [[ "$output" == *"brew update failed (status 1): lea is not updated"* ]]
  run calls
  [[ "$output" != *"brew [upgrade]"* ]]
  [[ "$output" != *"docker [pull]"* ]]
}

@test "a Homebrew install without brew on PATH is never updated by lea itself" {
  run lea_update
  [ "$status" -eq 1 ]
  [[ "$output" == *"lea is managed by Homebrew but brew is not on PATH"* ]]
  run calls
  [[ "$output" != *curl* ]]
  [[ "$output" != *docker* ]]
  nothing_outside_brew
}

@test "the background update installs nothing on a Homebrew install, even with LEA_AUTOUPDATE=1" {
  with_brew
  lea_user_config_set LEA_AUTOUPDATE 1
  export FAKE_CURL_EFFECTIVE=https://github.com/DavidGodefroid/lea/releases/tag/v0.13.1
  run lea_update --background </dev/null
  [ "$status" -eq 0 ]
  [ "$(cat "$NOTICE")" = "v0.13.1 available" ]
  [[ "$output" == *"lea v0.13.1 is available: this lea is a Homebrew install, run lea update (brew upgrade)"* ]]
  run calls
  [[ "$output" != *"/download/"* ]]
  [[ "$output" != *brew* ]]
  [[ "$output" != *docker* ]]
  nothing_outside_brew
}

@test "a path that only mentions Cellar is not a Homebrew install" {
  with_brew
  for r in "$BATS_TEST_TMPDIR/Cellar-notes/lea" "$BATS_TEST_TMPDIR/Cellar/lea/0.13.0/x/libexec" "$BATS_TEST_TMPDIR/Cellar/lea/libexec"; do
    mkdir -p "$r/config"; cp "$src/config/lea.conf" "$r/config/"
    LEA_ROOT=$r
    : >"$FAKE_LOG"
    run lea_update
    # The release path: it asks for the latest release (none here) and never calls brew.
    [[ "$output" == *"could not find the latest lea release"* ]]
    run calls
    [[ "$output" == *"curl [-fsSLI]"* ]]
    [[ "$output" != *brew* ]]
  done
}

@test "lea init links no ~/.local/bin/lea on a Homebrew install: brew links bin/lea" {
  lea__init_launcher
  [ ! -e "$HOME/.local/bin/lea" ] && [ ! -L "$HOME/.local/bin/lea" ]
}

@test "lea init still links ~/.local/bin/lea on a release install" {
  r="$BATS_TEST_TMPDIR/inst/v0.13.0"; mkdir -p "$r/bin"; LEA_ROOT=$r
  lea__init_launcher
  [ "$(readlink "$HOME/.local/bin/lea")" = "$r/bin/lea" ]
}

@test "lea init on a Homebrew install still removes the locksql link an older lea made" {
  mkdir -p "$HOME/.local/bin"
  ln -s "$BATS_TEST_TMPDIR/gone/bin/locksql" "$HOME/.local/bin/locksql"
  lea__init_launcher
  [ ! -L "$HOME/.local/bin/locksql" ]
  [ ! -e "$HOME/.local/bin/lea" ] && [ ! -L "$HOME/.local/bin/lea" ]
}
