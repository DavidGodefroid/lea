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
  lea__doctor_host_versions || _drc=1

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
    if [ "$_dmode" = isolated ]; then lea__ok "$_dnet isolated from the host"
    else lea__warn "$_dnet isolated from the host" "gateway_mode_ipv4=${_dmode:-unset}; run make test-isolation"; fi
  done
  if [ "$_dnets" -eq 0 ]; then lea__ok "internal networks: none (each lea creates its own at start)"; fi

  # The running lea containers' CPU and memory limits (#132): one started by an older lea, or
  # before a limit changed, gets them at the next lea in its projects directory.
  for _dctr in $(docker ps --filter "label=lea.prefix=$LEA_PREFIX" --filter label=lea.role=session --format '{{.Names}}' 2>/dev/null || true); do
    _dlim=$(docker inspect --format '{{.HostConfig.NanoCpus}} {{.HostConfig.Memory}}' "$_dctr" 2>/dev/null || true)
    case $_dlim in *[!0-9\ ]*|'') continue ;; esac
    _dcpu=${_dlim%% *}; _dmem=${_dlim#* }; _dmiss=
    if [ "$_dcpu" = 0 ] && [ "${LEA_CPUS:-75%}" != 0 ]; then _dmiss="CPU"; fi
    if [ "$_dmem" = 0 ] && [ "${LEA_MEMORY:-50%}" != 0 ]; then _dmiss="${_dmiss:+$_dmiss and }memory"; fi
    if [ -n "$_dmiss" ]; then
      lea__warn "$_dctr limits" "no $_dmiss limit: run lea in its projects directory to apply LEA_CPUS and LEA_MEMORY"
    else
      lea__ok "$_dctr limits: $(awk -v c="$_dcpu" -v m="$_dmem" 'BEGIN { printf "%s CPUs, %s MiB", (c ? c / 1e9 : "no limit on"), (m ? int(m / 1048576) : "no limit on") }')"
    fi
  done

  # The domains the proxy always allows: EGRESS_ALWAYS of proxy/render-squid-conf.sh.
  if [ "${LEA_LOCKHTTP:-0}" = 1 ]; then
    lea__ok "egress: the LockHTTP gate decides (its policy's trusted reads replace LEA_EGRESS_ALLOW: lockhttp init --trusted)"
  else case ${LEA_EGRESS_ALLOW:-} in
    '') lea__ok "egress allowlist: lea's default (forges, registries, model APIs; LEA_EGRESS_ALLOW extends it with 'default,<domain>')" ;;
    '*') lea__warn "egress allowlist" "off (LEA_EGRESS_ALLOW='*'): agents reach any public web domain; only DoH resolvers and IP literals are refused" ;;
    *) lea__ok "egress allowlist: $LEA_EGRESS_ALLOW (always: anthropic.com,claude.ai,claude.com)" ;;
  esac; fi

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
  lea__doctor_lockhttp || _drc=1

  if [ -f "$_dds/kube/config" ]; then
    if [ -n "$(find "$_dds/kube/config" -mmin -480 2>/dev/null)" ]; then lea__ok "kubernetes token"
    else lea__warn "kubernetes token" "older than 8 h: run lea kube refresh"; fi
  else
    lea__warn "kubernetes token" "none (optional): lea kube refresh"
  fi
  return "$_drc"
}

# The kernel fixes lea's containment relies on (config/host-minimums).
LEA__KERNEL_CVES="CVE-2026-74753, CVE-2026-80521, CVE-2026-80841"

# lea_version_ge A B: version A is B or later, field by field (digits only: v2.1.4 is 2.1.4,
# 7.0.0-38-generic is 7.0.0.38).
lea_version_ge() {
  awk -v a="$1" -v b="$2" 'BEGIN {
    sub(/^[^0-9]+/, "", a); sub(/^[^0-9]+/, "", b)
    na = split(a, x, /[^0-9]+/); nb = split(b, y, /[^0-9]+/); n = na > nb ? na : nb
    for (i = 1; i <= n; i++) { if (x[i] + 0 > y[i] + 0) exit 0; if (x[i] + 0 < y[i] + 0) exit 1 }
    exit 0 }'
}

# lea__doctor_host_versions: the Docker Engine (with containerd and runc) and the kernel lea's
# containers run on, against config/host-minimums. Fails below the kernel minimum; Podman (lea is
# built for Docker Engine), an unknown engine, or a distribution without a kernel minimum is a
# warning. No Docker Engine minimum: lea runs its own seccomp profile (image/seccomp.json).
lea__doctor_host_versions() {
  _hvf=${LEA_HOST_MINIMUMS:-$LEA_ROOT/config/host-minimums}; _hvrc=0
  _hvpmin=$(awk '$1 == "podman" { print $2; exit }' "$_hvf" 2>/dev/null || true)
  _hveng=$(lea_host_engine); _hve=${_hveng#* }
  case $_hveng in
    docker\ *)
      _hvc=$(docker version --format '{{range .Server.Components}}{{.Name}}={{.Version}}{{"\n"}}{{end}}' 2>/dev/null || true)
      _hvctd=$(printf '%s\n' "$_hvc" | sed -n 's/^containerd=//p' | head -n 1)
      _hvrunc=$(printf '%s\n' "$_hvc" | sed -n 's/^runc=//p' | head -n 1)
      lea__ok "Docker Engine $_hve (containerd ${_hvctd:-?}, runc ${_hvrunc:-?})" ;;
    podman\ *)
      if [ -n "$_hvpmin" ] && ! lea_version_ge "$_hve" "$_hvpmin"; then
        lea__warn "Podman $_hve" "older than $_hvpmin (CVE-2026-57231): update Podman"
      else
        lea__warn "Podman $_hve" "lea is built for Docker Engine; Podman minimum ${_hvpmin:-none} met; its networks have no gateway_mode_ipv4=isolated"
      fi ;;
    *) lea__warn "Docker Engine" "version unknown (not Docker Engine?)" ;;
  esac

  _hvk=$(uname -r 2>/dev/null || true)
  _hvos=${LEA_OS_RELEASE:-/etc/os-release}
  _hvid=$(sed -n 's/^ID=//p' "$_hvos" 2>/dev/null | tr -d '"' | head -n 1)
  _hvver=$(sed -n 's/^VERSION_ID=//p' "$_hvos" 2>/dev/null | tr -d '"' | head -n 1)
  _hvkmin=$(awk -v id="$_hvid" -v v="$_hvver" '$1 == "kernel" && $2 == id && $3 == v { print $4; exit }' "$_hvf" 2>/dev/null || true)
  if [ -z "$_hvkmin" ]; then
    lea__warn "kernel $_hvk" "no known minimum for ${_hvid:-?} ${_hvver:-?} in $_hvf: check that it has the fixes for $LEA__KERNEL_CVES"
  elif lea_version_ge "$_hvk" "$_hvkmin"; then
    lea__ok "kernel $_hvk ($_hvid $_hvver, minimum $_hvkmin)"
  else
    lea__fail "kernel" "$_hvk is older than $_hvkmin for $_hvid $_hvver ($LEA__KERNEL_CVES): update the host kernel"; _hvrc=1
  fi
  return "$_hvrc"
}
