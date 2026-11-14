load helpers
setup() { setup_fakes; load_lea; I="$LEA_ROOT/image"; }

@test "containers.conf gives nested containers HTTPS_PROXY only" {
  grep -q '"HTTPS_PROXY=@LEA_NESTED_PROXY@"' "$I/containers.conf.tmpl"
  run grep -q '"HTTP_PROXY=' "$I/containers.conf.tmpl"
  [ "$status" -ne 0 ]
  grep -q '^http_proxy = false' "$I/containers.conf.tmpl"
}

@test "nested git reaches the git SSH endpoints through the ssh config the entrypoint writes" {
  grep -q '"GIT_SSH_COMMAND=ssh -F /run/lea/ssh_config"' "$I/containers.conf.tmpl"
  grep -q 'SSH_AUTH_SOCK=/run/lea/ssh-agent.sock' "$I/containers.conf.tmpl"
}

@test "managed settings: telemetry through the proxy, lea attribute, updates off, prompts redacted" {
  m="$I/managed-settings.json"
  [ "$(jq -r .env.CLAUDE_CODE_ENABLE_TELEMETRY "$m")" = 1 ]
  [ "$(jq -r .env.OTEL_EXPORTER_OTLP_ENDPOINT "$m")" = "http://lea-proxy:4318" ]
  [ "$(jq -r .env.OTEL_RESOURCE_ATTRIBUTES "$m")" = "service.name=lea,service.version=@LEA_VERSION@" ]
  [ "$(jq -r .env.DISABLE_UPDATES "$m")" = 1 ]
  [ "$(jq -r '.env.OTEL_LOG_USER_PROMPTS // "unset"' "$m")" = unset ]
}

# The profile is built from the pinned moby default and lea's reviewed changes (#100).
@test "seccomp profile is what image/seccomp/generate.py builds from the pinned moby default" {
  python3 -I "$I/seccomp/generate.py" --check
  [ "$(lea_sha256_file "$I/seccomp/moby-default.json")" = "$(jq -r .base.sha256 "$I/seccomp/lea.json")" ]
  [ "$(jq -r .defaultAction "$I/seccomp.json")" = SCMP_ACT_ERRNO ]
}

# A copy of image/ with seccomp/ and seccomp.json, for the generator's refusals.
seccomp_copy() { mkdir -p "$BATS_TEST_TMPDIR/i/seccomp"; cp "$I"/seccomp/* "$BATS_TEST_TMPDIR/i/seccomp/"; cp "$I/seccomp.json" "$BATS_TEST_TMPDIR/i/"; G="$BATS_TEST_TMPDIR/i/seccomp/generate.py"; }

@test "seccomp check fails on a hand edit of the profile" {
  seccomp_copy
  jq '.syscalls[0].names += ["keyctl"]' "$I/seccomp.json" >"$BATS_TEST_TMPDIR/i/seccomp.json"
  run python3 -I "$G" --check
  [ "$status" -ne 0 ]
  [[ "$output" == *"not what image/seccomp/generate.py builds"* ]]
}

@test "seccomp check fails on a base that is not the pinned moby default" {
  seccomp_copy
  jq '.syscalls[0].names += ["keyctl"]' "$I/seccomp/moby-default.json" >"$BATS_TEST_TMPDIR/i/seccomp/moby-default.json"
  run python3 -I "$G" --check
  [ "$status" -ne 0 ]
  [[ "$output" == *"not the pinned base"* ]]
}

@test "seccomp generator refuses a widened syscall without a reason" {
  seccomp_copy
  jq '.allow += [{"name": "keyctl"}]' "$I/seccomp/lea.json" >"$BATS_TEST_TMPDIR/i/seccomp/lea.json"
  run python3 -I "$G"
  [ "$status" -ne 0 ]
  [[ "$output" == *"allow keyctl: a one-line reason (why) is required"* ]]
}

# Every syscall the profile allows unconditionally and moby does not carries lea's reason, in the
# profile itself; what the nested Podman does not need is refused (keyring, perf, reboot, ...).
@test "seccomp profile widens moby only with a reason, and not for what the nested Podman does not need" {
  f="$I/seccomp.json"
  wide=$(jq -r --slurpfile m "$I/seccomp/moby-default.json" '($m[0].syscalls[0].names) as $base
    | .syscalls[] | select(.action == "SCMP_ACT_ALLOW" and (.args // []) == [] and (.includes // {}) == {} and (.excludes // {}) == {})
    | .names[] as $n | select($base | index($n) | not) | $n' "$f" | sort | tr '\n' ' ')
  [ "$wide" = "clone mount mount_setattr move_mount open_tree pivot_root setns umount2 unshare " ]
  [ "$(jq '[.syscalls[] | select(.action == "SCMP_ACT_ALLOW" and (.names | length) == 1 and ((.comment // "") | startswith("lea: ") | not)
    and (.args // []) == [] and (.includes // {}) == {} and (.excludes // {}) == {})] | length' "$f")" -eq 0 ]
  for n in keyctl add_key request_key reboot bpf perf_event_open syslog fanotify_init clone3 fsopen fsconfig fsmount fspick \
           pidfd_getfd sethostname setdomainname kexec_load init_module vmsplice; do
    [ "$(jq --arg n "$n" '[.syscalls[] | select(.action == "SCMP_ACT_ALLOW" and (.names | index($n)) and (.includes // {}) == {})] | length' "$f")" -eq 0 ]
  done
  [ "$(jq -r '.syscalls[] | select(.names == ["clone3"]) | .errnoRet' "$f")" = 38 ]
}

# The hand-written profile lea ran before #100 refused these on purpose (EPERM, unconditionally or
# without the capability lea's container lacks): each stays refused, under any capability lea has.
# vmsplice, cachestat, map_shadow_stack, the futex2 calls and io_pgetevents are allowed by moby's
# default: lea.json removes them, each with its reason.
@test "seccomp profile still refuses every syscall the pre-#100 profile refused on purpose" {
  f="$I/seccomp.json"
  caps='["CAP_CHOWN","CAP_DAC_OVERRIDE","CAP_FSETID","CAP_FOWNER","CAP_SETGID","CAP_SETUID","CAP_SETFCAP","CAP_SETPCAP","CAP_NET_BIND_SERVICE","CAP_SYS_CHROOT","CAP_KILL"]'
  for n in vmsplice cachestat map_shadow_stack futex_waitv futex_wake futex_wait futex_requeue io_pgetevents io_pgetevents_time64 \
           bdflush kexec_file_load kexec_load migrate_pages move_pages nfsservctl nice oldfstat oldlstat oldolduname oldstat \
           olduname pciconfig_iobase pciconfig_read pciconfig_write sgetmask ssetmask swapoff swapon syscall sysfs uselib \
           userfaultfd ustat vm86 vm86old open_by_handle_at bpf lookup_dcookie perf_event_open quotactl quotactl_fd \
           setdomainname sethostname delete_module finit_module init_module query_module acct kcmp process_madvise ioperm iopl \
           clock_settime clock_settime64 settimeofday stime vhangup; do
    run jq --arg n "$n" --argjson caps "$caps" '[.syscalls[] | select(.action == "SCMP_ACT_ALLOW" and (.names | index($n))
      and ((.includes.caps // []) - $caps) == [] and ([(.excludes.caps // [])[] | select(. as $c | $caps | index($c))] | length) == 0
      and ((.includes.arches // ["amd64"]) | index("amd64") or index("arm64")))] | length' "$f"
    [ "$output" = 0 ] || { echo "$n is allowed"; return 1; }
  done
  for n in vmsplice cachestat map_shadow_stack futex_waitv futex_wake futex_wait futex_requeue io_pgetevents io_pgetevents_time64; do
    [ "$(jq --arg n "$n" '[.remove[] | select(.name == $n and (.why | length) > 0)] | length' "$I/seccomp/lea.json")" = 1 ]
  done
}

# CVE-2026-31431: the kernel crypto API (AF_ALG, 38) is out of reach. socketcall multiplexes
# socket(2) behind a pointer seccomp cannot read: it is not allowed, and 32-bit x86, where libseccomp
# turns the socket rules into socketcall rules without the family test, is not in the profile. Every
# rule that allows socket names a family other than 38 (moby's list) or excludes 38 itself (a rule's
# conditions are ANDed, rules are ORed), and family 38 gets EAFNOSUPPORT from a rule no allow rule
# overlaps (crun refuses overlaps: EEXIST).
@test "seccomp profile refuses socketcall and AF_ALG sockets" {
  f="$I/seccomp.json"
  [ "$(jq '[.syscalls[] | select(.action == "SCMP_ACT_ALLOW" and (.names | index("socketcall")))] | length' "$f")" -eq 0 ]
  [ "$(jq -c '[.archMap[] | select(.architecture == "SCMP_ARCH_X86_64") | .subArchitectures[]]' "$f")" = '["SCMP_ARCH_X32"]' ]
  [ "$(jq '[.syscalls[] | select(.action == "SCMP_ACT_ALLOW" and (.names | index("socket")))] | length' "$f")" -gt 0 ]
  [ "$(jq '[.syscalls[] | select(.action == "SCMP_ACT_ALLOW" and (.names | index("socket")))
    | select([(.args // [])[] | select(.index == 0 and ((.op == "SCMP_CMP_NE" and .value == 38)
        or (.op == "SCMP_CMP_EQ" and .value != 38) or (.op == "SCMP_CMP_LT" and .value <= 38)))] | length == 0)]
    | length' "$f")" -eq 0 ]
  [ "$(jq -c '[.syscalls[] | select(.names == ["socket"] and .action == "SCMP_ACT_ERRNO" and .errnoRet == 97) | .args]' "$f")" = \
    '[[{"index":0,"value":4294967295,"valueTwo":38,"op":"SCMP_CMP_MASKED_EQ"}]]' ]
  # Each rule compares an argument once: runc splits a rule that compares one twice into ORed rules.
  [ "$(jq '[.syscalls[] | select((.args // []) | map(.index) | length != (unique | length))] | length' "$f")" -eq 0 ]
}

# NETLINK_AUDIT (AF_NETLINK 16, protocol 9) gets EINVAL, as in the pre-#100 profile: lea drops
# CAP_AUDIT_WRITE. moby's allow rule for family 16 is narrowed to the other netlink protocols, each
# compared in 64 bits (LT 9, EQ 10 to 31), so no allow rule overlaps the errno rule (crun refuses
# overlaps: EEXIST) and none lets 0x100000009 through, which the kernel truncates to 9; the errno
# rule compares the low 32 bits only. Each change carries its reason.
@test "seccomp profile refuses NETLINK_AUDIT sockets and keeps the other netlink sockets" {
  f="$I/seccomp.json"
  [ "$(jq -c '[.syscalls[] | select(.names == ["socket"] and .action == "SCMP_ACT_ERRNO" and .errnoRet == 22) | .args]' "$f")" = \
    '[[{"index":0,"value":4294967295,"valueTwo":16,"op":"SCMP_CMP_MASKED_EQ"},{"index":2,"value":4294967295,"valueTwo":9,"op":"SCMP_CMP_MASKED_EQ"}]]' ]
  # Every allow rule for socket tests a family other than 16 or a protocol below 2^32 other than 9.
  [ "$(jq '[.syscalls[] | select(.action == "SCMP_ACT_ALLOW" and (.names | index("socket")))
    | select([(.args // [])[] | select((.index == 0 and ((.op == "SCMP_CMP_EQ" and .value != 16) or (.op == "SCMP_CMP_LT" and .value <= 16)))
        or (.index == 2 and ((.op == "SCMP_CMP_EQ" and .value != 9 and .value < 4294967296) or (.op == "SCMP_CMP_LT" and .value <= 9))))] | length == 0)] | length' "$f")" -eq 0 ]
  [ "$(jq -c '[.syscalls[] | select(.names == ["socket"] and .action == "SCMP_ACT_ALLOW" and (.args | map(select(.index == 0 and .value == 16)) | length) > 0)
    | .args[1] | [.op, .value]]' "$f")" = \
    "[[\"SCMP_CMP_LT\",9]$(for p in $(seq 10 31); do printf ',["SCMP_CMP_EQ",%d]' "$p"; done)]" ]
  l="$I/seccomp/lea.json"
  [ "$(jq '[.rules[] | select(.rule.errnoRet == 22 and (.why | test("NETLINK_AUDIT")))] | length' "$l")" = 1 ]
  [ "$(jq '[.narrow[] | select(.base.args == [{"index":0,"value":16,"op":"SCMP_CMP_EQ"}] and (.why | test("NETLINK_AUDIT")))] | length' "$l")" = 1 ]
}

# seccomp compares 64 bits, the kernel reads an int argument's low 32 (socket's family, type and
# protocol, personality's persona): the generator refuses a comparison that high bits would get past.
@test "seccomp generator refuses a 64-bit comparison of a 32-bit argument that fails open" {
  seccomp_copy
  # The scan's finding: NE 9 on the protocol lets 0x100000009 (NETLINK_AUDIT) through.
  jq '.narrow[0].rules = [{"names": ["socket"], "action": "SCMP_ACT_ALLOW", "args": [{"index": 0, "value": 16, "op": "SCMP_CMP_EQ"}, {"index": 2, "value": 9, "op": "SCMP_CMP_NE"}]}]' \
    "$I/seccomp/lea.json" >"$BATS_TEST_TMPDIR/i/seccomp/lea.json"
  run python3 -I "$G"
  [ "$status" -ne 0 ]
  [[ "$output" == *"socket: argument 2 is a 32-bit int the kernel truncates, compared as SCMP_CMP_NE 9: an allow rule must match no value"* ]]
  # A refusal by EQ misses the same value.
  jq '(.rules[] | select(.rule.errnoRet == 22) | .rule.args[1]) = {"index": 2, "value": 9, "valueTwo": 0, "op": "SCMP_CMP_EQ"}' \
    "$I/seccomp/lea.json" >"$BATS_TEST_TMPDIR/i/seccomp/lea.json"
  run python3 -I "$G"
  [ "$status" -ne 0 ]
  [[ "$output" == *"socket: argument 2 is a 32-bit int the kernel truncates, compared as SCMP_CMP_EQ 9: a rule that refuses must test its low 32 bits only"* ]]
  # An allow rule by GE, or by an EQ above 2^32, also matches high bits.
  jq '.narrow[0].rules[0].args[1] = {"index": 2, "value": 10, "op": "SCMP_CMP_GE"}' "$I/seccomp/lea.json" >"$BATS_TEST_TMPDIR/i/seccomp/lea.json"
  run python3 -I "$G"
  [ "$status" -ne 0 ]
  [[ "$output" == *"compared as SCMP_CMP_GE 10"* ]]
  jq '.narrow[0].rules[0].args[1] = {"index": 2, "value": 4294967306, "op": "SCMP_CMP_EQ"}' "$I/seccomp/lea.json" >"$BATS_TEST_TMPDIR/i/seccomp/lea.json"
  run python3 -I "$G"
  [ "$status" -ne 0 ]
  [[ "$output" == *"compared as SCMP_CMP_EQ 4294967306"* ]]
  # A masked refusal that keeps high bits in its mask.
  jq '(.rules[] | select(.rule.errnoRet == 97) | .rule.args[0].value) = 8589934591' "$I/seccomp/lea.json" >"$BATS_TEST_TMPDIR/i/seccomp/lea.json"
  run python3 -I "$G"
  [ "$status" -ne 0 ]
  [[ "$output" == *"socket: argument 0 is a 32-bit int the kernel truncates, compared as SCMP_CMP_MASKED_EQ 8589934591"* ]]
  # An argument whose width the generator does not know.
  jq '.rules += [{"why": "test", "rule": {"names": ["ioctl"], "action": "SCMP_ACT_ERRNO", "args": [{"index": 1, "value": 4294967295, "valueTwo": 21531, "op": "SCMP_CMP_MASKED_EQ"}], "errnoRet": 1}}]' \
    "$I/seccomp/lea.json" >"$BATS_TEST_TMPDIR/i/seccomp/lea.json"
  run python3 -I "$G"
  [ "$status" -ne 0 ]
  [[ "$output" == *"ioctl: argument 1 is tested but its width is not in ARG_BITS"* ]]
}

@test "seccomp generator narrows a base rule only by tests of other arguments, with a reason" {
  seccomp_copy
  jq '.narrow[0].rules[3].args[1] = {"index": 0, "value": 9, "op": "SCMP_CMP_LT"}' "$I/seccomp/lea.json" >"$BATS_TEST_TMPDIR/i/seccomp/lea.json"
  run python3 -I "$G"
  [ "$status" -ne 0 ]
  [[ "$output" == *"the rule must be the base rule with tests of other arguments added"* ]]
  jq '.narrow[0].rules[0].action = "SCMP_ACT_LOG"' "$I/seccomp/lea.json" >"$BATS_TEST_TMPDIR/i/seccomp/lea.json"
  run python3 -I "$G"
  [ "$status" -ne 0 ]
  [[ "$output" == *"the rule must be the base rule with tests of other arguments added"* ]]
  jq '.narrow[0].base.args[0].value = 99' "$I/seccomp/lea.json" >"$BATS_TEST_TMPDIR/i/seccomp/lea.json"
  run python3 -I "$G"
  [ "$status" -ne 0 ]
  [[ "$output" == *"the base has 0 such rules, not one"* ]]
  jq '.narrow[0].rules = []' "$I/seccomp/lea.json" >"$BATS_TEST_TMPDIR/i/seccomp/lea.json"
  run python3 -I "$G"
  [ "$status" -ne 0 ]
  [[ "$output" == *"rules lists the narrowed rules"* ]]
  jq 'del(.narrow[0].why)' "$I/seccomp/lea.json" >"$BATS_TEST_TMPDIR/i/seccomp/lea.json"
  run python3 -I "$G"
  [ "$status" -ne 0 ]
  [[ "$output" == *"a one-line reason (why) is required"* ]]
}

# #100: once the entrypoint drops to the developer, nothing gives root back; crun joins no keyring.
@test "Containerfile removes sudo and every setuid or setgid bit" {
  c="$I/Containerfile"
  grep -q '^RUN rpm -e sudo \\$' "$c"
  grep -qF ' && find / -xdev -type f \( -perm -4000 -o -perm -2000 \) -exec chmod ug-s {} + \' "$c"
  run grep -E '^(RUN|COPY|ADD|ENV|ARG)' <(sed -n '/^RUN rpm -e sudo/,$p' "$c" | sed 1d)
  [[ "$output" != *RUN* && "$output" != *COPY* ]]
}

@test "containers.conf: nested containers join no session keyring (seccomp refuses keyctl)" {
  grep -q '^keyring = false$' "$I/containers.conf.tmpl"
}

@test "Containerfile pins every version" {
  c="$I/Containerfile"
  grep -q '^FROM quay.io/podman/stable:v5.8.7-immutable$' "$c"
  grep -q '^ARG CLAUDE_CODE_VERSION=2.1.296$' "$c"
  # rtk is no longer installed (#359).
  if grep -qiw rtk "$c"; then false; fi
  # The Locks' clients: no version here, config/locks.lock's set (lea#134).
  grep -q '^COPY config/locks.lock /usr/local/share/lea/locks.lock$' "$c"
  ! grep -Eq '(V|MQ|LF)=[0-9]+\.[0-9]+' "$c"
}

@test "Containerfile ships Go 1.26 by digest, staticcheck and govulncheck pinned, with Go's state in the state dir go" {
  c="$I/Containerfile"
  grep -q '^ARG GO_IMAGE=docker.io/library/golang:1\.26\.[0-9]*@sha256:[0-9a-f]\{64\}$' "$c"
  grep -q '^FROM --platform=$BUILDPLATFORM ${GO_IMAGE} AS go-tools$' "$c"
  grep -q '^ARG STATICCHECK_VERSION=v0.8.1$' "$c"
  grep -q '^ARG GOVULNCHECK_VERSION=v1.8.0$' "$c"
  grep -q '^COPY --from=go /usr/local/go /usr/local/go$' "$c"
  grep -q '^ENV PATH=/usr/local/go/bin:/lea/state/go/bin:$PATH' "$c"
  for v in GOTOOLCHAIN=local GOPATH=/lea/state/go GOCACHE=/lea/state/go/cache GOFLAGS=-modcacherw; do grep -q "^ *$v\( \\\\\)\?\$" "$c"; done
  grep -q '^\[ -d /lea/state/go \] || install -d -o "$LEA_UID" -g "$LEA_GID" /lea/state/go$' "$I/entrypoint"
}

@test "go-cross runs go for another platform without a variable prefix" {
  g="$I/go-cross"
  [ -x "$g" ]
  grep -q '^COPY image/go-cross /usr/local/bin/$' "$I/Containerfile"
  grep -q 'chmod 0755 .*/usr/local/bin/go-cross' "$I/Containerfile"
  mkdir -p "$BATS_TEST_TMPDIR/bin"
  printf '#!/bin/sh\necho "$GOOS/$GOARCH $*"\n' >"$BATS_TEST_TMPDIR/bin/go"
  chmod +x "$BATS_TEST_TMPDIR/bin/go"
  PATH="$BATS_TEST_TMPDIR/bin:$PATH" run "$g" darwin arm64 vet ./...
  [ "$status" -eq 0 ]
  [ "$output" = "darwin/arm64 vet ./..." ]
  run "$g" darwin arm64
  [ "$status" -eq 2 ]
}

@test "lea's Claude settings let agents run go, go-cross, staticcheck and govulncheck" {
  s="$I/claude-settings.json"
  for r in 'Bash(go *)' 'Bash(go-cross *)' 'Bash(staticcheck *)' 'Bash(govulncheck *)'; do
    jq -e --arg r "$r" '.permissions.allow | index($r)' "$s" >/dev/null
  done
  # Claude Code honours the managed rules only (#111): lea's are there too.
  for r in 'Bash(go *)' 'Bash(go-cross *)' 'Bash(staticcheck *)' 'Bash(govulncheck *)'; do
    jq -e --arg r "$r" '.permissions.allow | index($r)' "$I/managed-settings.json" >/dev/null
  done
}

# An agent can rewrite its own ~/.claude/settings.json (#111) and a project's .claude/settings*.json:
# the permission rules are the managed settings' only, bypass mode is off, the default mode is
# Claude Code's own, as on the host (#333: lea sets none), and the --settings file (server-managed
# hosts) disables bypass too.
@test "managed settings: permission rules managed only, bypass disabled, no default mode of lea's" {
  m="$I/managed-settings.json" s="$I/claude-settings.json"
  [ "$(jq -r .allowManagedPermissionRulesOnly "$m")" = true ]
  [ "$(jq -r .permissions.disableBypassPermissionsMode "$m")" = disable ]
  [ "$(jq -r .permissions.defaultMode "$m")" = null ]
  [ "$(jq -r .permissions.defaultMode "$s")" = null ]
  [ "$(jq -r .permissions.disableBypassPermissionsMode "$s")" = disable ]
}

# The variables lea's hooks read that lea sets the same in every container are pinned in the
# managed settings and the --settings file, both above an agent's own settings (#111).
@test "managed settings pin the bus socket, the ssh-agent socket and gh's and glab's config dirs" {
  for f in "$I/managed-settings.json" "$I/claude-settings.json"; do
    [ "$(jq -r .env.LEA_BUS_SOCK "$f")" = /run/lea/bus/bus.sock ]
    [ "$(jq -r .env.SSH_AUTH_SOCK "$f")" = /run/lea/ssh-agent.sock ]
    [ "$(jq -r .env.GH_CONFIG_DIR "$f")" = /lea/state/gh ]
    [ "$(jq -r .env.GLAB_CONFIG_DIR "$f")" = /lea/state/glab-cli ]
  done
  grep -q 'SSH_AUTH_SOCK=/run/lea/ssh-agent.sock' "$I/Containerfile"
  grep -q 'GH_CONFIG_DIR=/lea/state/gh' "$I/Containerfile"
  grep -q 'GLAB_CONFIG_DIR=/lea/state/glab-cli' "$I/Containerfile"
}

# The guard fails closed (#111): a missing lea exits 2 (a deny), and a crash or a timeout blocks
# the call too (onFailure).
@test "lea's PreToolUse hook fails closed when lea is missing, crashes or times out" {
  for f in "$I/managed-settings.json" "$I/claude-settings.json"; do
    jq -e '.hooks.PreToolUse[0].hooks[0] | .onFailure == "block" and (.command | startswith("[ -x /usr/local/bin/lea ] && exec /usr/local/bin/lea hook claude PreToolUse;") and endswith("exit 2"))' "$f" >/dev/null
  done
  c=$(jq -r '.hooks.PreToolUse[0].hooks[0].command' "$I/managed-settings.json" | sed 's|/usr/local/bin/lea|/nonexistent/lea|g')
  run sh -c "$c"
  [ "$status" -eq 2 ]
  [[ "$output" == *"lea refuses the call"* ]]
}

@test "Containerfile installs the Locks' clients of consoles in other containers, from the set" {
  c="$I/Containerfile"
  grep -q 'useradd -r -u 10001 -U -M -s /sbin/nologin locksql' "$c"
  grep -q 'for t in locksql lockmq lockfile locklog; do' "$c"
  grep -q '"socket_dir   = \\"/run/$t\\""' "$c"
  grep -q 'a="${t}_${v}_linux_${TARGETARCH}.tar.gz"' "$c"
  # Each archive pinned by the set's sha256, for each architecture.
  grep -q 'sum=$(sed -n "s/^LOCKS_${T}_SHA256_${A}=' "$c"
  grep -q 'echo "$sum  $a" | sha256sum -c' "$c"
  grep -q 'amd64) A=AMD64 ;;' "$c"
  grep -q 'arm64) A=ARM64 ;;' "$c"
  grep -q 'install -d -m 2710 -o 10001 -g 10000 /run/locksql' "$c"
  # locklog's account and clients group, with the ids of locklog's image (10051, 10050), and its
  # socket directory as that image makes it (2750).
  grep -q 'groupadd -g 10050 locklog-clients && useradd -r -u 10051 -U -M -s /sbin/nologin locklog' "$c"
  grep -q 'install -d -m 2750 -o 10051 -g 10050 /run/locklog' "$c"
  ! grep -q 'LOCKSQL_REF\|COPY --from=locksql' "$c"
}

@test "the set's version and sha256 lines are the ones the Containerfile reads" {
  # The sed patterns of the Containerfile, run on the set: every client has its version and both
  # architectures' sha256.
  for t in LOCKSQL LOCKMQ LOCKFILE LOCKLOG; do
    v=$(sed -n "s/^LOCKS_${t}_VERSION=v\([0-9][0-9.]*\)\$/\1/p" "$LEA_ROOT/config/locks.lock")
    [ -n "$v" ]
    for A in AMD64 ARM64; do
      [ -n "$(sed -n "s/^LOCKS_${t}_SHA256_${A}=\([0-9a-f]\{64\}\)\$/\1/p" "$LEA_ROOT/config/locks.lock")" ]
    done
  done
}

# render_ssh_config OUT: the entrypoint's /run/lea/ssh_config block, run with its output in OUT.
render_ssh_config() {
  sed -n '/^{$/,/^} >\/run\/lea\/ssh_config$/p' "$I/entrypoint" | sed 's|>/run/lea/ssh_config$|>"$1"|' >"$BATS_TEST_TMPDIR/block.sh"
  grep -q 'Host %s' "$BATS_TEST_TMPDIR/block.sh"
  sh -c '. "$0"' "$BATS_TEST_TMPDIR/block.sh" "$1"
}
# check_endpoints: the entrypoint's LEA_GIT_SSH validation loop.
check_endpoints() {
  sed -n '/^for ep in \${LEA_GIT_SSH:-}; do$/,/^done$/p' "$I/entrypoint" >"$BATS_TEST_TMPDIR/check.sh"
  grep -q 'invalid LEA_GIT_SSH endpoint' "$BATS_TEST_TMPDIR/check.sh"
  sh "$BATS_TEST_TMPDIR/check.sh"
}

@test "the entrypoint writes one Host block per git SSH endpoint, on ports 2222, 2223..." {
  LEA_PROXY_IP=10.1.2.3 LEA_GIT_SSH='github.com:22 gitlab.example.org:2022' render_ssh_config "$BATS_TEST_TMPDIR/ssh_config"
  [ "$(grep -c '^Host ' "$BATS_TEST_TMPDIR/ssh_config")" -eq 2 ]
  run ssh -G -F "$BATS_TEST_TMPDIR/ssh_config" github.com
  [[ "$output" == *$'\nhostname 10.1.2.3\n'* ]]
  [[ "$output" == *$'\nport 2222\n'* ]]
  [[ "$output" == *$'\nhostkeyalias github.com\n'* ]]
  [[ "$output" == *$'\nuserknownhostsfile /run/lea/known_hosts\n'* ]]
  [[ "$output" == *$'\nstricthostkeychecking true\n'* ]]
  [[ "$output" == *$'\nidentityagent /run/lea/ssh-agent.sock\n'* ]]
  run ssh -G -F "$BATS_TEST_TMPDIR/ssh_config" gitlab.example.org
  [[ "$output" == *$'\nport 2223\n'* ]]
  [[ "$output" == *$'\nhostkeyalias gitlab.example.org\n'* ]]
  run ssh -G -F "$BATS_TEST_TMPDIR/ssh_config" other.example
  [[ "$output" == *$'\nport 22\n'* ]]
}

@test "without LEA_GIT_SSH the entrypoint writes an ssh config with no Host block" {
  LEA_PROXY_IP=10.1.2.3 LEA_GIT_SSH= render_ssh_config "$BATS_TEST_TMPDIR/ssh_config"
  [ -f "$BATS_TEST_TMPDIR/ssh_config" ]
  run grep -c '^Host ' "$BATS_TEST_TMPDIR/ssh_config"
  [ "$output" = 0 ]
}

@test "the entrypoint refuses a git SSH endpoint whose host is not a plain host name" {
  LEA_GIT_SSH='github.com:22 gitlab.com:22' check_endpoints
  for v in 'evil/x:22' ':22' 'a;b:22' 'x$y:22'; do
    LEA_GIT_SSH=$v run check_endpoints
    [ "$status" -eq 1 ]
    [[ "$output" == *"lea: invalid LEA_GIT_SSH endpoint"* ]]
  done
}

@test "the ssh client config includes the rendered file and both are made 0644, or ssh refuses them" {
  grep -qx 'Include /run/lea/ssh_config' "$I/ssh_config.tmpl"
  grep -q '^install -m 0644 /usr/local/share/lea/ssh_config.tmpl /etc/ssh/ssh_config.d/10-lea.conf$' "$I/entrypoint"
  grep -q '^chmod 0644 /run/lea/ssh_config$' "$I/entrypoint"
  run grep -q 'LEA_GITLAB' "$I/entrypoint" "$I/ssh_config.tmpl" "$I/containers.conf.tmpl" "$I/lea-session"
  [ "$status" -ne 0 ]
}

@test "the entrypoint adds the user to locksql-clients only with LEA_LOCKSQL=1" {
  grep -q '^if \[ "${LEA_LOCKSQL:-0}" = 1 \]; then usermod -aG locksql-clients "$user"; fi$' "$I/entrypoint"
  [ "$(grep -c 'locksql-clients' "$I/entrypoint")" -eq 1 ]
}

@test "the session exports no GitLab token" {
  run grep -q 'GITLAB_TOKEN\|gitlab/token' "$I/lea-session"
  [ "$status" -ne 0 ]
}

# The base image's BUILDAH_ISOLATION=chroot remounts /dev read-only, which lea's container may not:
# every RUN step of a docker build failed. Rootless isolation builds in a nested user namespace.
@test "Containerfile builds images with rootless buildah isolation" {
  grep -q '^ENV BUILDAH_ISOLATION=rootless$' "$I/Containerfile"
}

@test "Containerfile installs gh with its config in the state, and git credentials in the state" {
  c="$I/Containerfile"
  grep -q ' gh glab ' "$c"
  grep -q 'GH_CONFIG_DIR=/lea/state/gh' "$c"
  grep -q "git config --system credential.helper 'store --file=/lea/state/git/credentials'" "$c"
}

@test "no organisation host is baked into the image" {
  run grep -rn 'gitlab\.' "$I/Containerfile" "$I/containers.conf.tmpl" "$I/ssh_config.tmpl" "$I/entrypoint"
  [ "$status" -ne 0 ]
}

@test "Containerfile sets SSH_AUTH_SOCK for every exec session" {
  grep -q '^ENV SSH_AUTH_SOCK=/run/lea/ssh-agent.sock' "$I/Containerfile"
}

@test "user-init clears ready first and waits for the podman socket before ready" {
  u="$I/user-init"
  [ "$(grep -n 'rm -f /run/lea/ready' "$u" | head -1 | cut -d: -f1)" -lt "$(grep -n 'touch /run/lea/ready' "$u" | cut -d: -f1)" ]
  grep -q 'podman.sock" \]' "$u"
  [ "$(grep -n 'podman.sock" \]' "$u" | cut -d: -f1)" -lt "$(grep -n 'touch /run/lea/ready' "$u" | cut -d: -f1)" ]
}

@test "user-init registers lea mcp best-effort, only when absent, before ready" {
  u="$I/user-init"
  grep -q 'jq -e .\.mcpServers\.lea\.command == "/usr/local/bin/lea"' "$u"
  grep -q 'could not register the lea MCP server' "$u"
  ! grep -q 'mv ' "$u" || false
  [ "$(grep -n 'mcpServers.lea = ' "$u" | cut -d: -f1)" -lt "$(grep -n 'touch /run/lea/ready' "$u" | cut -d: -f1)" ]
}

@test "entrypoint refuses uid 0 and gid 0" {
  grep -q 'LEA_UID" -ne 0' "$I/entrypoint"
  grep -q 'LEA_GID" -ne 0' "$I/entrypoint"
}

lea_sha256_file() { sha256sum "$1" | cut -d' ' -f1; }

@test "containers.conf keeps the base image's rootless-in-container settings" {
  t="$I/containers.conf.tmpl"
  grep -q '^default_sysctls = \[\]$' "$t"
  grep -q '"/proc:/proc"' "$t"
  grep -q '"/run/lea:/run/lea:ro"' "$t"
  grep -q '^cgroups = "disabled"$' "$t"
  grep -q '^log_driver = "k8s-file"$' "$t"
  grep -q '^\[engine\]$' "$t"
  grep -q '^cgroup_manager = "cgroupfs"$' "$t"
  grep -q '^events_logger = "file"$' "$t"
  grep -q '^runtime = "crun"$' "$t"
}

# utsns is the one host namespace: a nested container cannot set its own hostname (no
# CAP_SYS_ADMIN in lea's bounding set), every other namespace stays its own.
@test "containers.conf has a single volumes key and no host namespaces for nested containers but uts" {
  t="$I/containers.conf.tmpl"
  [ "$(grep -c '^volumes' "$t")" -eq 1 ]
  run grep -E '^(netns|userns|ipcns|cgroupns|pidns) *= *"host"' "$t"
  [ "$status" -eq 1 ]
  grep -qx 'utsns = "host"' "$t"
}

@test "entrypoint falls back to u<uid> for an unusable user name" {
  e="$I/entrypoint"
  grep -q 'LEA_USER=u\$LEA_UID' "$e"
  grep -q '\[!abcdefghijklmnopqrstuvwxyz0123456789_-\]' "$e"
}

@test "the image's wl-copy only writes into the clipboard FIFO" {
  w="$I/wl-copy"
  [ -x "$w" ]
  grep -q '^f=/lea/clipboard/copy$' "$w"
  grep -q 'COPY image/lea-session image/lea-agent image/wl-copy /usr/local/bin/' "$I/Containerfile"
}

@test "Containerfile installs abduco and lea-agent" {
  c="$I/Containerfile"
  grep -Eq '^ +.*[[:space:]]abduco([[:space:]]|$)' "$c"
  grep -q '^COPY image/lea-session image/lea-agent image/wl-copy /usr/local/bin/$' "$c"
  grep -q 'chmod 0755 .*/usr/local/bin/lea-agent' "$c"
}

# lea's hooks are in the managed settings (#111), which an agent's own writable settings cannot
# undo, and in the --settings file too, which keeps them on a host whose organisation has
# server-managed settings (Claude Code then skips the file-based ones): the same commands, which
# Claude Code runs once (TestClaudeManagedAndFlagHooksRunOnce).
@test "lea's Claude hooks are in the managed settings and the --settings file, identical" {
  s="$I/claude-settings.json" m="$I/managed-settings.json"
  for e in SessionStart UserPromptSubmit PreToolUse PostToolUse PermissionRequest Notification SubagentStart SubagentStop Stop StopFailure SessionEnd; do
    jq -e --arg e "$e" '.hooks[$e][0].hooks[0].command | contains("/usr/local/bin/lea hook claude " + $e)' "$s" >/dev/null
  done
  [ "$(jq -r '.hooks.PermissionRequest[0].hooks[0].timeout' "$s")" = 620 ]
  # The usage-limit resume job (#20) runs in the background and wakes the model on exit 2.
  jq -e '.hooks.StopFailure[0].hooks[1] | (.command | contains("/usr/local/bin/lea hook claude LimitWait")) and .asyncRewake == true and .timeout == 691200' "$s" >/dev/null
  # Its SessionStart twin (#83) takes a pending resume over after a container restart.
  jq -e '.hooks.SessionStart[0].hooks[1] | (.command | contains("/usr/local/bin/lea hook claude LimitResume")) and .asyncRewake == true and .timeout == 691200' "$s" >/dev/null
  # A Lock call's own rewake job (#161): no Stop follows a turn suspended on a pending decision.
  jq -e '.hooks.PostToolUse[1] | .matcher == "^mcp__lock[a-z]*__" and (.hooks[0] | (.command | contains("/usr/local/bin/lea hook claude Rewake")) and .asyncRewake == true and .timeout == 691200)' "$s" >/dev/null
  # The inbox rewake job (#297) at each turn end and each session start: it renews its watch for
  # days, so its timeout is LimitWait's.
  jq -e '.hooks.Stop[0].hooks[1] | (.command | contains("/usr/local/bin/lea hook claude Rewake")) and .asyncRewake == true and .timeout == 691200' "$s" >/dev/null
  jq -e '.hooks.SessionStart[0].hooks[2] | (.command | contains("/usr/local/bin/lea hook claude Rewake")) and .asyncRewake == true and .timeout == 691200' "$s" >/dev/null
  jq -e --slurpfile f "$s" '.hooks == $f[0].hooks' "$m" >/dev/null
  [ "$(jq -r '.disableAllHooks // .allowManagedHooksOnly // "none"' "$m")" = none ]
  grep -q '^COPY image/claude-settings.json /usr/local/share/lea/claude-settings.json$' "$I/Containerfile"
  grep -q '^COPY image/managed-settings.json /etc/claude-code/managed-settings.json$' "$I/Containerfile"
  grep -q 'settings=${LEA_CLAUDE_SETTINGS:-/usr/local/share/lea/claude-settings.json}' "$I/lea-session"
}

# managed_check: image/managed-check on copies of the repository's files, owned by the test's
# user, with a claude that reports VERSION (default: the Containerfile's pin).
managed_check() {
  d=$BATS_TEST_TMPDIR/mc
  mkdir -p "$d/etc/claude-code" "$d/bin"
  [ -f "$d/etc/claude-code/managed-settings.json" ] || cp "$I/managed-settings.json" "$d/etc/claude-code/managed-settings.json"
  chmod 0755 "$d/etc/claude-code"
  pin=$(sed -n 's/^ARG CLAUDE_CODE_VERSION=//p' "$I/Containerfile")
  echo "$pin" >"$d/pin"
  printf '#!/bin/sh
echo "%s (Claude Code)"
' "${VERSION:-$pin}" >"$d/bin/claude"
  chmod +x "$d/bin/claude"
  PATH="$d/bin:$PATH" LEA_MANAGED_SETTINGS="$d/etc/claude-code/managed-settings.json" LEA_CLAUDE_SETTINGS="${LEA_CLAUDE_SETTINGS_OVERRIDE:-$I/claude-settings.json}" \
    LEA_CLAUDE_PIN="$d/pin" LEA_MANAGED_OWNER=$(id -u) run "$I/managed-check"
}

@test "managed-check confirms lea's hooks in the managed settings, with the verified Claude Code (#111)" {
  [ -x "$I/managed-check" ]
  grep -q '^COPY image/lea-crun image/managed-check /usr/local/libexec/lea/$' "$I/Containerfile"
  grep -q 'echo "${CLAUDE_CODE_VERSION}" >/usr/local/share/lea/claude-code-version' "$I/Containerfile"
  managed_check
  [ "$status" -eq 0 ]
  [[ "$output" == "lea's hooks and managed-only permission rules in "*", Claude Code $(sed -n 's/^ARG CLAUDE_CODE_VERSION=//p' "$I/Containerfile")" ]]
}

@test "managed-check fails closed: a hook missing, other hooks, hooks off, a writable file, a drop-in, another Claude Code" {
  m=$BATS_TEST_TMPDIR/mc/etc/claude-code/managed-settings.json
  mkdir -p "${m%/*}"
  jq 'del(.hooks.PreToolUse)' "$I/managed-settings.json" >"$m"
  managed_check
  [ "$status" -eq 1 ]; [[ "$output" == *"lacks lea's PreToolUse hook"* ]]
  jq '.hooks.Stop[0].hooks[0].command += " "' "$I/managed-settings.json" >"$m"
  managed_check
  [ "$status" -eq 1 ]; [[ "$output" == *"do not carry the same hooks"* ]]
  jq '.disableAllHooks = true' "$I/managed-settings.json" >"$m"
  managed_check
  [ "$status" -eq 1 ]; [[ "$output" == *"turns hooks off"* ]]
  cp "$I/managed-settings.json" "$m"; chmod 0666 "$m"
  managed_check
  [ "$status" -eq 1 ]; [[ "$output" == *"is writable by others than root"* ]]
  chmod 0644 "$m"; mkdir "${m%/*}/managed-settings.d"
  managed_check
  [ "$status" -eq 1 ]; [[ "$output" == *"managed-settings.d exists"* ]]
  rmdir "${m%/*}/managed-settings.d"
  VERSION=0.0.1 managed_check
  [ "$status" -eq 1 ]; [[ "$output" == *"Claude Code 0.0.1 is not "*"the version lea verified"* ]]
}

# A guard that fails open, permission rules an agent's settings can widen, a hook env it can
# redirect: each one fails the check (#111). Both files change alike, so the hooks stay identical.
@test "managed-check fails closed: a guard that fails open, rules not managed only, bypass on, an unpinned env" {
  m=$BATS_TEST_TMPDIR/mc/etc/claude-code/managed-settings.json
  mkdir -p "${m%/*}"
  s=$BATS_TEST_TMPDIR/claude-settings.json
  for edit in \
    '.hooks.PreToolUse[0].hooks[0].command |= sub("exit 2$"; "exit 0")@@does not fail closed' \
    'del(.hooks.PreToolUse[0].hooks[0].onFailure)@@does not fail closed' \
    '.allowManagedPermissionRulesOnly = false@@managed only' \
    'del(.allowManagedPermissionRulesOnly)@@managed only' \
    'del(.permissions.disableBypassPermissionsMode)@@managed only' \
    '.permissions.defaultMode = "bypassPermissions"@@managed only' \
    'del(.env.LEA_BUS_SOCK)@@does not pin LEA_BUS_SOCK=/run/lea/bus/bus.sock' \
    '.env.GH_CONFIG_DIR = "/tmp/gh"@@does not pin GH_CONFIG_DIR=/lea/state/gh'; do
    jq "${edit%%@@*}" "$I/managed-settings.json" >"$m"
    jq --slurpfile m "$m" '.hooks = $m[0].hooks' "$I/claude-settings.json" >"$s"
    LEA_CLAUDE_SETTINGS_OVERRIDE="$s" managed_check
    [ "$status" -eq 1 ]
    [[ "$output" == *"${edit#*@@}"* ]] || { echo "$edit: $output"; false; }
  done
}

# The entrypoint's managed-settings block: the image's file, then the host's rules (#111).
merge_permissions() {
  r=$BATS_TEST_TMPDIR/root
  mkdir -p "$r/etc/claude-code" "$r/etc/lea" "$r/usr/local/share/lea"
  cp "$I/managed-settings.json" "$r/usr/local/share/lea/managed-settings.json"
  sed -n '/^# The permission rules are the managed settings/,/^if \[ "\${LEA_LOCKHTTP:-0}" = 1 \]; then$/p' "$I/entrypoint" | sed '$d' |
    sed -e "s|/etc/|$r/etc/|g" -e "s|/usr/local/share/|$r/usr/local/share/|g" >"$BATS_TEST_TMPDIR/merge.sh"
  grep -q 'allowManagedPermissionRulesOnly' "$BATS_TEST_TMPDIR/merge.sh"
  run sh -eu "$BATS_TEST_TMPDIR/merge.sh"
  M=$r/etc/claude-code/managed-settings.json
}

@test "the entrypoint adds the host's permission rules to the managed ones, once, never bypass (#111)" {
  merge_permissions
  [ "$status" -eq 0 ]
  # No host file: the image's file as it is.
  [ "$(jq -cS . "$M")" = "$(jq -cS . "$I/managed-settings.json")" ]
  printf '%s\n' '{"allow":["Bash(npm test)",3,""],"ask":["Bash(git push *)"],"deny":["Read(./.env)"],"additionalDirectories":["/srv"],"defaultMode":"bypassPermissions"}' \
    >"$r/etc/lea/claude-permissions.json"
  merge_permissions
  [ "$status" -eq 0 ]
  [ "$(jq -c .permissions.allow "$M")" = '["Bash(go *)","Bash(go-cross *)","Bash(staticcheck *)","Bash(govulncheck *)","Bash(npm test)"]' ]
  [ "$(jq -c .permissions.ask "$M")" = '["Bash(git push *)"]' ]
  [ "$(jq -c .permissions.deny "$M")" = '["Read(./.env)"]' ]
  [ "$(jq -c .permissions.additionalDirectories "$M")" = '["/srv"]' ]
  [ "$(jq -r .permissions.defaultMode "$M")" = null ]
  [ "$(jq -r .permissions.disableBypassPermissionsMode "$M")" = disable ]
  [ "$(jq -r .allowManagedPermissionRulesOnly "$M")" = true ]
  jq -e --slurpfile f "$I/managed-settings.json" '.hooks == $f[0].hooks and .env == $f[0].env' "$M" >/dev/null
  # A restart begins again from the image's file: nothing twice.
  merge_permissions
  [ "$(jq '.permissions.allow | length' "$M")" = 5 ]
  # A mode lea accepts is the host's.
  printf '%s\n' '{"defaultMode":"acceptEdits"}' >"$r/etc/lea/claude-permissions.json"
  merge_permissions
  [ "$(jq -r .permissions.defaultMode "$M")" = acceptEdits ]
  # A file jq cannot read leaves lea's rules alone, and says so.
  printf 'not json\n' >"$r/etc/lea/claude-permissions.json"
  merge_permissions
  [ "$status" -eq 0 ]
  [[ "$output" == *"could not be read; lea's own apply"* ]]
  [ "$(jq -cS . "$M")" = "$(jq -cS . "$I/managed-settings.json")" ]
}

# The agents start in the mode a local Claude Code starts in (#333): Claude Code's own default
# (auto on the accounts that have it) when the host's settings.json sets no default mode, or one
# lea does not accept; a mode lea accepts is the host's.
@test "the managed settings set no default mode unless the host sets a valid one (#333)" {
  merge_permissions
  [ "$status" -eq 0 ]
  [ "$(jq -r .permissions.defaultMode "$M")" = null ]
  for h in '{}' '{"allow":["Bash(ls)"]}' '{"defaultMode":"bypassPermissions"}' '{"defaultMode":"yolo"}' \
    '{"defaultMode":7}' '{"defaultMode":""}' '[]'; do
    printf '%s\n' "$h" >"$r/etc/lea/claude-permissions.json"
    merge_permissions
    [ "$status" -eq 0 ]
    [ "$(jq -r .permissions.defaultMode "$M")" = null ] || { echo "$h: $(jq -r .permissions.defaultMode "$M")"; false; }
  done
  for mode in default acceptEdits plan dontAsk auto; do
    printf '{"defaultMode":"%s"}\n' "$mode" >"$r/etc/lea/claude-permissions.json"
    merge_permissions
    [ "$(jq -r .permissions.defaultMode "$M")" = "$mode" ]
  done
}

# lea config claude (#334): its removals take the image's rules out too, an entry is there once,
# and its env and preferences join the managed settings without replacing a key the image sets.
@test "the entrypoint applies lea config claude's settings, never over lea's own keys (#334)" {
  merge_permissions
  printf '%s\n' '{"allow":["Bash(make *)","Bash(ls)","Bash(go *)"],"deny":["Read(./.env)","Read(./.env)"],
    "remove":{"allow":["Bash(go *)","Bash(staticcheck *)"],"deny":7},"defaultMode":"auto",
    "env":{"MY_TOOL_HOME":"/opt/tool","LEA_BUS_SOCK":"/tmp/elsewhere","N":3},
    "settings":{"model":"opus","alwaysThinkingEnabled":true,"hooks":{},"permissions":{},"disableAllHooks":true,
      "allowManagedHooksOnly":true,"statusLine":"x","allowManagedPermissionRulesOnly":false,"x":{"y":1}}}' \
    >"$r/etc/lea/claude-permissions.json"
  merge_permissions
  [ "$status" -eq 0 ]
  [ "$(jq -c .permissions.allow "$M")" = '["Bash(go-cross *)","Bash(govulncheck *)","Bash(make *)","Bash(ls)"]' ]
  [ "$(jq -c .permissions.deny "$M")" = '["Read(./.env)"]' ]
  [ "$(jq -r .permissions.defaultMode "$M")" = auto ]
  [ "$(jq -r .env.MY_TOOL_HOME "$M")" = /opt/tool ]
  [ "$(jq -r '.env.N // "unset"' "$M")" = unset ]
  [ "$(jq -r .model "$M")" = opus ]
  [ "$(jq -r .alwaysThinkingEnabled "$M")" = true ]
  for k in disableAllHooks allowManagedHooksOnly statusLine x; do
    [ "$(jq -r --arg k "$k" '.[$k] // "unset"' "$M")" = unset ] || { echo "$k: $(jq -c --arg k "$k" '.[$k]' "$M")"; false; }
  done
  [ "$(jq -r .allowManagedPermissionRulesOnly "$M")" = true ]
  [ "$(jq -r .permissions.disableBypassPermissionsMode "$M")" = disable ]
  jq -e --slurpfile f "$I/managed-settings.json" '.hooks == $f[0].hooks and (.env | del(.MY_TOOL_HOME)) == $f[0].env' "$M" >/dev/null
  # The result still passes lea doctor's check of the managed settings.
  mkdir -p "$BATS_TEST_TMPDIR/mc/etc/claude-code"
  cp "$M" "$BATS_TEST_TMPDIR/mc/etc/claude-code/managed-settings.json"
  managed_check
  [ "$status" -eq 0 ] || { echo "$output"; false; }
}

@test "the entrypoint keeps the managed settings root's, with no drop-in directory (#111)" {
  grep -q '^chown -R 0:0 /etc/claude-code$' "$I/entrypoint"
  grep -q '^chmod 0644 /etc/claude-code/managed-settings.json$' "$I/entrypoint"
  grep -q '^rm -rf /etc/claude-code/managed-settings.d$' "$I/entrypoint"
}

# lea's Claude plugin comes with the image: a manifest named lea and the control-tower skill.
@test "the image ships lea's Claude plugin with the control-tower skill" {
  p="$I/claude-plugin"
  [ "$(jq -r .name "$p/.claude-plugin/plugin.json")" = lea ]
  k="$p/skills/control-tower/SKILL.md"
  [ "$(sed -n 1p "$k")" = --- ]
  grep -q '^name: control-tower$' "$k"
  grep -q '^name: clear-ct$' "$p/skills/clear-ct/SKILL.md"
  grep -q '^description: Use when ' "$k"
  for t in agents_spawn agents_list board_read msg_inbox msg_reply task_update "lea inbox --agent --wait"; do grep -q "$t" "$k"; done
  grep -q '^COPY image/claude-plugin /usr/local/share/lea/claude-plugin$' "$I/Containerfile"
  grep -q 'chmod -R u=rwX,go=rX /usr/local/share/lea/claude-plugin' "$I/Containerfile"
  grep -q 'plugin=${LEA_CLAUDE_PLUGIN:-/usr/local/share/lea/claude-plugin}' "$I/lea-session"
}

# Server-managed settings replace the file-based managed settings, env included: lea's telemetry
# env also comes in through the --settings file, which keeps it on such hosts.
@test "lea's telemetry env is in the --settings file too, with the version filled in" {
  s="$I/claude-settings.json"
  [ "$(jq -cS .env "$s")" = "$(jq -cS .env "$I/managed-settings.json")" ]
  [ "$(jq -r .env.OTEL_EXPORTER_OTLP_ENDPOINT "$s")" = "http://lea-proxy:4318" ]
  [ "$(jq -r '.env.OTEL_LOG_USER_PROMPTS // "unset"' "$s")" = unset ]
  grep -q 's/@LEA_VERSION@/${LEA_VERSION}/" /etc/claude-code/managed-settings.json /usr/local/share/lea/managed-settings.json /usr/local/share/lea/claude-settings.json' "$I/Containerfile"
}

@test "lea's Claude settings run lea statusline as the status line, guarded on the core binary" {
  s="$I/claude-settings.json"
  jq -e '.statusLine.type == "command"' "$s" >/dev/null
  jq -e '.statusLine.command | startswith("[ -x /usr/local/bin/lea ]") and contains("/usr/local/bin/lea statusline") and endswith("exit 0")' "$s" >/dev/null
}

# Moved from the retired core.bats.
@test "lea's Claude settings register every hook guarded on the core binary, permission timeout above the approval maximum" {
  f="$LEA_ROOT/image/claude-settings.json"
  for e in SessionStart UserPromptSubmit PostToolUse PermissionRequest Notification SubagentStart SubagentStop Stop SessionEnd; do
    jq -e --arg e "$e" '.hooks[$e][0].hooks[0].command | contains("lea hook claude " + $e) and startswith("[ -x /usr/local/bin/lea ]") and endswith("exit 0")' "$f" >/dev/null
  done
  # PreToolUse is guarded too, and fails closed (exit 2) without the core binary.
  jq -e '.hooks.PreToolUse[0].hooks[0].command | contains("lea hook claude PreToolUse") and startswith("[ -x /usr/local/bin/lea ]") and endswith("exit 2")' "$f" >/dev/null
  jq -e '.hooks.PermissionRequest[0].hooks[0].timeout >= 610' "$f" >/dev/null
}

# reset_runtime ROOT: the entrypoint's runtime-dir block, with /run under ROOT.
reset_runtime() {
  sed -n '/^# Runtime state of a previous boot/,/^install -d -m 0755 -o "\$LEA_UID" -g "\$LEA_GID" \/run\/lea$/p' "$I/entrypoint" |
    sed "s|/run/|$1/run/|g" >"$BATS_TEST_TMPDIR/runtime.sh"
  grep -q 'rm -rf' "$BATS_TEST_TMPDIR/runtime.sh"
  LEA_UID=$(id -u) LEA_GID=$(id -g) sh -eu "$BATS_TEST_TMPDIR/runtime.sh"
}

@test "a docker start after a reboot clears the previous boot's Podman runtime state (#97)" {
  r=$BATS_TEST_TMPDIR/root u=$(id -u)
  # What a crash leaves in the container's writable layer: Podman's cached boot ID, its tmp dir,
  # a socket path the new service's wait would take for its own, stale agent and shell pid files.
  mkdir -p "$r/run/user/$u/containers" "$r/run/user/$u/libpod/tmp" "$r/run/user/$u/podman" "$r/run/lea/agents" "$r/run/lea/shells" "$r/run/lea/bus"
  echo stale-boot-id >"$r/run/user/$u/libpod/tmp/alive"
  touch "$r/run/user/$u/podman/podman.sock" "$r/run/lea/agents/dev-1.pid" "$r/run/lea/shells/sh-1.pid" "$r/run/lea/ready" "$r/run/lea/bus/keep"
  reset_runtime "$r"
  [ -d "$r/run/user/$u" ]
  [ "$(stat -c %a "$r/run/user/$u")" = 700 ]
  [ -z "$(ls -A "$r/run/user/$u")" ]
  [ ! -e "$r/run/lea/agents" ]
  [ ! -e "$r/run/lea/shells" ]
  [ ! -e "$r/run/lea/ready" ]
  # The bus directory is the daemon's bind mount: never touched.
  [ -f "$r/run/lea/bus/keep" ]
}

# lea__verify_image: a pulled release image is checked by digest against lea's release workflow.
verify_image() { lea__verify_image "$@"; }
REL=ghcr.io/davidgodefroid/lea:0.5.0
DIGEST=ghcr.io/davidgodefroid/lea@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef

@test "lea__verify_image checks the digest against the release workflow at that tag" {
  FAKE_DOCKER_OUT_IMAGE_INSPECT_FORMAT=$DIGEST run verify_image "$REL"
  [ "$status" -eq 0 ]
  [[ "$output" == *"lea: image $DIGEST: signature verified"* ]]
  calls | grep -qF "docker [image] [inspect] [--format] [{{index .RepoDigests 0}}] [$REL]"
  calls | grep -qF "[--certificate-oidc-issuer] [https://token.actions.githubusercontent.com]"
  calls | grep -qF '[--certificate-identity-regexp] [^https://github\.com/DavidGodefroid/lea/\.github/workflows/release\.yml@refs/tags/v0\.5\.0$]'
  calls | grep -qF "[$DIGEST]"
  ! calls | grep -qF '[image] [rm]'
}

@test "lea__verify_image removes an image whose signature does not verify and dies" {
  FAKE_COSIGN_RC=1 FAKE_DOCKER_OUT_IMAGE_INSPECT_FORMAT=$DIGEST run verify_image "$REL"
  [ "$status" -eq 1 ]
  [[ "$output" == *"lea: error: image $REL is not signed by lea's release v0.5.0: removed; check your network or registry, then run lea again"* ]]
  calls | grep -qF "docker [image] [rm] [$REL]"
}

@test "lea__verify_image refuses an image without a registry digest" {
  FAKE_DOCKER_RC_IMAGE_INSPECT_FORMAT=1 run verify_image "$REL"
  [ "$status" -eq 1 ]
  ! calls | grep -q '^cosign'
  calls | grep -qF "docker [image] [rm] [$REL]"
}

@test "lea__verify_image skips a locally built :dev image" {
  run verify_image lea:dev
  [ "$status" -eq 0 ]
  [ -z "$(calls)" ]
  run verify_image lea-proxy:dev
  [ "$status" -eq 0 ]
  [ -z "$(calls)" ]
}

# no_cosign_on_path: PATH with the fakes but no cosign (skips when the test image ships one).
no_cosign_on_path() {
  d=$BATS_TEST_TMPDIR/nocosign
  mkdir -p "$d"
  for f in "$LEA_ROOT"/tests/fakes/*; do [ "${f##*/}" = cosign ] || ln -s "$f" "$d/"; done
  PATH="$d:/usr/bin:/bin"
  if command -v cosign >/dev/null 2>&1; then skip "cosign is installed in the test image"; fi
}

@test "lea__verify_image without cosign and without a pinned one says how to install it, removes nothing" {
  no_cosign_on_path
  lea__cosign_pin() { return 1; }
  run verify_image "$REL"
  [ "$status" -eq 1 ]
  [[ "$output" == *"lea: error: cosign not found: brew install cosign (lea verifies its images)"* ]]
  ! calls | grep -qF '[image] [rm]'
}

@test "lea__verify_image uses lea's pinned cosign when none is on PATH, and cleans it up" {
  no_cosign_on_path
  mkdir -p "$BATS_TEST_TMPDIR/rel"
  cp "$LEA_ROOT/tests/fakes/cosign" "$BATS_TEST_TMPDIR/rel/cosign-linux-amd64"
  pin=$(sha256sum "$BATS_TEST_TMPDIR/rel/cosign-linux-amd64" | cut -d' ' -f1)
  eval "lea__cosign_pin() { [ \"\$1\" = linux-amd64 ] && echo $pin; }"
  uname() { case $1 in -s) echo Linux ;; -m) echo x86_64 ;; esac; }
  export FAKE_CURL_DIR="$BATS_TEST_TMPDIR/rel" TMPDIR="$BATS_TEST_TMPDIR/tmp"
  mkdir -p "$TMPDIR"
  FAKE_DOCKER_OUT_IMAGE_INSPECT_FORMAT=$DIGEST run verify_image "$REL"
  [ "$status" -eq 0 ]
  [[ "$output" == *"signature verified"* ]]
  calls | grep -qF "[https://github.com/sigstore/cosign/releases/download/v3.0.6/cosign-linux-amd64]"
  [ -z "$(ls -A "$TMPDIR")" ]
}

@test "lea__verify_image says so when the unverified image cannot be removed" {
  FAKE_COSIGN_RC=1 FAKE_DOCKER_RC_IMAGE_RM=1 FAKE_DOCKER_OUT_IMAGE_INSPECT_FORMAT=$DIGEST run verify_image "$REL"
  [ "$status" -eq 1 ]
  [[ "$output" == *"is not signed by lea's release v0.5.0"*"; could not remove it: docker image rm $REL"* ]]
}

@test "lea__verify_image checks a tag that is not a release against any release tag" {
  L=ghcr.io/davidgodefroid/lea:latest
  FAKE_DOCKER_OUT_IMAGE_INSPECT_FORMAT=$DIGEST run verify_image "$L"
  [ "$status" -eq 0 ]
  calls | grep -qF '@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+$]'
  FAKE_COSIGN_RC=1 FAKE_DOCKER_OUT_IMAGE_INSPECT_FORMAT=$DIGEST run verify_image "$L"
  [ "$status" -eq 1 ]
  [[ "$output" == *"image $L is not signed by lea's release a release: removed"* ]]
}

@test "lea__ensure_image without any cosign pulls nothing" {
  no_cosign_on_path
  lea__cosign_pin() { return 1; }
  FAKE_DOCKER_RC_IMAGE_INSPECT=1 run lea__ensure_image lea "$REL"
  [ "$status" -eq 1 ]
  ! calls | grep -qF 'docker [pull]'
  ! calls | grep -qF '[image] [rm]'
}

@test "lea__ensure_image verifies a pulled image and builds nothing for it" {
  FAKE_DOCKER_RC_IMAGE_INSPECT=1 run lea__ensure_image lea "$REL"
  [ "$status" -eq 0 ]
  calls | grep -qF "docker [pull] [$REL]"
  calls | grep -qF "[ghcr.io/davidgodefroid/lea@sha256:"
  ! calls | grep -q 'docker \[build\]'
}
