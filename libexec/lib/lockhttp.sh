# shellcheck shell=sh
# LockHTTP (LEA_LOCKHTTP=1): lea doctor's checks of the host setup and of the gate in the proxy
# container (contract: lockhttp's docs/lea.md, §6). The gate runs as uid 10003 in the proxy
# container; the console accepts it by its uid as the host sees it.

LEA__LOCKHTTP_GATE_UID=10003

# lea__lockhttp_self: the user's uid (root runs Podman rootful).
lea__lockhttp_self() { id -u; }

# lea__lockhttp_gate_uid: the gate's uid on the host, the one /etc/lockhttp/system.toml's
# allowed_uids must hold: 10003 under rootful Docker, the user's subuid base + 10003 - 1 under
# rootless Podman (container uid 0 is the user, uid 1 the first subuid).
lea__lockhttp_gate_uid() {
  case $(lea_host_engine) in
    podman\ *)
      if [ "$(lea__lockhttp_self)" != 0 ]; then
        _lg_b=$(awk -F: -v u="$(lea_user_name)" -v i="$(lea__lockhttp_self)" '$1 == u || $1 == i { print $2; exit }' "$(lea__lockhost_root)/etc/subuid" 2>/dev/null)
        case $_lg_b in ''|*[!0-9]*) return 1 ;; esac
        echo $((_lg_b + LEA__LOCKHTTP_GATE_UID - 1)); return 0
      fi ;;
  esac
  echo "$LEA__LOCKHTTP_GATE_UID"
}

# lea__lockhttp_allowed UID: system.toml's allowed_uids lists UID.
lea__lockhttp_allowed() {
  sed -n 's/^[[:space:]]*allowed_uids[[:space:]]*=[[:space:]]*\[\([0-9,[:space:]]*\)\].*/\1/p' "$(lea__lockhost_root)/etc/lockhttp/system.toml" 2>/dev/null |
    tr -s ', ' '\n' | grep -qx "$1"
}

# lea__lockhttp_in_clients: the user is in lockhttp-clients (the console's socket directory).
lea__lockhttp_in_clients() { id -nG 2>/dev/null | tr ' ' '\n' | grep -qx lockhttp-clients; }

lea__doctor_lockhttp() {
  [ "${LEA_LOCKHTTP:-0}" = 1 ] || return 0
  _dh_rc=0 _dh_r=$(lea__lockhost_root) _dh_rl=
  case $(lea_host_engine) in podman\ *) [ "$(lea__lockhttp_self)" = 0 ] || _dh_rl=' --rootless' ;; esac
  _dh_inst="sudo lockhttp install --client $(lea_user_name) --gate-uid $LEA__LOCKHTTP_GATE_UID$_dh_rl"
  if lea__lockhost_bin lockhttp >/dev/null; then lea__ok "lockhttp binary"
  else lea__fail "lockhttp binary" "not in /usr/local/bin (root-owned): install lockhttp, then $_dh_inst"; _dh_rc=1; fi
  if [ -z "${LEA_GIT_SSH:-}" ]; then lea__ok "LockHTTP: no SSH forwarder (LEA_GIT_SSH empty)"
  else lea__fail "LockHTTP" "LEA_GIT_SSH is set: its forwarders would bypass the gate"; _dh_rc=1; fi
  if [ -n "${LEA_OTEL_UPSTREAM:-}" ]; then lea__fail "LockHTTP" "LEA_OTEL_UPSTREAM is set: its TCP forwarder would bypass the gate"; _dh_rc=1; fi
  if [ -f "$_dh_r/etc/lockhttp/system.toml" ]; then
    if _dh_u=$(lea__lockhttp_gate_uid) && lea__lockhttp_allowed "$_dh_u"; then lea__ok "lockhttp allows the gate (uid $_dh_u)"
    else lea__fail "lockhttp allowed_uids" "/etc/lockhttp/system.toml does not allow the gate's uid ${_dh_u:-?}: $_dh_inst"; _dh_rc=1; fi
  else
    lea__fail "lockhttp separated setup" "none: $_dh_inst"; _dh_rc=1
  fi
  if [ -d "$_dh_r/run/lockhttp" ]; then lea__ok "lockhttp socket directory /run/lockhttp"
  else lea__fail "lockhttp socket directory" "/run/lockhttp absent: $_dh_inst"; _dh_rc=1; fi
  if lea__lockhttp_in_clients; then lea__ok "you are in lockhttp-clients"
  else lea__fail "lockhttp-clients" "you are not in the group: sudo usermod -aG lockhttp-clients $(lea_user_name), then log in again"; _dh_rc=1; fi
  if lea_container_running "$LEA_PREFIX-proxy"; then
    if docker exec "$LEA_PREFIX-proxy" lockhttp gate --check --listen 127.0.0.1:3130 >/dev/null 2>&1; then lea__ok "LockHTTP gate answers"
    else lea__fail "LockHTTP gate" "does not answer in $LEA_PREFIX-proxy: docker logs $LEA_PREFIX-proxy (lea stop --all, then lea)"; _dh_rc=1; fi
    # squid behind the gate: on the loopback only, never on an address the agents reach.
    _dh_l=$(docker exec "$LEA_PREFIX-proxy" netstat -ltn 2>/dev/null | awk '$4 ~ /:3128$/ { print $4 }')
    if [ -n "$_dh_l" ] && ! printf '%s\n' "$_dh_l" | grep -qv '^127\.0\.0\.1:3128$'; then lea__ok "squid listens on 127.0.0.1 only"
    else lea__fail "squid" "listens on '$(printf '%s' "$_dh_l" | tr '\n' ' ')', not 127.0.0.1:3128 only: lea stop --all, then lea"; _dh_rc=1; fi
  fi
  _dh_s=$(lea_data_dir)/lockhttp/status/status.json
  case $(sed -n 's/.*"console"[[:space:]]*:[[:space:]]*"\([a-z]*\)".*/\1/p' "$_dh_s" 2>/dev/null | head -n 1) in
    up) lea__ok "lockhttp console up" ;;
    down) lea__warn "lockhttp console" "down: agents' requests are refused (E_NO_CONSOLE); as lockhttp, in its own session: lockhttp console --profile default" ;;
    *) lea__warn "lockhttp console" "unknown (no gate status yet)" ;;
  esac
  return "$_dh_rc"
}
