# shellcheck shell=sh
# lea doctor: checks this workstation's lea setup.

lea__ok() { printf 'ok    %s\n' "$1"; }
lea__warn() { printf 'warn  %s: %s\n' "$1" "$2"; }
lea__fail() { printf 'FAIL  %s: %s\n' "$1" "$2"; }

lea_doctor() {
  _dds=$(lea_state_dir); _drc=0
  if docker info >/dev/null 2>&1; then lea__ok "docker reachable"; else lea__fail "docker reachable" "docker info failed"; return 1; fi

  if docker info --format '{{json .SecurityOptions}}' 2>/dev/null | grep -q 'name=apparmor'; then
    if ! docker image inspect "$LEA_IMAGE" >/dev/null 2>&1; then
      lea__warn "AppArmor profile lea" "image $LEA_IMAGE not present: run lea init"
    elif docker run --rm --pull never --network none --security-opt apparmor=lea --entrypoint true "$LEA_IMAGE" >/dev/null 2>&1; then
      lea__ok "AppArmor profile lea"
    else lea__fail "AppArmor profile lea" "not loaded: run lea init"; _drc=1; fi
  fi

  _dfiles=claude.json
  if [ -n "$LEA_GIT_SSH" ]; then _dfiles="$_dfiles ssh/id_ed25519 ssh/known_hosts"; fi
  for _dfile in $_dfiles; do
    if [ -f "$_dds/$_dfile" ]; then lea__ok "state $_dfile"; else lea__fail "state $_dfile" "missing: run lea init"; _drc=1; fi
  done

  _duc=$(lea_user_config_path)
  if [ -f "$_duc" ]; then lea__ok "user config $_duc"; else lea__fail "user config" "$_duc missing: run lea init"; _drc=1; fi
  # Set by the Go launcher when it refused a line of the configuration (it hands doctor over).
  if [ -n "${LEA_GO_CONFIG_ERROR:-}" ]; then lea__fail "configuration" "$LEA_GO_CONFIG_ERROR"; _drc=1; fi
  if _dpm=$( (lea_projects_dir >/dev/null && lea__guard_projects "$(lea_projects_dir)") 2>&1); then
    lea__ok "projects dir $LEA_PROJECTS_DIR"
  else
    lea__fail "projects dir" "${_dpm#lea: error: }; run lea init"; _drc=1
  fi
  for _dimg in "$LEA_IMAGE" "$LEA_PROXY_IMAGE"; do
    if docker image inspect "$_dimg" >/dev/null 2>&1; then lea__ok "image $_dimg"
    else lea__fail "image $_dimg" "not present: run lea init"; _drc=1; fi
  done
  if _dtb=$(lea_tmux_bin); then
    _dtv=$("$_dtb" -V 2>/dev/null || true)
    if lea_tmux_version_ok "$_dtv"; then lea__ok "tmux ($_dtv, $(lea_tmux_source "$_dtb"))"
    else lea__fail "tmux" "$_dtv is older than $LEA_TMUX_MIN: $(lea_tmux_install_hint), or $(lea_tmux_build_hint)"; _drc=1; fi
  else
    lea__fail "tmux" "$(lea_tmux_missing) (the deck needs it)"; _drc=1
  fi

  if lea_container_running "$LEA_PREFIX-proxy"; then
    lea__ok "proxy running"
    while read -r _dh _drest; do
      case $_dh in ''|\#*) continue ;; *[!0-9.]*) ;; *) continue ;; esac
      if docker exec "$LEA_PREFIX-proxy" nslookup "$_dh" >/dev/null 2>&1; then lea__ok "resolves $_dh"
      else lea__warn "resolves $_dh" "not resolvable from the proxy (VPN?)"; fi
    done <"$LEA_INTERNAL_HOSTS"
  else
    lea__warn "proxy running" "not running; the next lea starts it"
  fi

  _dnets=0
  for _dnet in $(docker network ls --filter "label=lea.prefix=$LEA_PREFIX" --format '{{.Name}}' 2>/dev/null || true); do
    case $_dnet in "$LEA_PREFIX"-internal-*) ;; *) continue ;; esac
    _dnets=$((_dnets + 1))
    _dinfo=$(docker network inspect -f '{{.Internal}} {{index .Options "com.docker.network.bridge.gateway_mode_ipv4"}}' "$_dnet" 2>/dev/null || true)
    _dint=${_dinfo%% *}; _dmode=${_dinfo#* }
    if [ "$_dint" = true ]; then lea__ok "$_dnet is internal"
    else lea__fail "$_dnet is internal" "not created with --internal: lea stop --all, then restart lea"; _drc=1; fi
    # The launcher refuses a network without the isolated gateway mode unless LEA_ALLOW_GATEWAY=1 (#105).
    _dgw="$_dnet gateway_mode_ipv4=${_dmode:-unset}"
    if [ "$_dmode" = isolated ]; then lea__ok "$_dgw"
    elif [ "${LEA_ALLOW_GATEWAY:-}" = 1 ]; then
      lea__warn "$_dgw" "not isolated from the host, accepted by LEA_ALLOW_GATEWAY=1; run make test-isolation"
    else
      lea__fail "$_dgw" "not isolated from the host (needs Docker Engine 28.0 or later): upgrade Docker, lea stop --all, then restart lea"; _drc=1
    fi
  done
  if [ "$_dnets" -eq 0 ]; then lea__ok "internal networks: none (each lea creates its own at start)"; fi

  # The domains the proxy always allows: EGRESS_ALWAYS of proxy/render-squid-conf.sh.
  if [ -n "${LEA_EGRESS_ALLOW:-}" ]; then lea__ok "egress allowlist: $LEA_EGRESS_ALLOW (always: anthropic.com,claude.ai,claude.com)"
  else lea__ok "egress allowlist: off, any web destination (LEA_EGRESS_ALLOW)"; fi

  if [ -n "$LEA_OTEL_UPSTREAM" ]; then lea__ok "telemetry upstream $LEA_OTEL_UPSTREAM"
  else lea__ok "telemetry: off (LEA_OTEL_UPSTREAM)"; fi

  if [ "$LEA_LOCKSQL" = 1 ]; then
    if docker image inspect "$LEA_LOCKSQL_IMAGE" >/dev/null 2>&1; then lea__ok "locksql image $LEA_LOCKSQL_IMAGE"
    else lea__warn "locksql image" "$LEA_LOCKSQL_IMAGE absent: run lea init (or set LEA_LOCKSQL=0)"; fi
    if docker volume inspect locksql-run >/dev/null 2>&1; then lea__ok "locksql-run volume"
    else lea__warn "locksql-run volume" "absent: start the locksql console once (see the hint printed by lea)"; fi
  fi
  lea__doctor_lockhost lockmq
  lea__doctor_lockhost lockfile

  if [ -f "$_dds/kube/config" ]; then
    if [ -n "$(find "$_dds/kube/config" -mmin -480 2>/dev/null)" ]; then lea__ok "kubernetes token"
    else lea__warn "kubernetes token" "older than 8 h: run lea kube refresh"; fi
  else
    lea__warn "kubernetes token" "none (optional): lea kube refresh"
  fi
  return "$_drc"
}
