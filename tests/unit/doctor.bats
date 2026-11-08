load helpers
setup() {
  setup_fakes; load_lea
  S=$(lea_state_dir); mkdir -p "$S"
  : >"$S/claude.json"
  P="$BATS_TEST_TMPDIR/projects"; mkdir -p "$P"; export LEA_PROJECTS_DIR="$P"
  lea_user_config_set LEA_PROJECTS_DIR "$P"
  locksql_on
}

@test "healthy workstation: no FAIL, warnings for optional parts" {
  fake_tmux
  export FAKE_DOCKER_OUT_INSPECT__F=true
  export FAKE_DOCKER_OUT_NETWORK_LS=lea-internal-abc
  export FAKE_DOCKER_OUT_NETWORK_INSPECT='true isolated' 
  export LEA_INTERNAL_HOSTS="$BATS_TEST_TMPDIR/internal-hosts"
  printf 'gitlab.com 443\n' >"$LEA_INTERNAL_HOSTS"
  run lea_doctor
  [ "$status" -eq 0 ]
  [[ "$output" != *FAIL* ]]
  [[ "$output" == *"ok    proxy running"* ]]
  [[ "$output" == *"ok    resolves gitlab.com"* ]]
  [[ "$output" == *"ok    telemetry: off (LEA_OTEL_UPSTREAM)"* ]]
  [[ "$output" != *"telemetry upstream"* ]]
  [[ "$output" == *"warn  kubernetes token"* ]]
}

@test "unreachable Docker is a failure" {
  export FAKE_DOCKER_RC_INFO_=1
  run lea_doctor
  [ "$status" -eq 1 ]
  [[ "$output" == *"FAIL  docker reachable"* ]]
}

@test "missing init state is a failure" {
  rm "$S/claude.json"
  run lea_doctor
  [ "$status" -eq 1 ]
  [[ "$output" == *"FAIL  state claude.json"* ]]
}

@test "without LEA_GIT_SSH the SSH key and known_hosts are not required, and there is no token" {
  run lea_doctor
  [[ "$output" == *"ok    state claude.json"* ]]
  [[ "$output" != *"state ssh/"* ]]
  [[ "$output" != *"gitlab/token"* ]]
}

@test "with LEA_GIT_SSH the SSH key and known_hosts are required" {
  export LEA_GIT_SSH=github.com:22
  run lea_doctor
  [ "$status" -eq 1 ]
  [[ "$output" == *"FAIL  state ssh/id_ed25519"* ]]
  [[ "$output" == *"FAIL  state ssh/known_hosts"* ]]
  mkdir -p "$S/ssh"; : >"$S/ssh/id_ed25519"; : >"$S/ssh/known_hosts"
  run lea_doctor
  [[ "$output" == *"ok    state ssh/id_ed25519"* ]]
  [[ "$output" == *"ok    state ssh/known_hosts"* ]]
}

@test "locksql: lox run's lock is checked with LEA_LOCKSQL=1, never with 0, and no image of lea's" {
  export FAKE_DOCKER_RC_VOLUME_INSPECT=1
  run lea_doctor
  [[ "$output" != *"locksql image"* ]]
  [[ "$output" == *"warn  locksql lock: no socket volume of a locksql lock of lox run for $LEA_PROJECTS_DIR"*"no such lock yet: \`lox add locksql\`"* ]]
  [[ "$output" != *"locksql-run"* ]]
  grep -qF "docker [volume] [ls] [--filter] [label=dev.lockfamily.role=socket] [--filter] [label=dev.lockfamily.type=locksql] [--filter] [label=dev.lockfamily.workspace=$LEA_PROJECTS_DIR] [--format] [{{.Name}}]" "$FAKE_LOG"
  export FAKE_DOCKER_OUT_VOLUME_LS=lockfamily-1a2b3c4d_crm-run
  run lea_doctor
  [[ "$output" == *"ok    locksql lock of lox run (socket volume lockfamily-1a2b3c4d_crm-run)"* ]]
  export FAKE_DOCKER_OUT_VOLUME_LS='lockfamily-1a2b3c4d_a-run
lockfamily-1a2b3c4d_b-run'
  run lea_doctor
  [[ "$output" == *"ok    2 locksql locks of lox run (lea's agents reach each on /run/locksql/<instance>)"* ]]
  LEA_LOCKSQL=0 run lea_doctor
  [[ "$output" != *locksql* ]]
}

@test "locksql: a locksql-run volume of an earlier lea's standalone console is reported, never mounted" {
  export FAKE_DOCKER_OUT_VOLUME_LS=lockfamily-1a2b3c4d_crm-run
  run lea_doctor
  [[ "$output" == *"warn  locksql-run volume: left by a standalone console of an earlier lea"* ]]
  export FAKE_DOCKER_RC_VOLUME_INSPECT_AT_LOCKSQL_RUN=1
  run lea_doctor
  [[ "$output" != *"locksql-run"* ]]
}

@test "a non-isolated internal network is a failure (#105)" {
  fake_tmux
  export FAKE_DOCKER_OUT_NETWORK_LS=lea-internal-abc
  export FAKE_DOCKER_OUT_NETWORK_INSPECT='true nat'
  run lea_doctor
  [ "$status" -eq 1 ]
  [[ "$output" == *"FAIL  lea-internal-abc gateway_mode_ipv4=nat: not isolated"*"LEA_ALLOW_GATEWAY=1"* ]]
  export FAKE_DOCKER_OUT_NETWORK_INSPECT='true <no value>'
  run lea_doctor
  [ "$status" -eq 1 ]
  [[ "$output" == *"FAIL  lea-internal-abc gateway_mode_ipv4=unset: not isolated"* ]]
}

@test "a non-isolated internal network is a warning with LEA_ALLOW_GATEWAY=1" {
  fake_tmux
  export FAKE_DOCKER_OUT_NETWORK_LS=lea-internal-abc
  export FAKE_DOCKER_OUT_NETWORK_INSPECT='true nat'
  export LEA_ALLOW_GATEWAY=1
  run lea_doctor
  [ "$status" -eq 0 ]
  [[ "$output" == *"warn  lea-internal-abc gateway_mode_ipv4=nat: not isolated"*"(LEA_ALLOW_GATEWAY=1)"* ]]
  [[ "$output" != *FAIL* ]]
}

@test "an internal network that is not --internal is a failure" {
  export FAKE_DOCKER_OUT_NETWORK_LS=lea-internal-abc
  export FAKE_DOCKER_OUT_NETWORK_INSPECT='false isolated'
  run lea_doctor
  [ "$status" -eq 1 ]
  [[ "$output" == *"FAIL  lea-internal-abc is internal"* ]]
}

@test "every internal network of the prefix is checked, other networks are not" {
  export FAKE_DOCKER_OUT_NETWORK_LS=$'lea-internal-abc\nlea-internal-def\nlea-egress'
  export FAKE_DOCKER_OUT_NETWORK_INSPECT='true isolated'
  run lea_doctor
  [[ "$output" == *"ok    lea-internal-abc gateway_mode_ipv4=isolated"* ]]
  [[ "$output" == *"ok    lea-internal-def gateway_mode_ipv4=isolated"* ]]
  [[ "$output" != *"lea-egress"* ]]
}

@test "no internal network yet is reported, not a failure" {
  run lea_doctor
  [[ "$output" == *"internal networks"*"none"* ]]
}

@test "AppArmor profile loaded is ok" {
  export FAKE_DOCKER_OUT_INFO___FORMAT='["name=apparmor"]'
  run lea_doctor
  [[ "$output" == *"ok    AppArmor profile lea"* ]]
  grep -q '\[--pull\] \[never\]' "$FAKE_LOG"
}

@test "AppArmor profile not loaded is a failure" {
  export FAKE_DOCKER_OUT_INFO___FORMAT='["name=apparmor"]'
  export FAKE_DOCKER_RC_RUN___RM=1
  run lea_doctor
  [ "$status" -eq 1 ]
  [[ "$output" == *"FAIL  AppArmor profile lea"* ]]
}

@test "missing image fails as an image, not as the AppArmor profile" {
  export FAKE_DOCKER_OUT_INFO___FORMAT='["name=apparmor"]'
  export FAKE_DOCKER_RC_IMAGE_INSPECT=1
  export FAKE_DOCKER_RC_RUN___RM=1
  run lea_doctor
  [[ "$output" == *"warn  AppArmor profile lea: image "*"not present"* ]]
  [[ "$output" != *"FAIL  AppArmor"* ]]
  [[ "$output" == *"FAIL  image $LEA_IMAGE"* ]]
}

@test "telemetry upstream set is ok" {
  LEA_OTEL_UPSTREAM=otel.example:4317 run lea_doctor
  [[ "$output" == *"ok    telemetry upstream otel.example:4317"* ]]
}

@test "user config, projects dir and images are reported" {
  run lea_doctor
  [[ "$output" == *"ok    user config $HOME/.config/lea/lea.conf"* ]]
  [[ "$output" == *"ok    projects dir $P"* ]]
  [[ "$output" == *"ok    image $LEA_IMAGE"* ]]
  [[ "$output" == *"ok    image $LEA_PROXY_IMAGE"* ]]
}

@test "a missing user config, projects dir or image is a failure pointing to lea init" {
  rm "$HOME/.config/lea/lea.conf"
  export LEA_PROJECTS_DIR= FAKE_DOCKER_RC_IMAGE_INSPECT=1
  run lea_doctor
  [ "$status" -eq 1 ]
  [[ "$output" == *"FAIL  user config"*"run lea init"* ]]
  [[ "$output" == *"FAIL  projects dir"*"run lea init"* ]]
  [[ "$output" == *"FAIL  image $LEA_IMAGE"*"run lea init"* ]]
}

@test "a guarded projects dir is a failure" {
  export LEA_PROJECTS_DIR="$HOME"
  run lea_doctor
  [ "$status" -eq 1 ]
  [[ "$output" == *"FAIL  projects dir"*"cannot be a projects directory"* ]]
}

@test "tmux: missing is always a failure" {
  no_tmux
  export LEA_OS_RELEASE="$BATS_TEST_TMPDIR/os-release"; printf 'ID=fedora\n' >"$LEA_OS_RELEASE"
  run lea_doctor
  [ "$status" -eq 1 ]
  [[ "$output" == *"FAIL  tmux: tmux is not installed. Install it with: sudo dnf install tmux, or build a static one with: make -C \"$LEA_ROOT\" tmux-build (the deck needs it)"* ]]
}

@test "tmux: a recent one is ok, an old one fails" {
  fake_tmux "tmux 3.4"
  run lea_doctor
  [[ "$output" == *"ok    tmux (tmux 3.4, host)"* ]]
  export FAKE_TMUX_VERSION="tmux 3.0"
  run lea_doctor
  [[ "$output" == *"FAIL  tmux: tmux 3.0 is older than 3.2"* ]]
}

@test "tmux: a bad LEA_TMUX_BIN is named, not reported as missing" {
  export LEA_TMUX_BIN="$BATS_TEST_TMPDIR"
  run lea_doctor
  [[ "$output" == *"FAIL  tmux: LEA_TMUX_BIN=$BATS_TEST_TMPDIR is not an executable file"* ]]
}

@test "tmux: the lea-built binary is reported as such" {
  fake_tmux "tmux 3.0"
  mkdir -p "$BATS_TEST_TMPDIR/b"; LEA_TMUX_BUILT="$BATS_TEST_TMPDIR/b/tmux"
  printf '#!/bin/sh\necho "tmux 3.5a"\n' >"$LEA_TMUX_BUILT"; chmod +x "$LEA_TMUX_BUILT"
  export LEA_TMUX_BUILT
  run lea_doctor
  [[ "$output" == *"ok    tmux (tmux 3.5a, lea-built)"* ]]
}

@test "doctor has no launcher line: the Go binary is the only launcher" {
  run lea_doctor
  [[ "$output" != *"launcher"* ]]
}

@test "a configuration line the Go launcher refused is a failure" {
  export LEA_GO_CONFIG_ERROR='/x/lea.conf line 2: only $HOME and ${HOME} are read'
  run lea_doctor
  [ "$status" -eq 1 ]
  [[ "$output" == *'FAIL  configuration: /x/lea.conf line 2: only $HOME and ${HOME} are read'* ]]
}

@test "the egress allowlist is lea's default when unset, a warning when open, its domains when set" {
  run lea_doctor
  [[ "$output" == *"ok    egress allowlist: lea's default (forges, registries, model APIs; LEA_EGRESS_ALLOW extends it with 'default,<domain>')"* ]]
  export LEA_EGRESS_ALLOW='*'
  run lea_doctor
  [[ "$output" == *"warn  egress allowlist: off (LEA_EGRESS_ALLOW='*'): agents reach any public web domain; only DoH resolvers and IP literals are refused"* ]]
  export LEA_EGRESS_ALLOW=github.com,proxy.golang.org
  run lea_doctor
  [[ "$output" == *"ok    egress allowlist: github.com,proxy.golang.org (always: anthropic.com,claude.ai,claude.com)"* ]]
}

# Host versions (#108): the kernel and the Docker Engine every lea container runs on.
docker_components() {
  export FAKE_DOCKER_OUT_VERSION___FORMAT=$'Engine=29.4.2\ncontainerd=v2.1.4\nrunc=1.3.0\ndocker-init=0.19.0'
}
host_minimums() {
  export LEA_HOST_MINIMUMS="$BATS_TEST_TMPDIR/host-minimums"
  printf '# test\nkernel ubuntu 24.04 7.0.0-38\n' >"$LEA_HOST_MINIMUMS"
  export LEA_OS_RELEASE="$BATS_TEST_TMPDIR/os-release"
  printf 'ID=ubuntu\nVERSION_ID="24.04"\n' >"$LEA_OS_RELEASE"
}

@test "versions: Docker Engine, containerd and runc are shown" {
  docker_components
  run lea_doctor
  [[ "$output" == *"ok    Docker Engine 29.4.2 (containerd v2.1.4, runc 1.3.0)"* ]]
  grep -q '\[version\] \[--format\]' "$FAKE_LOG"
}

@test "versions: no Docker Engine version is a failure (lea runs its own seccomp profile)" {
  for v in 29.4.1 28.10.3 29.10.0; do
    export FAKE_DOCKER_OUT_VERSION___FORMAT="Engine=$v"
    run lea_doctor
    [[ "$output" == *"ok    Docker Engine $v"* ]]
    [[ "$output" != *"FAIL  Docker Engine"* ]]
  done
}

@test "versions: an engine that is not Docker Engine is a warning, not a failure" {
  export FAKE_DOCKER_RC_VERSION___FORMAT=125
  run lea_doctor
  [[ "$output" == *"warn  Docker Engine: version unknown (not Docker Engine?)"* ]]
  [[ "$output" != *"FAIL  Docker Engine"* ]]
}

@test "versions: Podman as docker is detected and never a failure (#108)" {
  host_minimums; printf 'podman 5.8.4\n' >>"$LEA_HOST_MINIMUMS"
  fake_uname 7.0.0-38-generic
  export FAKE_DOCKER_RC_VERSION___FORMAT=125 FAKE_DOCKER_OUT___VERSION_="podman version 5.8.9"
  run lea_doctor
  [[ "$output" == *"warn  Podman 5.8.9: lea is built for Docker Engine; Podman minimum 5.8.4 met"* ]]
  [[ "$output" == *"ok    kernel 7.0.0-38-generic (ubuntu 24.04, minimum 7.0.0-38)"* ]]
  [[ "$output" != *"FAIL  Docker Engine"* ]]
  [[ "$output" != *"FAIL  Podman"* ]]
  export FAKE_DOCKER_OUT___VERSION_="podman version 5.8.3"
  run lea_doctor
  [[ "$output" == *"warn  Podman 5.8.3: older than 5.8.4 (CVE-2026-57231): update Podman"* ]]
  [[ "$output" != *"FAIL  Podman"* ]]
  # Docker's CLI on Podman's Docker API names a Podman Engine component.
  export FAKE_DOCKER_RC_VERSION___FORMAT=0 FAKE_DOCKER_OUT_VERSION___FORMAT=$'Podman Engine=5.8.9\nConmon=conmon version 2.1.13'
  run lea_doctor
  [[ "$output" == *"warn  Podman 5.8.9: lea is built for Docker Engine"* ]]
}

@test "lea_host_engine names the engine behind docker and its version" {
  export FAKE_DOCKER_OUT_VERSION___FORMAT=$'Engine=29.4.2\ncontainerd=v2.1.4'
  [ "$(lea_host_engine)" = "docker 29.4.2" ]
  export FAKE_DOCKER_OUT_VERSION___FORMAT=$'Podman Engine=5.8.9'
  [ "$(lea_host_engine)" = "podman 5.8.9" ]
  export FAKE_DOCKER_OUT_VERSION___FORMAT="" FAKE_DOCKER_RC_VERSION___FORMAT=125 FAKE_DOCKER_OUT___VERSION_="podman version 5.8.9"
  [ "$(lea_host_engine)" = "podman 5.8.9" ]
  export FAKE_DOCKER_OUT___VERSION_="Docker version 29.4.2, build 1234567"
  [ -z "$(lea_host_engine)" ]
}

@test "versions: the kernel release is checked against the minimum of its distribution" {
  docker_components; host_minimums
  fake_uname 7.0.0-38-generic
  run lea_doctor
  [[ "$output" == *"ok    kernel 7.0.0-38-generic (ubuntu 24.04, minimum 7.0.0-38)"* ]]
  fake_uname 7.0.0-37-generic
  run lea_doctor
  [ "$status" -eq 1 ]
  [[ "$output" == *"FAIL  kernel: 7.0.0-37-generic is older than 7.0.0-38 for ubuntu 24.04 (CVE-2026-74753, CVE-2026-80521, CVE-2026-80841): update the host kernel"* ]]
}

@test "versions: a distribution without a known minimum is a warning" {
  docker_components; host_minimums
  printf 'ID=fedora\nVERSION_ID=44\n' >"$LEA_OS_RELEASE"
  fake_uname 6.17.1-300.fc44.x86_64
  run lea_doctor
  [[ "$output" == *"warn  kernel 6.17.1-300.fc44.x86_64: no known minimum for fedora 44 in "*"host-minimums: check that it has the fixes for CVE-2026-74753, CVE-2026-80521, CVE-2026-80841"* ]]
}

@test "versions: the minimums file of the repository sets Podman's and no Docker Engine minimum" {
  grep -qx 'podman 5.8.4' "$LEA_ROOT/config/host-minimums"
  ! grep -q '^docker-engine' "$LEA_ROOT/config/host-minimums" || false
}

@test "lea_version_ge compares numeric fields" {
  lea_version_ge 29.4.2 29.4.2
  lea_version_ge 29.10.0 29.4.2
  ! lea_version_ge 29.4.1 29.4.2 || false
  lea_version_ge v2.1.4 2.1
  lea_version_ge 7.0.0-38-generic 7.0.0-38
  ! lea_version_ge 7.0.0-37-generic 7.0.0-38 || false
  ! lea_version_ge 6.17.1 7.0.0 || false
}

@test "limits: a running lea container without CPU or memory limits is a warning (#132)" {
  export FAKE_DOCKER_OUT_PS___FILTER=lea-abc
  export FAKE_DOCKER_OUT_INSPECT___FORMAT='0 0 0 0'
  run lea_doctor
  [[ "$output" == *"warn  lea-abc limits: no CPU and memory limit: run lea in its projects directory to apply LEA_CPUS and LEA_MEMORY"* ]]
  export FAKE_DOCKER_OUT_INSPECT___FORMAT='16500000000 0 0 <nil>'
  run lea_doctor
  [[ "$output" == *"warn  lea-abc limits: no memory limit"* ]]
  export FAKE_DOCKER_OUT_INSPECT___FORMAT='16500000000 17179869184 17179869184 16384 c33f5c7309f710f8'
  run lea_doctor
  [[ "$output" == *"ok    lea-abc limits: 16.5 CPUs, 16384 MiB"* ]]
  LEA_CPUS=0 FAKE_DOCKER_OUT_INSPECT___FORMAT='0 17179869184' run lea_doctor
  [[ "$output" == *"ok    lea-abc limits: no CPU limit, 16384 MiB"* ]]
}

@test "limits: a running lea container whose limits differ from the configured ones is stale (#132)" {
  export FAKE_DOCKER_OUT_PS___FILTER=lea-abc
  # What the Go launcher hands over: 12 CPUs, 16 GiB (swap the same), 16384 pids.
  export LEA_LIMITS_WANT='12000000000 17179869184 17179869184 16384' LEA_LIMITS_DIGEST=c33f5c7309f710f8
  # Started by a lea older than the limits (no lea.limits label): every limit is stale.
  export FAKE_DOCKER_OUT_INSPECT___FORMAT='0 0 0 0'
  run lea_doctor
  [[ "$output" == *"warn  lea-abc limits: stale (digest none: started by an older lea, configured c33f5c7309f710f8): no CPU limit (configured 12 CPUs), no memory limit (configured 16384 MiB), pids no limit (configured 16384); run lea in its projects directory: it applies them, or restarts the container once no agent or session runs in it"* ]]
  # Started with other limits (LEA_CPUS changed since): only the CPUs are stale.
  export FAKE_DOCKER_OUT_INSPECT___FORMAT='8000000000 17179869184 17179869184 16384 0123456789abcdef'
  run lea_doctor
  [[ "$output" == *"warn  lea-abc limits: stale (digest 0123456789abcdef, configured c33f5c7309f710f8): 8 CPUs (configured 12 CPUs);"* ]]
  [[ "$output" != *"configured 16384 MiB"* ]]
  # The right label, but someone changed the limits by hand.
  export FAKE_DOCKER_OUT_INSPECT___FORMAT='12000000000 8589934592 8589934592 16384 c33f5c7309f710f8'
  run lea_doctor
  [[ "$output" == *"warn  lea-abc limits: stale (changed since it started (docker update)): 8192 MiB (configured 16384 MiB);"* ]]
  # Matching limits: ok. A limit turned off (0) is never stale.
  export FAKE_DOCKER_OUT_INSPECT___FORMAT='12000000000 17179869184 17179869184 16384 c33f5c7309f710f8'
  run lea_doctor
  [[ "$output" == *"ok    lea-abc limits: 12 CPUs, 16384 MiB"* ]]
  LEA_LIMITS_WANT='0 17179869184 17179869184 16384' FAKE_DOCKER_OUT_INSPECT___FORMAT='12000000000 17179869184 17179869184 16384' run lea_doctor
  [[ "$output" == *"ok    lea-abc limits: 12 CPUs, 16384 MiB"* ]]
}

@test "cgroup delegation: rootless Podman without the cpu controller is a warning (#132)" {
  key=$(printf '%s' 'INFO_--FORMAT_AT_{{.Host.Security.Rootless}} {{.Host.CgroupControllers}}' | tr '[:lower:]' '[:upper:]' | tr -c 'A-Z0-9_' '_')
  export "FAKE_DOCKER_OUT_$key=true [memory pids]"
  run lea_doctor
  [[ "$output" == *"warn  cgroup delegation: rootless Podman does not get the cpu controller, so lea's container runs without that limit: sudo systemctl edit user@.service, add [Service] Delegate=cpu cpuset io memory pids, log in again, then lea stop and lea"* ]]
  export "FAKE_DOCKER_OUT_$key=true [pids]"
  run lea_doctor
  [[ "$output" == *"does not get the cpu, memory controller"* ]]
  export "FAKE_DOCKER_OUT_$key=true [cpuset cpu io memory pids]"
  run lea_doctor
  [[ "$output" == *"ok    cgroup delegation: cpu, memory and pids (rootless Podman)"* ]]
  # Docker, rootful Podman, or a Podman that does not tell: nothing.
  for v in 'false [cpu]' 'true []' ''; do
    export "FAKE_DOCKER_OUT_$key=$v"
    run lea_doctor
    [[ "$output" != *"cgroup delegation"* ]]
  done
}

@test "the agents' CPU windows of each running lea container are listed (#132)" {
  export FAKE_DOCKER_OUT_PS___FILTER=lea-abc
  export FAKE_DOCKER_OUT_EXEC_LEA_ABC=$'dev-1 0,1,2,3\nlead-1 4,5,6,7'
  run lea_doctor
  [[ "$output" == *"ok    lea-abc CPU windows: dev-1 0,1,2,3, lead-1 4,5,6,7"* ]]
  export FAKE_DOCKER_OUT_EXEC_LEA_ABC=
  run lea_doctor
  [[ "$output" == *"ok    lea-abc CPU windows: none"* ]]
}

@test "DNS: each running lea container resolves no external name and has lea's resolver (#98)" {
  export FAKE_DOCKER_OUT_PS___FILTER=lea-abc
  export FAKE_DOCKER_OUT_CONTAINER_INSPECT='["192.0.2.1"]' FAKE_DOCKER_RC_EXEC__W=2
  run lea_doctor
  [[ "$output" == *"ok    lea-abc DNS: resolves no external name (resolver 192.0.2.1)"* ]]
  grep -q -- '\[exec\] \[-w\] \[/\] \[lea-abc\] \[timeout\] \[10\] \[getent\] \[hosts\] \[example.com\]' "$FAKE_LOG"
  # Started by an older lea: Docker's default resolver, which forwards to the host's.
  export FAKE_DOCKER_OUT_CONTAINER_INSPECT='null'
  run lea_doctor
  [[ "$output" == *"warn  lea-abc DNS: no pinned resolver (started by an older lea): its queries may reach the host's resolver; run lea stop, then lea"* ]]
  # A loopback resolver (an earlier lea of #98): Docker may query it from the host's namespace.
  export FAKE_DOCKER_OUT_CONTAINER_INSPECT='["127.0.0.1"]'
  run lea_doctor
  [[ "$output" == *"warn  lea-abc DNS: no pinned resolver"* ]]
  # It resolves an external name: a way out that bypasses the proxy, a failure.
  export FAKE_DOCKER_OUT_CONTAINER_INSPECT='["192.0.2.1"]' FAKE_DOCKER_RC_EXEC__W=0 FAKE_DOCKER_OUT_EXEC__W='93.184.215.14 example.com'
  run lea_doctor
  [ "$status" -eq 1 ]
  [[ "$output" == *"FAIL  lea-abc DNS: resolves example.com (93.184.215.14): DNS reaches outside lea-proxy; run lea stop, then lea"* ]]
}

@test "pressure: PSI, each lea container's cgroup and the thresholds (#132, P2)" {
  export LEA_DOCTOR_PROC="$BATS_TEST_TMPDIR/proc" LEA_DOCTOR_CGROUP="$BATS_TEST_TMPDIR/cg"
  mkdir -p "$LEA_DOCTOR_PROC/pressure" "$LEA_DOCTOR_PROC/4242" "$LEA_DOCTOR_CGROUP/user.slice/libpod-abc.scope"
  : >"$LEA_DOCTOR_PROC/pressure/cpu"; : >"$LEA_DOCTOR_PROC/pressure/memory"
  printf '0::/user.slice/libpod-abc.scope\n' >"$LEA_DOCTOR_PROC/4242/cgroup"
  echo 1 >"$LEA_DOCTOR_CGROUP/user.slice/libpod-abc.scope/memory.current"
  export FAKE_DOCKER_OUT_PS___FILTER=lea-abc FAKE_DOCKER_OUT_INSPECT__F_AT_LEA_ABC=4242
  run lea_doctor
  [[ "$output" == *"ok    pressure stall information (PSI)"* ]]
  [[ "$output" == *"ok    lea-abc pressure readable (cgroup /user.slice/libpod-abc.scope)"* ]]
  [[ "$output" == *"ok    admission: cpu PSI 40%, memory 85% of the limit, memory PSI 10%, io PSI 40%, load 2 per CPU (0: off)"* ]]
  [[ "$output" == *"ok    shedding: after 30 s at memory 95% (LEA_SHED_*)"* ]]
  rm "$LEA_DOCTOR_PROC/pressure/cpu" "$LEA_DOCTOR_PROC/4242/cgroup"
  LEA_SHED_MEM_PCT=80 run lea_doctor
  [[ "$output" == *"warn  pressure stall information (PSI): "*"psi=0"* ]]
  [[ "$output" == *"warn  lea-abc pressure: its cgroup v2 is not readable from the host"* ]]
  [[ "$output" == *"warn  shedding: LEA_SHED_MEM_PCT (80) is not above LEA_ADMIT_MEM_PCT (85)"* ]]
  LEA_ADMIT_CPU_PSI=0 LEA_ADMIT_MEM_PCT=0 LEA_ADMIT_MEM_PSI=0 LEA_ADMIT_IO_PSI=0 LEA_ADMIT_LOAD=0 LEA_SHED_AFTER=0 run lea_doctor
  [[ "$output" == *"warn  admission: every LEA_ADMIT_* threshold is 0"* ]]
  [[ "$output" == *"ok    shedding: off (LEA_SHED_AFTER=0)"* ]]
}

@test "hub: off is reported, on without a facade socket is a warning, with one is ok" {
  export LEA_HUB=0
  run lea_doctor
  [[ "$output" == *"ok    approval hub: off (LEA_HUB=0)"* ]]
  export LEA_HUB=1 XDG_RUNTIME_DIR="$BATS_TEST_TMPDIR/run"
  unset LEA_HUB_SOCKET
  run lea_doctor
  [[ "$output" == *"warn  approval hub: LEA_HUB=1 but no lockhub facade at $BATS_TEST_TMPDIR/run/lockhub/facade.sock"* ]]
  unset XDG_RUNTIME_DIR
  run lea_doctor
  [[ "$output" == *"warn  approval hub: LEA_HUB=1 but no facade socket to read"* ]]
  export LEA_HUB_SOCKET="$BATS_TEST_TMPDIR/f.sock"
  python3 -c 'import socket,sys; socket.socket(socket.AF_UNIX).bind(sys.argv[1])' "$LEA_HUB_SOCKET"
  run lea_doctor
  [[ "$output" == *"ok    approval hub facade $LEA_HUB_SOCKET"* ]]
  [[ "$output" != *"warn  approval hub"* ]]
}

@test "managed settings: lea's hooks confirmed in the image and in each running lea container (#111)" {
  export FAKE_DOCKER_OUT_RUN___RM="lea's hooks in /etc/claude-code/managed-settings.json, Claude Code 2.1.296"
  export FAKE_DOCKER_OUT_PS___FILTER=lea-abc
  export FAKE_DOCKER_OUT_EXEC__U="lea's hooks in /etc/claude-code/managed-settings.json, Claude Code 2.1.296"
  run lea_doctor
  [[ "$output" == *"ok    managed settings (image): lea's hooks in /etc/claude-code/managed-settings.json, Claude Code 2.1.296"* ]]
  [[ "$output" == *"ok    lea-abc managed settings: lea's hooks in"* ]]
  grep -q -- '\[run\] \[--rm\] \[--pull\] \[never\] \[--network\] \[none\] \[--entrypoint\] \[/usr/local/libexec/lea/managed-check\]' "$FAKE_LOG"
  grep -q -- "\[exec\] \[-u\] \[$(id -u):$(id -g)\] \[lea-abc\] \[/usr/local/libexec/lea/managed-check\]" "$FAKE_LOG"
}

@test "managed settings: a check that fails or cannot run is a failure (fails closed, #111)" {
  export FAKE_DOCKER_RC_RUN___RM=1 FAKE_DOCKER_OUT_RUN___RM="Claude Code 2.1.400 is not 2.1.296, the version lea verified its managed settings with"
  run lea_doctor
  [ "$status" -eq 1 ]
  [[ "$output" == *"FAIL  managed settings (image): Claude Code 2.1.400 is not 2.1.296"*"run lea update"* ]]
  unset FAKE_DOCKER_RC_RUN___RM FAKE_DOCKER_OUT_RUN___RM
  export FAKE_DOCKER_OUT_PS___FILTER=lea-abc FAKE_DOCKER_RC_EXEC__U=126
  run lea_doctor
  [ "$status" -eq 1 ]
  [[ "$output" == *"FAIL  lea-abc managed settings: the check did not run: restart it (lea stop, then lea) on the current image"* ]]
}

@test "doctor lists the credentials an agent can read, by path, never their value (#103)" {
  run lea_doctor
  [[ "$output" == *"ok    agent-readable credentials: none"* ]]
  [[ "$output" == *"ok    not agent-readable: no GitHub token (lea github login keeps one in the proxy)"* ]]
  mkdir -p "$S/claude/abc" "$S/codex/abc" "$S/gh" "$S/git" "$S/glab-cli" "$S/containers" "$S/kube" "$S/../proxy/forge"
  printf '{"claudeAiOauth":{"accessToken":"sk-ant-oat-secret"}}\n' >"$S/claude/abc/.credentials.json"
  printf '{"tokens":{}}\n' >"$S/codex/abc/auth.json"
  printf 'github.com:\n    oauth_token: gho_secretvalue\n' >"$S/gh/hosts.yml"
  printf 'https://u:glpat-secret@gitlab.com\n' >"$S/git/credentials"
  printf 'hosts:\n    gitlab.com:\n        token: glpat-secret\n' >"$S/glab-cli/config.yml"
  printf '{"auths":{}}\n' >"$S/containers/auth.json"
  : >"$S/kube/config"
  printf 'ghp_proxyvalue\n' >"$S/../proxy/forge/github.com"
  run lea_doctor
  [[ "$output" == *"ok    agent-readable: ~/.claude/.credentials.json (Claude Code's OAuth login; it refreshes it)"* ]]
  [[ "$output" == *"ok    agent-readable: ~/.codex/auth.json (Codex's login)"* ]]
  [[ "$output" == *"warn  agent-readable: /lea/state/gh/hosts.yml: holds a gh token"* ]]
  [[ "$output" == *"ok    agent-readable: /lea/state/git/credentials (git's credential store: 1 entries, other forges)"* ]]
  [[ "$output" == *"ok    agent-readable: /lea/state/glab-cli/config.yml (glab's GitLab token)"* ]]
  [[ "$output" == *"ok    agent-readable: /lea/state/containers/auth.json (podman and docker registry logins)"* ]]
  [[ "$output" == *"ok    agent-readable: ~/.kube/lea/config"* ]]
  [[ "$output" == *"ok    not agent-readable: the GitHub token (in the proxy, lea github status)"* ]]
  [[ "$output" != *"agent-readable credentials: none"* ]]
  [[ "$output" != *secret* ]]
  [[ "$output" != *proxyvalue* ]]
  printf 'https://u:ghp_x@github.com\n' >"$S/git/credentials"
  run lea_doctor
  [[ "$output" == *"warn  agent-readable: /lea/state/git/credentials: holds a github.com token"* ]]
}

# fake_pgrep RC: a pgrep first on PATH that exits RC (0: a lox run runs, 1: none does).
fake_pgrep() {
  mkdir -p "$BATS_TEST_TMPDIR/pbin"
  printf '#!/bin/sh\nexit %s\n' "$1" >"$BATS_TEST_TMPDIR/pbin/pgrep"
  chmod +x "$BATS_TEST_TMPDIR/pbin/pgrep"
  PATH="$BATS_TEST_TMPDIR/pbin:$PATH"
}

# lox_journal EVENT...: lox run's session journal of the projects directory, events in order.
lox_journal() {
  _lj=$XDG_STATE_HOME/lockfamily/lockfamily-$(printf '%s' "$(cd "$P" && pwd -P)" | sha256sum | cut -c1-8)
  mkdir -p "$_lj"; : >"$_lj/runs.jsonl"
  for e in "$@"; do printf '{"at":"2026-11-06T10:00:00Z","event":"%s","user":"me"}\n' "$e" >>"$_lj/runs.jsonl"; done
}

@test "lox: lea doctor checks its version against the lox of config/locks.lock, and runs nothing else of it" {
  run lea_doctor
  [[ "$output" == *"ok    lox 0.10.0"* ]]
  FAKE_LOX_VERSION=0.8.0 run lea_doctor
  [[ "$output" == *"warn  lox: 0.8.0 is older than 0.10.0, which has lox run: brew upgrade lox"* ]]
  FAKE_LOX_VERSION=dev run lea_doctor
  [[ "$output" == *"ok    lox dev (its version is not checked)"* ]]
  run calls
  [[ "$output" == *"lox [version]"* ]]
  [[ "$output" != *"lox [up]"* && "$output" != *"lox [init]"* && "$output" != *"lox [attach]"* && "$output" != *"lox [run]"* ]]
}

@test "lox run: lea doctor says to run it while the workspace has locks and none serves them" {
  export XDG_STATE_HOME="$BATS_TEST_TMPDIR/state"
  # No lock declared: nothing to say of lox run.
  run lea_doctor
  [[ "$output" != *"lox run:"* ]]
  mkdir -p "$P/.lockfamily"; printf 'locks: {}\n' >"$P/.lockfamily/lockfamily.yaml"
  fake_pgrep 1
  run lea_doctor
  [[ "$output" == *"warn  lox run: not serving the locks of $P: their requests wait. Open another terminal (outside lea) and run \`lox run\` to approve the agents' connections and writes."* ]]
  # Serving: its journal ends with a start (or a mode switch) and its process runs.
  fake_pgrep 0
  lox_journal start
  run lea_doctor
  [[ "$output" == *"ok    lox run: serves the decisions of $P"* ]]
  lox_journal start mode
  run lea_doctor
  [[ "$output" == *"ok    lox run: serves the decisions of $P"* ]]
  # Stopped, or killed before its stop (no process): not serving.
  lox_journal start stop
  run lea_doctor
  [[ "$output" == *"warn  lox run: not serving"* ]]
  fake_pgrep 1
  lox_journal start
  run lea_doctor
  [[ "$output" == *"warn  lox run: not serving"* ]]
  # Without lox at all, it says how to get it.
  mkdir -p "$BATS_TEST_TMPDIR/nolox"
  for f in "$LEA_ROOT"/tests/fakes/*; do [ "$(basename "$f")" = lox ] || ln -s "$f" "$BATS_TEST_TMPDIR/nolox/"; done
  PATH="$BATS_TEST_TMPDIR/pbin:$BATS_TEST_TMPDIR/nolox:/usr/local/bin:/usr/bin:/bin" run lea_doctor
  [[ "$output" == *"warn  lox: not found: brew install davidgodefroid/tap/lox, then run \`lox run\` in another terminal (outside lea)"* ]]
}

@test "LEA_LOX_UP is obsolete: lea doctor says so" {
  LEA_LOX_UP=1 run lea_doctor
  [[ "$output" == *"warn  LEA_LOX_UP: obsolete and ignored: remove it from your configuration"* ]]
  run calls
  [[ "$output" != *"lox [up]"* ]]
}

# fake_docker_key ARGS...: the fake docker's FAKE_DOCKER_OUT_ suffix for a call with these first
# two arguments and this last one (tests/fakes/docker).
fake_docker_key() {
  printf '%s_%s_AT_%s' "$1" "$2" "$3" | tr '[:lower:]' '[:upper:]' | tr -c 'A-Z0-9_' '_'
}

@test "runtime: LEA_RUNTIME, the runtimes the engine knows and each running lea container's (#106)" {
  export "FAKE_DOCKER_OUT_$(fake_docker_key info --format '{{.DefaultRuntime}}')=runc"
  export "FAKE_DOCKER_OUT_$(fake_docker_key info --format '{{range $k, $v := .Runtimes}}{{$k}} {{end}}')=io.containerd.runc.v2 runc runsc "
  run lea_doctor
  [[ "$output" == *"ok    agent runtime: runc (LEA_RUNTIME empty; runsc or kata adds a second kernel: README)"* ]]
  export LEA_RUNTIME=runsc
  run lea_doctor
  [[ "$output" == *"ok    agent runtime: runsc (LEA_RUNTIME; the engine's default: runc)"* ]]
  [[ "$output" != *"FAIL  agent runtime"* ]]
  export LEA_RUNTIME=kata
  run lea_doctor
  [ "$status" -eq 1 ]
  [[ "$output" == *"FAIL  agent runtime kata: the engine has no such runtime (it knows: io.containerd.runc.v2 runc runsc)"* ]]
  export LEA_RUNTIME='run sc'
  run lea_doctor
  [ "$status" -eq 1 ]
  [[ "$output" == *"FAIL  agent runtime: LEA_RUNTIME 'run sc' is not a runtime name"* ]]
  # A running lea container on another runtime than LEA_RUNTIME's: a warning.
  export LEA_RUNTIME=runsc FAKE_DOCKER_OUT_PS___FILTER=lea-abc
  export "FAKE_DOCKER_OUT_INSPECT___FORMAT_AT_LEA_ABC=runc"
  run lea_doctor
  [[ "$output" == *"warn  lea-abc runtime: runs on runc, not runsc: run 'lea stop', then lea, to restart it on runsc"* ]]
  export "FAKE_DOCKER_OUT_INSPECT___FORMAT_AT_LEA_ABC=runsc"
  run lea_doctor
  [[ "$output" == *"ok    lea-abc runtime: runsc"* ]]
  # LEA_RUNTIME empty: the engine's default is expected.
  unset LEA_RUNTIME
  run lea_doctor
  [[ "$output" == *"warn  lea-abc runtime: runs on runsc, not runc"* ]]
}

@test "runtime seccomp: warns when runsc's registration lacks --oci-seccomp (#106)" {
  export "FAKE_DOCKER_OUT_$(fake_docker_key info --format '{{range $k, $v := .Runtimes}}{{$k}} {{end}}')=runc runsc gvisor "
  k=$(fake_docker_key info --format '{{json (index .Runtimes "runsc")}}')
  export LEA_RUNTIME=runsc
  export "FAKE_DOCKER_OUT_$k={\"path\":\"/opt/gvisor/runsc\",\"runtimeArgs\":[\"--host-uds=open\",\"--host-fifo=open\"]}"
  run lea_doctor
  [[ "$output" == *"warn  runsc seccomp: its dockerd registration has no --oci-seccomp: runsc ignores lea's seccomp profile"* ]]
  # A status string that merely mentions the flag (escaped, in the runtime's features) is not it.
  export "FAKE_DOCKER_OUT_$k={\"path\":\"/opt/gvisor/runsc\",\"status\":{\"f\":\"{\\\"a\\\":[\\\"--oci-seccomp\\\"]}\"}}"
  run lea_doctor
  [[ "$output" == *"warn  runsc seccomp: its dockerd registration has no --oci-seccomp"* ]]
  export "FAKE_DOCKER_OUT_$k={\"path\":\"/opt/gvisor/runsc\",\"runtimeArgs\":[\"--oci-seccomp\",\"--host-uds=open\"]}"
  run lea_doctor
  [[ "$output" == *"ok    runsc seccomp: lea's profile applied in the sandbox (--oci-seccomp)"* ]]
  [[ "$output" != *"warn  runsc seccomp"* ]]
  # A runtime of another name whose path is runsc is gVisor too.
  export LEA_RUNTIME=gvisor
  export "FAKE_DOCKER_OUT_$(fake_docker_key info --format '{{json (index .Runtimes "gvisor")}}')={\"path\":\"/usr/bin/runsc\"}"
  run lea_doctor
  [[ "$output" == *"warn  gvisor seccomp: its dockerd registration has no --oci-seccomp"* ]]
}

@test "runtime seccomp: warns when Kata's configuration lacks disable_guest_seccomp = false (#106)" {
  export "FAKE_DOCKER_OUT_$(fake_docker_key info --format '{{range $k, $v := .Runtimes}}{{$k}} {{end}}')=runc kata "
  k=$(fake_docker_key info --format '{{json (index .Runtimes "kata")}}')
  export LEA_RUNTIME=kata "FAKE_DOCKER_OUT_$k={\"runtimeType\":\"io.containerd.kata.v2\"}"
  c="$BATS_TEST_TMPDIR/configuration.toml"
  export LEA_KATA_CONFIGS="$BATS_TEST_TMPDIR/absent.toml $c"
  # No configuration found: a warning, never an ok.
  run lea_doctor
  [[ "$output" == *"warn  kata seccomp: no Kata configuration.toml found"* ]]
  # Kata's default (commented out, or true): not applied.
  printf '[runtime]\n#disable_guest_seccomp = false\n' >"$c"
  run lea_doctor
  [[ "$output" == *"warn  kata seccomp: $c does not set disable_guest_seccomp = false (Kata's default is true)"* ]]
  printf '[runtime]\ndisable_guest_seccomp=true\n' >"$c"
  run lea_doctor
  [[ "$output" == *"warn  kata seccomp: $c does not set disable_guest_seccomp = false"* ]]
  printf '[runtime]\n  disable_guest_seccomp = false # lea\n' >"$c"
  run lea_doctor
  [[ "$output" == *"ok    kata seccomp: lea's profile applied in the guest (disable_guest_seccomp = false in $c)"* ]]
  [[ "$output" != *"warn  kata seccomp"* ]]
  # Docker Engine reports a shim runtime as {} (no runtimeType): its name tells Kata.
  export "FAKE_DOCKER_OUT_$k={}"
  run lea_doctor
  [[ "$output" == *"ok    kata seccomp: lea's profile applied in the guest (disable_guest_seccomp = false in $c)"* ]]
  export "FAKE_DOCKER_OUT_$k={\"runtimeType\":\"io.containerd.kata.v2\"}"
  # The first file that exists wins (/etc over the defaults): a default without the setting is not read.
  e="$BATS_TEST_TMPDIR/etc.toml"; printf 'disable_guest_seccomp = false\n' >"$e"
  printf 'disable_guest_seccomp = true\n' >"$c"
  export LEA_KATA_CONFIGS="$e $c"
  run lea_doctor
  [[ "$output" == *"ok    kata seccomp: lea's profile applied in the guest (disable_guest_seccomp = false in $e)"* ]]
  [[ "$output" != *"warn  kata seccomp"* ]]
  # An unknown shim flavour: the file each flavour would read is checked.
  export LEA_KATA_CONFIGS="$e|$BATS_TEST_TMPDIR/absent.toml $c"
  run lea_doctor
  [[ "$output" == *"ok    kata seccomp: lea's profile applied in the guest (disable_guest_seccomp = false in $e)"* ]]
  [[ "$output" == *"warn  kata seccomp: $c does not set disable_guest_seccomp = false"* ]]
  # A ConfigPath in the registration's options is the file Kata reads.
  o="$BATS_TEST_TMPDIR/other.toml"; printf 'disable_guest_seccomp = true\n' >"$o"
  export "FAKE_DOCKER_OUT_$k={\"runtimeType\":\"io.containerd.kata.v2\",\"options\":{\"ConfigPath\":\"$o\"}}"
  run lea_doctor
  [[ "$output" == *"warn  kata seccomp: $o does not set disable_guest_seccomp = false"* ]]
  [[ "$output" != *"$e"* ]]
  export "FAKE_DOCKER_OUT_$k={\"runtimeType\":\"io.containerd.kata.v2\",\"options\":{\"ConfigPath\":\"$BATS_TEST_TMPDIR/gone.toml\"}}"
  run lea_doctor
  [[ "$output" == *"warn  kata seccomp: $BATS_TEST_TMPDIR/gone.toml does not exist (ConfigPath of its registration)"* ]]
}

@test "runtime seccomp: says nothing for runc or an unreadable registration (Podman) (#106)" {
  export "FAKE_DOCKER_OUT_$(fake_docker_key info --format '{{.DefaultRuntime}}')=runc"
  export "FAKE_DOCKER_OUT_$(fake_docker_key info --format '{{json (index .Runtimes "runc")}}')={\"path\":\"runc\"}"
  run lea_doctor
  [[ "$output" != *"runc seccomp"* ]]
  export LEA_RUNTIME=kata
  export "FAKE_DOCKER_OUT_$(fake_docker_key info --format '{{range $k, $v := .Runtimes}}{{$k}} {{end}}')=runc kata "
  run lea_doctor
  [[ "$output" != *"kata seccomp"* ]]
}

@test "without the deck (LEA_DECK=0) a missing or old tmux is no warning (#135)" {
  no_tmux
  export LEA_DECK=0 LEA_TOOL=claude
  run lea_doctor
  [[ "$output" == *"ok    deck off (LEA_DECK=0): lea runs claude in the lea container"* ]]
  [[ "$output" == *"ok    tmux not needed without the deck"* ]]
  [[ "$output" != *"FAIL  tmux"* ]] && [[ "$output" != *"warn  tmux"* ]]
  fake_tmux "tmux 3.0"
  run lea_doctor
  [[ "$output" == *"ok    tmux not needed without the deck"* ]]
  [[ "$output" != *"FAIL  tmux"* ]]
  export FAKE_TMUX_VERSION="tmux 3.4"
  run lea_doctor
  [[ "$output" == *"ok    tmux (tmux 3.4, host)"* ]]
}

@test "without the deck, doctor says whether lea's notifications can show (#135)" {
  export LEA_DECK=0 LEA_TOOL=claude
  mkdir -p "$BATS_TEST_TMPDIR/nbin"
  printf '#!/bin/sh\nexit 0\n' >"$BATS_TEST_TMPDIR/nbin/notify-send"; chmod +x "$BATS_TEST_TMPDIR/nbin/notify-send"
  PATH="$BATS_TEST_TMPDIR/nbin:$PATH" run lea_doctor
  [[ "$output" == *"ok    notifications (notify-send)"* ]]
  # With LEA_CTL=1 the daemon runs in lea-ctl, where neither notifier exists.
  LEA_CTL=1 PATH="$BATS_TEST_TMPDIR/nbin:$PATH" run lea_doctor
  [[ "$output" == *"warn  notifications: LEA_CTL=1 runs lea's daemon in lea-ctl, which shows no desktop notification without the deck"* ]]
  [[ "$output" != *"ok    notifications"* ]]
  # With the deck there is no notification to check.
  LEA_DECK=1 LEA_CTL=1 run lea_doctor
  [[ "$output" != *"notifications"* ]]
}

@test "without the deck and no notifier, doctor warns (#135)" {
  if command -v notify-send >/dev/null 2>&1 || command -v osascript >/dev/null 2>&1; then skip "a notifier is installed in the test image"; fi
  export LEA_DECK=0 LEA_TOOL=claude
  run lea_doctor
  [[ "$output" == *"warn  notifications: no notify-send nor osascript"* ]]
}
