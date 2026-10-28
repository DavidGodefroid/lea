# shellcheck shell=sh
# The docker run flags that confine a locksql console (lockHarden in internal/agents/locks.go).
lea__locksql_harden="--cap-drop ALL --security-opt no-new-privileges --read-only --tmpfs /tmp:rw,nosuid,nodev,noexec,size=64m --pids-limit 256 --memory 1g"

# lea__locksql_launcher: the path of locksql's own launcher on PATH (scripts/locksql of locksql,
# which scripts/install.sh of locksql installs as locksql), or nothing. Any other locksql (the
# binary of a former host install, the link an older lea made) is not it, nor a launcher that
# cannot run the console as lea does: confined, the projects directory read-only but for
# .locksql, under lea's container name.
# It must also be a file only this account or root could have written: resolved through its links,
# outside the projects directory (the agents write it), the projects directories of the running lea
# agents (DIRS, one per line) and lea's data directory; it and every directory up to / owned by
# this account or root and writable by neither group nor others (but for a sticky directory such
# as /tmp, where nobody else can move an entry of this account).
lea__locksql_launcher() {
  _ll=$(command -v locksql 2>/dev/null) || return 0
  case $_ll in /*) ;; *) return 0 ;; esac
  # At most 40 links, as the kernel follows: a cycle ends there.
  _lh=0
  while [ -L "$_ll" ]; do
    _lh=$((_lh + 1))
    [ "$_lh" -le 40 ] || return 0
    _lt=$(readlink "$_ll") || return 0
    case $_lt in /*) _ll=$_lt ;; *) _ll=$(dirname "$_ll")/$_lt ;; esac
  done
  [ -f "$_ll" ] || return 0
  _ld=$(CDPATH='' cd -- "$(dirname "$_ll")" 2>/dev/null && pwd -P) || return 0
  _ll=${_ld%/}/$(basename "$_ll")
  _lx=0
  while IFS= read -r _lu; do
    if [ -z "$_lu" ] || [ ! -d "$_lu" ]; then continue; fi
    _lu=$(CDPATH='' cd -- "$_lu" && pwd -P) || { _lx=1; break; }
    case $_ll in "${_lu%/}"/*) _lx=1; break ;; esac
  done <<EOT
${LEA_PROJECTS_DIR:-}
$(lea_data_dir)
${1:-}
EOT
  [ "$_lx" = 0 ] || return 0
  _lme=$(id -u)
  _lf=$_ll
  while :; do
    # shellcheck disable=SC2012 # ls -ldn: the mode and the numeric owner, portably
    _lm=$(ls -ldn "$_lf" 2>/dev/null | awk '{ print $1, $3 }') || return 0
    case ${_lm%% *} in
      '') return 0 ;;
      d????????[tT]) ;;
      ?????w* | ????????w*) return 0 ;;
    esac
    [ "${_lm#* }" = "$_lme" ] || [ "${_lm#* }" = 0 ] || return 0
    [ "$_lf" = / ] && break
    _lf=$(dirname "$_lf")
  done
  for _lw in '^launcher_version=' '--cap-drop ALL' 'no-new-privileges' '--read-only' \
    'LOCKSQL_READONLY_PROJECT' 'LOCKSQL_CONSOLE_NAME'; do
    grep -q -e "$_lw" "$_ll" 2>/dev/null || return 0
  done
  printf '%s\n' "$_ll"
}

# lea locksql [console flags]: the locksql console of the projects directory, in its own
# container, on this terminal (keep it in view: each query of an agent waits for your approval
# there). lea locksql vault ARGS: the vault CLI as the console account (vault login, when its
# token expires); its token stays in the locksql-home volume, out of the agents' reach.
lea_locksql() {
  [ "$LEA_LOCKSQL" = 1 ] || lea_die "locksql is off (LEA_LOCKSQL=0): lea init --locksql turns it on"
  [ -n "${LEA_PROJECTS_DIR:-}" ] || lea_die "no projects directory: run lea init"
  docker image inspect "$LEA_LOCKSQL_IMAGE" >/dev/null 2>&1 || lea_die "no locksql console image $LEA_LOCKSQL_IMAGE: run lea init"
  _lk_p=$LEA_PROJECTS_DIR
  if [ "${1:-}" = vault ]; then
    shift
    set -- --entrypoint vault "$LEA_LOCKSQL_IMAGE" "$@"
  else
    # A console that ended without --rm keeps the name: a stopped one is only in the way.
    if [ "$(docker inspect -f '{{.State.Running}}' locksql-console 2>/dev/null)" = false ]; then
      docker rm locksql-console >/dev/null
    fi
    # The agents write the projects directory: the console reads it, and writes only .locksql
    # (its PII rules). A bind mount follows a symbolic link on this host, so .locksql must be a
    # directory of its own; created here when missing (docker would create it as root).
    _lk_c=$_lk_p/.locksql
    if [ ! -e "$_lk_c" ] && [ ! -L "$_lk_c" ]; then mkdir -m 0755 "$_lk_c" || lea_die "could not create $_lk_c"; fi
    if [ -L "$_lk_c" ] || [ ! -d "$_lk_c" ]; then
      lea_die "$_lk_c is not a directory (a symbolic link or a file): the locksql console only writes there; remove it and retry"
    fi
    # A symbolic link (or a fifo, a device) left in .locksql by an agent, before the agents got it
    # read-only, would take the console's writes out of it, a hard link let the agent write in it:
    # only regular files of a single name and directories (EnsureConsoleDir in internal/agents).
    _lk_x=$(find "$_lk_c" -mindepth 1 ! -type f ! -type d -print -quit) || lea_die "could not read $_lk_c"
    [ -z "$_lk_x" ] || lea_die "$_lk_x is not a regular file or directory: the locksql console writes in $_lk_c; remove it and retry"
    _lk_x=$(find "$_lk_c" -type f -links +1 -print -quit) || lea_die "could not read $_lk_c"
    [ -z "$_lk_x" ] || lea_die "$_lk_x has another name (a hard link): the locksql console writes in $_lk_c; remove it and retry"
    # An agent started before LEA_LOCKSQL=1, or before lea mounted .locksql read-only in it
    # (locksqlDir in internal/launcher/container.go), can still swap it for a symbolic link: the
    # console starts only when every running agent of the projects directory, or of one above it
    # (its mount holds .locksql), has that mount (Unconfined in internal/agents/docker.go).
    _lk_r=$(lea_abspath "$_lk_p") || lea_die "could not resolve $_lk_p"
    _lk_ps=$(docker ps --filter label=lea.role=session --format '{{.ID}} {{.Label "lea.projects"}}') ||
      lea_die "could not list the lea agents (docker ps): the locksql console checks them first"
    _lk_ids=$(printf '%s\n' "$_lk_ps" | while read -r _lk_id _lk_lp; do
      if [ -z "$_lk_id" ] || [ -z "$_lk_lp" ]; then continue; fi
      case $_lk_r in "$_lk_lp" | "${_lk_lp%/}"/*) printf '%s\n' "$_lk_id" ;; esac
    done)
    if [ -n "$_lk_ids" ]; then
      # shellcheck disable=SC2086 # container IDs
      _lk_m=$(docker inspect -f "{{.Name}} {{range .Mounts}}{{if eq .Destination \"$_lk_r/.locksql\"}}{{.RW}}{{end}}{{end}}" $_lk_ids) ||
        lea_die "could not inspect the lea agents (docker inspect): the locksql console checks them first"
      _lk_bad=$(printf '%s\n' "$_lk_m" | awk '$2 != "false" { sub("^/", "", $1); printf "%s%s", s, $1; s = " " }')
      [ -z "$_lk_bad" ] || lea_die "$_lk_bad running without $_lk_r/.locksql read-only (started before locksql or this lea): restart the agents (lea stop, then lea; they resume) and retry"
    fi
    # locksql's launcher runs the console as lea did: the console account, confined, the projects
    # directory read-only but for .locksql (LOCKSQL_READONLY_PROJECT), lea's console image and
    # container name. Until a locksql release ships it, lea runs the same docker run itself.
    _lk_l=$(lea__locksql_launcher "$(printf '%s\n' "$_lk_ps" | awk 'NF > 1 { sub(/^[^ ]+ /, ""); print }')")
    if [ -n "$_lk_l" ]; then
      # The launcher mounts the project of its working directory (the .locksql checked above); it
      # runs nothing from there.
      cd "$_lk_p" || lea_die "could not enter $_lk_p"
      if [ -n "${LEA_LOCKSQL_VAULT_ADDR:-}" ]; then VAULT_ADDR=$LEA_LOCKSQL_VAULT_ADDR; export VAULT_ADDR; fi
      LOCKSQL_IMAGE=$LEA_LOCKSQL_IMAGE LOCKSQL_READONLY_PROJECT=1 LOCKSQL_CONSOLE_NAME=locksql-console \
        exec "$_lk_l" console --project "$_lk_p" "$@"
    fi
    # host.docker.internal is this machine, as the profile wizard offers it.
    set -- --name locksql-console --add-host=host.docker.internal:host-gateway -v "$_lk_p:$_lk_p:ro" \
      -v "$_lk_c:$_lk_c" -v locksql-run:/run/locksql "$LEA_LOCKSQL_IMAGE" console --project "$_lk_p" "$@"
  fi
  if [ -n "${LEA_LOCKSQL_VAULT_ADDR:-}" ]; then set -- -e "VAULT_ADDR=$LEA_LOCKSQL_VAULT_ADDR" "$@"; fi
  _lk_t=-i
  if [ -t 0 ] && [ -t 1 ]; then _lk_t=-it; fi
  # No log driver: with a tty, docker's json-file log would keep every screen of the console
  # (results the user chose to show) for anyone who runs docker logs.
  # The console holds the database credentials: no capability, no privilege gain, a read-only
  # root with a small /tmp, bounded processes and memory (lea__locksql_harden).
  # shellcheck disable=SC2086 # lea__locksql_harden is a list of fixed words
  docker run "$_lk_t" --rm --log-driver none --user locksql $lea__locksql_harden -v locksql-home:/home/locksql "$@"
}
