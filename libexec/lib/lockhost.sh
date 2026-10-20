# shellcheck shell=sh
# lockmq and lockfile, lock tools whose console runs on this host: in the tool's own account and
# login session (sudo lockmq install, sudo lockfile install), never in a container (lockkit's
# isolation checks refuse a console there). lea only lets its agents reach the console's socket:
# the launcher mounts /run/<tool> and /etc/<tool>/system.toml read-only into its containers and
# adds the clients group (internal/launcher/lockhost.go). lea never starts, sees or drives a
# console, and never holds a credential of one.

# lea__lockhost_var TOOL: the switch of TOOL (LEA_LOCKMQ, LEA_LOCKFILE).
lea__lockhost_var() {
  case $1 in
    lockmq) printf LEA_LOCKMQ ;;
    lockfile) printf LEA_LOCKFILE ;;
    *) lea_die "unknown lock tool '$1'" ;;
  esac
}

# lea__lockhost_get TOOL [SUFFIX]: the value of LEA_<TOOL><SUFFIX>, for the two fixed names only.
lea__lockhost_get() {
  case $(lea__lockhost_var "$1")${2:-} in
    LEA_LOCKMQ) printf '%s' "${LEA_LOCKMQ:-0}" ;;
    LEA_LOCKFILE) printf '%s' "${LEA_LOCKFILE:-0}" ;;
    LEA_LOCKMQ_REF) printf '%s' "${LEA_LOCKMQ_REF:-}" ;;
    LEA_LOCKFILE_REF) printf '%s' "${LEA_LOCKFILE_REF:-}" ;;
  esac
}

# lea__lockhost_set TOOL 0|1: the switch, in the user config and in this run.
lea__lockhost_set() {
  lea_user_config_set "$(lea__lockhost_var "$1")" "$2"
  case $1 in lockmq) LEA_LOCKMQ=$2 ;; lockfile) LEA_LOCKFILE=$2 ;; esac
}

# lea__lockhost_what TOOL: what TOOL guards, for the questions.
lea__lockhost_what() {
  case $1 in lockmq) printf 'RabbitMQ brokers' ;; lockfile) printf 'files on S3, SFTP or a local directory' ;; esac
}

# lea__lockfile_safe vX.Y.Z: lockfile v0.1.1 or later (v0.1.0 lets a plugin escape its sandbox).
lea__lockfile_safe() {
  _ls_v=$(lea__semver "$1") || return 1
  # shellcheck disable=SC2086 # three numbers
  set -- $_ls_v
  [ "$1" -gt 0 ] || [ "$2" -gt 1 ] || { [ "$2" -eq 1 ] && [ "$3" -ge 1 ]; }
}

# The host's root and the owner of its system files: / and root. No environment variable changes
# them (lea runs the binary they vouch for with sudo); the unit tests redefine these functions.
lea__lockhost_root() { :; }
lea__lockhost_owner() { printf 0; }

# lea__lockhost_bin TOOL: the root-owned TOOL binary in /usr/local/bin, the one TOOL install
# accepts for the console account (a binary another account could replace would hand it the
# console, and lea runs it with sudo), or nothing. Neither it nor the directory may be writable by
# anyone but root.
lea__lockhost_bin() {
  _lb_d=$(lea__lockhost_root)/usr/local/bin
  _lb_b=$_lb_d/$1
  [ -f "$_lb_b" ] && [ ! -L "$_lb_b" ] && [ -x "$_lb_b" ] && [ -d "$_lb_d" ] && [ ! -L "$_lb_d" ] || return 1
  # The binary and its directory: owned by root, writable by root only (lea runs it with sudo).
  for _lb_f in "$_lb_b" "$_lb_d"; do
    [ "$(stat -c %u "$_lb_f" 2>/dev/null)" = "$(lea__lockhost_owner)" ] || return 1
    case $(stat -c %A "$_lb_f" 2>/dev/null) in ?????w????|????????w?|'') return 1 ;; esac
  done
  printf '%s' "$_lb_b"
}

# lea__lockhost_version BIN: the version BIN prints (vX.Y.Z), from "lockmq 0.1.0" or
# "lockfile v0.2.0".
lea__lockhost_version() {
  _lv_v=$("$1" version 2>/dev/null </dev/null | awk 'NR == 1 { print $NF }') || return 1
  case $_lv_v in v*) ;; *) _lv_v=v$_lv_v ;; esac
  lea__semver "$_lv_v" >/dev/null || return 1
  printf '%s' "$_lv_v"
}

# lea__lockhost_setup_ok TOOL: the separated setup of TOOL is on this host (its system.toml).
lea__lockhost_setup_ok() { [ -f "$(lea__lockhost_root)/etc/$1/system.toml" ]; }

# lea__lockhost_arch: the release architecture of this host.
lea__lockhost_arch() {
  case $(uname -m) in
    x86_64|amd64) printf amd64 ;;
    aarch64|arm64) printf arm64 ;;
    *) return 1 ;;
  esac
}

# lea__lockhost_install TOOL vX.Y.Z: the release binary in /usr/local/bin (root-owned, with sudo),
# after the Sigstore signature of its checksums.txt is checked against TOOL's release workflow
# (cosign is required: nothing is installed without it) and the archive against checksums.txt.
lea__lockhost_install() {
  _li_t=$1 _li_r=$2
  command -v cosign >/dev/null 2>&1 || {
    lea_log "$_li_t: cosign is needed to check the release signature (https://docs.sigstore.dev/cosign/system_config/installation/); nothing installed"
    return 1
  }
  _li_a=$(lea__lockhost_arch) || { lea_log "$_li_t: no release for $(uname -m); nothing installed"; return 1; }
  _li_arc=${_li_t}_${_li_r#v}_linux_$_li_a.tar.gz
  _li_u=https://github.com/DavidGodefroid/$_li_t/releases/download/$_li_r
  _li_tmp=$(mktemp -d) || return 1
  for _li_f in checksums.txt checksums.txt.sigstore.json "$_li_arc"; do
    if ! curl -fsSL -o "$_li_tmp/$_li_f" "$_li_u/$_li_f"; then
      lea_log "$_li_t: could not download $_li_u/$_li_f; nothing installed"
      rm -rf "$_li_tmp"
      return 1
    fi
  done
  if ! cosign verify-blob --bundle "$_li_tmp/checksums.txt.sigstore.json" \
    --certificate-identity-regexp "^https://github\\.com/DavidGodefroid/$_li_t/\\.github/workflows/release\\.yml@refs/tags/$(printf '%s' "$_li_r" | sed 's/\./\\./g')\$" \
    --certificate-oidc-issuer https://token.actions.githubusercontent.com \
    "$_li_tmp/checksums.txt" >&2; then
    lea_log "$_li_t: checksums.txt of $_li_r is not signed by $_li_t's release workflow; nothing installed"
    rm -rf "$_li_tmp"
    return 1
  fi
  _li_want=$(awk -v f="$_li_arc" '$2 == f { print $1 }' "$_li_tmp/checksums.txt")
  if [ -z "$_li_want" ] || [ "$(lea__sha256 "$_li_tmp/$_li_arc")" != "$_li_want" ]; then
    lea_log "$_li_t: $_li_arc does not match the signed checksums.txt; nothing installed"
    rm -rf "$_li_tmp"
    return 1
  fi
  if ! tar -xzf "$_li_tmp/$_li_arc" -C "$_li_tmp" "$_li_t" || [ "$(lea__lockhost_version "$_li_tmp/$_li_t")" != "$_li_r" ]; then
    lea_log "$_li_t: the archive of $_li_r holds no $_li_t $_li_r; nothing installed"
    rm -rf "$_li_tmp"
    return 1
  fi
  lea_log "$_li_t $_li_r: signature and checksum verified; installing $(lea__lockhost_root)/usr/local/bin/$_li_t (needs sudo)"
  if ! sudo install -m 0755 -o root -g root "$_li_tmp/$_li_t" "$(lea__lockhost_root)/usr/local/bin/$_li_t"; then
    lea_log "$_li_t: could not install the binary"
    rm -rf "$_li_tmp"
    return 1
  fi
  rm -rf "$_li_tmp"
}

# lea__lockhost_console_hint TOOL: how to open TOOL's console (lea never starts it).
lea__lockhost_console_hint() {
  lea_log "$1 console: open a session of the $1 account (switch user; prefer Wayland), then run there:"
  lea_log "  $1 console --project ${LEA_PROJECTS_DIR:-DIR} --profile PROFILE"
  lea_log "  (lea's agents reach it after their lea restarts: lea stop --all, then lea)"
}

# lea__init_lockhost TOOL: lea init's part for TOOL. Without a decision yet (no flag, not in
# the user config) and on a terminal, it asks, no by default (pre-release tools). When on: the
# pinned release on the host (asked, cosign-checked), its separated setup (asked: TOOL install, with
# sudo), the guided profile setup when TOOL offers one, and how to open the console. Never fatal.
lea__init_lockhost() {
  _ih_t=$1
  _ih_v=$(lea__lockhost_var "$_ih_t")
  if ! grep -q "^$_ih_v=" "$(lea_user_config_path)" 2>/dev/null; then
    if [ -z "${LEA_INIT_NONINTERACTIVE:-}" ] && lea__init_tty; then
      if lea__ask_no "Let lea's agents query $(lea__lockhost_what "$_ih_t") through a $_ih_t console on this host (pre-release; a human approves every read)?"; then
        lea__lockhost_set "$_ih_t" 1
      else
        lea__lockhost_set "$_ih_t" 0
      fi
    fi
  fi
  [ "$(lea__lockhost_get "$_ih_t")" = 1 ] || return 0
  _ih_r=$(lea__lockhost_get "$_ih_t" _REF)
  _ih_b=$(lea__lockhost_bin "$_ih_t") || _ih_b=
  _ih_have=
  if [ -n "$_ih_b" ]; then _ih_have=$(lea__lockhost_version "$_ih_b") || _ih_have=; fi
  if [ -z "$_ih_r" ]; then
    lea_log "$_ih_t: no pinned release ($(lea__lockhost_var "$_ih_t")_REF is empty): lea installs nothing"
  elif [ "$_ih_have" != "$_ih_r" ]; then
    lea_log "$_ih_t: lea pins $_ih_r; this host has ${_ih_have:-none} in $(lea__lockhost_root)/usr/local/bin"
    if [ -z "${LEA_INIT_NONINTERACTIVE:-}" ] && lea__init_tty && lea__ask "Install $_ih_t $_ih_r there (cosign checks its signature; sudo)?"; then
      lea__lockhost_install "$_ih_t" "$_ih_r" || true
    else
      lea_log "later: lea init again (or lea init --$_ih_t)"
    fi
    _ih_b=$(lea__lockhost_bin "$_ih_t") || _ih_b=
  fi
  if [ -z "$_ih_b" ]; then
    lea_log "$_ih_t: not installed; lea's agents cannot reach a $_ih_t console yet (lea works without it)"
    return 0
  fi
  if [ "$_ih_t" = lockfile ]; then
    _ih_have=$(lea__lockhost_version "$_ih_b") || _ih_have=
    if ! lea__lockfile_safe "$_ih_have"; then
      lea_log "warning: lockfile ${_ih_have:-of unknown version} lets a plugin escape its sandbox: lea does not wire it (install $_ih_r: lea init)"
      return 0
    fi
  fi
  if ! lea__lockhost_setup_ok "$_ih_t"; then
    lea_log "$_ih_t: no separated console account yet ($_ih_t install: a $_ih_t account, a $_ih_t-clients group with you in it, /run/$_ih_t)"
    if [ -z "${LEA_INIT_NONINTERACTIVE:-}" ] && lea__init_tty && lea__ask "Set it up now ($_ih_t prints the root commands before it runs them with sudo)?"; then
      case $_ih_t in
        lockmq) "$_ih_b" install --client "$(lea_user_name)" || lea_log "warning: lockmq install failed (lea init again to retry)" ;;
        lockfile) sudo "$_ih_b" install --client "$(lea_user_name)" || lea_log "warning: lockfile install failed (lea init again to retry)" ;;
      esac
    else
      lea_log "later: sudo $_ih_t install --client $(lea_user_name)"
    fi
    lea__lockhost_setup_ok "$_ih_t" || return 0
    lea_log "$_ih_t: log out and in again for the new group, then give the $_ih_t account a password"
  fi
  case $_ih_t in
    lockmq)
      # lockmq setup asks for a broker profile, never its password, and writes it to the project,
      # a tree the agents can write: a symlink there (even one planted after any check) could send
      # what it writes elsewhere. So it runs as you in lea's image (its pinned lockmq client) with
      # only the projects directory mounted, where no symlink can reach anything else.
      if [ -L "${LEA_PROJECTS_DIR:-}/.lockmq" ] || [ -L "${LEA_PROJECTS_DIR:-}/.lockmq/config.toml" ]; then
        lea_log "warning: $LEA_PROJECTS_DIR/.lockmq or its config.toml is a symbolic link: lockmq setup not offered (remove it, then lea init)"
      elif [ -n "${LEA_PROJECTS_DIR:-}" ] && [ ! -f "$LEA_PROJECTS_DIR/.lockmq/config.toml" ] &&
        [ -z "${LEA_INIT_NONINTERACTIVE:-}" ] && lea__init_tty; then
        lea_log "lockmq has no broker profile for $LEA_PROJECTS_DIR yet"
        if lea__ask "Add one now (URLs, vhosts, user, where the password comes from; never the password)?"; then
          docker run -it --rm --network none --user "$(id -u):$(id -g)" -e HOME=/tmp -v "$LEA_PROJECTS_DIR:$LEA_PROJECTS_DIR" \
            -w "$LEA_PROJECTS_DIR" --entrypoint lockmq "$LEA_IMAGE" setup --project "$LEA_PROJECTS_DIR" ||
            lea_log "warning: no profile added (lea init again to retry)"
        else
          lea_log "later: lockmq setup --project $LEA_PROJECTS_DIR"
        fi
      fi ;;
    lockfile)
      lea_log "lockfile: its profiles and plugins live in the lockfile account; there, run lockfile profile add NAME --source s3|sftp|local ... and lockfile plugin install NAME@VERSION" ;;
  esac
  lea__lockhost_console_hint "$_ih_t"
}

# lea lockmq | lea lockfile: whether lea's agents reach that tool's console, and how to open it.
# It never starts a console: the console runs in the tool's own account and session.
lea_lockhost() {
  _lh_t=$1
  shift
  [ $# -eq 0 ] || lea_die "usage: lea $_lh_t (lea never starts the console: it runs in the $_lh_t account's own session)"
  _lh_r=$(lea__lockhost_get "$_lh_t" _REF)
  if [ "$(lea__lockhost_get "$_lh_t")" != 1 ]; then
    lea_log "$_lh_t is off ($(lea__lockhost_var "$_lh_t")=0): lea init --$_lh_t turns it on"
    return 0
  fi
  if _lh_b=$(lea__lockhost_bin "$_lh_t"); then
    lea_log "$_lh_t on this host: $(lea__lockhost_version "$_lh_b" || echo 'unknown version') (lea pins $_lh_r)"
  else
    lea_log "$_lh_t: not installed in /usr/local/bin (lea init installs $_lh_r)"
  fi
  if lea__lockhost_setup_ok "$_lh_t"; then lea_log "$_lh_t: separated setup present (/etc/$_lh_t/system.toml)"
  else lea_log "$_lh_t: no separated setup: sudo $_lh_t install --client $(lea_user_name)"; fi
  lea__lockhost_console_hint "$_lh_t"
}

# lea__doctor_lockhost TOOL: lea doctor's lines for TOOL when it is on.
lea__doctor_lockhost() {
  [ "$(lea__lockhost_get "$1")" = 1 ] || return 0
  if _dl_b=$(lea__lockhost_bin "$1"); then
    _dl_v=$(lea__lockhost_version "$_dl_b") || _dl_v=
    if [ "$1" = lockfile ] && ! lea__lockfile_safe "$_dl_v"; then
      lea__warn "$1 binary" "${_dl_v:-unknown version}: below v0.1.1 (plugin sandbox escape); lea does not wire it: lea init"
    else
      lea__ok "$1 binary $_dl_v"
    fi
  else
    lea__warn "$1 binary" "not in /usr/local/bin (root-owned): lea init installs $(lea__lockhost_get "$1" _REF)"
  fi
  if lea__lockhost_setup_ok "$1"; then lea__ok "$1 separated setup"
  else lea__warn "$1 separated setup" "none: sudo $1 install --client $(lea_user_name)"; fi
  if [ -d "$(lea__lockhost_root)/run/$1" ]; then lea__ok "$1 socket directory /run/$1"
  else lea__warn "$1 socket directory" "/run/$1 absent: $1 doctor (in the $1 account)"; fi
}
