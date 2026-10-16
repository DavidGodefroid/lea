# shellcheck shell=sh
# Shared helpers for the lea launcher.

lea_log() { printf 'lea: %s\n' "$*" >&2; }
lea_die() { printf 'lea: error: %s\n' "$*" >&2; exit 1; }

# Absolute physical path of an existing directory.
lea_abspath() {
  [ -d "$1" ] || lea_die "not a directory: $1"
  (CDPATH='' cd -- "$1" && pwd -P)
}

# Claude Code project slug: every character other than [A-Za-z0-9] becomes '-'.
lea_slug() { printf '%s' "$1" | sed 's/[^A-Za-z0-9]/-/g'; }

lea_sha256() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum | cut -d' ' -f1
  else shasum -a 256 | cut -d' ' -f1; fi
}

lea_id() { printf '%s' "$1" | lea_sha256 | cut -c1-10; }

# <data>/lea holds state/ (mounted into lea item by item) and host/ (locks, sessions, scan
# snapshots, scratch files; never mounted, except host/run/<container>, the bus directory each
# container gets at /run/lea/bus).
lea_data_dir() { printf '%s/lea' "${XDG_DATA_HOME:-$HOME/.local/share}"; }
lea_state_dir() { printf '%s/state' "$(lea_data_dir)"; }
lea_host_dir() { printf '%s/host' "$(lea_data_dir)"; }

# lea_safe_path BASE PATH: dies unless PATH is BASE or below it, without '..', and no component
# below BASE (PATH included) is a symbolic link. Missing components are fine. lea calls it before
# every host-side mount, mkdir, write or rm under <data>/lea and ~/.claude, so that a symlink
# planted from inside lea never redirects the host.
lea_safe_path() {
  case $2 in
    "$1") return 0 ;;
    "$1"/*) ;;
    *) lea_die "internal: $2 is not below $1" ;;
  esac
  case /$2/ in */../*|*/./*) lea_die "refusing to use $2: it contains '.' or '..'" ;; esac
  _sp_cur=$1
  _sp_rest=${2#"$1"/}
  while [ -n "$_sp_rest" ]; do
    case $_sp_rest in
      */*) _sp_cur=$_sp_cur/${_sp_rest%%/*}; _sp_rest=${_sp_rest#*/} ;;
      *) _sp_cur=$_sp_cur/$_sp_rest; _sp_rest= ;;
    esac
    if [ -L "$_sp_cur" ]; then
      lea_die "refusing to use $2: $_sp_cur is a symbolic link (remove it, then retry)"
    fi
  done
}

# lea_mkdirs BASE DIR...: mkdir -p each DIR after lea_safe_path.
lea_mkdirs() {
  _md_base=$1; shift
  for _md_d in "$@"; do
    lea_safe_path "$_md_base" "$_md_d"
    mkdir -p "$_md_d"
  done
}

# lea_write_file BASE PATH: writes stdin to PATH through a temp file in the same directory, then mv.
lea_write_file() {
  lea_safe_path "$1" "$2"
  _wf_t=$(mktemp "$2.XXXXXX") || lea_die "cannot create a temporary file next to $2"
  if cat >"$_wf_t" && mv -f "$_wf_t" "$2"; then return 0; fi
  rm -f "$_wf_t"
  return 1
}

lea_user_name() { id -un 2>/dev/null || printf 'u%s' "$(id -u)"; }
