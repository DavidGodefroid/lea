# shellcheck shell=sh
# Exit scan: files the host executes from the projects directory (git hooks and config, MCP and Claude settings).
# Variables inside pipelines run in subshells, so they never reach the caller. lea_scan_report
# runs in the caller's scope and therefore uses the _sr_ prefix.

# lea__scan_entry KIND PATH: one line for a regular file or a symlink. A symlink hashes what it
# points to ('-' when dangling) and records its target, so retargeting shows up.
lea__scan_entry() {
  [ -f "$2" ] || [ -L "$2" ] || return 0
  if [ -f "$2" ]; then _e_h=$(lea_sha256 <"$2"); else _e_h=-; fi
  printf '%s %s %s' "$1" "$2" "$_e_h"
  if [ "$1" = hook ]; then
    if [ -x "$2" ]; then printf ' x'; else printf ' -'; fi
  fi
  if [ -L "$2" ]; then printf ' -> %s' "$(readlink "$2")"; fi
  printf '\n'
}

lea__scan_file() { lea__scan_entry file "$1"; }

# lea__scan_benign KEY VALUE: git config keys that never make git run anything. Every other key
# is listed. A remote or submodule URL is benign unless it uses the ext:: or fd:: transport.
lea__scan_benign() {
  case $1 in
    remote.*.url|remote.*.pushurl|submodule.*.url)
      case $2 in ext::*|fd::*) return 1 ;; esac
      return 0 ;;
    core.repositoryformatversion|core.filemode|core.bare|core.logallrefupdates|core.ignorecase) return 0 ;;
    core.precomposeunicode|core.autocrlf|core.eol|core.symlinks|core.worktree) return 0 ;;
    branch.*|remote.*.fetch|user.*|submodule.*.active|init.*|pull.*|push.*|fetch.*) return 0 ;;
    rebase.autosquash|color.*|extensions.*) return 0 ;;
  esac
  return 1
}

# lea__scan_intree PATH: the physical PATH (its directory resolved) if it lies in the scanned
# projects dir ($_sm_root); fails otherwise, so out-of-tree files are never read.
lea__scan_intree() {
  if [ -d "$1" ]; then _it=$(CDPATH='' cd -- "$1" 2>/dev/null && pwd -P) || return 1
  elif [ -d "${1%/*}" ]; then _it=$(CDPATH='' cd -- "${1%/*}" 2>/dev/null && pwd -P)/${1##*/} || return 1
  else return 1; fi
  case $_it in "$_sm_root"|"$_sm_root"/*) printf '%s\n' "$_it" ;; *) return 1 ;; esac
}

# lea__scan_abs VALUE BASE: VALUE as an absolute path ('~/' is the home directory, a relative
# path is relative to BASE; empty when BASE is unknown).
lea__scan_abs() {
  # shellcheck disable=SC2088 # matches a literal '~/' written in git config
  case $1 in
    /*) printf '%s\n' "$1" ;;
    '~/'*) printf '%s/%s\n' "$HOME" "${1#'~/'}" ;;
    *) if [ -n "$2" ]; then printf '%s/%s\n' "$2" "$1"; fi ;;
  esac
}

# lea__scan_hooks_path VALUE WORKTREE: every hook under an in-tree core.hooksPath (husky...).
lea__scan_hooks_path() {
  _hp=$(lea__scan_abs "$1" "$2"); [ -n "$_hp" ] || return 0
  _hp=$(lea__scan_intree "$_hp") || return 0
  [ -d "$_hp" ] || return 0
  find "$_hp" \( -type f -o -type l \) ! -name '*.sample' | LC_ALL=C sort | while IFS= read -r _f; do
    lea__scan_entry hook "$_f"
  done
}

# lea__scan_include VALUE CONFIG_DIR: an in-tree include.path / includeIf.*.path target.
lea__scan_include() {
  _ip=$(lea__scan_abs "$1" "$2"); [ -n "$_ip" ] || return 0
  _ip=$(lea__scan_intree "$_ip") || return 0
  lea__scan_entry include "$_ip"
}

# lea__scan_gitconfig FILE ARGS...: git config -f FILE ARGS, run from / so that git never
# discovers a repository around the caller's working directory: a .git file there that points
# nowhere (planted in the projects dir, or a worktree whose gitdir is not reachable) makes every
# git config -f fail, which would leave the scan blind.
lea__scan_gitconfig() {
  case $1 in /*) _gc_f=$1 ;; *) _gc_f=$PWD/$1 ;; esac
  shift
  (cd / && unset GIT_DIR GIT_WORK_TREE GIT_COMMON_DIR && exec git config -f "$_gc_f" "$@")
}

# lea__scan_gitdir GITDIR [WORKTREE]: hooks, every non-benign key of the config files git reads
# without being asked, and the in-tree files those keys point to.
lea__scan_gitdir() {
  if [ -d "$1/hooks" ]; then
    find "$1/hooks" \( -type f -o -type l \) ! -name '*.sample' | LC_ALL=C sort | while IFS= read -r _f; do
      lea__scan_entry hook "$_f"
    done
  fi
  _wt=${2:-}
  if [ -z "$_wt" ]; then
    _wt=$(lea__scan_gitconfig "$1/config" core.worktree 2>/dev/null || true)
    case $_wt in ''|/*) ;; *) _wt=$1/$_wt ;; esac
  fi
  for _c in "$1/config" "$1/config.worktree" "$1"/worktrees/*/config "$1"/worktrees/*/config.worktree; do
    [ -f "$_c" ] || continue
    case $_c in "$1"/worktrees/*) [ ! -L "$1/worktrees" ] || continue ;; esac
    lea__scan_gitconfig "$_c" --get-regexp '.' 2>/dev/null | while IFS= read -r _kv; do
      _k=${_kv%% *}
      case $_kv in *' '*) _v=${_kv#* } ;; *) _v= ;; esac
      if lea__scan_benign "$_k" "$_v"; then continue; fi
      printf 'gitconfig %s %s\n' "$_c" "$_kv"
      case $_k in
        core.hookspath) lea__scan_hooks_path "$_v" "$_wt" ;;
        include.path|includeif.*.path) lea__scan_include "$_v" "${_c%/*}" ;;
      esac
    done
  done
}

# lea__scan_git GITDIR WORKTREE: the repository's gitdir, then every submodule gitdir below .git/modules.
lea__scan_git() {
  [ -d "$1" ] || return 0
  lea__scan_gitdir "$1" "$2"
  find "$1/modules" \( -name hooks -type d -o -name config -type f \) 2>/dev/null \
    | sed 's|/[^/]*$||' | LC_ALL=C sort -u | while IFS= read -r _g; do
    lea__scan_gitdir "$_g"
  done
}

# A symlinked .git or .claude is listed with its target and never followed.
lea_scan_manifest() {
  _sm_root=$(CDPATH='' cd -- "$1" 2>/dev/null && pwd -P) || _sm_root=$1
  find "$1" \( -name node_modules -o -name vendor \) -prune \
    -o -name .git -print -prune \
    -o \( -name .mcp.json -o -name .claude \) -print 2>/dev/null \
  | LC_ALL=C sort | while IFS= read -r _p; do
    case ${_p##*/} in
      .git|.claude)
        if [ -L "$_p" ]; then printf 'link %s -> %s\n' "$_p" "$(readlink "$_p")"; continue; fi ;;
    esac
    case ${_p##*/} in
      .git) lea__scan_git "$_p" "${_p%/*}" ;;
      .mcp.json) lea__scan_file "$_p" ;;
      .claude)
        lea__scan_file "$_p/settings.json"
        lea__scan_file "$_p/settings.local.json"
        if [ -d "$_p/hooks" ]; then
          find "$_p/hooks" \( -type f -o -type l \) | LC_ALL=C sort | while IFS= read -r _f; do lea__scan_file "$_f"; done
        fi ;;
    esac
  done
}

# The start snapshot of a projects dir, in the host-only dir.
lea_scan_start_file() { printf '%s/scan/%s.start' "$(lea_host_dir)" "$(lea_id "$1")"; }

lea_scan_report() {
  _sr_start=$(lea_scan_start_file "$1")
  lea_safe_path "$(lea_data_dir)" "$_sr_start"
  if [ ! -f "$_sr_start" ]; then
    lea_log "WARNING: no start snapshot for $1; the exit scan cannot compare"
    return 0
  fi
  _sr_now=$(mktemp)
  lea_scan_manifest "$1" >"$_sr_now"
  if ! diff -u "$_sr_start" "$_sr_now" >"$_sr_now.diff"; then
    lea_log "WARNING: files that the host executes changed during this lea run:"
    sed -n '3,$p' "$_sr_now.diff" | grep -E '^[+-]' >&2 || true
    lea_log "Review them before running git, an IDE or Claude Code on the host in $1."
  fi
  rm -f "$_sr_now" "$_sr_now.diff"
}

# lea_scan_finish DIR: the exit scan, then the start snapshot is dropped, so that a snapshot found
# at the next start means that the previous run ended without its exit scan.
lea_scan_finish() {
  lea_scan_report "$1"
  rm -f "$(lea_scan_start_file "$1")"
}

# lea_scan_begin DIR: the start snapshot of a run. A snapshot left by a run that ended without
# its exit scan (crash, reboot) is compared first.
lea_scan_begin() {
  _sg_start=$(lea_scan_start_file "$1")
  lea_mkdirs "$(lea_data_dir)" "${_sg_start%/*}"
  lea_safe_path "$(lea_data_dir)" "$_sg_start"
  if [ -f "$_sg_start" ]; then
    lea_log "the previous lea run on $1 ended without its exit scan (crash, reboot?); comparing with its start snapshot"
    lea_scan_finish "$1"
  fi
  lea_scan_manifest "$1" | lea_write_file "$(lea_data_dir)" "$_sg_start"
}
