# shellcheck shell=sh
# lockmq and lockfile, lock tools whose console runs in its own container, hub-attached only:
# lockhub's `lox up` runs it for the projects directory (lockkit's isolation checks refuse a
# console in lea's container). lea only lets its agents reach the console's socket: the socket
# volume of lox up's lock (.lockfamily/up.json) on /run/<tool> and the tool's clients group
# (internal/launcher/lockhost.go). lea never starts, sees or drives a console, holds none of its
# credentials, and runs no maintenance on its volumes.

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
# them (lea runs the binary they vouch for); the unit tests redefine these functions.
lea__lockhost_root() { :; }
lea__lockhost_owner() { printf 0; }

# lea__lockhost_bin TOOL: the root-owned TOOL launcher in /usr/local/bin (lockhttp's), or nothing.
# Neither it nor the directory may be writable by anyone but root.
lea__lockhost_bin() {
  _lb_d=$(lea__lockhost_root)/usr/local/bin
  _lb_b=$_lb_d/$1
  [ -f "$_lb_b" ] && [ ! -L "$_lb_b" ] && [ -x "$_lb_b" ] && [ -d "$_lb_d" ] && [ ! -L "$_lb_d" ] || return 1
  for _lb_f in "$_lb_b" "$_lb_d"; do
    [ "$(stat -c %u "$_lb_f" 2>/dev/null)" = "$(lea__lockhost_owner)" ] || return 1
    case $(stat -c %A "$_lb_f" 2>/dev/null) in ?????w????|????????w?|'') return 1 ;; esac
  done
  printf '%s' "$_lb_b"
}

# lea__lockhost_launcher TOOL: the path of TOOL's launcher (its install.sh puts it on the PATH),
# or nothing.
lea__lockhost_launcher() { command -v "$1" 2>/dev/null; }

# lea__lockhost_install_hint TOOL: how to install TOOL's launcher (lea installs nothing for it).
lea__lockhost_install_hint() {
  lea_log "$1: install its launcher (it runs $1 in its signed image): see https://github.com/DavidGodefroid/$1#install"
  lea_log "  V=$(lea__lockhost_get "$1" _REF); curl -fsSLO https://raw.githubusercontent.com/DavidGodefroid/$1/\$V/scripts/install.sh; less install.sh && sh install.sh"
}

# lea__lockhost_console_hint TOOL: how TOOL's console runs (lea never starts it): hub-attached
# only, under lox up. lea's agents run as your uid, which names no account in the console's image:
# the console serves it only when told so (the lock's allowed_uids), by you, never by lea.
lea__lockhost_console_hint() {
  lea_log "$1 console: it runs under lox up only: add a $1 lock to your projects directory (lox add $1 NAME, with allowed_uids: [$(id -u)] in .lockfamily/lockfamily.yaml), then run \`lox up\`"
  lea_log "  (allowed_uids $(id -u): lea's agents run as your uid, which has no account in the console's image; they reach the lock's socket volume after their lea restarts: lea stop --all, then lea)"
}

# lea__init_lockhost TOOL: lea init's part for TOOL. Without a decision yet (no flag, not in the
# user config) and on a terminal, it asks, no by default (pre-release tools). When on: whether
# TOOL's launcher is installed (how to install it otherwise), the guided profile setup when TOOL
# offers one, and how to open the console. Never fatal.
lea__init_lockhost() {
  _ih_t=$1
  _ih_v=$(lea__lockhost_var "$_ih_t")
  if ! grep -q "^$_ih_v=" "$(lea_user_config_path)" 2>/dev/null; then
    if [ -z "${LEA_INIT_NONINTERACTIVE:-}" ] && lea__init_tty; then
      if lea__ask_no "Let lea's agents query $(lea__lockhost_what "$_ih_t") through a $_ih_t console (pre-release; a human approves every read)?"; then
        lea__lockhost_set "$_ih_t" 1
      else
        lea__lockhost_set "$_ih_t" 0
      fi
    fi
  fi
  [ "$(lea__lockhost_get "$_ih_t")" = 1 ] || return 0
  if ! lea__lockhost_launcher "$_ih_t" >/dev/null; then
    lea__lockhost_install_hint "$_ih_t"
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
      lea_log "lockfile: its profiles and plugins live in its console's volume; with its launcher, run lockfile profile add NAME --source s3|sftp|local ... and lockfile plugin install NAME@VERSION" ;;
  esac
  lea__lockhost_console_hint "$_ih_t"
}

# lea lockmq | lea lockfile: whether the tool is on, and how its console runs. It never starts a
# console: lox up does.
lea_lockhost() {
  _lh_t=$1
  shift
  [ $# -eq 0 ] || lea_die "usage: lea $_lh_t (lea never starts the console: lox up does)"
  if [ "$(lea__lockhost_get "$_lh_t")" != 1 ]; then
    lea_log "$_lh_t is off ($(lea__lockhost_var "$_lh_t")=0): lea init --$_lh_t turns it on"
    return 0
  fi
  if _lh_b=$(lea__lockhost_launcher "$_lh_t"); then
    lea_log "$_lh_t launcher: $_lh_b (lea's image has the $(lea__lockhost_get "$_lh_t" _REF) client)"
  else
    lea__lockhost_install_hint "$_lh_t"
  fi
  lea__lockhost_console_hint "$_lh_t"
}

# lea__doctor_lockhost TOOL: lea doctor's lines for TOOL when it is on.
lea__doctor_lockhost() {
  [ "$(lea__lockhost_get "$1")" = 1 ] || return 0
  if _dl_b=$(lea__lockhost_launcher "$1"); then lea__ok "$1 launcher $_dl_b"
  else lea__warn "$1 launcher" "not installed: https://github.com/DavidGodefroid/$1#install"; fi
}
