# shellcheck shell=sh
# The egress log (#104): an nftables table of lea's own, bridge lea_egress, that logs
# (rate-limited, prefix "lea-egress: ") every IP packet an agent network sends to the host itself
# rather than across its bridge to the proxy: a direct connection to 1.1.1.1 or 169.254.169.254
# routed through the bridge, or one to the host. Agents have no route but their network, so a
# normal session never sends one: a logged packet means something got around lea's containment
# (a container with NET_ADMIN, a misconfigured network). The packets are dropped as before (the
# host has no address on an isolated bridge, Docker's rules for internal networks); this only
# makes them visible, in the kernel log (journalctl -k -g lea-egress), and lea doctor counts them.
# The bridge family's input hook sees exactly those frames (addressed to the bridge, pkttype
# host) before the routing that drops them; agent-proxy traffic is bridged and never reaches it.
# Every internal network lea creates has a bridge named lea<12 hex digits> (the launcher's
# bridgeName), so one rule on ibrname "lea*" covers them all without root at each start. Its own
# table, not Docker's DOCKER-USER chain: DOCKER-USER (FORWARD) never sees these packets, Docker's
# nftables firewall backend has none, and a table of its own is left alone by Docker and
# firewalld. Installed by lea init (sudo, like the AppArmor profile) with a systemd unit that
# loads it at boot and after nftables.service.

LEA_EGRESS_LOG_TABLE='bridge lea_egress'
LEA_EGRESS_LOG_UNIT_NAME=lea-egress-log.service

lea__egress_log_nft_path() { printf '%s' "${LEA_EGRESS_LOG_NFT:-/etc/lea/egress-log.nft}"; }
lea__egress_log_unit_path() { printf '%s' "${LEA_EGRESS_LOG_UNIT:-/etc/systemd/system/$LEA_EGRESS_LOG_UNIT_NAME}"; }

# lea__egress_log_nft_bin: the nft binary, which is often in /usr/sbin, off a user's PATH.
lea__egress_log_nft_bin() {
  if _eb=$(command -v nft 2>/dev/null); then printf '%s' "$_eb"; return 0; fi
  for _eb in /usr/sbin/nft /sbin/nft; do
    if [ -x "$_eb" ]; then printf '%s' "$_eb"; return 0; fi
  done
  return 1
}

# lea_egress_log_skip: prints why this host gets no egress log and succeeds, or fails (it gets
# one). Only a rootful Linux engine has the agents' bridges in the host's network namespace.
lea_egress_log_skip() {
  if [ "$(uname -s)" != Linux ]; then printf 'not a Linux host'; return 0; fi
  _es_info=$(docker info --format '{{.OperatingSystem}} {{json .SecurityOptions}}' 2>/dev/null || true)
  case $_es_info in
    *name=rootless*) printf "a rootless engine: the agents' bridges are not in the host's network namespace"; return 0 ;;
    'Docker Desktop'*) printf "Docker Desktop: the agents' bridges are in its VM"; return 0 ;;
  esac
  if ! lea__egress_log_nft_bin >/dev/null; then printf 'nft is not installed (the nftables package)'; return 0; fi
  if ! command -v systemctl >/dev/null 2>&1; then printf 'no systemd to load the table at boot'; return 0; fi
  return 1
}

lea_egress_log_ruleset() {
  cat <<'EOT'
#!/usr/sbin/nft -f
# lea's egress log (#104), installed by lea init: logs every IP packet an agent network (bridge
# lea*) sends to the host rather than to lea's proxy. Logging only: the host drops these packets.
# Read them with journalctl -k -g lea-egress.
table bridge lea_egress
delete table bridge lea_egress
table bridge lea_egress {
	chain input {
		type filter hook input priority filter - 10; policy accept;
		meta ibrname "lea*" pkttype host meta protocol { ip, ip6 } meta l4proto != ipv6-icmp limit rate 20/minute burst 20 packets log prefix "lea-egress: " level warn
	}
}
EOT
}

lea_egress_log_unit() {
  _eu_nft=$(lea__egress_log_nft_bin) || _eu_nft=/usr/sbin/nft
  cat <<EOT
[Unit]
Description=lea: log packets from agent networks that bypass lea's proxy (#104)
After=nftables.service
PartOf=nftables.service
ReloadPropagatedFrom=nftables.service

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=$_eu_nft -f $(lea__egress_log_nft_path)
ExecReload=$_eu_nft -f $(lea__egress_log_nft_path)
ExecStop=$_eu_nft delete table $LEA_EGRESS_LOG_TABLE

[Install]
WantedBy=multi-user.target
EOT
}

# lea__egress_log_render: writes the ruleset and the unit in lea's host directory, where lea init
# installs them from and lea doctor compares them with the installed ones.
lea__egress_log_render() {
  lea_mkdirs "$(lea_data_dir)" "$(lea_host_dir)"
  lea_egress_log_ruleset | lea_write_file "$(lea_data_dir)" "$(lea_host_dir)/egress-log.nft"
  lea_egress_log_unit | lea_write_file "$(lea_data_dir)" "$(lea_host_dir)/$LEA_EGRESS_LOG_UNIT_NAME"
}

# lea__egress_log_current: the installed ruleset and unit are lea's.
lea__egress_log_current() {
  cmp -s "$(lea_host_dir)/egress-log.nft" "$(lea__egress_log_nft_path)" &&
    cmp -s "$(lea_host_dir)/$LEA_EGRESS_LOG_UNIT_NAME" "$(lea__egress_log_unit_path)"
}

# lea_init_egress_log: installs the table and its unit (needs sudo once, like the AppArmor
# profile). Never fatal: on lea's first run without sudo, it is left to lea init.
lea_init_egress_log() {
  lea_egress_log_skip >/dev/null && return 0
  lea__egress_log_render
  if lea__egress_log_current && systemctl is-active --quiet "$LEA_EGRESS_LOG_UNIT_NAME" 2>/dev/null; then
    if [ -z "${LEA_FIRST_RUN:-}" ]; then lea_log "the egress log ($LEA_EGRESS_LOG_UNIT_NAME) is up to date"; fi
    return 0
  fi
  if [ -n "${LEA_FIRST_RUN:-}" ] && ! sudo -n true 2>/dev/null; then
    lea_log "the egress log (packets that bypass the proxy) needs sudo once: run lea init"
    return 0
  fi
  _ei_nft=$(lea__egress_log_nft_path); _ei_unit=$(lea__egress_log_unit_path)
  lea_log "installing the egress log in $_ei_nft and $LEA_EGRESS_LOG_UNIT_NAME (needs sudo)"
  sudo install -d -m 0755 "${_ei_nft%/*}"
  sudo install -m 0644 "$(lea_host_dir)/egress-log.nft" "$_ei_nft"
  sudo install -m 0644 "$(lea_host_dir)/$LEA_EGRESS_LOG_UNIT_NAME" "$_ei_unit"
  sudo systemctl daemon-reload
  sudo systemctl enable "$LEA_EGRESS_LOG_UNIT_NAME"
  sudo systemctl restart "$LEA_EGRESS_LOG_UNIT_NAME"
}

# lea__doctor_egress_log: lea doctor's lines on the egress log: installed, current, loaded (when
# sudo -n can tell), the internal networks it covers, and the packets it logged in the last 24
# hours (when this user, or sudo -n, may read the kernel log). Warnings only: it detects, it does not contain.
lea__doctor_egress_log() {
  if _ed_why=$(lea_egress_log_skip); then lea__ok "egress log: not applicable ($_ed_why)"; return 0; fi
  lea__egress_log_render
  if [ ! -e "$(lea__egress_log_nft_path)" ] || [ ! -e "$(lea__egress_log_unit_path)" ]; then
    lea__warn "egress log" "not installed: packets that bypass the proxy go unnoticed; run lea init (needs sudo once)"
  elif ! lea__egress_log_current; then
    lea__warn "egress log" "$(lea__egress_log_nft_path) or $LEA_EGRESS_LOG_UNIT_NAME is not lea's current one: run lea init (needs sudo)"
  elif ! systemctl is-active --quiet "$LEA_EGRESS_LOG_UNIT_NAME" 2>/dev/null; then
    lea__warn "egress log" "$LEA_EGRESS_LOG_UNIT_NAME is not active: sudo systemctl enable --now $LEA_EGRESS_LOG_UNIT_NAME"
  elif sudo -n true 2>/dev/null && ! sudo -n "$(lea__egress_log_nft_bin)" list table bridge lea_egress >/dev/null 2>&1; then
    lea__warn "egress log" "table $LEA_EGRESS_LOG_TABLE is not loaded (an nft flush ruleset?): sudo systemctl restart $LEA_EGRESS_LOG_UNIT_NAME"
  else
    lea__ok "egress log ($LEA_EGRESS_LOG_UNIT_NAME)"
  fi
  # Networks made by an older lea, or without gateway_mode_ipv4=isolated, keep Docker's br-<id>.
  for _ed_net in $(docker network ls --filter "label=lea.prefix=$LEA_PREFIX" --format '{{.Name}}' 2>/dev/null || true); do
    case $_ed_net in "$LEA_PREFIX"-internal-*) ;; *) continue ;; esac
    _ed_br=$(docker network inspect -f '{{index .Options "com.docker.network.bridge.name"}}' "$_ed_net" 2>/dev/null || true)
    case $_ed_br in
      lea*) ;;
      *) lea__warn "$_ed_net egress log" "its bridge is not named lea*, so the egress log does not cover it: lea stop --all, docker network rm $_ed_net, then lea" ;;
    esac
  done
  if command -v journalctl >/dev/null 2>&1; then
    # The kernel log needs the adm or systemd-journal group, or root: sudo -n when it works.
    if sudo -n true 2>/dev/null; then
      _ed_n=$(sudo -n journalctl -k -q --no-pager --since -24h 2>/dev/null | grep -c 'lea-egress: ' || true)
    else
      _ed_n=$(journalctl -k -q --no-pager --since -24h 2>/dev/null | grep -c 'lea-egress: ' || true)
    fi
    if [ "${_ed_n:-0}" -gt 0 ]; then
      lea__warn "egress log" "$_ed_n packet(s) from an agent network bypassed the proxy in the last 24 hours (journalctl -k -g lea-egress): something got around lea's containment"
    fi
  fi
  return 0
}
