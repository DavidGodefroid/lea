# shellcheck shell=sh
# Loads the organisation configuration, then the user configuration ($LEA_CONFIG, else
# ~/.config/lea/lea.conf, written by lea init). A non-empty LEA_PROJECTS_DIR in the environment
# beats both (so do LEA_APPROVALS and LEA_APPROVAL_TIMEOUT).

lea_user_config_path() {
  printf '%s' "${LEA_CONFIG:-${XDG_CONFIG_HOME:-$HOME/.config}/lea/lea.conf}"
}

lea_load_config() {
  _lc_env=${LEA_PROJECTS_DIR:-}
  _lc_appr=${LEA_APPROVALS:-}
  _lc_appt=${LEA_APPROVAL_TIMEOUT:-}
  # shellcheck source=/dev/null
  . "$LEA_ROOT/config/lea.conf"
  _lc_user=$(lea_user_config_path)
  case $_lc_user in /*) ;; *) _lc_user=./$_lc_user ;; esac
  if [ -n "${LEA_CONFIG:-}" ] || [ -f "$_lc_user" ]; then
    # shellcheck source=/dev/null
    . "$_lc_user"
  fi
  LEA_PROJECTS_DIR=${_lc_env:-${LEA_PROJECTS_DIR:-}}
  LEA_APPROVALS=${_lc_appr:-${LEA_APPROVALS:-deck}}
  LEA_APPROVAL_TIMEOUT=${_lc_appt:-${LEA_APPROVAL_TIMEOUT:-60}}
  case $LEA_APPROVALS in deck|pane) ;; *) lea_die "LEA_APPROVALS must be deck or pane, not '$LEA_APPROVALS'" ;; esac
  case $LEA_APPROVAL_TIMEOUT in
    ''|0*|*[!0-9]*|????*) lea_die "LEA_APPROVAL_TIMEOUT must be a number of seconds from 1 to 600 without leading zeros, not '$LEA_APPROVAL_TIMEOUT'" ;;
  esac
  if [ "$LEA_APPROVAL_TIMEOUT" -lt 1 ] || [ "$LEA_APPROVAL_TIMEOUT" -gt 600 ]; then
    lea_die "LEA_APPROVAL_TIMEOUT must be a number of seconds from 1 to 600 without leading zeros, not '$LEA_APPROVAL_TIMEOUT'"
  fi
  LEA_INTERNAL_HOSTS=${LEA_INTERNAL_HOSTS:-$LEA_ROOT/config/internal-hosts}
  lea__check_egress_allow
  # The git SSH endpoints (none by default: git over HTTPS needs none). LEA_GITLAB_SSH and
  # LEA_GITLAB_KNOWN_HOSTS, the keys of lea before any forge could be used, are read when the new
  # ones are unset.
  # shellcheck disable=SC2086
  LEA_GIT_SSH=$(set -f; echo ${LEA_GIT_SSH:-${LEA_GITLAB_SSH:-}})
  lea__check_git_ssh
  _lc_conf=${XDG_CONFIG_HOME:-$HOME/.config}/lea
  LEA_GIT_KNOWN_HOSTS=${LEA_GIT_KNOWN_HOSTS:-${LEA_GITLAB_KNOWN_HOSTS:-}}
  if [ -z "$LEA_GIT_KNOWN_HOSTS" ]; then
    LEA_GIT_KNOWN_HOSTS=$_lc_conf/git_known_hosts
    if [ ! -e "$LEA_GIT_KNOWN_HOSTS" ] && [ -e "$_lc_conf/gitlab_known_hosts" ]; then
      LEA_GIT_KNOWN_HOSTS=$_lc_conf/gitlab_known_hosts
    fi
  fi
  lea__check_otel_upstream
  LEA_LOCKSQL=${LEA_LOCKSQL:-1}
  case $LEA_LOCKSQL in 0|1) ;; *) lea_die "LEA_LOCKSQL must be 1 (wire lea for a locksql console) or 0, not '$LEA_LOCKSQL'" ;; esac
  LEA_LOCKMQ=${LEA_LOCKMQ:-0}
  case $LEA_LOCKMQ in 0|1) ;; *) lea_die "LEA_LOCKMQ must be 1 (reach a lockmq console on this host) or 0, not '$LEA_LOCKMQ'" ;; esac
  LEA_LOCKFILE=${LEA_LOCKFILE:-0}
  case $LEA_LOCKFILE in 0|1) ;; *) lea_die "LEA_LOCKFILE must be 1 (reach a lockfile console on this host) or 0, not '$LEA_LOCKFILE'" ;; esac
  lea__semver "${LEA_LOCKMQ_REF:-}" >/dev/null || lea_die "LEA_LOCKMQ_REF must be a lockmq release (vX.Y.Z), not '${LEA_LOCKMQ_REF:-}'"
  lea__semver "${LEA_LOCKFILE_REF:-}" >/dev/null || lea_die "LEA_LOCKFILE_REF must be a lockfile release (vX.Y.Z), not '${LEA_LOCKFILE_REF:-}'"
  lea__lockfile_safe "$LEA_LOCKFILE_REF" || lea_die "LEA_LOCKFILE_REF $LEA_LOCKFILE_REF is refused: lockfile v0.1.0 lets a plugin escape its sandbox (v0.1.1 or later)"
  LEA_AUTOUPDATE=${LEA_AUTOUPDATE:-1}
  case $LEA_AUTOUPDATE in 0|1) ;; *) lea_die "LEA_AUTOUPDATE must be 1 (check for a new release every day) or 0, not '$LEA_AUTOUPDATE'" ;; esac
  LEA_AUTOUPDATE_SKIP=${LEA_AUTOUPDATE_SKIP:-}
  if [ -n "$LEA_AUTOUPDATE_SKIP" ] && ! lea__semver "$LEA_AUTOUPDATE_SKIP" >/dev/null; then
    lea_die "LEA_AUTOUPDATE_SKIP must be empty or a release version (vX.Y.Z, set by lea rollback), not '$LEA_AUTOUPDATE_SKIP'"
  fi
  unset LEA_GITLAB_SSH LEA_GITLAB_KNOWN_HOSTS LEA_REGISTRY
  export LEA_PREFIX LEA_IMAGE LEA_PROXY_IMAGE LEA_GIT_SSH LEA_GIT_KNOWN_HOSTS LEA_LOCKSQL LEA_LOCKSQL_IMAGE LEA_LOCKSQL_REF LEA_LOCKSQL_VAULT_ADDR \
    LEA_LOCKMQ LEA_LOCKFILE LEA_LOCKMQ_REF LEA_LOCKFILE_REF \
    LEA_OTEL_UPSTREAM LEA_NESTED_NETWORKS LEA_K8S_NAMESPACE LEA_INTERNAL_HOSTS LEA_EGRESS_ALLOW LEA_PROJECTS_DIR \
    LEA_APPROVALS LEA_APPROVAL_TIMEOUT LEA_SOUND LEA_SOUND_ATTENTION LEA_SOUND_DONE
}

# lea__check_egress_allow: LEA_EGRESS_ALLOW lists at most 64 domain names, separated by commas or
# blanks (each one also stands for its subdomains; no IP literal, no wildcard). Empty: no
# allowlist. Normalized to lower-case names joined by commas.
lea__check_egress_allow() {
  _ce_n=0 _ce_out=
  set -f
  for _ce_d in $(printf '%s' "${LEA_EGRESS_ALLOW:-}" | tr ',' ' '); do
    _ce_n=$((_ce_n + 1))
    case $_ce_d in
      *[!ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789.-]*|.*|*.|*..*) _ce_bad=1 ;;
      *[!0-9.]*) _ce_bad= ;;
      *) _ce_bad=1 ;;
    esac
    if [ -n "$_ce_bad" ]; then
      set +f; lea_die "LEA_EGRESS_ALLOW must list domain names separated by commas, got '$_ce_d'"
    fi
    _ce_out="$_ce_out,$(printf '%s' "$_ce_d" | tr '[:upper:]' '[:lower:]')"
  done
  set +f
  [ "$_ce_n" -le 64 ] || lea_die "LEA_EGRESS_ALLOW lists $_ce_n domains, at most 64"
  LEA_EGRESS_ALLOW=${_ce_out#,}
}

# lea__check_otel_upstream: LEA_OTEL_UPSTREAM is empty or host:port, a plain host name and a port
# from 1 to 65535 (the proxy hands it to socat as TCP:<value>: no address options).
lea__check_otel_upstream() {
  _co_v=${LEA_OTEL_UPSTREAM:-}
  [ -n "$_co_v" ] || return 0
  _co_h=${_co_v%%:*} _co_p=${_co_v#*:}
  case $_co_v in *:*) ;; *) _co_p= ;; esac
  case $_co_h in ''|*[!ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789.-]*) _co_p= ;; esac
  case $_co_p in
    ''|0*|*[!0-9]*|??????*) _co_bad=1 ;;
    *) if [ "$_co_p" -gt 65535 ]; then _co_bad=1; else _co_bad=; fi ;;
  esac
  [ -z "$_co_bad" ] || lea_die "LEA_OTEL_UPSTREAM must be host:port with a plain host name, not '$_co_v'"
}

# lea__check_git_ssh: LEA_GIT_SSH lists at most 16 host:port endpoints, each a plain host name
# (written into the image's ssh configuration) and a port from 1 to 65535, no host twice.
lea__check_git_ssh() {
  _cg_n=0 _cg_seen=' '
  set -f
  for _cg_ep in $LEA_GIT_SSH; do
    _cg_n=$((_cg_n + 1))
    _cg_h=${_cg_ep%%:*} _cg_p=${_cg_ep#*:}
    case $_cg_ep in *:*) ;; *) _cg_p= ;; esac
    case $_cg_h in ''|*[!ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789.-]*) _cg_p= ;; esac
    case $_cg_p in ''|0*|*[!0-9]*|??????*) _cg_bad=1 ;; *) _cg_bad= ;; esac
    if [ -z "$_cg_bad" ] && [ "$_cg_p" -gt 65535 ]; then _cg_bad=1; fi
    if [ -n "$_cg_bad" ]; then
      set +f; lea_die "LEA_GIT_SSH must list host:port endpoints with plain host names, got '$_cg_ep'"
    fi
    case $_cg_seen in *" $_cg_h "*) set +f; lea_die "LEA_GIT_SSH lists $_cg_h twice" ;; esac
    _cg_seen="$_cg_seen$_cg_h "
  done
  set +f
  [ "$_cg_n" -le 16 ] || lea_die "LEA_GIT_SSH lists $_cg_n endpoints, at most 16"
}

# lea_user_config_set KEY VALUE: sets KEY='VALUE' in the user config and keeps every other line.
# The file is private; a symlinked file is refused.
lea_user_config_set() {
  _cs_f=$(lea_user_config_path)
  if [ -L "$_cs_f" ]; then lea_die "refusing to write $_cs_f: it is a symbolic link"; fi
  mkdir -p "${_cs_f%/*}"
  _cs_v=$(printf '%s' "$2" | sed "s/'/'\\\\''/g")
  _cs_t=$(mktemp "$_cs_f.XXXXXX") || lea_die "cannot create a temporary file next to $_cs_f"
  if {
    if [ -f "$_cs_f" ]; then grep -v "^$1=" "$_cs_f" || true; fi
    printf "%s='%s'\n" "$1" "$_cs_v"
  } >"$_cs_t" && chmod 600 "$_cs_t" && mv -f "$_cs_t" "$_cs_f"; then return 0; fi
  rm -f "$_cs_t"
  return 1
}
