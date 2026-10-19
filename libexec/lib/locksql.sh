# shellcheck shell=sh
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
    # host.docker.internal is this machine, as the profile wizard offers it.
    set -- --name locksql-console --add-host=host.docker.internal:host-gateway -v "$_lk_p:$_lk_p" \
      -v locksql-run:/run/locksql "$LEA_LOCKSQL_IMAGE" console --project "$_lk_p" "$@"
  fi
  if [ -n "${LEA_LOCKSQL_VAULT_ADDR:-}" ]; then set -- -e "VAULT_ADDR=$LEA_LOCKSQL_VAULT_ADDR" "$@"; fi
  _lk_t=-i
  if [ -t 0 ] && [ -t 1 ]; then _lk_t=-it; fi
  docker run "$_lk_t" --rm --user locksql -v locksql-home:/home/locksql "$@"
}
