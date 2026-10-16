# shellcheck shell=sh
# Host helpers the remaining shell (init, update, doctor) still calls, kept from the retired shell
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

# lea__guard_projects PROJECTS (physical): refuses /, $HOME and its ancestors, and any directory
# that is, contains or lies inside ~/.claude, the lea data dir or the lea installation.
lea__guard_projects() {
  if [ "$1" = / ] || lea__under "$1" "$(lea__phys "$HOME")"; then
    lea_die "$1 cannot be a projects directory: it is / or contains your home directory"
  fi
  for _gp_x in "$(lea__phys "$HOME/.claude")" "$(lea__phys "$(lea_data_dir)")" "$LEA_ROOT"; do
    if lea__under "$1" "$_gp_x" || lea__under "$_gp_x" "$1"; then
      lea_die "$1 cannot be a projects directory: it overlaps $_gp_x"
    fi
  done
}

# lea_update: a git checkout pulls and rebuilds; a release install (no .git) is replaced by the
# installer, which lea update runs for the latest release.
lea_update() {
  if [ ! -e "$LEA_ROOT/.git" ]; then
    lea_log "updating this release install with the installer ($LEA_INSTALL_URL)"
    curl -fsSL "$LEA_INSTALL_URL" | sh -s -- --no-init || lea_die "the installer failed"
    lea_log "updated; running lea containers keep their image until they stop (lea stop --all)"
    return 0
  fi
  git -C "$LEA_ROOT" pull --ff-only
  lea_core_build
  lea_load_config
  lea_refresh_images
  lea_log "updated; running lea containers keep their image until they stop (lea stop --all); the daemon restarts on the new binary at the next lea"
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
