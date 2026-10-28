# shellcheck shell=sh
# Host helpers the remaining shell (init, update, rollback, doctor) still calls, kept from the retired shell
# launcher unchanged: the projects-dir guards, lea update, the container check, tmux resolution
# (the doctor reports it) and the core binary build.

# lea_projects_dir: the physical path of $LEA_PROJECTS_DIR, or dies.
lea_projects_dir() {
  [ -n "${LEA_PROJECTS_DIR:-}" ] || lea_die "no projects directory: run 'lea init', or set LEA_PROJECTS_DIR"
  [ -d "$LEA_PROJECTS_DIR" ] || lea_die "LEA_PROJECTS_DIR=$LEA_PROJECTS_DIR is not a directory"
  lea_abspath "$LEA_PROJECTS_DIR"
}

# lea__phys PATH: physical path of PATH, or of its parent when PATH does not exist yet.
lea__phys() {
  if [ -d "$1" ]; then lea_abspath "$1"
  elif [ -d "${1%/*}" ]; then printf '%s/%s' "$(lea_abspath "${1%/*}")" "${1##*/}"
  else printf '%s' "$1"; fi
}

# lea__under A B: B is A or below it.
lea__under() {
  [ "$1" = / ] && return 0
  case $2 in "$1"|"$1"/*) return 0 ;; esac
  return 1
}

# lea__guard_projects PROJECTS (physical): refuses /, $HOME and its ancestors, a directory that
# contains the user configuration, and any directory that is, contains or lies inside ~/.claude, a
# credential directory (LEA_GUARD_CREDENTIALS, from $HOME), ~/.config/lea, the lea data dir or the
# lea installation.
LEA_GUARD_CREDENTIALS=".ssh .gnupg .aws .config/gh .kube .docker"
lea__guard_projects() {
  if [ "$1" = / ] || lea__under "$1" "$(lea__phys "$HOME")"; then
    lea_die "$1 cannot be a projects directory: it is / or contains your home directory"
  fi
  lea__guard_overlap "$1" "$(lea__phys "$HOME/.claude")"
  # The user configuration is sourced as shell on the host: agents must not reach it.
  _gp_u=$(lea_user_config_path)
  case $_gp_u in */*) _gp_d=${_gp_u%/*} ;; *) _gp_d=. ;; esac
  if lea__under "$1" "$(lea__phys "${_gp_d:-/}")"; then
    lea_die "$1 cannot be a projects directory: it contains the user configuration $_gp_u"
  fi
  lea__guard_overlap "$1" "$(lea__phys "${XDG_CONFIG_HOME:-$HOME/.config}/lea")"
  for _gp_c in $LEA_GUARD_CREDENTIALS; do
    lea__guard_overlap "$1" "$(lea__phys "$HOME/$_gp_c")"
  done
  lea__guard_overlap "$1" "$(lea__phys "$(lea_data_dir)")"
  lea__guard_overlap "$1" "$LEA_ROOT"
}

# lea__guard_overlap PROJECTS X: dies when PROJECTS is, contains or lies inside X.
lea__guard_overlap() {
  if lea__under "$1" "$2" || lea__under "$2" "$1"; then
    lea_die "$1 cannot be a projects directory: it overlaps $2"
  fi
}

# lea_update [--background | --deps ask|all|none]: a git checkout pulls and rebuilds; a release
# install (no .git) is replaced by the latest release, installed with that release's own
# installer. Then the updated lea checks its dependencies (lea_update_deps: ask, the default, asks
# for each newer release; all takes them all, none none). --background is lea_update_background,
# which never touches a dependency.
lea_update() {
  _up_deps=ask
  case ${1:-} in
    --background) lea_update_background; return ;;
    --deps) _up_deps=${2:-} ;;
  esac
  case $_up_deps in ask|all|none) ;; *) lea_die "usage: lea update [--deps ask|all|none]" ;; esac
  if [ ! -e "$LEA_ROOT/.git" ]; then
    lea__update_release "$_up_deps"
    return 0
  fi
  git -C "$LEA_ROOT" pull --ff-only
  lea_core_build
  lea_load_config
  lea_refresh_images
  lea_log "updated; running lea containers keep their image until they stop (lea stop --all); the daemon restarts on the new binary at the next lea"
  # The pulled code checks the dependencies, with its own pins and floors.
  "$LEA_ROOT/libexec/lea-sh" update-deps "$_up_deps" || lea_log "warning: the dependencies were not checked (rerun lea update)"
}

# lea__sha256 FILE: the SHA-256 of FILE, in hex.
lea__sha256() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | cut -d' ' -f1
  else shasum -a 256 "$1" | cut -d' ' -f1; fi
}

# lea__cosign_pin PLATFORM: the SHA-256 of cosign v3.0.6 for PLATFORM (linux-amd64, ...), the
# pins of install.sh (tests/unit/install.bats keeps them equal); fails for any other platform.
lea__cosign_pin() {
  case $1 in
    linux-amd64) echo c956e5dfcac53d52bcf058360d579472f0c1d2d9b69f55209e256fe7783f4c74 ;;
    linux-arm64) echo bedac92e8c3729864e13d4a17048007cfafa79d5deca993a43a90ffe018ef2b8 ;;
    darwin-amd64) echo 4c3e7af8372d3ca3296e62fa56f23fcbb5721cc6ac1827900d398f110d7cd280 ;;
    darwin-arm64) echo 5fadd012ae6381a6a29ff86a7d39aa873878852f1073fc90b15995961ecfb084 ;;
    *) return 1 ;;
  esac
}

# lea__cosign DIR: the cosign that verifies a release, printed: the one on PATH, else cosign
# v3.0.6 downloaded into DIR and checked against its pinned SHA-256 (never "latest"). Fails, with
# a message, when neither is available.
lea__cosign() {
  if command -v cosign >/dev/null 2>&1; then echo cosign; return 0; fi
  case $(uname -s) in Linux) _co_p=linux ;; Darwin) _co_p=darwin ;; *) _co_p=$(uname -s) ;; esac
  case $(uname -m) in x86_64|amd64) _co_p=$_co_p-amd64 ;; aarch64|arm64) _co_p=$_co_p-arm64 ;; *) _co_p=$_co_p-$(uname -m) ;; esac
  if ! _co_s=$(lea__cosign_pin "$_co_p"); then
    lea_log "cosign is missing and lea pins no cosign for $_co_p: install cosign (https://docs.sigstore.dev)"
    return 1
  fi
  _co_url=https://github.com/sigstore/cosign/releases/download/v3.0.6/cosign-$_co_p
  if ! curl -fsSL -o "$1/cosign" "$_co_url"; then
    lea_log "cannot download cosign from $_co_url: install cosign (https://docs.sigstore.dev)"
    return 1
  fi
  if [ "$(lea__sha256 "$1/cosign")" != "$_co_s" ]; then
    rm -f "$1/cosign"
    lea_log "the cosign downloaded from $_co_url does not match its pinned SHA-256: install cosign (https://docs.sigstore.dev)"
    return 1
  fi
  chmod +x "$1/cosign"
  echo "$1/cosign"
}

# lea__verify_checksums DIR: verifies DIR/checksums.txt against its Sigstore bundle
# (DIR/checksums.txt.sigstore.json), signed keyless by the release workflow on a v* tag.
lea__verify_checksums() {
  _vc_cosign=$(lea__cosign "$1") || return 1
  "$_vc_cosign" verify-blob --bundle "$1/checksums.txt.sigstore.json" \
    --certificate-identity-regexp '^https://github\.com/DavidGodefroid/lea/\.github/workflows/release\.yml@refs/tags/v[0-9]' \
    --certificate-oidc-issuer https://token.actions.githubusercontent.com \
    "$1/checksums.txt" >&2
}

# lea__link_root LINK: the installation LINK (a launcher link, ~/.local/bin/lea) leads to, the
# directory above its bin/lea; fails when LINK does not exist.
lea__link_root() {
  _lr=$1
  [ -e "$_lr" ] || return 1
  while [ -L "$_lr" ]; do
    _lr_l=$(readlink "$_lr")
    case $_lr_l in /*) _lr=$_lr_l ;; *) _lr=$(dirname "$_lr")/$_lr_l ;; esac
  done
  (cd "$(dirname "$_lr")/.." && pwd -P)
}

# lea__update_release ask|all|none: resolves the latest release under LEA_RELEASES_URL, runs that release's
# install.sh (downloaded to <data>/lea/host/update, checked against the release's checksums.txt,
# whose Sigstore signature is verified first) with --no-init, then hands over to the new installation (its libexec/lea-sh refresh-images) to
# pull its versioned images and build its locksql console image, and to check its dependencies.
lea__update_release() {
  _ur_v=$(curl -fsSLI -o /dev/null -w '%{url_effective}' "$LEA_RELEASES_URL/latest" | sed 's|.*/tag/||') || _ur_v=
  case $_ur_v in v*) ;; *) lea_die "could not find the latest lea release ($LEA_RELEASES_URL/latest)" ;; esac
  _ur_base=$LEA_RELEASES_URL/download/$_ur_v
  lea_log "updating this release install to lea $_ur_v with its installer ($_ur_base/install.sh)"
  _ur_tmp=$(lea_host_dir)/update
  lea_safe_path "$(lea_data_dir)" "$_ur_tmp"
  rm -rf "$_ur_tmp"
  lea_mkdirs "$(lea_data_dir)" "$_ur_tmp"
  if ! curl -fsSL -o "$_ur_tmp/install.sh" "$_ur_base/install.sh" ||
     ! curl -fsSL -o "$_ur_tmp/checksums.txt" "$_ur_base/checksums.txt" ||
     ! curl -fsSL -o "$_ur_tmp/checksums.txt.sigstore.json" "$_ur_base/checksums.txt.sigstore.json"; then
    rm -rf "$_ur_tmp"
    lea_die "cannot download the installer of lea $_ur_v from $_ur_base"
  fi
  if ! lea__verify_checksums "$_ur_tmp"; then
    rm -rf "$_ur_tmp"
    lea_die "the checksums.txt of lea $_ur_v does not verify against its Sigstore signature"
  fi
  _ur_want=$(awk '$2 == "install.sh" { print $1 }' "$_ur_tmp/checksums.txt")
  if [ -z "$_ur_want" ] || [ "$(lea__sha256 "$_ur_tmp/install.sh")" != "$_ur_want" ]; then
    rm -rf "$_ur_tmp"
    lea_die "the installer of lea $_ur_v does not match its checksums.txt"
  fi
  if ! LEA_REQUIRE_COSIGN=1 sh "$_ur_tmp/install.sh" --version "$_ur_v" --no-init; then
    rm -rf "$_ur_tmp"
    lea_die "the installer failed"
  fi
  rm -rf "$_ur_tmp"
  _ur_link=${LEA_BIN_DIR:-$HOME/.local/bin}/lea
  _ur_new=$(lea__link_root "$_ur_link") || lea_die "the installer left no $_ur_link"
  "$_ur_new/libexec/lea-sh" refresh-images ||
    lea_die "lea $_ur_v is installed but its images could not be refreshed: rerun lea update"
  if [ "$_ur_new" != "$LEA_ROOT" ]; then lea__prune_installs "$_ur_new" "$LEA_ROOT"; fi
  lea_log "updated; running lea containers keep their image until they stop (lea stop --all)"
  # The new installation checks the dependencies, with its own pins and floors.
  "$_ur_new/libexec/lea-sh" update-deps "${1:-ask}" || lea_log "warning: the dependencies were not checked (rerun lea update)"
}

# lea__semver V: "MAJOR MINOR PATCH" of a release version vMAJOR.MINOR.PATCH, one line of it;
# fails on anything else (dev, a git describe, a pre-release, a leading zero, several lines).
lea__semver() {
  case $1 in *'
'*) return 1 ;; esac
  _sv=$(printf '%s\n' "$1" | sed -n 's/^v\([0-9][0-9]*\)\.\([0-9][0-9]*\)\.\([0-9][0-9]*\)$/\1 \2 \3/p')
  [ -n "$_sv" ] || return 1
  # No leading zero (v01.2.3 is not v1.2.3), at most 9 digits (the shell compares them).
  for _sv_n in $_sv; do
    case $_sv_n in 0?*|??????????*) return 1 ;; esac
  done
  printf '%s\n' "$_sv"
}

# lea__notice TEXT: the deck's update notice (<host>/update-notice), "vX.Y.Z installed",
# "vX.Y.Z major" or "vX.Y.Z available"; the deck hides it once it runs vX.Y.Z or later.
lea__notice() {
  printf '%s\n' "$1" | lea_write_file "$(lea_data_dir)" "$(lea_host_dir)/update-notice"
}

# lea_update_background: lea update --background, started detached by the launcher at most once a
# day (internal/launcher/autoupdate.go), its output in <host>/update.log. A release install only,
# never a dev build. One updater at a time (<host>/update.lock; a lock older than an hour is a
# killed updater's). Failing to reach the releases is silent.
lea_update_background() {
  [ ! -e "$LEA_ROOT/.git" ] || return 0
  _ub_cur=$("$LEA_ROOT/build/lea" version 2>/dev/null </dev/null) || _ub_cur=
  lea__semver "$_ub_cur" >/dev/null || return 0
  _ub_host=$(lea_host_dir)
  lea_mkdirs "$(lea_data_dir)" "$_ub_host"
  _ub_lock=$_ub_host/update.lock
  if [ -n "$(find "$_ub_lock" -maxdepth 0 -mmin +60 2>/dev/null)" ]; then rmdir "$_ub_lock" 2>/dev/null || true; fi
  if ! mkdir "$_ub_lock" 2>/dev/null; then
    lea_log "background update: another update runs ($_ub_lock)"
    return 0
  fi
  _ub_rc=0
  lea__update_background "$_ub_cur" || _ub_rc=$?
  rm -rf "$_ub_host/update-bg"
  rmdir "$_ub_lock" 2>/dev/null || true
  return "$_ub_rc"
}

# lea__update_background CURRENT: the latest release, when newer than CURRENT. A newer major
# release is only announced, and so is a newer minor release of 0.x. A newer minor (from 1.0) or
# patch release is installed beside this one with its
# own installer, after the Sigstore signature of its checksums.txt (lea__cosign) and
# the installer's checksum are verified; the launcher link moves to it only once its images are
# in place. Any failure leaves this version linked.
lea__update_background() {
  _ub_v=$(curl -fsSLI -o /dev/null -w '%{url_effective}' "$LEA_RELEASES_URL/latest" 2>/dev/null | sed 's|.*/tag/||') || _ub_v=
  if ! _ub_new=$(lea__semver "$_ub_v"); then
    lea_log "background update: no release found at $LEA_RELEASES_URL/latest"
    return 0
  fi
  _ub_old=$(lea__semver "$1")
  # shellcheck disable=SC2086
  set -- $_ub_old $_ub_new
  if [ "$4" -lt "$1" ] || { [ "$4" -eq "$1" ] && { [ "$5" -lt "$2" ] || { [ "$5" -eq "$2" ] && [ "$6" -le "$3" ]; }; }; }; then
    return 0
  fi
  _ub_cur=v$1.$2.$3
  if [ "$_ub_v" = "${LEA_AUTOUPDATE_SKIP:-}" ]; then
    lea_log "background update: lea $_ub_v is skipped (LEA_AUTOUPDATE_SKIP, set by lea rollback): lea update installs it"
    return 0
  fi
  if [ "$4" -ne "$1" ]; then
    lea__notice "$_ub_v major"
    lea_log "background update: lea $_ub_v is a new major version: run lea update"
    return 0
  fi
  # In 0.x a minor release may break (semver): only its patch releases install themselves.
  if [ "$1" -eq 0 ] && [ "$5" -ne "$2" ]; then
    lea__notice "$_ub_v available"
    lea_log "background update: lea $_ub_v is a new minor version of lea 0.x: run lea update"
    return 0
  fi
  _ub_bin=${LEA_BIN_DIR:-$HOME/.local/bin}
  if [ "$(lea__link_root "$_ub_bin/lea" 2>/dev/null)" != "$LEA_ROOT" ]; then
    lea_log "background update: $_ub_bin/lea does not lead to $LEA_ROOT, left alone"
    return 0
  fi
  _ub_fail() { lea_log "background update to lea $_ub_v failed: $1; lea $_ub_cur stays"; }
  _ub_base=$LEA_RELEASES_URL/download/$_ub_v
  _ub_tmp=$(lea_host_dir)/update-bg
  lea_safe_path "$(lea_data_dir)" "$_ub_tmp"
  rm -rf "$_ub_tmp"
  lea_mkdirs "$(lea_data_dir)" "$_ub_tmp"
  for _ub_f in install.sh checksums.txt checksums.txt.sigstore.json; do
    if ! curl -fsSL -o "$_ub_tmp/$_ub_f" "$_ub_base/$_ub_f"; then
      _ub_fail "cannot download $_ub_base/$_ub_f"
      return 1
    fi
  done
  if ! lea__verify_checksums "$_ub_tmp"; then
    _ub_fail "the signature of checksums.txt does not verify"
    return 1
  fi
  _ub_want=$(awk '$2 == "install.sh" { print $1 }' "$_ub_tmp/checksums.txt")
  if [ -z "$_ub_want" ] || [ "$(lea__sha256 "$_ub_tmp/install.sh")" != "$_ub_want" ]; then
    _ub_fail "its installer does not match checksums.txt"
    return 1
  fi
  _ub_inst=$(dirname "$LEA_ROOT")
  if ! LEA_REQUIRE_COSIGN=1 LEA_INSTALL_DIR=$_ub_inst sh "$_ub_tmp/install.sh" --version "$_ub_v" --no-init --no-link </dev/null >&2; then
    _ub_fail "the installer failed"
    return 1
  fi
  _ub_dir=$_ub_inst/$_ub_v
  if [ ! -x "$_ub_dir/bin/lea" ] || ! "$_ub_dir/libexec/lea-sh" refresh-images </dev/null >&2; then
    _ub_fail "its images could not be refreshed"
    return 1
  fi
  lea__relink "$_ub_dir"
  lea__notice "$_ub_v installed"
  lea__prune_installs "$_ub_dir" "$LEA_ROOT"
  lea_log "background update: lea $_ub_v installed, active at the next lea; running lea containers keep their image until they stop (lea stop --all)"
}

# lea__relink DIR: points the launcher link (and the locksql link, when it leads into the same
# installation directory) at the installation DIR. The links move by rename: they are never missing.
lea__relink() {
  _rl_bin=${LEA_BIN_DIR:-$HOME/.local/bin}
  ln -sfn "$1/bin/lea" "$_rl_bin/.lea.update.$$"
  mv -f "$_rl_bin/.lea.update.$$" "$_rl_bin/lea"
  case $(readlink "$_rl_bin/locksql" 2>/dev/null) in
    "$(dirname "$1")/"*)
      if [ -e "$1/bin/locksql" ]; then
        ln -sfn "$1/bin/locksql" "$_rl_bin/.locksql.update.$$"
        mv -f "$_rl_bin/.locksql.update.$$" "$_rl_bin/locksql"
      fi ;;
  esac
}

# lea__older A B: release A (vX.Y.Z) is older than release B.
lea__older() {
  # shellcheck disable=SC2046
  set -- $(lea__semver "$1") $(lea__semver "$2")
  [ $# -eq 6 ] || return 1
  [ "$1" -lt "$4" ] || { [ "$1" -eq "$4" ] && { [ "$2" -lt "$5" ] || { [ "$2" -eq "$5" ] && [ "$3" -lt "$6" ]; }; }; }
}

# lea__prune_installs NEW PREVIOUS: after an update from the installation PREVIOUS to NEW, removes
# every other release (a vX.Y.Z directory) beside them, except the one the running daemon uses
# (<host>/daemon.version). Anything else in that directory is left alone; so is an installation
# whose directory is not a release's.
lea__prune_installs() {
  _pi_dir=$(dirname "$1")
  [ "$(dirname "$2")" = "$_pi_dir" ] || return 0
  if ! lea__semver "${1##*/}" >/dev/null || ! lea__semver "${2##*/}" >/dev/null; then return 0; fi
  _pi_used=$(cat "$(lea_host_dir)/daemon.version" 2>/dev/null) || _pi_used=
  for _pi_d in "$_pi_dir"/v*; do
    _pi_n=${_pi_d##*/}
    if [ ! -d "$_pi_d" ] || [ -L "$_pi_d" ] || ! lea__semver "$_pi_n" >/dev/null; then continue; fi
    case $_pi_n in "${1##*/}"|"${2##*/}"|"$_pi_used") continue ;; esac
    if rm -rf "${_pi_d:?}"; then lea_log "removed lea $_pi_n ($_pi_d): only the current and the previous versions are kept"
    else lea_log "warning: could not remove $_pi_d"; fi
  done
}

# lea_rollback: lea rollback. Links the newest release older than this one, installed beside it,
# and sets LEA_AUTOUPDATE_SKIP to this version in the user config so the background update does
# not reinstall it. This version stays installed (a running daemon may use it); the next update
# removes it.
lea_rollback() {
  [ ! -e "$LEA_ROOT/.git" ] || lea_die "lea rollback is for a release install: in a git checkout, check out the version you want"
  _rb_cur=${LEA_ROOT##*/}
  lea__semver "$_rb_cur" >/dev/null || lea_die "lea rollback is for a release install: $LEA_ROOT is not one"
  _rb_bin=${LEA_BIN_DIR:-$HOME/.local/bin}
  if [ "$(lea__link_root "$_rb_bin/lea" 2>/dev/null)" != "$LEA_ROOT" ]; then
    lea_die "$_rb_bin/lea does not lead to $LEA_ROOT: nothing rolled back"
  fi
  _rb_inst=$(dirname "$LEA_ROOT")
  _rb_prev=
  for _rb_d in "$_rb_inst"/v*; do
    _rb_n=${_rb_d##*/}
    if [ ! -d "$_rb_d" ] || [ ! -e "$_rb_d/bin/lea" ] || ! lea__older "$_rb_n" "$_rb_cur"; then continue; fi
    if [ -z "$_rb_prev" ] || lea__older "$_rb_prev" "$_rb_n"; then _rb_prev=$_rb_n; fi
  done
  [ -n "$_rb_prev" ] || lea_die "no previous version of lea in $_rb_inst to roll back to"
  lea_user_config_set LEA_AUTOUPDATE_SKIP "$_rb_cur" ||
    lea_die "cannot write LEA_AUTOUPDATE_SKIP to $(lea_user_config_path): nothing rolled back"
  lea__relink "$_rb_inst/$_rb_prev"
  rm -f "$(lea_host_dir)/update-notice"
  lea_log "rolled back to lea $_rb_prev, active at the next lea; the background update skips lea $_rb_cur (LEA_AUTOUPDATE_SKIP in $(lea_user_config_path)); running lea containers keep their image until they stop (lea stop --all)"
}

lea_container_running() { [ "$(docker inspect -f '{{.State.Running}}' "$1" 2>/dev/null)" = true ]; }

LEA_TMUX_MIN=3.2

# lea_tmux_install_hint: the command that installs tmux on this host.
lea_tmux_install_hint() {
  if [ "$(uname -s 2>/dev/null)" = Darwin ]; then printf 'brew install tmux\n'; return 0; fi
  _ti_f=${LEA_OS_RELEASE:-/etc/os-release}
  _ti_ids=
  if [ -r "$_ti_f" ]; then
    _ti_ids=$(sed -n 's/^ID=//p; s/^ID_LIKE=//p' "$_ti_f" | tr -d '"' | tr '\n' ' ')
  fi
  # shellcheck disable=SC2086
  for _ti_id in $_ti_ids; do
    case $_ti_id in
      debian|ubuntu) printf 'sudo apt install tmux\n'; return 0 ;;
      fedora|rhel|centos) printf 'sudo dnf install tmux\n'; return 0 ;;
      arch) printf 'sudo pacman -S tmux\n'; return 0 ;;
      opensuse*|suse|sles) printf 'sudo zypper install tmux\n'; return 0 ;;
      alpine) printf 'sudo apk add tmux\n'; return 0 ;;
    esac
  done
  printf 'install tmux %s or later with your package manager\n' "$LEA_TMUX_MIN"
}

# lea_tmux_build_hint: the alternative to a package: lea's own static build (never sudo).
lea_tmux_build_hint() { printf 'make -C "%s" tmux-build' "$LEA_ROOT"; }

# lea_tmux_built: where `make tmux-build` puts lea's own tmux (LEA_TMUX_BUILT overrides, for tests).
lea_tmux_built() { printf '%s\n' "${LEA_TMUX_BUILT:-$LEA_ROOT/build/tmux}"; }

# lea_tmux_bin: the one place that resolves the tmux binary every deck call goes through (the
# launcher, the doctor and, via LEA_TMUX_BIN, the daemon). Prints its path, or fails when there
# is none. Resolution: $LEA_TMUX_BIN if set (it must be an executable file); else the host tmux
# when it is 3.2 or later; else the lea-built $LEA_ROOT/build/tmux; else the (too old) host tmux,
# so the caller can name its version; else nothing.
lea_tmux_bin() {
  if [ -n "${LEA_TMUX_BIN:-}" ]; then
    [ -f "$LEA_TMUX_BIN" ] && [ -x "$LEA_TMUX_BIN" ] || return 1
    printf '%s\n' "$LEA_TMUX_BIN"
    return 0
  fi
  _tb_host=$(command -v tmux 2>/dev/null || true)
  if [ -n "$_tb_host" ] && lea_tmux_version_ok "$("$_tb_host" -V 2>/dev/null)"; then
    printf '%s\n' "$_tb_host"; return 0
  fi
  _tb_built=$(lea_tmux_built)
  if [ -f "$_tb_built" ] && [ -x "$_tb_built" ]; then
    printf '%s\n' "$_tb_built"; return 0
  fi
  [ -n "$_tb_host" ] || return 1
  printf '%s\n' "$_tb_host"
}

# lea_tmux_source BIN: "lea-built" for the binary lea built itself, else "host".
lea_tmux_source() {
  if [ "$1" = "$(lea_tmux_built)" ]; then printf 'lea-built\n'; else printf 'host\n'; fi
}

# lea_tmux_missing: the message for "no usable tmux" (names a bad LEA_TMUX_BIN).
lea_tmux_missing() {
  if [ -n "${LEA_TMUX_BIN:-}" ]; then
    printf 'LEA_TMUX_BIN=%s is not an executable file. Unset it, or point it at tmux %s or later' "$LEA_TMUX_BIN" "$LEA_TMUX_MIN"
  else
    printf 'tmux is not installed. Install it with: %s, or build a static one with: %s' "$(lea_tmux_install_hint)" "$(lea_tmux_build_hint)"
  fi
}

# lea_tmux_version_ok LINE: LINE is `tmux -V` output ("tmux 3.4", "tmux 3.3a", "tmux next-3.6");
# true for 3.2 or later (control-mode client flags and format subscriptions).
lea_tmux_version_ok() {
  _tv=$(printf '%s' "$1" | sed -n 's/^tmux [^0-9]*\([0-9][0-9]*\)\.\([0-9][0-9]*\).*/\1 \2/p')
  [ -n "$_tv" ] || return 1
  _tv_maj=${_tv% *}; _tv_min=${_tv#* }
  [ "$_tv_maj" -gt 3 ] || { [ "$_tv_maj" -eq 3 ] && [ "$_tv_min" -ge 2 ]; }
}

# lea_core_build: rebuilds the core binary from this checkout (lea update: the Go launcher and the
# daemon follow the source). Never fatal.
lea_core_build() {
  make -C "$LEA_ROOT" go-build >&2 || lea_log "warning: make go-build failed; run it before the next lea"
}

# lea_host_engine prints the engine behind docker and its version ("docker 29.4.2", "podman
# 5.8.9"), nothing when unknown: Docker Engine names an Engine component, Podman's Docker API a
# "Podman Engine" one, and the podman-docker shim has no components but says "podman version".
lea_host_engine() {
  _hec=$(docker version --format '{{range .Server.Components}}{{.Name}}={{.Version}}{{"\n"}}{{end}}' 2>/dev/null || true)
  _hev=$(printf '%s\n' "$_hec" | sed -n 's/^Engine=//p' | head -n 1)
  if [ -n "$_hev" ]; then echo "docker $_hev"; return 0; fi
  _hev=$(printf '%s\n' "$_hec" | sed -n 's/^Podman Engine=//p' | head -n 1)
  if [ -z "$_hev" ]; then _hev=$(docker --version 2>/dev/null | sed -n 's/^podman version //p' | head -n 1); fi
  if [ -n "$_hev" ]; then echo "podman $_hev"; fi
}
