# shellcheck shell=sh
# The docker run flags that confine a locksql console (lockHarden in internal/agents/locks.go).
lea__locksql_harden="--cap-drop ALL --security-opt no-new-privileges --read-only --tmpfs /tmp:rw,nosuid,nodev,noexec,size=64m --pids-limit 256 --memory 1g"

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
