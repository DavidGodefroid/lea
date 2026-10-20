# shellcheck shell=sh
# lea update's dependencies: locksql (its console image, built from the source of a release) and
# lockmq and lockfile (their binary on the host). lea pins a release of each (config/lea.conf); lea
# update looks for a newer one and installs it only when you accept it, after checking that the
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
      if [ -d "$LEA_LOCKSQL_REF" ] || ! lea__semver "$LEA_LOCKSQL_REF" >/dev/null; then
        lea_log "locksql: LEA_LOCKSQL_REF is $LEA_LOCKSQL_REF, not a release: lea update leaves it"
        return 1
      fi
      printf '%s' "$LEA_LOCKSQL_REF" ;;
    lockmq|lockfile)
      [ "$(lea__lockhost_get "$1")" = 1 ] || return 1
      _dc_r=$(lea__lockhost_get "$1" _REF)
      if [ -z "$_dc_r" ]; then
        lea_log "$1: no pinned release ($(lea__lockhost_var "$1")_REF is empty): lea update installs nothing"
        return 1
      fi
      if ! _dc_b=$(lea__lockhost_bin "$1"); then
        lea_log "$1: not installed in /usr/local/bin (root-owned): lea init installs $_dc_r"
        return 1
      fi
      if ! _dc_v=$(lea__lockhost_version "$_dc_b"); then
        lea_log "$1: $_dc_b reports no version: lea update leaves it (lea init installs $_dc_r)"
        return 1
      fi
      printf '%s' "$_dc_v" ;;
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
  for _ud_d in locksql lockmq lockfile; do
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
    case $_ud_d in
      lockmq|lockfile)
        # lea's agents talk to the console with the client of lea's image, the release lea pins.
        _ud_c=$(lea__dep_pin "$(lea__lockhost_var "$_ud_d")_REF")
        if [ "$_ud_c" != "$_ud_new" ]; then
          lea_log "  lea's image has the $_ud_d ${_ud_c:-?} client: a $_ud_new console may refuse it until a lea release ships that client"
        fi ;;
    esac
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
    lockmq|lockfile)
      lea__lockhost_install "$1" "$2" || return 1
      lea_user_config_set "$(lea__lockhost_var "$1")_REF" "$2"
      # shellcheck disable=SC2034 # read by lea__lockhost_get
      case $1 in lockmq) LEA_LOCKMQ_REF=$2 ;; lockfile) LEA_LOCKFILE_REF=$2 ;; esac
      lea_log "$1 $2 installed and recorded; restart the $1 console in the $1 account to run it" ;;
  esac
}

# lea__locksql_update vX.Y.Z: the locksql console image locksql:X.Y.Z, built from locksql's source
# at that tag, cloned with git, once cosign proves that locksql's release workflow, run at that tag
# on that very commit, signed the release's checksums.txt. Records LEA_LOCKSQL_REF and
# LEA_LOCKSQL_IMAGE once it is built; the profiles and the locksql-home volume stay.
lea__locksql_update() {
  _lu_v=$1 _lu_u=$(lea__dep_url locksql)
  command -v git >/dev/null 2>&1 || { lea_log "locksql: git is needed to fetch its source; nothing built"; return 1; }
  _lu_tmp=$(mktemp -d) || return 1
  for _lu_f in checksums.txt checksums.txt.sigstore.json; do
    if ! curl -fsSL -o "$_lu_tmp/$_lu_f" "$_lu_u/releases/download/$_lu_v/$_lu_f"; then
      lea_log "locksql: could not download $_lu_u/releases/download/$_lu_v/$_lu_f; nothing built"
      rm -rf "$_lu_tmp"
      return 1
    fi
  done
  if ! git -c advice.detachedHead=false clone -q --depth 1 --branch "$_lu_v" "$_lu_u.git" "$_lu_tmp/src" >&2; then
    lea_log "locksql: could not clone $_lu_u at $_lu_v; nothing built"
    rm -rf "$_lu_tmp"
    return 1
  fi
  _lu_sha=$(git -C "$_lu_tmp/src" rev-parse HEAD 2>/dev/null) || _lu_sha=
  case $_lu_sha in
    *[!0-9a-f]*|'') _lu_sha= ;;
  esac
  if [ ${#_lu_sha} -ne 40 ] || ! _lu_co=$(lea__cosign "$_lu_tmp"); then
    lea_log "locksql: cannot check the signature of $_lu_v; nothing built"
    rm -rf "$_lu_tmp"
    return 1
  fi
  if ! "$_lu_co" verify-blob --bundle "$_lu_tmp/checksums.txt.sigstore.json" \
    --certificate-identity-regexp "^https://github\\.com/DavidGodefroid/locksql/\\.github/workflows/release\\.yml@refs/tags/$(printf '%s' "$_lu_v" | sed 's/\./\\./g')\$" \
    --certificate-oidc-issuer https://token.actions.githubusercontent.com \
    --certificate-github-workflow-repository DavidGodefroid/locksql \
    --certificate-github-workflow-sha "$_lu_sha" \
    "$_lu_tmp/checksums.txt" >&2; then
    lea_log "locksql: $_lu_v at $_lu_sha is not what locksql's release workflow signed; nothing built"
    rm -rf "$_lu_tmp"
    return 1
  fi
  _lu_old=$LEA_LOCKSQL_IMAGE _lu_img=locksql:${_lu_v#v}
  set --
  if lea__locksql_wants_vault; then set -- --target vault; fi
  lea_log "locksql $_lu_v: signature verified (commit $_lu_sha); building the console image $_lu_img${1:+ with the vault CLI}"
  # As lea__init_locksql: the console checks each client's uid against its own accounts.
  if ! docker build "$@" -t "$_lu_img" --build-arg "ALLOWED_UIDS=$(id -u)" "$_lu_tmp/src" >&2; then
    lea_log "locksql: could not build $_lu_img"
    rm -rf "$_lu_tmp"
    return 1
  fi
  rm -rf "$_lu_tmp"
  lea_user_config_set LEA_LOCKSQL_REF "$_lu_v"
  lea_user_config_set LEA_LOCKSQL_IMAGE "$_lu_img"
  LEA_LOCKSQL_REF=$_lu_v LEA_LOCKSQL_IMAGE=$_lu_img
  lea_log "locksql $_lu_v recorded (profiles and the locksql-home volume kept); a running console keeps $_lu_old until you restart it (lea locksql)"
}
