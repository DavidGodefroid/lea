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
  # LEA_DECK=0 (#135): lea runs your own tool in its container, without the deck; tmux only hosts
  # the terminals of lea's agents (lea new), so a missing or old one is no warning.
  if [ "${LEA_DECK:-1}" = 0 ]; then
    lea__ok "deck off (LEA_DECK=0): lea runs ${LEA_TOOL:-claude} in the lea container (lea config deck on: the deck)"
    _dtv=
    if _dtb=$(lea_tmux_bin); then _dtv=$("$_dtb" -V 2>/dev/null || true); fi
    if [ -n "$_dtv" ] && lea_tmux_version_ok "$_dtv"; then lea__ok "tmux ($_dtv, $(lea_tmux_source "$_dtb"))"
    else lea__ok "tmux not needed without the deck (lea new needs tmux $LEA_TMUX_MIN or later for its agents' terminals)"; fi
    # What waits for you comes as a desktop notification from the daemon: with LEA_CTL=1 the
    # daemon runs in lea-ctl, a container with no notify-send nor osascript, so none shows.
    if [ "${LEA_CTL:-0}" = 1 ]; then
      lea__warn "notifications" "LEA_CTL=1 runs lea's daemon in lea-ctl, which shows no desktop notification without the deck: lea requests lists what waits for you (lea config ctl off: notifications)"
    elif command -v notify-send >/dev/null 2>&1 || command -v osascript >/dev/null 2>&1; then
      lea__ok "notifications ($(command -v notify-send >/dev/null 2>&1 && echo notify-send || echo osascript))"
    else
      lea__warn "notifications" "no notify-send nor osascript: nothing tells you when an approval waits without the deck (lea requests lists them)"
    fi
  elif _dtb=$(lea_tmux_bin); then
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
    # #105: the launcher refuses such a network unless LEA_ALLOW_GATEWAY=1, so does the doctor.
    case $_dmode in ''|'<no value>') _dmode='unset' ;; esac
    if [ "$_dmode" = isolated ]; then lea__ok "$_dnet gateway_mode_ipv4=isolated"
    elif [ "${LEA_ALLOW_GATEWAY:-0}" = 1 ]; then
      lea__warn "$_dnet gateway_mode_ipv4=$_dmode" "not isolated: host services are reachable from its agents (LEA_ALLOW_GATEWAY=1)"
    else
      lea__fail "$_dnet gateway_mode_ipv4=$_dmode" "not isolated: host services are reachable from its agents; lea stop --all, then restart lea on Docker Engine 28.0 or later (or set LEA_ALLOW_GATEWAY=1)"; _drc=1
    fi
  done
  if [ "$_dnets" -eq 0 ]; then lea__ok "internal networks: none (each lea creates its own at start)"; fi

  # The running lea containers' limits (#132): one started by an older lea, or before a limit
  # changed, gets them at the next lea in its projects directory. The Go launcher hands over the
  # limits the configuration resolves to (LEA_LIMITS_WANT: NanoCpus Memory MemorySwap PidsLimit,
  # 0 for none) and their digest, the lea.limits label of a container started with them.
  for _dctr in $(docker ps --filter "label=lea.prefix=$LEA_PREFIX" --filter label=lea.role=session --format '{{.Names}}' 2>/dev/null || true); do
    _dlim=$(docker inspect --format '{{.HostConfig.NanoCpus}} {{.HostConfig.Memory}} {{.HostConfig.MemorySwap}} {{with .HostConfig.PidsLimit}}{{.}}{{else}}0{{end}} {{index .Config.Labels "lea.limits"}}' "$_dctr" 2>/dev/null || true)
    _dcpu=; _dmem=; _dswp=; _dpid=; _ddig=
    read -r _dcpu _dmem _dswp _dpid _ddig <<EOF
$_dlim
EOF
    [ -n "$_dmem" ] || continue
    _dswp=${_dswp:-0}; _dpid=${_dpid:-0}
    if [ "$_dpid" = '<nil>' ]; then _dpid=0; fi
    case "$_dcpu $_dmem $_dswp $_dpid" in *[!0-9\ -]*) continue ;; esac
    _dmiss=
    _wcpu=; _wmem=; _wswp=; _wpid=; _wx=
    read -r _wcpu _wmem _wswp _wpid _wx <<EOF
${LEA_LIMITS_WANT:-}
EOF
    if [ -n "$_wpid" ] && [ -z "$_wx" ]; then
      # A configured limit the container does not have: stale. A limit turned off (0) is only
      # taken away by a restart, which is not stale.
      if [ "$_wcpu" != 0 ] && [ "$_dcpu" != "$_wcpu" ]; then _dmiss="$(lea__doctor_cpus "$_dcpu") (configured $(lea__doctor_cpus "$_wcpu"))"; fi
      if [ "$_wmem" != 0 ] && { [ "$_dmem" != "$_wmem" ] || [ "$_dswp" != "$_wswp" ]; }; then
        _dmiss="${_dmiss:+$_dmiss, }$(lea__doctor_mib "$_dmem") (configured $(lea__doctor_mib "$_wmem"))"
      fi
      if [ "$_wpid" != 0 ] && [ "$_dpid" != "$_wpid" ]; then _dmiss="${_dmiss:+$_dmiss, }pids $(lea__doctor_pids "$_dpid") (configured $_wpid)"; fi
      if [ -n "$_dmiss" ]; then
        _dwhy="digest ${_ddig:-none: started by an older lea}, configured ${LEA_LIMITS_DIGEST:-?}"
        if [ "$_ddig" = "${LEA_LIMITS_DIGEST:-}" ]; then _dwhy="changed since it started (docker update)"; fi
        lea__warn "$_dctr limits" "stale ($_dwhy): $_dmiss; run lea in its projects directory: it applies them, or restarts the container once no agent or session runs in it"
        continue
      fi
    else
      if [ "$_dcpu" = 0 ] && [ "${LEA_CPUS:-75%}" != 0 ]; then _dmiss="CPU"; fi
      if [ "$_dmem" = 0 ] && [ "${LEA_MEMORY:-50%}" != 0 ]; then _dmiss="${_dmiss:+$_dmiss and }memory"; fi
      if [ -n "$_dmiss" ]; then
        lea__warn "$_dctr limits" "no $_dmiss limit: run lea in its projects directory to apply LEA_CPUS and LEA_MEMORY"
        continue
      fi
    fi
    lea__ok "$_dctr limits: $(lea__doctor_cpus "$_dcpu"), $(lea__doctor_mib "$_dmem")"
  done
  lea__doctor_delegation

  # Each running lea container's agents and their CPU windows (#132): nproc, PHPStan, Go... see
  # that many CPUs in the agent and in its nested containers.
  for _dctr in $(docker ps --filter "label=lea.prefix=$LEA_PREFIX" --filter label=lea.role=session --format '{{.Names}}' 2>/dev/null || true); do
    _dwin=$(docker exec "$_dctr" lea-agent windows 2>/dev/null | tr -cd 'a-z0-9 ,\n-' | awk 'NF == 2 { printf "%s%s %s", (n++ ? ", " : ""), $1, $2 }' || true)
    if [ -n "$_dwin" ]; then lea__ok "$_dctr CPU windows: $_dwin"
    else lea__ok "$_dctr CPU windows: none (no agent runs, or LEA_AGENT_CPUS=0)"; fi
  done

  lea__doctor_dns || _drc=1

  lea__doctor_managed || _drc=1

  lea__doctor_runtime || _drc=1
  lea__doctor_pressure

  # The domains the proxy always allows: EGRESS_ALWAYS of proxy/render-squid-conf.sh.
  if [ "${LEA_LOCKHTTP:-0}" = 1 ]; then
    lea__ok "egress: the LockHTTP gate decides (its policy's trusted reads replace LEA_EGRESS_ALLOW: lockhttp profile add --trusted)"
  else case ${LEA_EGRESS_ALLOW:-} in
    '') lea__ok "egress allowlist: lea's default (forges, registries, model APIs; LEA_EGRESS_ALLOW extends it with 'default,<domain>')" ;;
    '*') lea__warn "egress allowlist" "off (LEA_EGRESS_ALLOW='*'): agents reach any public web domain; only DoH resolvers and IP literals are refused" ;;
    *) lea__ok "egress allowlist: $LEA_EGRESS_ALLOW (always: anthropic.com,claude.ai,claude.com)" ;;
  esac; fi

  if [ -n "$LEA_OTEL_UPSTREAM" ]; then lea__ok "telemetry upstream $LEA_OTEL_UPSTREAM"
  else lea__ok "telemetry: off (LEA_OTEL_UPSTREAM)"; fi

  # locksql needs no image of lea's (lox runs the lock's): only the lock is checked. The Go
  # launcher then compares each lock lox runs with lea's client of its type (lea#134).
  if lea__tool_on locksql; then lea__doctor_locksql_lock; fi
  lea__doctor_lockhost lockmq
  lea__doctor_lockhost lockfile
  lea__doctor_lockhost locklog
  lea__doctor_lockhttp || _drc=1
  lea__doctor_hub
  lea__doctor_lox

  lea__doctor_secrets "$_dds"

  if [ -f "$_dds/kube/config" ]; then
    if [ -n "$(find "$_dds/kube/config" -mmin -480 2>/dev/null)" ]; then lea__ok "kubernetes token"
    else lea__warn "kubernetes token" "older than 8 h: run lea kube refresh"; fi
  else
    lea__warn "kubernetes token" "none (optional): lea kube refresh"
  fi
  return "$_drc"
}

# lea__doctor_managed: lea's Claude hooks are in the image's managed settings, which the agents
# cannot change and the pinned Claude Code honours over their own writable settings (#111):
# managed-check in the image, then in each running lea container (one started from an older
# image lacks them until it restarts). Fails closed: a FAIL for anything it cannot confirm.
# lea__doctor_dns: each running lea container resolves no external name (#98). lea starts it with
# --dns 192.0.2.1, which its internal network has no route to: Docker's embedded resolver answers
# the names of its network's containers only. A query that resolves outside would carry data past
# lea-proxy: a failure.
lea__doctor_dns() {
  _ddrc=0
  for _dctr in $(docker ps --filter "label=lea.prefix=$LEA_PREFIX" --filter label=lea.role=session --format '{{.Names}}' 2>/dev/null || true); do
    _ddns=$(docker container inspect -f '{{json .HostConfig.Dns}}' "$_dctr" 2>/dev/null || true)
    if _ddr=$(docker exec -w / "$_dctr" timeout 10 getent hosts example.com 2>/dev/null) && [ -n "$_ddr" ]; then
      lea__fail "$_dctr DNS" "resolves example.com ($(printf '%s' "$_ddr" | awk 'NR == 1 { print $1 }')): DNS reaches outside lea-proxy; run lea stop, then lea"; _ddrc=1
    elif [ "$_ddns" != '["192.0.2.1"]' ]; then
      lea__warn "$_dctr DNS" "no pinned resolver (started by an older lea): its queries may reach the host's resolver; run lea stop, then lea"
    else
      lea__ok "$_dctr DNS: resolves no external name (resolver 192.0.2.1)"
    fi
  done
  return "$_ddrc"
}

lea__doctor_managed() {
  _dmrc=0
  if docker image inspect "$LEA_IMAGE" >/dev/null 2>&1; then
    if _dmo=$(docker run --rm --pull never --network none --entrypoint /usr/local/libexec/lea/managed-check "$LEA_IMAGE" 2>&1); then
      lea__ok "managed settings (image): ${_dmo:-confirmed}"
    else
      lea__fail "managed settings (image)" "${_dmo:-the check did not run}: run lea update, or rebuild the image"; _dmrc=1
    fi
  fi
  for _dctr in $(docker ps --filter "label=lea.prefix=$LEA_PREFIX" --filter label=lea.role=session --format '{{.Names}}' 2>/dev/null || true); do
    if _dmo=$(docker exec -u "$(id -u):$(id -g)" "$_dctr" /usr/local/libexec/lea/managed-check 2>&1); then
      lea__ok "$_dctr managed settings: ${_dmo:-confirmed}"
    else
      lea__fail "$_dctr managed settings" "${_dmo:-the check did not run}: restart it (lea stop, then lea) on the current image"; _dmrc=1
    fi
  done
  return "$_dmrc"
}

# lea__doctor_locksql_lock: the locksql console runs hub-attached only, under lox run: lea's agents
# reach the socket volume of lox run's locksql lock of the projects directory (docker's labels say
# whose), never a fixed one. A locksql-run volume is what a standalone console of an earlier lea
# left: lea mounts it no more.
lea__doctor_locksql_lock() {
  _dl_hint="no such lock yet: \`lox add locksql\` (lox add fills allowed_uids: [$(id -u)] in .lockfamily/lockfamily.yaml when lea is on your PATH; lea's agents get its socket once lea restarts: lea stop, then lea)"
  if [ -z "${LEA_PROJECTS_DIR:-}" ]; then
    lea__warn "locksql lock" "no projects directory (lea init): lox run runs the console for one"
  elif ! _dl_v=$(docker volume ls --filter label=dev.lockfamily.role=socket --filter label=dev.lockfamily.type=locksql \
    --filter "label=dev.lockfamily.workspace=$LEA_PROJECTS_DIR" --format '{{.Name}}' 2>/dev/null); then
    lea__warn "locksql lock" "could not list lox run's volumes (docker volume ls)"
  else
    case $(printf '%s\n' "$_dl_v" | grep -c .) in
      0) lea__warn "locksql lock" "no socket volume of a locksql lock of lox run for $LEA_PROJECTS_DIR: lea's agents get no locksql socket; $_dl_hint" ;;
      1) lea__ok "locksql lock of lox run (socket volume $_dl_v)" ;;
      *) lea__ok "$(printf '%s\n' "$_dl_v" | grep -c .) locksql locks of lox run (lea's agents reach each on /run/locksql/<instance>)" ;;
    esac
  fi
  if docker volume inspect locksql-run >/dev/null 2>&1; then
    lea__warn "locksql-run volume" "left by a standalone console of an earlier lea, which lea no longer mounts: docker volume rm locksql-run once no console uses it"
  fi
}

# lea__doctor_hub: with LEA_HUB=1 (the default), whether the Lock Family hub's facade socket is
# there (LEA_HUB_SOCKET, else $XDG_RUNTIME_DIR/lockhub/facade.sock). Without it lea cannot read the
# hub's decisions, so it does not suspend an agent whose Lock call waits for one: a warning.
lea__doctor_hub() {
  if [ "${LEA_HUB:-1}" = 0 ]; then lea__ok "approval hub: off (LEA_HUB=0)"; return 0; fi
  _dhs=${LEA_HUB_SOCKET:-}
  if [ -z "$_dhs" ] && [ -n "${XDG_RUNTIME_DIR:-}" ]; then _dhs=$XDG_RUNTIME_DIR/lockhub/facade.sock; fi
  if [ -z "$_dhs" ]; then
    lea__warn "approval hub" "LEA_HUB=1 but no facade socket to read (set LEA_HUB_SOCKET, or XDG_RUNTIME_DIR): agents are not suspended on a Lock call that waits for the hub (LEA_HUB=0 to silence this)"
  elif [ -S "$_dhs" ]; then lea__ok "approval hub facade $_dhs"
  else
    lea__warn "approval hub" "LEA_HUB=1 but no lockhub facade at $_dhs (lox run starts it): agents are not suspended on a Lock call that waits for the hub (LEA_HUB=0 to silence this)"
  fi
}

# lea__doctor_lox: approvals live outside lea (#300): lox run, in a terminal of its own, starts the
# workspace's hub and locks and decides the agents' Lock requests. lea runs no lox command but
# lox version; it says whether lox is at least the set's (LOCKS_LOX_VERSION of config/locks.lock,
# lea#134) and, when the projects directory declares locks, whether a lox run serves them: lox
# run's session journal
# ($XDG_STATE_HOME/lockfamily/<project>/runs.jsonl) ends with a start or a mode switch while it
# serves, and where pgrep can tell, a lox run process of this user runs. A LEA_LOX_UP still set is
# obsolete.
lea__doctor_lox() {
  if [ -n "${LEA_LOX_UP:-}" ]; then
    lea__warn "LEA_LOX_UP" "obsolete and ignored: lea no longer runs lox up; remove it from your configuration"
  fi
  _dxl=
  if [ -n "${LEA_PROJECTS_DIR:-}" ] && { [ -f "$LEA_PROJECTS_DIR/.lockfamily/lockfamily.yaml" ] || [ -f "$LEA_PROJECTS_DIR/.lockfamily/up.json" ]; }; then
    _dxl=1
  fi
  _dxmin=$(lea__lockset LOCKS_LOX_VERSION); _dxmin=${_dxmin#v}
  if ! command -v lox >/dev/null 2>&1; then
    if [ -n "$_dxl" ]; then lea__warn "lox" "not found: brew install davidgodefroid/tap/lox, then run \`lox run\` in another terminal (outside lea)"; fi
  else
    _dxv=$(lox version 2>/dev/null </dev/null | sed -n '1s/^lox //p') || _dxv=
    case $_dxv in
      '') lea__warn "lox" "lox version printed no version: reinstall it (brew reinstall davidgodefroid/tap/lox)" ;;
      dev) lea__ok "lox dev (its version is not checked)" ;;
      *) if [ -n "$_dxmin" ] && ! lea_version_ge "$_dxv" "$_dxmin"; then
           lea__warn "lox" "$_dxv is older than $_dxmin, which has lox run: brew upgrade lox"
         else lea__ok "lox $_dxv"; fi ;;
    esac
  fi
  [ -n "$_dxl" ] || return 0
  _dxp=$(lea_abspath "$LEA_PROJECTS_DIR" 2>/dev/null) || _dxp=$LEA_PROJECTS_DIR
  _dxj=${XDG_STATE_HOME:-$HOME/.local/state}/lockfamily/lockfamily-$(printf '%s' "$_dxp" | lea_sha256 | cut -c1-8)/runs.jsonl
  _dxe=
  if [ -f "$_dxj" ]; then
    _dxe=$(tail -c 65536 "$_dxj" | grep -o '"event":"[a-z]*"' | cut -d'"' -f4 | grep -xE 'start|mode|stop' | tail -n 1) || _dxe=
  fi
  _dxs=
  case $_dxe in start | mode) _dxs=1 ;; esac
  if [ -n "$_dxs" ] && command -v pgrep >/dev/null 2>&1 && ! pgrep -u "$(id -u)" -f '(^|/)lox run( |$)' >/dev/null 2>&1; then
    _dxs= # its journal says it serves, but no lox run runs: it was killed before its stop
  fi
  if [ -n "$_dxs" ]; then lea__ok "lox run: serves the decisions of $LEA_PROJECTS_DIR"
  else lea__warn "lox run" "not serving the locks of $LEA_PROJECTS_DIR: their requests wait. Open another terminal (outside lea) and run \`lox run\` to approve the agents' connections and writes."
  fi
}

# lea__doctor_runtime: the agent containers' OCI runtime (#106): LEA_RUNTIME (empty: the engine's
# default, runc), a runtime the engine must know (runsc for gVisor, kata for Kata: a second kernel
# between the agents and the host's), and the one each running lea container was started on.
lea__doctor_runtime() {
  _drtd=$(docker info --format '{{.DefaultRuntime}}' 2>/dev/null | tr -cd 'A-Za-z0-9._-' || true)
  _drta=$(docker info --format '{{range $k, $v := .Runtimes}}{{$k}} {{end}}' 2>/dev/null | tr -cd 'A-Za-z0-9._ -' || true)
  _drtrc=0
  case ${LEA_RUNTIME:-} in
    '') lea__ok "agent runtime: ${_drtd:-the engine default} (LEA_RUNTIME empty; runsc or kata adds a second kernel: README)" ;;
    [!A-Za-z0-9]* | *[!A-Za-z0-9._-]*) lea__fail "agent runtime" "LEA_RUNTIME '$LEA_RUNTIME' is not a runtime name"; return 1 ;;
    *)
      if [ -n "$_drta" ] && ! printf ' %s ' "$_drta" | grep -qF " $LEA_RUNTIME "; then
        lea__fail "agent runtime $LEA_RUNTIME" "the engine has no such runtime (it knows: ${_drta% }): install it and register it with dockerd (README), or leave LEA_RUNTIME empty"; _drtrc=1
      else
        lea__ok "agent runtime: $LEA_RUNTIME (LEA_RUNTIME; the engine's default: ${_drtd:-unknown})"
      fi ;;
  esac
  _drtw=${LEA_RUNTIME:-$_drtd}
  [ "$_drtrc" != 0 ] || [ -z "$_drtw" ] || lea__doctor_runtime_seccomp "$_drtw"
  for _dctr in $(docker ps --filter "label=lea.prefix=$LEA_PREFIX" --filter label=lea.role=session --format '{{.Names}}' 2>/dev/null || true); do
    _drtc=$(docker inspect --format '{{.HostConfig.Runtime}}' "$_dctr" 2>/dev/null | tr -cd 'A-Za-z0-9._-' || true)
    [ -n "$_drtc" ] || continue
    if [ -n "$_drtw" ] && [ "$_drtc" != "$_drtw" ]; then
      lea__warn "$_dctr runtime" "runs on $_drtc, not $_drtw: run 'lea stop', then lea, to restart it on $_drtw"
    else
      lea__ok "$_dctr runtime: $_drtc"
    fi
  done
  return "$_drtrc"
}

# lea__doctor_runtime_seccomp NAME: whether runtime NAME applies lea's seccomp profile (#106). gVisor
# applies it inside the sandbox only with --oci-seccomp in its dockerd registration, Kata in the
# guest only with disable_guest_seccomp = false in its configuration.toml (its default is true):
# without them the agents run without lea's profile (AF_ALG, socketcall), silently.
lea__doctor_runtime_seccomp() {
  _drsj=$(docker info --format "{{json (index .Runtimes \"$1\")}}" 2>/dev/null | tr -d '\n' || true)
  # Not readable (Podman): nothing said. A shim runtime (runtimeType, Kata's) reads {}: dockerd
  # leaves its type and options out of docker info, so its name tells Kata.
  case $_drsj in '' | null) return 0 ;; esac
  _drsp=$(printf '%s' "$_drsj" | sed -n 's/.*"path":"\([^"]*\)".*/\1/p')
  _drst=$(printf '%s' "$_drsj" | sed -n 's/.*"runtimeType":"\([^"]*\)".*/\1/p')
  if [ "$1" = runsc ] || [ "${_drsp##*/}" = runsc ]; then
    if printf '%s' "$_drsj" | grep -qE '"--?oci-seccomp(=true)?"'; then
      lea__ok "$1 seccomp: lea's profile applied in the sandbox (--oci-seccomp)"
    else
      lea__warn "$1 seccomp" "its dockerd registration has no --oci-seccomp: runsc ignores lea's seccomp profile, so the agents run without it; add \"--oci-seccomp\" to its runtimeArgs (README, a second kernel), restart dockerd, then lea stop and lea"
    fi
    return 0
  fi
  case $1:$_drst in *kata*:* | *:*kata*) ;; *) return 0 ;; esac
  # The file Kata reads: the registration's ConfigPath, else the first that exists of its shim's
  # flavour (runtime-rs, or the Go runtime), /etc before the defaults.
  _drsl=$(printf '%s' "$_drsj" | sed -n 's/.*"ConfigPath":"\([^"]*\)".*/\1/p')
  if [ -z "$_drsl" ]; then
    case $_drst in /*) _drsb=$_drst ;; *) _drsb=$(command -v containerd-shim-kata-v2 2>/dev/null || true) ;; esac
    _drsb=$(readlink -f "$_drsb" 2>/dev/null || printf '%s' "$_drsb")
    _drsr="/etc/kata-containers/runtime-rs/configuration.toml /usr/share/defaults/kata-containers/runtime-rs/configuration.toml /opt/kata/share/defaults/kata-containers/runtime-rs/configuration.toml"
    _drsg="/etc/kata-containers/configuration.toml /usr/share/defaults/kata-containers/configuration.toml /opt/kata/share/defaults/kata-containers/configuration.toml"
    case $_drsb in */runtime-rs/*) _drss=$_drsr ;; '') _drss="$_drsr|$_drsg" ;; *) _drss=$_drsg ;; esac
    _drss=${LEA_KATA_CONFIGS:-$_drss}
    _drsl=
    _drsifs=$IFS; IFS='|'
    for _drsgrp in $_drss; do
      IFS=$_drsifs
      for _drsf in $_drsgrp; do
        if [ -e "$_drsf" ]; then _drsl="${_drsl:+$_drsl }$_drsf"; break; fi
      done
    done
    IFS=$_drsifs
    [ -n "$_drsl" ] || { lea__warn "$1 seccomp" "no Kata configuration.toml found ($(printf '%s' "$_drss" | tr '|' ' ')): check that Kata's sets disable_guest_seccomp = false, or lea's seccomp profile is not applied in the guest"; return 0; }
  fi
  _drsn=0
  for _drsf in $_drsl; do
    [ -e "$_drsf" ] || continue
    _drsn=$((_drsn + 1))
    if [ ! -r "$_drsf" ]; then
      lea__warn "$1 seccomp" "cannot read $_drsf: check it sets disable_guest_seccomp = false, or Kata does not apply lea's seccomp profile in the guest"
      continue
    fi
    _drsv=$(sed -n 's/^[[:space:]]*disable_guest_seccomp[[:space:]]*=[[:space:]]*\([a-z]*\).*/\1/p' "$_drsf" | tail -n 1)
    if [ "$_drsv" = false ]; then
      lea__ok "$1 seccomp: lea's profile applied in the guest (disable_guest_seccomp = false in $_drsf)"
    else
      lea__warn "$1 seccomp" "$_drsf does not set disable_guest_seccomp = false (Kata's default is true): Kata does not apply lea's seccomp profile in the guest, so the agents run without it; set it (README, a second kernel), then lea stop and lea"
    fi
  done
  [ "$_drsn" != 0 ] || lea__warn "$1 seccomp" "$_drsl does not exist (ConfigPath of its registration): Kata cannot read it"
  return 0
}

# lea__doctor_cpus/_mib/_pids: an inspected limit (NanoCpus, bytes, pids; 0 for none), read.
lea__doctor_cpus() { awk -v c="$1" 'BEGIN { if (c > 0) printf "%s CPUs", c / 1e9; else printf "no CPU limit" }'; }
lea__doctor_mib() { awk -v m="$1" 'BEGIN { if (m > 0) printf "%d MiB", int(m / 1048576); else printf "no memory limit" }'; }
lea__doctor_pids() { if [ "$1" -gt 0 ] 2>/dev/null; then printf '%s' "$1"; else printf 'no limit'; fi; }

# lea__doctor_delegation: under rootless Podman, the cgroup controllers systemd delegates to the
# user (#132). Without cpu (or memory, pids) Podman refuses that limit: lea starts its container
# without it, so the host has no ceiling there.
lea__doctor_delegation() {
  _dctl=$(docker info --format '{{.Host.Security.Rootless}} {{.Host.CgroupControllers}}' 2>/dev/null || true)
  case $_dctl in "true "*) ;; *) return 0 ;; esac
  _dctl=$(printf '%s' "${_dctl#true }" | tr -d '[]')
  [ -n "$(printf '%s' "$_dctl" | tr -d ' ')" ] || return 0
  _dmissc=
  for _dc in cpu memory pids; do
    case " $_dctl " in *" $_dc "*) ;; *) _dmissc="${_dmissc:+$_dmissc, }$_dc" ;; esac
  done
  if [ -n "$_dmissc" ]; then
    lea__warn "cgroup delegation" "rootless Podman does not get the $_dmissc controller, so lea's container runs without that limit: sudo systemctl edit user@.service, add [Service] Delegate=cpu cpuset io memory pids, log in again, then lea stop and lea"
  else
    lea__ok "cgroup delegation: cpu, memory and pids (rootless Podman)"
  fi
}

# lea__doctor_pressure: what the daemon reads to refuse or queue new agents and pause some under
# pressure (#132, P2): PSI, each running lea container's cgroup v2 (found from its pid, either
# engine), and thresholds that make sense together.
lea__doctor_pressure() {
  _dproc=${LEA_DOCTOR_PROC:-/proc}; _dcg=${LEA_DOCTOR_CGROUP:-/sys/fs/cgroup}
  if [ -r "$_dproc/pressure/cpu" ] && [ -r "$_dproc/pressure/memory" ]; then lea__ok "pressure stall information (PSI)"
  else lea__warn "pressure stall information (PSI)" "$_dproc/pressure is not readable (a kernel without PSI, or booted with psi=0): admission and shedding see memory and load only"; fi
  for _dctr in $(docker ps --filter "label=lea.prefix=$LEA_PREFIX" --filter label=lea.role=session --format '{{.Names}}' 2>/dev/null || true); do
    _dpid=$(docker inspect -f '{{.State.Pid}}' "$_dctr" 2>/dev/null || true); _dcgp=
    case $_dpid in '' | *[!0-9]* | 0) ;; *) _dcgp=$(sed -n 's/^0:://p' "$_dproc/$_dpid/cgroup" 2>/dev/null | head -n 1) ;; esac
    case $_dcgp in *..*) _dcgp= ;; esac
    if [ -n "$_dcgp" ] && [ -r "$_dcg$_dcgp/memory.current" ]; then
      lea__ok "$_dctr pressure readable (cgroup $_dcgp)"
    else
      lea__warn "$_dctr pressure" "its cgroup v2 is not readable from the host: admission and shedding see the host's readings only"
    fi
  done
  _dnum() { case $1 in '' | *[!0-9.]* | *.*.*) echo "$2" ;; *) echo "$1" ;; esac; }
  _dac=$(_dnum "${LEA_ADMIT_CPU_PSI:-}" 40); _dam=$(_dnum "${LEA_ADMIT_MEM_PCT:-}" 85); _dap=$(_dnum "${LEA_ADMIT_MEM_PSI:-}" 10)
  _dai=$(_dnum "${LEA_ADMIT_IO_PSI:-}" 40); _dal=$(_dnum "${LEA_ADMIT_LOAD:-}" 2)
  _dsm=$(_dnum "${LEA_SHED_MEM_PCT:-}" 95); _dsa=$(_dnum "${LEA_SHED_AFTER:-}" 30)
  if awk -v a="$_dac" -v b="$_dam" -v c="$_dap" -v d="$_dai" -v e="$_dal" 'BEGIN { exit !(a + b + c + d + e == 0) }'; then
    lea__warn "admission" "every LEA_ADMIT_* threshold is 0: new agents start whatever the pressure"
  else
    lea__ok "admission: cpu PSI $_dac%, memory $_dam% of the limit, memory PSI $_dap%, io PSI $_dai%, load $_dal per CPU (0: off)"
  fi
  if awk -v a="$_dsa" 'BEGIN { exit !(a == 0) }'; then lea__ok "shedding: off (LEA_SHED_AFTER=0)"
  elif awk -v s="$_dsm" -v a="$_dam" 'BEGIN { exit !(s > 0 && a > 0 && s <= a) }'; then
    lea__warn "shedding" "LEA_SHED_MEM_PCT ($_dsm) is not above LEA_ADMIT_MEM_PCT ($_dam): lea would pause agents before it stops starting new ones"
  else lea__ok "shedding: after ${_dsa} s at memory $_dsm% (LEA_SHED_*)"; fi
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
        lea__warn "Podman $_hve" "lea is built for Docker Engine; Podman minimum ${_hvpmin:-none} met; its networks have no gateway_mode_ipv4=isolated (lea starts agents only with LEA_ALLOW_GATEWAY=1)"
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

# lea__doctor_secrets STATE: every credential an agent of lea can read (issue #103), by its path
# in the lea container, never its value; a GitHub token left in an agent's state is a warning (the
# next lea moves it to the proxy).
# shellcheck disable=SC2088 # the paths are printed as the agent sees them, ~ included
lea__doctor_secrets() {
  _dsn=0
  _dsr() { lea__ok "agent-readable: $1"; _dsn=$((_dsn + 1)); }
  for _dsf in "$1"/claude/*/.credentials.json; do
    if [ -f "$_dsf" ]; then _dsr "~/.claude/.credentials.json (Claude Code's OAuth login; it refreshes it)"; break; fi
  done
  for _dsf in "$1"/codex/*/auth.json; do
    if [ -f "$_dsf" ]; then _dsr "~/.codex/auth.json (Codex's login)"; break; fi
  done
  if [ -f "$1/gh/hosts.yml" ] && grep -q 'oauth_token' "$1/gh/hosts.yml" 2>/dev/null; then
    lea__warn "agent-readable: /lea/state/gh/hosts.yml" "holds a gh token (a gh auth login in lea): the next lea moves a github.com one to the proxy; on the host, gh auth token | lea github login"
    _dsn=$((_dsn + 1))
  fi
  if [ -s "$1/git/credentials" ]; then
    if grep -qi '@github\.com' "$1/git/credentials" 2>/dev/null; then
      lea__warn "agent-readable: /lea/state/git/credentials" "holds a github.com token: the next lea moves it to the proxy"
      _dsn=$((_dsn + 1))
    else
      _dsr "/lea/state/git/credentials (git's credential store: $(grep -c . "$1/git/credentials") entries, other forges)"
    fi
  fi
  if [ -f "$1/glab-cli/config.yml" ] && grep -q 'token:' "$1/glab-cli/config.yml" 2>/dev/null; then
    _dsr "/lea/state/glab-cli/config.yml (glab's GitLab token)"
  fi
  if [ -s "$1/containers/auth.json" ]; then _dsr "/lea/state/containers/auth.json (podman and docker registry logins)"; fi
  if [ -f "$1/kube/config" ]; then _dsr "~/.kube/lea/config (read-only: a view token of lea kube refresh, 8 h)"; fi
  if [ "$_dsn" -eq 0 ]; then lea__ok "agent-readable credentials: none"; fi
  if [ -s "$(dirname "$1")/proxy/forge/github.com" ]; then _dsg="the GitHub token (in the proxy, lea github status)"
  else _dsg="no GitHub token (lea github login keeps one in the proxy)"; fi
  lea__ok "not agent-readable: $_dsg, provider API keys (proxy), lea's SSH key (ssh-agent socket only)"
}
