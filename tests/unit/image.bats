load helpers
setup() { setup_fakes; I="$LEA_ROOT/image"; }

@test "containers.conf gives nested containers HTTPS_PROXY only" {
  grep -q '"HTTPS_PROXY=http://@LEA_PROXY_IP@:3128"' "$I/containers.conf.tmpl"
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

@test "seccomp profile is the vendored containers/common v0.64.2 file" {
  [ "$(lea_sha256_file "$I/seccomp.json")" = "$(cat "$I/seccomp.json.sha256")" ]
  [ "$(jq -r .defaultAction "$I/seccomp.json")" = SCMP_ACT_ERRNO ]
}

@test "Containerfile pins every version" {
  c="$I/Containerfile"
  grep -q '^FROM quay.io/podman/stable:v5.8.7-immutable$' "$c"
  grep -q '^ARG CLAUDE_CODE_VERSION=2.1.296$' "$c"
  grep -q '^ARG RTK_VERSION=0.36.0$' "$c"
  grep -q ' V=0.8.0 ' "$c"
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
  [ "$(jq -r '.permissions // "none"' "$I/managed-settings.json")" = none ]
}

@test "Containerfile installs the locksql client of a console in another container" {
  c="$I/Containerfile"
  grep -q 'useradd -r -u 10001 -U -M -s /sbin/nologin locksql' "$c"
  grep -q "'socket_dir   = \"/run/locksql\"'" "$c"
  grep -q 'A="locksql_${V}_linux_${TARGETARCH}.tar.gz"' "$c"
  grep -q 'checksums.txt" | grep " $A\\$" | sha256sum -c' "$c"
  grep -q 'install -d -m 2710 -o 10001 -g 10000 /run/locksql' "$c"
  ! grep -q 'LOCKSQL_REF\|COPY --from=locksql' "$c"
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
  ! grep -q 'mv ' "$u"
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

@test "lea's Claude hooks live in a --settings file, not in managed settings" {
  s="$I/claude-settings.json"
  for e in SessionStart UserPromptSubmit PreToolUse PostToolUse PermissionRequest Notification SubagentStart SubagentStop Stop StopFailure SessionEnd; do
    jq -e --arg e "$e" '.hooks[$e][0].hooks[0].command | contains("/usr/local/bin/lea hook claude " + $e)' "$s" >/dev/null
  done
  [ "$(jq -r '.hooks.PermissionRequest[0].hooks[0].timeout' "$s")" = 620 ]
  # The usage-limit resume job (#20) runs in the background and wakes the model on exit 2.
  jq -e '.hooks.StopFailure[0].hooks[1] | (.command | contains("/usr/local/bin/lea hook claude LimitWait")) and .asyncRewake == true and .timeout == 691200' "$s" >/dev/null
  # Its SessionStart twin (#83) takes a pending resume over after a container restart.
  jq -e '.hooks.SessionStart[0].hooks[1] | (.command | contains("/usr/local/bin/lea hook claude LimitResume")) and .asyncRewake == true and .timeout == 691200' "$s" >/dev/null
  [ "$(jq -r '.hooks // "none"' "$I/managed-settings.json")" = none ]
  grep -q '^COPY image/claude-settings.json /usr/local/share/lea/claude-settings.json$' "$I/Containerfile"
  grep -q 'settings=${LEA_CLAUDE_SETTINGS:-/usr/local/share/lea/claude-settings.json}' "$I/lea-session"
}

# lea's Claude plugin comes with the image: a manifest named lea and the control-tower skill.
@test "the image ships lea's Claude plugin with the control-tower skill" {
  p="$I/claude-plugin"
  [ "$(jq -r .name "$p/.claude-plugin/plugin.json")" = lea ]
  k="$p/skills/control-tower/SKILL.md"
  [ "$(sed -n 1p "$k")" = --- ]
  grep -q '^name: control-tower$' "$k"
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
  grep -q 's/@LEA_VERSION@/${LEA_VERSION}/" /etc/claude-code/managed-settings.json /usr/local/share/lea/claude-settings.json' "$I/Containerfile"
}

@test "lea's Claude settings run lea statusline as the status line, guarded on the core binary" {
  s="$I/claude-settings.json"
  jq -e '.statusLine.type == "command"' "$s" >/dev/null
  jq -e '.statusLine.command | startswith("[ -x /usr/local/bin/lea ]") and contains("/usr/local/bin/lea statusline") and endswith("exit 0")' "$s" >/dev/null
}

# Moved from the retired core.bats.
@test "lea's Claude settings register every hook guarded on the core binary, permission timeout above the approval maximum" {
  f="$LEA_ROOT/image/claude-settings.json"
  for e in SessionStart UserPromptSubmit PreToolUse PostToolUse PermissionRequest Notification SubagentStart SubagentStop Stop SessionEnd; do
    jq -e --arg e "$e" '.hooks[$e][0].hooks[0].command | contains("lea hook claude " + $e) and startswith("[ -x /usr/local/bin/lea ]") and endswith("exit 0")' "$f" >/dev/null
  done
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
  [ -d "$r/run/user/$u" ] && [ "$(stat -c %a "$r/run/user/$u")" = 700 ]
  [ -z "$(ls -A "$r/run/user/$u")" ]
  [ ! -e "$r/run/lea/agents" ] && [ ! -e "$r/run/lea/shells" ] && [ ! -e "$r/run/lea/ready" ]
  # The bus directory is the daemon's bind mount: never touched.
  [ -f "$r/run/lea/bus/keep" ]
}
