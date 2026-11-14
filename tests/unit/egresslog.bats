load helpers
# The egress log (#104): lea init installs an nftables table that logs packets from agent networks
# that bypass the proxy, and lea doctor checks it and counts what it logged.
setup() {
  setup_fakes; load_lea
  H=$(lea_host_dir)
  [ "$(uname -s)" = Linux ] || skip "the egress log is Linux only"
}

installed() {
  mkdir -p "${LEA_EGRESS_LOG_NFT%/*}" "${LEA_EGRESS_LOG_UNIT%/*}"
  lea_egress_log_ruleset >"$LEA_EGRESS_LOG_NFT"
  lea_egress_log_unit >"$LEA_EGRESS_LOG_UNIT"
}

@test "the ruleset logs, rate-limited, the IP packets a lea* bridge takes up to the host" {
  run lea_egress_log_ruleset
  [[ "$output" == *"table bridge lea_egress {"* ]]
  [[ "$output" == *"delete table bridge lea_egress"* ]]
  [[ "$output" == *'type filter hook input priority filter - 10; policy accept;'* ]]
  [[ "$output" == *'meta ibrname "lea*" pkttype host meta protocol { ip, ip6 } meta l4proto != ipv6-icmp limit rate 20/minute burst 20 packets log prefix "lea-egress: " level warn'* ]]
  # Logging only: the table drops and accepts nothing on its own.
  run sh -c 'grep -E "^[[:space:]]*meta ibrname" | grep -wE "drop|reject|accept|return"' <<<"$output"
  [ "$status" -eq 1 ]
}

@test "the unit loads the table at boot and again after nftables.service" {
  run lea_egress_log_unit
  [[ "$output" == *"ExecStart=$LEA_ROOT/tests/fakes/nft -f $LEA_EGRESS_LOG_NFT"* ]]
  [[ "$output" == *"ExecStop=$LEA_ROOT/tests/fakes/nft delete table bridge lea_egress"* ]]
  [[ "$output" == *"PartOf=nftables.service"* ]]
  [[ "$output" == *"ReloadPropagatedFrom=nftables.service"* ]]
  [[ "$output" == *"WantedBy=multi-user.target"* ]]
}

@test "lea init installs the table and its unit with sudo" {
  lea_init_egress_log 2>/dev/null
  run calls
  [[ "$output" == *"sudo [install] [-m] [0644] [$H/egress-log.nft] [$LEA_EGRESS_LOG_NFT]"* ]]
  [[ "$output" == *"sudo [install] [-m] [0644] [$H/lea-egress-log.service] [$LEA_EGRESS_LOG_UNIT]"* ]]
  [[ "$output" == *"sudo [systemctl] [daemon-reload]"* ]]
  [[ "$output" == *"sudo [systemctl] [enable] [lea-egress-log.service]"* ]]
  [[ "$output" == *"sudo [systemctl] [restart] [lea-egress-log.service]"* ]]
  cmp -s "$H/egress-log.nft" <(lea_egress_log_ruleset)
}

@test "lea init leaves a current, active egress log alone" {
  installed
  export FAKE_SYSTEMCTL_ACTIVE=1
  run lea_init_egress_log
  [ "$status" -eq 0 ]
  [[ "$output" == *"the egress log (lea-egress-log.service) is up to date"* ]]
  run calls
  [[ "$output" != *sudo* ]]
}

@test "lea init reinstalls an egress log that is out of date" {
  installed
  printf '# changed\n' >>"$LEA_EGRESS_LOG_NFT"
  export FAKE_SYSTEMCTL_ACTIVE=1
  lea_init_egress_log 2>/dev/null
  run calls
  [[ "$output" == *"sudo [install] [-m] [0644] [$H/egress-log.nft] [$LEA_EGRESS_LOG_NFT]"* ]]
}

@test "the first run without sudo leaves the egress log to lea init, and goes on" {
  export LEA_FIRST_RUN=1 FAKE_SUDO_RC=1
  run lea_init_egress_log
  [ "$status" -eq 0 ]
  [[ "$output" == *"the egress log (packets that bypass the proxy) needs sudo once: run lea init"* ]]
  run calls
  [[ "$output" != *"sudo [install]"* ]]
}

@test "a rootless engine gets no egress log, and lea doctor says why" {
  export FAKE_DOCKER_OUT_INFO___FORMAT='linux ["name=seccomp,profile=builtin","name=rootless"]'
  lea_init_egress_log
  run calls
  [[ "$output" != *sudo* ]]
  run lea__doctor_egress_log
  [[ "$output" == *"ok    egress log: not applicable (a rootless engine: the agents' bridges are not in the host's network namespace)"* ]]
}

@test "lea doctor warns when the egress log is not installed" {
  run lea__doctor_egress_log
  [ "$status" -eq 0 ]
  [[ "$output" == *"warn  egress log: not installed: packets that bypass the proxy go unnoticed; run lea init (needs sudo once)"* ]]
}

@test "lea doctor warns when the unit is not active" {
  installed
  run lea__doctor_egress_log
  [[ "$output" == *"warn  egress log: lea-egress-log.service is not active: sudo systemctl enable --now lea-egress-log.service"* ]]
}

@test "lea doctor warns when the table was flushed, if sudo -n can tell" {
  installed
  export FAKE_SYSTEMCTL_ACTIVE=1 FAKE_NFT_RC=1
  # A sudo that runs what it is given: sudo -n true succeeds, nft list table fails.
  mkdir -p "$BATS_TEST_TMPDIR/sbin"
  printf '#!/bin/sh\n[ "$1" != -n ] || shift\nexec "$@"\n' >"$BATS_TEST_TMPDIR/sbin/sudo"
  chmod +x "$BATS_TEST_TMPDIR/sbin/sudo"
  PATH="$BATS_TEST_TMPDIR/sbin:$PATH"
  run lea__doctor_egress_log
  [[ "$output" == *"warn  egress log: table bridge lea_egress is not loaded (an nft flush ruleset?): sudo systemctl restart lea-egress-log.service"* ]]
}

@test "lea doctor is fine with a current, active, loaded egress log" {
  installed
  export FAKE_SYSTEMCTL_ACTIVE=1
  run lea__doctor_egress_log
  [[ "$output" == *"ok    egress log (lea-egress-log.service)"* ]]
  [[ "$output" != *warn* ]]
}

@test "lea doctor counts the packets logged in the last 24 hours" {
  installed
  # Without sudo -n, journalctl as this user (with sudo -n, as root: the next test).
  export FAKE_SYSTEMCTL_ACTIVE=1 FAKE_SUDO_RC=1
  export FAKE_JOURNAL='Nov 14 10:00:01 host kernel: lea-egress: IN=lea0123456789ab OUT=eth0 SRC=172.20.0.2 DST=169.254.169.254 PROTO=TCP DPT=80
Nov 14 10:00:02 host kernel: something else
Nov 14 10:00:03 host kernel: lea-egress: IN=lea0123456789ab OUT=eth0 SRC=172.20.0.2 DST=1.1.1.1 PROTO=TCP DPT=80'
  run lea__doctor_egress_log
  [[ "$output" == *"warn  egress log: 2 packet(s) from an agent network bypassed the proxy in the last 24 hours (journalctl -k -g lea-egress): something got around lea's containment"* ]]
  run calls
  [[ "$output" == *"journalctl [-k] [-q] [--no-pager] [--since] [-24h]"* ]]
}

@test "lea doctor names an internal network whose bridge the egress log does not cover" {
  installed
  export FAKE_SYSTEMCTL_ACTIVE=1
  export FAKE_DOCKER_OUT_NETWORK_LS=$'lea-internal-abc\nlea-internal-def\nlea-egress'
  export FAKE_DOCKER_OUT_NETWORK_INSPECT_AT_LEA_INTERNAL_ABC=lea0123456789ab
  export FAKE_DOCKER_OUT_NETWORK_INSPECT_AT_LEA_INTERNAL_DEF='<no value>'
  run lea__doctor_egress_log
  [[ "$output" == *"warn  lea-internal-def egress log: its bridge is not named lea*, so the egress log does not cover it: lea stop --all, docker network rm lea-internal-def, then lea"* ]]
  [[ "$output" != *"lea-internal-abc egress log"* ]]
  [[ "$output" != *"lea-egress egress log"* ]]
}

@test "lea doctor reads the kernel log with sudo -n when it works" {
  installed
  export FAKE_SYSTEMCTL_ACTIVE=1
  run lea__doctor_egress_log
  run calls
  [[ "$output" == *"sudo [-n] [journalctl] [-k] [-q] [--no-pager] [--since] [-24h]"* ]]
}
