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

@test "locksql: its image and lox up's lock are checked with LEA_LOCKSQL=1, never with 0" {
  export FAKE_DOCKER_RC_IMAGE_INSPECT_AT_LOCKSQL_0_11_5=1 FAKE_DOCKER_RC_VOLUME_INSPECT=1
  run lea_doctor
  [[ "$output" == *"warn  locksql image: locksql:0.11.5 absent: run lea init (or set LEA_LOCKSQL=0)"* ]]
  [[ "$output" == *"warn  locksql lock: no socket volume of a locksql lock of lox up for $LEA_PROJECTS_DIR"*"lox add locksql NAME"*"run \`lox up\`"* ]]
  [[ "$output" != *"locksql-run"* ]]
  grep -qF "docker [volume] [ls] [--filter] [label=dev.lockfamily.role=socket] [--filter] [label=dev.lockfamily.type=locksql] [--filter] [label=dev.lockfamily.workspace=$LEA_PROJECTS_DIR] [--format] [{{.Name}}]" "$FAKE_LOG"
  unset FAKE_DOCKER_RC_IMAGE_INSPECT_AT_LOCKSQL_0_11_5
  export FAKE_DOCKER_OUT_VOLUME_LS=lockfamily-1a2b3c4d_crm-run
  run lea_doctor
  [[ "$output" == *"ok    locksql image locksql:0.11.5"* ]]
  [[ "$output" == *"ok    locksql lock of lox up (socket volume lockfamily-1a2b3c4d_crm-run)"* ]]
  export FAKE_DOCKER_OUT_VOLUME_LS='lockfamily-1a2b3c4d_a-run
lockfamily-1a2b3c4d_b-run'
  run lea_doctor
  [[ "$output" == *"warn  locksql lock: lox up runs 2 locksql locks"* ]]
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

@test "a non-isolated internal network is a warning" {
  fake_tmux
  export FAKE_DOCKER_OUT_NETWORK_LS=lea-internal-abc
  export FAKE_DOCKER_OUT_NETWORK_INSPECT='true nat'
  run lea_doctor
  [ "$status" -eq 0 ]
  [[ "$output" == *"warn  lea-internal-abc isolated from the host"* ]]
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
  [[ "$output" == *"ok    lea-internal-abc isolated from the host"* ]]
  [[ "$output" == *"ok    lea-internal-def isolated from the host"* ]]
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
  export FAKE_DOCKER_OUT_INSPECT___FORMAT='0 0'
  run lea_doctor
  [[ "$output" == *"warn  lea-abc limits: no CPU and memory limit: run lea in its projects directory to apply LEA_CPUS and LEA_MEMORY"* ]]
  export FAKE_DOCKER_OUT_INSPECT___FORMAT='16500000000 0'
  run lea_doctor
  [[ "$output" == *"warn  lea-abc limits: no memory limit"* ]]
  export FAKE_DOCKER_OUT_INSPECT___FORMAT='16500000000 17179869184'
  run lea_doctor
  [[ "$output" == *"ok    lea-abc limits: 16.5 CPUs, 16384 MiB"* ]]
  LEA_CPUS=0 FAKE_DOCKER_OUT_INSPECT___FORMAT='0 17179869184' run lea_doctor
  [[ "$output" == *"ok    lea-abc limits: no limit on CPUs, 16384 MiB"* ]]
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
