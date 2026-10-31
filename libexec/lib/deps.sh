# shellcheck shell=sh
# lea update's dependencies: locksql (its console image, built from the source of a release). lockmq
# and lockfile run from their own launchers, which update themselves and their images (lockmq
# update, lockfile update): lea update only says so. lea pins a release of locksql
# (config/lea.conf); lea update looks for a newer one and installs it only when you accept it, after checking that the
# tool's release workflow signed it at that very tag (cosign), then records it in the user config.
# It never installs a release that is not newer than the current one, below the tool's security
# floor, or not a plain vX.Y.Z; it never installs anything for an empty pin, and never silently.

# lea__dep_url DEP: DEP's repository (its releases are under /releases).
lea__dep_url() {
  case $1 in
    locksql|lockmq|lockfile) printf 'https://github.com/DavidGodefroid/%s' "$1" ;;
    *) lea_die "unknown dependency '$1'" ;;
  esac
}

# lea__dep_latest DEP: the tag of DEP's latest release (never a pre-release), vX.Y.Z; fails when
# it cannot be found or is anything else.
lea__dep_latest() {
  _dl_v=$(curl -fsSLI -o /dev/null -w '%{url_effective}' "$(lea__dep_url "$1")/releases/latest" 2>/dev/null | sed 's|.*/tag/||')
  lea__semver "$_dl_v" >/dev/null || return 1
  printf '%s' "$_dl_v"
}

# lea__dep_floor_ok DEP vX.Y.Z: the release is not below DEP's security floor (lockfile v0.1.1:
# v0.1.0 lets a plugin escape its sandbox).
lea__dep_floor_ok() {
  case $1 in
    lockfile) lea__lockfile_safe "$2" ;;
    *) lea__semver "$2" >/dev/null ;;
  esac
}

# lea__dep_pin KEY: KEY as config/lea.conf sets it (lea's own pin, whatever the user config says).
lea__dep_pin() {
  sed -n "s/^$1=//p" "$LEA_ROOT/config/lea.conf" | tail -n 1
}

# lea__dep_current DEP: the release of DEP lea uses now, printed; fails, saying why, when lea
# update leaves DEP alone (off, no pin, a local checkout, not installed, unknown version).
lea__dep_current() {
  case $1 in
    locksql)
      [ "$LEA_LOCKSQL" = 1 ] || return 1
      if [ -z "${LEA_LOCKSQL_REF:-}" ]; then
        lea_log "locksql: no pinned release (LEA_LOCKSQL_REF is empty): lea update installs nothing"
        return 1
      fi
      if ! lea__semver "$LEA_LOCKSQL_REF" >/dev/null; then
        lea_log "locksql: LEA_LOCKSQL_REF is $LEA_LOCKSQL_REF, not a release: lea update leaves it"
        return 1
      fi
      printf '%s' "$LEA_LOCKSQL_REF" ;;
  esac
}

# lea__dep_ref DEP: the release lea pins for DEP now (the user config's choice, else lea's).
lea__dep_ref() {
  case $1 in
    locksql) printf '%s' "${LEA_LOCKSQL_REF:-}" ;;
    *) lea__lockhost_get "$1" _REF ;;
  esac
}

# lea_update_deps ask|all|none: the last step of lea update, run by the installation lea update
# just installed (its pins and floors). For each dependency that is on and pinned, a newer release
# is shown (current -> available, and its release notes) and installed only when accepted: asked
# one by one (no by default) with ask on a terminal, every one with all, none with none or without
# a terminal. Never fatal: a dependency that cannot be updated stays as it is.
lea_update_deps() {
  _ud_m=${1:-ask}
  case $_ud_m in ask|all|none) ;; *) lea_die "usage: lea update [--deps ask|all|none]" ;; esac
  _ud_left=
  for _ud_d in lockmq lockfile; do
    if [ "$(lea__lockhost_get "$_ud_d")" = 1 ]; then
      lea_log "$_ud_d: its launcher updates it and its image ($_ud_d update); lea's agents use the client of lea's image"
    fi
  done
  # shellcheck disable=SC2043 # one dependency today; the loop keeps room for others
  for _ud_d in locksql; do
    _ud_cur=$(lea__dep_current "$_ud_d") || continue
    if ! _ud_new=$(lea__dep_latest "$_ud_d"); then
      lea_log "$_ud_d: cannot find its latest release ($(lea__dep_url "$_ud_d")/releases/latest): not updated"
      continue
    fi
    _ud_ref=$(lea__dep_ref "$_ud_d")
    if ! lea__older "$_ud_cur" "$_ud_new" || lea__older "$_ud_new" "$_ud_ref"; then
      lea_log "$_ud_d $_ud_cur: up to date"
      continue
    fi
    if ! lea__dep_floor_ok "$_ud_d" "$_ud_new"; then
      lea_log "warning: $_ud_d $_ud_new is below its security floor: not offered"
      continue
    fi
    lea_log "$_ud_d: $_ud_cur -> $_ud_new (release notes: $(lea__dep_url "$_ud_d")/releases/tag/$_ud_new)"
    case $_ud_m in
      all) ;;
      none)
        lea_log "$_ud_d: not updated (--deps none)"
        _ud_left="$_ud_left $_ud_d"
        continue ;;
      ask)
        if ! lea__init_tty; then
          lea_log "$_ud_d: not updated (no terminal to ask)"
          _ud_left="$_ud_left $_ud_d"
          continue
        fi
        if ! lea__ask_no "Update $_ud_d to $_ud_new (its signature is checked first)?"; then
          lea_log "$_ud_d: kept at $_ud_cur"
          continue
        fi ;;
    esac
    lea__dep_update "$_ud_d" "$_ud_new" || lea_log "warning: $_ud_d stays at $_ud_cur"
  done
  if [ -n "$_ud_left" ]; then
    lea_log "not updated:$_ud_left (lea update on a terminal asks; lea update --deps all takes them all)"
  fi
}

# lea__dep_update DEP vX.Y.Z: installs that release of DEP, then records it in the user config.
lea__dep_update() {
  case $1 in
    locksql) lea__locksql_update "$2" ;;
  esac
}

# lea__locksql_update vX.Y.Z: the locksql console image locksql:X.Y.Z, built from that release's
# verified source (lea__locksql_build). Records LEA_LOCKSQL_REF and LEA_LOCKSQL_IMAGE once it is
# built; the profiles and the locksql-home volume stay.
lea__locksql_update() {
  _lu_v=$1 _lu_old=$LEA_LOCKSQL_IMAGE _lu_img=locksql:${1#v} _lu_t=
  if lea__locksql_wants_vault; then _lu_t=vault; fi
  # shellcheck disable=SC2086 # no argument without the vault target
  if ! lea__locksql_build "$_lu_v" "$_lu_img" $_lu_t; then
    lea_log "locksql: could not build $_lu_img"
    return 1
  fi
  # Both keys in one write: never a REF without its IMAGE. In a subshell: a refused write
  # (lea_die) only fails this step.
  if ! (lea_user_config_set LEA_LOCKSQL_REF "$_lu_v" LEA_LOCKSQL_IMAGE "$_lu_img"); then
    lea_log "locksql: $_lu_img is built but $_lu_v could not be recorded in $(lea_user_config_path); lea keeps $_lu_old"
    return 1
  fi
  LEA_LOCKSQL_REF=$_lu_v LEA_LOCKSQL_IMAGE=$_lu_img
  lea_log "locksql $_lu_v recorded (profiles kept); lox up pins the console's own image: lox add locksql NAME again moves a lock to a newer release"
}
