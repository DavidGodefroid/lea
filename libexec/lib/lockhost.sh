# shellcheck shell=sh
# lockmq and lockfile, lock tools whose console runs in its own container, started by the tool's
# launcher (scripts/<tool>, installed by its install.sh; lockkit's isolation checks refuse a
# console in lea's container). lea only lets its agents reach the console's socket, as locksql's:
# the socket volume <tool>-run on /run/<tool> and the tool's clients group
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

# lea__lockhost_volume TOOL: the state of TOOL's socket volume <tool>-run: absent, old (lockfile
# before 0.3: locksql's ids, until its launcher migrates it), ok, or unknown.
lea__lockhost_volume() {
  docker volume inspect "$1-run" >/dev/null 2>&1 || { printf absent; return 0; }
  [ "$1" = lockfile ] || { printf ok; return 0; }
  _lv_o=$(docker run --rm --network none --entrypoint stat --mount "type=volume,src=lockfile-run,dst=/r,readonly" \
    "${LEA_IMAGE:-}" -c %u:%g /r 2>/dev/null) || { printf unknown; return 0; }
  case $_lv_o in
    10031:10030) printf ok ;;
    10001:10000) printf old ;;
    *) printf unknown ;;
  esac
}

# lea__lockhost_console_hint TOOL: how to open TOOL's console (lea never starts it).
# lea's agents run as your uid, which names no account in the console's image: the console serves
# it only when told so (--allowed-uids, lockmq v0.4.1 and lockfile v0.5.1 on), by you, never by lea.
lea__lockhost_console_hint() {
  lea_log "$1 console: start it with $1's launcher, in a terminal of yours: $1 console --profile PROFILE --allowed-uids $(id -u)"
  lea_log "  (--allowed-uids $(id -u): lea's agents run as your uid, which has no account in the console's image; they reach it through the $1-run volume after their lea restarts: lea stop --all, then lea)"
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
  if [ "$_ih_t" = lockfile ] && [ "$(lea__lockhost_volume lockfile)" = old ]; then
    lea_log "lockfile: start \`lockfile console\` once: it migrates the volume (lockfile-run still has the ids of lockfile before 0.3; until then lea leaves lockfile out)"
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

# lea lockmq | lea lockfile: whether lea's agents reach that tool's console, and how to open it.
# It never starts a console: the tool's launcher does.
lea_lockhost() {
  _lh_t=$1
  shift
  [ $# -eq 0 ] || lea_die "usage: lea $_lh_t (lea never starts the console: $_lh_t's launcher does, $_lh_t console)"
  if [ "$(lea__lockhost_get "$_lh_t")" != 1 ]; then
    lea_log "$_lh_t is off ($(lea__lockhost_var "$_lh_t")=0): lea init --$_lh_t turns it on"
    return 0
  fi
  if _lh_b=$(lea__lockhost_launcher "$_lh_t"); then
    lea_log "$_lh_t launcher: $_lh_b (lea's image has the $(lea__lockhost_get "$_lh_t" _REF) client)"
  else
    lea__lockhost_install_hint "$_lh_t"
  fi
  case $(lea__lockhost_volume "$_lh_t") in
    ok) lea_log "$_lh_t: socket volume $_lh_t-run present" ;;
    absent) lea_log "$_lh_t: no $_lh_t-run volume yet (the console makes it when it starts)" ;;
    old) lea_log "$_lh_t: start \`$_lh_t console\` once: it migrates the volume (lea leaves $_lh_t out until then)" ;;
    *) lea_log "$_lh_t: cannot read the owner of the $_lh_t-run volume" ;;
  esac
  lea__lockhost_console_hint "$_lh_t"
}

# lea__doctor_lockhost TOOL: lea doctor's lines for TOOL when it is on.
lea__doctor_lockhost() {
  [ "$(lea__lockhost_get "$1")" = 1 ] || return 0
  if _dl_b=$(lea__lockhost_launcher "$1"); then lea__ok "$1 launcher $_dl_b"
  else lea__warn "$1 launcher" "not installed: https://github.com/DavidGodefroid/$1#install"; fi
  case $(lea__lockhost_volume "$1") in
    ok) lea__ok "$1-run volume" ;;
    absent) lea__warn "$1-run volume" "absent: start the console once ($1 console --profile PROFILE --allowed-uids $(id -u))" ;;
    old) lea__warn "$1-run volume" "ids of lockfile before 0.3: start \`lockfile console\` once: it migrates the volume" ;;
    *) lea__warn "$1-run volume" "owner unknown" ;;
  esac
}
