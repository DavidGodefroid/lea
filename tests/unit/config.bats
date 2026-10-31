load helpers
setup() { setup_fakes; load_lea; U="$HOME/.config/lea/lea.conf"; }

@test "the user config at ~/.config/lea/lea.conf is loaded without LEA_CONFIG" {
  mkdir -p "${U%/*}"; printf 'LEA_IMAGE=lea:dev\n' >"$U"
  lea_load_config
  [ "$LEA_IMAGE" = "lea:dev" ]
}

@test "XDG_CONFIG_HOME moves the user config" {
  export XDG_CONFIG_HOME="$BATS_TEST_TMPDIR/xdg"
  mkdir -p "$XDG_CONFIG_HOME/lea"; printf 'LEA_IMAGE=lea:xdg\n' >"$XDG_CONFIG_HOME/lea/lea.conf"
  [ "$(lea_user_config_path)" = "$XDG_CONFIG_HOME/lea/lea.conf" ]
  lea_load_config
  [ "$LEA_IMAGE" = "lea:xdg" ]
}

@test "LEA_CONFIG replaces the default user config path" {
  mkdir -p "${U%/*}"; printf 'LEA_IMAGE=lea:default\n' >"$U"
  printf 'LEA_IMAGE=lea:explicit\n' >"$BATS_TEST_TMPDIR/t.conf"
  export LEA_CONFIG="$BATS_TEST_TMPDIR/t.conf"
  [ "$(lea_user_config_path)" = "$BATS_TEST_TMPDIR/t.conf" ]
  lea_load_config
  [ "$LEA_IMAGE" = "lea:explicit" ]
}

@test "a missing user config is not an error" {
  run lea_load_config
  [ "$status" -eq 0 ]
}

@test "LEA_PROJECTS_DIR comes from the user config" {
  mkdir -p "${U%/*}"; printf "LEA_PROJECTS_DIR='/srv/p'\n" >"$U"
  lea_load_config
  [ "$LEA_PROJECTS_DIR" = /srv/p ]
}

@test "LEA_PROJECTS_DIR from the environment beats the user config" {
  mkdir -p "${U%/*}"; printf "LEA_PROJECTS_DIR='/srv/p'\n" >"$U"
  export LEA_PROJECTS_DIR=/srv/env
  lea_load_config
  [ "$LEA_PROJECTS_DIR" = /srv/env ]
}

@test "user config set adds a key and keeps the other lines" {
  mkdir -p "${U%/*}"; printf '# mine\nLEA_IMAGE=x\n' >"$U"
  lea_user_config_set LEA_PROJECTS_DIR "/srv/it's"
  lea_user_config_set LEA_IMAGE lea:dev
  run cat "$U"
  [ "${lines[0]}" = "# mine" ]
  [[ "$output" == *"LEA_IMAGE='lea:dev'"* ]]
  [[ "$output" != *"LEA_IMAGE=x"* ]]
  lea_load_config
  [ "$LEA_PROJECTS_DIR" = "/srv/it's" ]
  [ "$LEA_IMAGE" = "lea:dev" ]
}

@test "user config set creates the file, private" {
  lea_user_config_set LEA_PROJECTS_DIR /srv/p
  [ -f "$U" ]
  [ "$(stat -c %a "$U")" = 600 ]
}

@test "user config set refuses a symlinked config file" {
  mkdir -p "${U%/*}"; ln -s "$BATS_TEST_TMPDIR/elsewhere" "$U"
  run lea_user_config_set LEA_PROJECTS_DIR /srv/p
  [ "$status" -eq 1 ]
  [ ! -e "$BATS_TEST_TMPDIR/elsewhere" ]
}

@test "LEA_APPROVALS and LEA_APPROVAL_TIMEOUT: deck and 60 by default, the user config, then the environment" {
  lea_load_config
  [ "$LEA_APPROVALS" = deck ]
  [ "$LEA_APPROVAL_TIMEOUT" = 60 ]
  mkdir -p "${U%/*}"; printf 'LEA_APPROVALS=pane\nLEA_APPROVAL_TIMEOUT=120\n' >"$U"
  LEA_APPROVALS=; LEA_APPROVAL_TIMEOUT=
  lea_load_config
  [ "$LEA_APPROVALS" = pane ]
  [ "$LEA_APPROVAL_TIMEOUT" = 120 ]
  export LEA_APPROVALS=deck LEA_APPROVAL_TIMEOUT=30
  lea_load_config
  [ "$LEA_APPROVALS" = deck ]
  [ "$LEA_APPROVAL_TIMEOUT" = 30 ]
}

@test "bad LEA_APPROVALS or LEA_APPROVAL_TIMEOUT values are refused" {
  export LEA_APPROVALS=always
  run lea_load_config
  [ "$status" -eq 1 ]
  [[ "$output" == *"LEA_APPROVALS must be deck or pane"* ]]
  export LEA_APPROVALS=deck
  for v in 0 601 1m 9999 -5 060 007 0600; do
    export LEA_APPROVAL_TIMEOUT=$v
    run lea_load_config
    [ "$status" -eq 1 ]
    [[ "$output" == *"LEA_APPROVAL_TIMEOUT must be a number of seconds from 1 to 600"* ]]
  done
}

@test "LEA_LAUNCHER is ignored, whatever its value and wherever it is set" {
  run grep -q LEA_LAUNCHER "$LEA_ROOT/config/lea.conf"
  [ "$status" -eq 1 ]
  for v in go shell rust; do
    LEA_LAUNCHER=$v run lea_load_config
    [ "$status" -eq 0 ]
    mkdir -p "${U%/*}"; printf 'LEA_LAUNCHER=%s\n' "$v" >"$U"
    lea_load_config
  done
}

@test "LEA_GIT_SSH: empty by default, git over HTTPS needs no endpoint" {
  lea_load_config
  [ -z "$LEA_GIT_SSH" ]
  run sh -c 'printenv LEA_GITLAB_SSH LEA_REGISTRY LEA_GITLAB_KNOWN_HOSTS'
  [ "$status" -ne 0 ]
  [ -z "$output" ]
}

@test "LEA_GIT_SSH accepts space-separated host:port endpoints, from the environment or the user config" {
  export LEA_GIT_SSH='github.com:22  gitlab.example.org:2222'
  lea_load_config
  [ "$LEA_GIT_SSH" = 'github.com:22 gitlab.example.org:2222' ]
  [ "$(sh -c 'printenv LEA_GIT_SSH')" = 'github.com:22 gitlab.example.org:2222' ]
  unset LEA_GIT_SSH
  mkdir -p "${U%/*}"; printf 'LEA_GIT_SSH="github.com:22"\n' >"$U"
  lea_load_config
  [ "$LEA_GIT_SSH" = github.com:22 ]
}

@test "LEA_GIT_SSH refuses endpoints that are not plain host:port" {
  for v in 'github.com' 'github.com:' ':22' 'git/hub:22' 'git hub.com:22x' 'h:0' 'h:022' 'h:65536' 'h:1234567' 'evil$(id):22' 'h*:22'; do
    LEA_GIT_SSH=$v run lea_load_config
    [ "$status" -eq 1 ]
    [[ "$output" == *"LEA_GIT_SSH must list host:port endpoints with plain host names"* ]]
  done
  export LEA_GIT_SSH='h:1 k:65535'
  lea_load_config
}

@test "LEA_GIT_SSH refuses a host listed twice" {
  LEA_GIT_SSH='github.com:22 github.com:443' run lea_load_config
  [ "$status" -eq 1 ]
  [[ "$output" == *"LEA_GIT_SSH lists github.com twice"* ]]
}

@test "LEA_GIT_SSH lists at most 16 endpoints" {
  v=; for n in $(seq 1 16); do v="$v h$n:22"; done
  export LEA_GIT_SSH="$v"
  lea_load_config
  export LEA_GIT_SSH="$v h17:22"
  run lea_load_config
  [ "$status" -eq 1 ]
  [[ "$output" == *"LEA_GIT_SSH lists 17 endpoints, at most 16"* ]]
}

@test "LEA_GITLAB_SSH is read when LEA_GIT_SSH is unset, and is then dropped" {
  export LEA_GITLAB_SSH=gitlab.com:22
  lea_load_config
  [ "$LEA_GIT_SSH" = gitlab.com:22 ]
  [ -z "${LEA_GITLAB_SSH:-}" ]
  unset LEA_GIT_SSH
  mkdir -p "${U%/*}"; printf 'LEA_GITLAB_SSH=gitlab.example.org:2222\n' >"$U"
  lea_load_config
  [ "$LEA_GIT_SSH" = gitlab.example.org:2222 ]
  rm "$U"
  export LEA_GIT_SSH=github.com:22 LEA_GITLAB_SSH=gitlab.com:22
  lea_load_config
  [ "$LEA_GIT_SSH" = github.com:22 ]
  unset LEA_GIT_SSH; export LEA_GITLAB_SSH='bad/host:22'
  run lea_load_config
  [ "$status" -eq 1 ]
}

@test "LEA_GIT_KNOWN_HOSTS: git_known_hosts by default, else LEA_GITLAB_KNOWN_HOSTS, else an existing gitlab_known_hosts" {
  c="$HOME/.config/lea"
  kh() { unset LEA_GIT_KNOWN_HOSTS LEA_GITLAB_KNOWN_HOSTS; "$@"; lea_load_config; }
  kh :
  [ "$LEA_GIT_KNOWN_HOSTS" = "$c/git_known_hosts" ]
  mkdir -p "$c"; : >"$c/gitlab_known_hosts"
  kh :
  [ "$LEA_GIT_KNOWN_HOSTS" = "$c/gitlab_known_hosts" ]
  : >"$c/git_known_hosts"
  kh :
  [ "$LEA_GIT_KNOWN_HOSTS" = "$c/git_known_hosts" ]
  kh export LEA_GITLAB_KNOWN_HOSTS=/x/old
  [ "$LEA_GIT_KNOWN_HOSTS" = /x/old ]
  [ -z "${LEA_GITLAB_KNOWN_HOSTS:-}" ]
  kh export LEA_GIT_KNOWN_HOSTS=/x/new LEA_GITLAB_KNOWN_HOSTS=/x/old
  [ "$LEA_GIT_KNOWN_HOSTS" = /x/new ]
  kh export XDG_CONFIG_HOME="$BATS_TEST_TMPDIR/xdg"
  [ "$LEA_GIT_KNOWN_HOSTS" = "$BATS_TEST_TMPDIR/xdg/lea/git_known_hosts" ]
}

@test "LEA_LOCKSQL is 1 by default, 0 or 1 only" {
  lea_load_config
  [ "$LEA_LOCKSQL" = 1 ]
  [ "$LEA_LOCKSQL_IMAGE" = locksql:0.11.4 ]
  [ "$LEA_LOCKSQL_REF" = v0.11.4 ]
  mkdir -p "${U%/*}"; printf "LEA_LOCKSQL='0'\n" >"$U"
  lea_load_config
  [ "$LEA_LOCKSQL" = 0 ]
  printf 'LEA_LOCKSQL=yes\n' >"$U"
  run lea_load_config
  [ "$status" -eq 1 ]
  [[ "$output" == *"LEA_LOCKSQL must be 1 (wire lea for a locksql console) or 0, not 'yes'"* ]]
}

@test "LEA_AUTOUPDATE is on by default, 0 turns it off, any other value is refused" {
  lea_load_config
  [ "$LEA_AUTOUPDATE" = 1 ]
  mkdir -p "${U%/*}"; printf 'LEA_AUTOUPDATE=0\n' >"$U"
  lea_load_config
  [ "$LEA_AUTOUPDATE" = 0 ]
  printf 'LEA_AUTOUPDATE=yes\n' >"$U"
  run lea_load_config
  [ "$status" -eq 1 ]
  [[ "$output" == *"LEA_AUTOUPDATE must be 1 (check for a new release every day) or 0, not 'yes'"* ]]
}

@test "LEA_AUTOUPDATE_SKIP is empty or a release version" {
  lea_load_config
  [ -z "${LEA_AUTOUPDATE_SKIP:-}" ]
  mkdir -p "${U%/*}"; printf "LEA_AUTOUPDATE_SKIP='v1.2.3'\n" >"$U"
  lea_load_config
  [ "$LEA_AUTOUPDATE_SKIP" = v1.2.3 ]
  printf 'LEA_AUTOUPDATE_SKIP=latest\n' >"$U"
  run lea_load_config
  [ "$status" -eq 1 ]
  [[ "$output" == *"LEA_AUTOUPDATE_SKIP must be empty or a release version (vX.Y.Z, set by lea rollback), not 'latest'"* ]]
}

@test "LEA_EGRESS_ALLOW: '*' (open egress) is accepted alone, never with domain names" {
  export LEA_EGRESS_ALLOW='*'
  lea_load_config
  [ "$LEA_EGRESS_ALLOW" = '*' ]
  LEA_EGRESS_ALLOW='*,github.com' run lea_load_config
  [ "$status" -eq 1 ]
  [[ "$output" == *"LEA_EGRESS_ALLOW: '*' opens the web to any domain and cannot be combined with domain names"* ]]
}

@test "LEA_EGRESS_ALLOW: empty by default (lea's default allowlist), normalized to lower-case comma-separated names" {
  lea_load_config
  [ -z "$LEA_EGRESS_ALLOW" ]
  export LEA_EGRESS_ALLOW='GitHub.com, api.anthropic.com  proxy.golang.org,'
  lea_load_config
  [ "$LEA_EGRESS_ALLOW" = 'github.com,api.anthropic.com,proxy.golang.org' ]
  [ "$(sh -c 'printenv LEA_EGRESS_ALLOW')" = 'github.com,api.anthropic.com,proxy.golang.org' ]
}

@test "LEA_EGRESS_ALLOW refuses entries that are not domain names" {
  for v in 'git/hub.com' '.github.com' 'github.com.' 'a..b' '10.0.0.1' '*.github.com' 'evil$(id)' 'h_x.com'; do
    LEA_EGRESS_ALLOW=$v run lea_load_config
    [ "$status" -eq 1 ]
    [[ "$output" == *"LEA_EGRESS_ALLOW must list domain names separated by commas, got '$v'"* ]]
  done
}

@test "LEA_EGRESS_ALLOW lists at most 64 domains" {
  v=; for n in $(seq 1 64); do v="$v,d$n.example"; done
  export LEA_EGRESS_ALLOW="$v"
  lea_load_config
  export LEA_EGRESS_ALLOW="$v,d65.example"
  run lea_load_config
  [ "$status" -eq 1 ]
  [[ "$output" == *"LEA_EGRESS_ALLOW lists 65 domains, at most 64"* ]]
}

@test "LEA_OTEL_UPSTREAM is empty or host:port with a plain host name" {
  mkdir -p "${U%/*}"; printf "LEA_OTEL_UPSTREAM='otel.corp.example:4317'\n" >"$U"
  lea_load_config
  [ "$LEA_OTEL_UPSTREAM" = otel.corp.example:4317 ]
  for v in '1.2.3.4,fork:80' 'h:80,fork' 'h;x:80' 'h:080' 'h:0' 'h:65536' 'h' ':80'; do
    printf "LEA_OTEL_UPSTREAM='%s'\n" "$v" >"$U"
    run lea_load_config
    [ "$status" -eq 1 ]
    [[ "$output" == *"LEA_OTEL_UPSTREAM must be host:port with a plain host name, not '$v'"* ]]
  done
}
