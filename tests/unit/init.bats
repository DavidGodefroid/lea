load helpers
setup() {
  setup_fakes; load_lea
  export LEA_INIT_NONINTERACTIVE=1
  export LEA_GIT_KNOWN_HOSTS="$BATS_TEST_TMPDIR/known_hosts"
  ssh-keygen -q -t ed25519 -N '' -f "$BATS_TEST_TMPDIR/hostkey"
  ssh-keygen -q -t ed25519 -N '' -f "$BATS_TEST_TMPDIR/hostkey2"
  printf 'github.com %s\n' "$(cut -d' ' -f1,2 "$BATS_TEST_TMPDIR/hostkey.pub")" >"$LEA_GIT_KNOWN_HOSTS"
  printf 'gitlab.com %s\n' "$(cut -d' ' -f1,2 "$BATS_TEST_TMPDIR/hostkey2.pub")" >>"$LEA_GIT_KNOWN_HOSTS"
  export LEA_BIN_DIR="$BATS_TEST_TMPDIR/bin"
  P="$BATS_TEST_TMPDIR/projects"; mkdir -p "$P"
  export LEA_PROJECTS_DIR="$P"
  U="$HOME/.config/lea/lea.conf"
  echo '{"a":1}' >"$HOME/.claude.json"
  S=$(lea_state_dir); H=$(lea_host_dir)
}
# interactive: lea init as on a terminal (the guided locksql steps need one).
interactive() { unset LEA_INIT_NONINTERACTIVE; lea__init_tty() { :; }; }
# git_ssh ENDPOINTS: LEA_GIT_SSH for this test, loaded (and checked) as lea loads it.
git_ssh() { export LEA_GIT_SSH="$1"; lea_load_config; }

@test "init without LEA_GIT_SSH: no key, no known_hosts, claude.json copied" {
  : >"$LEA_GIT_KNOWN_HOSTS"
  run lea_init </dev/null
  [ "$status" -eq 0 ]
  [[ "$output" == *"ready. Run: lea"* ]]
  [[ "$output" != *"public key"* ]]
  [ ! -e "$S/ssh/id_ed25519" ]
  [ ! -e "$S/ssh/known_hosts" ]
  [ "$(cat "$S/claude.json")" = '{"a":1}' ]
}

@test "init never asks for a token, never writes one, never calls curl" {
  git_ssh 'github.com:22'
  run lea_init </dev/null
  [ "$status" -eq 0 ]
  [ ! -e "$S/gitlab" ]
  [ ! -e "$S/containers/auth.json" ]
  [[ "$output" != *token* ]]
  run calls
  [[ "$output" != *curl* ]]
}

@test "init creates the state dirs of git, gh and glab, and no gitlab dir" {
  lea_init </dev/null 2>/dev/null
  for d in ssh git gh containers glab-cli go kube claude; do [ -d "$S/$d" ]; done
  [ ! -e "$S/gitlab" ]
}

@test "init with two endpoints pins both hosts, generates the key and prints it" {
  git_ssh 'github.com:22 gitlab.com:2222'
  run lea_init </dev/null
  [ "$status" -eq 0 ]
  [ -f "$S/ssh/id_ed25519" ]
  [ -f "$S/ssh/id_ed25519.pub" ]
  grep -q '^github.com ssh-ed25519 ' "$S/ssh/known_hosts"
  grep -q '^gitlab.com ssh-ed25519 ' "$S/ssh/known_hosts"
  [ "$(wc -l <"$S/ssh/known_hosts")" -eq 2 ]
  [[ "$output" == *"lea's public key: add it to your SSH keys on github.com gitlab.com:"*"$(cat "$S/ssh/id_ed25519.pub")"* ]]
}

@test "init refuses when one endpoint host has no pinned key, naming it" {
  git_ssh 'github.com:22 git.example.org:22'
  run lea_init </dev/null
  [ "$status" -eq 1 ]
  [[ "$output" == *"no git.example.org key in $LEA_GIT_KNOWN_HOSTS: pin the host keys of git.example.org there (see the README, SSH)"* ]]
  [ ! -e "$S/ssh/known_hosts" ]
  [ ! -e "$S/ssh/id_ed25519" ]
}

@test "init is idempotent and keeps the existing key" {
  git_ssh 'github.com:22'
  lea_init </dev/null 2>/dev/null
  k=$(cat "$S/ssh/id_ed25519.pub")
  run lea_init </dev/null
  [ "$status" -eq 0 ]
  [ "$(cat "$S/ssh/id_ed25519.pub")" = "$k" ]
  [[ "$output" == *"lea's public key"*"$k"* ]]
  [[ "$output" != *"generating"* ]]
}

@test "the pinned host keys default to ~/.config/lea/git_known_hosts, and the host known_hosts is not read" {
  mkdir -p "$HOME/.config/lea"; mv "$LEA_GIT_KNOWN_HOSTS" "$HOME/.config/lea/git_known_hosts"
  unset LEA_GIT_KNOWN_HOSTS; git_ssh 'github.com:22'
  [ "$LEA_GIT_KNOWN_HOSTS" = "$HOME/.config/lea/git_known_hosts" ]
  mkdir -p "$HOME/.ssh"; : >"$HOME/.ssh/known_hosts"; chmod 000 "$HOME/.ssh/known_hosts"
  lea_init </dev/null 2>/dev/null
  grep -q '^github.com ssh-ed25519 ' "$S/ssh/known_hosts"
}

@test "an existing ~/.config/lea/gitlab_known_hosts is still read when git_known_hosts is absent" {
  mkdir -p "$HOME/.config/lea"; mv "$LEA_GIT_KNOWN_HOSTS" "$HOME/.config/lea/gitlab_known_hosts"
  unset LEA_GIT_KNOWN_HOSTS; git_ssh 'gitlab.com:22'
  [ "$LEA_GIT_KNOWN_HOSTS" = "$HOME/.config/lea/gitlab_known_hosts" ]
  lea_init </dev/null 2>/dev/null
  grep -q '^gitlab.com ssh-ed25519 ' "$S/ssh/known_hosts"
}

@test "--no-locksql and --locksql record LEA_LOCKSQL in the user config" {
  lea_init --no-locksql </dev/null 2>/dev/null
  grep -qx "LEA_LOCKSQL='0'" "$U"
  run calls
  [[ "$output" != *"locksql"* ]]
  lea_init --locksql </dev/null 2>/dev/null
  grep -qx "LEA_LOCKSQL='1'" "$U"
  run grep -c '^LEA_LOCKSQL=' "$U"
  [ "$output" = 1 ]
}

@test "init refuses an unknown argument before doing anything" {
  run lea_init --gitlab
  [ "$status" -eq 1 ]
  [[ "$output" == *"usage: lea init [--no-locksql | --locksql]"* ]]
  [ ! -e "$S" ]
  [ ! -e "$U" ]
}

@test "a missing locksql image is built from locksql's repository at LEA_LOCKSQL_REF" {
  export FAKE_DOCKER_RC_IMAGE_INSPECT_AT_LOCKSQL_0_8_0=1
  run lea_init </dev/null
  [ "$status" -eq 0 ]
  run calls
  [[ "$output" == *"docker [image] [inspect] [locksql:0.8.0]"* ]]
  [[ "$output" == *"docker [build] [-t] [locksql:0.8.0] [https://github.com/DavidGodefroid/locksql.git#v0.8.0]"* ]]
}

@test "a present locksql image is not rebuilt" {
  lea_init </dev/null 2>/dev/null
  run calls
  [[ "$output" == *"docker [image] [inspect] [locksql:0.8.0]"* ]]
  [[ "$output" != *"docker [build]"* ]]
}

@test "a failing locksql build is only a warning" {
  export FAKE_DOCKER_RC_IMAGE_INSPECT_AT_LOCKSQL_0_8_0=1 FAKE_DOCKER_RC_BUILD__T=1
  run lea_init </dev/null
  [ "$status" -eq 0 ]
  [[ "$output" == *"warning: could not build locksql:0.8.0"* ]]
  [[ "$output" == *"ready. Run: lea"* ]]
}

@test "LEA_LOCKSQL=0 skips the locksql image" {
  lea_user_config_set LEA_LOCKSQL 0; lea_load_config
  export FAKE_DOCKER_RC_IMAGE_INSPECT_AT_LOCKSQL_0_8_0=1
  run lea_init </dev/null
  [ "$status" -eq 0 ]
  run calls
  [[ "$output" != *locksql* ]]
}

@test "init links the launcher into ~/.local/bin" {
  lea_init </dev/null 2>/dev/null
  [ "$(readlink "$LEA_BIN_DIR/lea")" = "$LEA_ROOT/bin/lea" ]
}

@test "init keeps a foreign ~/.local/bin/lea and says so" {
  mkdir -p "$LEA_BIN_DIR"; echo mine >"$LEA_BIN_DIR/lea"
  run lea_init </dev/null
  [ "$status" -eq 0 ]
  [ "$(cat "$LEA_BIN_DIR/lea")" = mine ]
  [[ "$output" == *"$LEA_BIN_DIR/lea exists and is not the lea launcher"* ]]
}

@test "init warns when the launcher dir is not on PATH" {
  run lea_init </dev/null
  [[ "$output" == *"$LEA_BIN_DIR is not on your PATH"* ]]
  PATH="$LEA_BIN_DIR:$PATH" run lea_init </dev/null
  [[ "$output" != *"is not on your PATH"* ]]
}

@test "non-interactive init records LEA_PROJECTS_DIR in the user config" {
  lea_init </dev/null 2>/dev/null
  grep -qx "LEA_PROJECTS_DIR='$P'" "$U"
}

@test "interactive init asks for the projects dir, defaulting to the current value" {
  interactive
  mkdir -p "$BATS_TEST_TMPDIR/other"
  printf '%s\n' "$BATS_TEST_TMPDIR/other" >"$BATS_TEST_TMPDIR/a2"
  run lea_init <"$BATS_TEST_TMPDIR/a2"
  [ "$status" -eq 0 ]
  [[ "$output" == *"Projects directory [$P]: "* ]]
  grep -qx "LEA_PROJECTS_DIR='$BATS_TEST_TMPDIR/other'" "$U"
  printf '\n' >"$BATS_TEST_TMPDIR/a3"
  unset LEA_PROJECTS_DIR; lea_load_config
  run lea_init <"$BATS_TEST_TMPDIR/a3"
  grep -qx "LEA_PROJECTS_DIR='$BATS_TEST_TMPDIR/other'" "$U"
}

@test "without a current value the projects dir defaults to the working directory" {
  unset LEA_PROJECTS_DIR
  cd "$P"
  lea_init </dev/null 2>/dev/null
  grep -qx "LEA_PROJECTS_DIR='$P'" "$U"
}

@test "init refuses a guarded projects dir before any secret or sudo" {
  export FAKE_DOCKER_OUT_INFO___FORMAT='["name=apparmor"]'
  git_ssh 'github.com:22'
  export LEA_PROJECTS_DIR="$HOME"
  run lea_init </dev/null
  [ "$status" -eq 1 ]
  [[ "$output" == *"cannot be a projects directory"* ]]
  [ ! -e "$S/ssh" ]
  run calls
  [[ "$output" != *sudo* ]]
}

@test "init refuses a projects dir that does not exist" {
  export LEA_PROJECTS_DIR="$BATS_TEST_TMPDIR/nope"
  run lea_init </dev/null
  [ "$status" -eq 1 ]
  [[ "$output" == *"not a directory"* ]]
}

@test "AppArmor step is skipped when Docker does not use AppArmor" {
  lea_init_apparmor
  run calls
  [[ "$output" != *sudo* ]]
}

@test "AppArmor profile is installed, with userns on ABI 4.0 hosts" {
  export FAKE_DOCKER_OUT_INFO___FORMAT='["name=apparmor"]'
  mkdir -p "$BATS_TEST_TMPDIR/abi"; : >"$BATS_TEST_TMPDIR/abi/4.0"
  export LEA_APPARMOR_ABI_DIR="$BATS_TEST_TMPDIR/abi"
  lea_init_apparmor 2>/dev/null
  run calls
  [[ "$output" == *"sudo [install] [-m] [0644] [$H/apparmor-lea] [/etc/apparmor.d/lea]"* ]]
  [[ "$output" == *"sudo [apparmor_parser] [-r] [/etc/apparmor.d/lea]"* ]]
  grep -q '^  userns,$' "$H/apparmor-lea"
  grep -q '^abi <abi/4.0>,$' "$H/apparmor-lea"
}

@test "AppArmor profile has no userns rule on ABI 3.0 hosts" {
  export FAKE_DOCKER_OUT_INFO___FORMAT='["name=apparmor"]'
  export LEA_APPARMOR_ABI_DIR="$BATS_TEST_TMPDIR/none"
  lea_init_apparmor 2>/dev/null
  run grep -q 'userns' "$H/apparmor-lea"
  [ "$status" -ne 0 ]
  grep -q '^abi <abi/3.0>,$' "$H/apparmor-lea"
}

@test "AppArmor profile carries the signal-receive rules and the /sys denies" {
  export FAKE_DOCKER_OUT_INFO___FORMAT='["name=apparmor"]'
  export LEA_APPARMOR_ABI_DIR="$BATS_TEST_TMPDIR/none"
  lea_init_apparmor 2>/dev/null
  grep -qF 'signal (receive) peer=unconfined,' "$H/apparmor-lea"
  grep -qF 'signal (receive) peer=runc,' "$H/apparmor-lea"
  grep -qF 'signal (receive) peer=dockerd,' "$H/apparmor-lea"
  grep -qF 'deny /sys/[^f]*/** wklx,' "$H/apparmor-lea"
  grep -qF 'deny /sys/f[^s]*/** wklx,' "$H/apparmor-lea"
  grep -qF 'deny /sys/fs/[^c]*/** wklx,' "$H/apparmor-lea"
  grep -qF 'deny /sys/fs/c[^g]*/** wklx,' "$H/apparmor-lea"
  grep -qF 'deny /sys/fs/cg[^r]*/** wklx,' "$H/apparmor-lea"
  grep -qF 'deny /sys/devices/virtual/powercap/** rwklx,' "$H/apparmor-lea"
}

@test "AppArmor profile is not reinstalled when identical" {
  export FAKE_DOCKER_OUT_INFO___FORMAT='["name=apparmor"]'
  export LEA_APPARMOR_ABI_DIR="$BATS_TEST_TMPDIR/none"
  export LEA_APPARMOR_DIR="$BATS_TEST_TMPDIR/aa"
  mkdir -p "$LEA_APPARMOR_DIR"
  lea_init_apparmor 2>/dev/null
  cp "$H/apparmor-lea" "$LEA_APPARMOR_DIR/lea"
  : >"$FAKE_LOG"
  lea_init_apparmor 2>/dev/null
  run calls
  [[ "$output" != *sudo* ]]
}

@test "init sets restrictive modes" {
  git_ssh 'github.com:22'
  lea_init </dev/null 2>/dev/null
  [ "$(stat -c %a "$S/ssh/id_ed25519")" = 600 ]
  [ "$(stat -c %a "$S/ssh/known_hosts")" = 600 ]
  [ "$(stat -c %a "$S/ssh")" = 700 ]
  [ "$(stat -c %a "$S/claude.json")" = 600 ]
}

@test "init restores the caller's umask" {
  umask 022
  lea_init </dev/null 2>/dev/null
  [ "$(umask)" = 0022 ]
}

@test "known_hosts keeps only the entries of the endpoint hosts" {
  git_ssh 'github.com:22'
  printf 'other.example %s\n' "$(cut -d' ' -f1,2 "$BATS_TEST_TMPDIR/hostkey.pub")" >>"$LEA_GIT_KNOWN_HOSTS"
  lea_init </dev/null 2>/dev/null
  [ "$(wc -l <"$S/ssh/known_hosts")" -eq 1 ]
  run grep -q 'other.example\|gitlab.com' "$S/ssh/known_hosts"
  [ "$status" -ne 0 ]
}

@test "a known_hosts refusal happens before any sudo call" {
  export FAKE_DOCKER_OUT_INFO___FORMAT='["name=apparmor"]'
  git_ssh 'github.com:22'
  : >"$LEA_GIT_KNOWN_HOSTS"
  run lea_init </dev/null
  [ "$status" -eq 1 ]
  run calls
  [[ "$output" != *sudo* ]]
}

@test "init creates the host-only dir next to the state, and no bookkeeping in the state" {
  lea_init </dev/null 2>/dev/null
  [ -d "$H/scan" ]
  [ -d "$H/sessions" ]
  [ ! -e "$S/scan" ]
  [ ! -e "$S/sessions" ]
  [ "$(stat -c %a "$H")" = 700 ]
}

@test "init refuses a symlinked SSH key file and writes nothing through it" {
  git_ssh 'github.com:22'
  mkdir -p "$S/ssh"; ln -s "$BATS_TEST_TMPDIR/planted" "$S/ssh/id_ed25519"
  run lea_init </dev/null
  [ "$status" -eq 1 ]
  [ ! -e "$BATS_TEST_TMPDIR/planted" ]
}

@test "init refuses a symlinked state subdirectory" {
  mkdir -p "$S" "$BATS_TEST_TMPDIR/out"; ln -s "$BATS_TEST_TMPDIR/out" "$S/ssh"
  run lea_init </dev/null
  [ "$status" -eq 1 ]
  [ -z "$(ls -A "$BATS_TEST_TMPDIR/out")" ]
}

@test "images already present are neither pulled nor built" {
  lea_init </dev/null 2>/dev/null
  run calls
  [[ "$output" == *"docker [image] [inspect] [$LEA_IMAGE]"* ]]
  [[ "$output" != *"docker [pull]"* ]]
  [[ "$output" != *"docker [build]"* ]]
}

@test "missing images are pulled" {
  export LEA_IMAGE=registry.example.com/lea/lea:1 LEA_PROXY_IMAGE=registry.example.com/lea/lea-proxy:1
  export FAKE_DOCKER_RC_IMAGE_INSPECT=1
  lea_init </dev/null 2>/dev/null
  run calls
  [[ "$output" == *"docker [pull] [$LEA_IMAGE]"* ]]
  [[ "$output" == *"docker [pull] [$LEA_PROXY_IMAGE]"* ]]
  [[ "$output" != *"docker [build] [-t] [lea"* ]]
}

@test "images that cannot be pulled are built locally and recorded in the user config" {
  export FAKE_DOCKER_RC_IMAGE_INSPECT=1
  LEA_IMAGE=reg/a:1 LEA_PROXY_IMAGE=reg/b:1
  export FAKE_DOCKER_RC_PULL_REG_A_1=1 FAKE_DOCKER_RC_PULL_REG_B_1=1
  run lea_init </dev/null
  [ "$status" -eq 0 ]
  run calls
  [[ "$output" == *"docker [build] [-t] [lea:dev] [--build-arg] [LEA_VERSION=dev] [-f] [$LEA_ROOT/image/Containerfile] [$LEA_ROOT]"* ]]
  [[ "$output" == *"docker [build] [-t] [lea-proxy:dev] [-f] [$LEA_ROOT/proxy/Containerfile] [$LEA_ROOT]"* ]]
  grep -qx "LEA_IMAGE='lea:dev'" "$U"
  grep -qx "LEA_PROXY_IMAGE='lea-proxy:dev'" "$U"
}

@test "a failing local build stops init before sudo" {
  export FAKE_DOCKER_OUT_INFO___FORMAT='["name=apparmor"]'
  export FAKE_DOCKER_RC_IMAGE_INSPECT=1 FAKE_DOCKER_RC_PULL_REG_A_1=1 FAKE_DOCKER_RC_BUILD__T=1
  LEA_IMAGE=reg/a:1
  run lea_init </dev/null
  [ "$status" -eq 1 ]
  [[ "$output" == *"could not build lea:dev"* ]]
  run calls
  [[ "$output" != *sudo* ]]
}

# update_root [checkout]: LEA_ROOT is a copy of the configuration, a git checkout (a .git and a
# fake git that logs) or a release install (no .git).
update_root() {
  R="$BATS_TEST_TMPDIR/root"; mkdir -p "$R/config" "$BATS_TEST_TMPDIR/gbin"
  cp "$LEA_ROOT/config/lea.conf" "$R/config/"
  if [ "${1:-}" = checkout ]; then
    mkdir -p "$R/.git"
    printf '#!/bin/sh\n{ printf git; for a in "$@"; do printf " [%%s]" "$a"; done; printf "\\n"; } >>"$FAKE_LOG"\n' >"$BATS_TEST_TMPDIR/gbin/git"
    chmod +x "$BATS_TEST_TMPDIR/gbin/git"
    export PATH="$BATS_TEST_TMPDIR/gbin:$PATH"
  fi
  LEA_ROOT=$R
}

@test "update of a checkout pulls fast-forward only, then rebuilds" {
  update_root checkout
  run lea_update
  [ "$status" -eq 0 ]
  run calls
  [[ "$output" == *"git [-C] [$R] [pull] [--ff-only]"*"make [-C] [$R] [go-build]"* ]]
  [[ "$output" != *curl* ]]
}

@test "a worktree (.git is a file) is a checkout too, never replaced by the installer" {
  update_root checkout
  rmdir "$R/.git"; echo 'gitdir: /elsewhere' >"$R/.git"
  run lea_update
  [ "$status" -eq 0 ]
  run calls
  [[ "$output" == *"git [-C] [$R] [pull] [--ff-only]"* ]]
  [[ "$output" != *curl* ]]
}

# fake_release VERSION: the release VERSION as lea update downloads it. $REL holds its install.sh
# (logs "installer ARGS", unpacks a copy of this checkout at $NEW, versioned images in its lea.conf, and
# links $LEA_BIN_DIR/lea to it) and its checksums.txt; the fake curl serves them and resolves the
# latest release to VERSION.
fake_release() {
  REL="$BATS_TEST_TMPDIR/release"; NEW="$BATS_TEST_TMPDIR/inst/$1"; mkdir -p "$REL"
  src=$(cd "$BATS_TEST_DIRNAME/../.." && pwd -P)
  cat >"$REL/install.sh" <<EOT
echo "installer \$*" >>"\$FAKE_LOG"
mkdir -p "$NEW"
cp -R "$src/bin" "$src/libexec" "$src/config" "$NEW/"
sed -i -e 's|^LEA_IMAGE=.*|LEA_IMAGE=ghcr.io/davidgodefroid/lea:${1#v}|' \\
  -e 's|^LEA_PROXY_IMAGE=.*|LEA_PROXY_IMAGE=ghcr.io/davidgodefroid/lea-proxy:${1#v}|' \\
  -e 's|^LEA_LOCKSQL_IMAGE=.*|LEA_LOCKSQL_IMAGE=locksql:9.0.0|' -e 's|^LEA_LOCKSQL_REF=.*|LEA_LOCKSQL_REF=v9.0.0|' "$NEW/config/lea.conf"
mkdir -p "$LEA_BIN_DIR"
ln -sfn "$NEW/bin/lea" "$LEA_BIN_DIR/lea"
EOT
  (cd "$REL" && sha256sum install.sh >checksums.txt)
  export FAKE_CURL_DIR="$REL" FAKE_CURL_EFFECTIVE="https://github.com/DavidGodefroid/lea/releases/tag/$1"
}

@test "update of a release install runs the installer of the latest release, never main's" {
  update_root
  fake_release v9.9.9
  run lea_update
  [ "$status" -eq 0 ]
  [[ "$output" == *"updating this release install to lea v9.9.9 with its installer (https://github.com/DavidGodefroid/lea/releases/download/v9.9.9/install.sh)"* ]]
  [[ "$output" == *"updated; running lea containers keep their image until they stop"* ]]
  run calls
  [[ "$output" == *"curl [-fsSLI] [-o] [/dev/null] [-w] [%{url_effective}] [https://github.com/DavidGodefroid/lea/releases/latest]"* ]]
  [[ "$output" == *"[https://github.com/DavidGodefroid/lea/releases/download/v9.9.9/install.sh]"* ]]
  [[ "$output" == *"[https://github.com/DavidGodefroid/lea/releases/download/v9.9.9/checksums.txt]"* ]]
  [[ "$output" == *"installer --version v9.9.9 --no-init"* ]]
  [[ "$output" != *raw.githubusercontent.com* ]]
  [[ "$output" != *"/main/"* ]]
  [[ "$output" != *"make "* ]]
  [ ! -e "$XDG_DATA_HOME/lea/host/update" ]
}

@test "release update ends with the new versioned lea and proxy images pulled and the new locksql image built" {
  update_root
  fake_release v9.9.9
  export FAKE_DOCKER_RC_IMAGE_INSPECT_AT_LOCKSQL_9_0_0=1
  run lea_update
  [ "$status" -eq 0 ]
  run calls
  [[ "$output" == *"installer --version v9.9.9 --no-init"*"docker [pull] [ghcr.io/davidgodefroid/lea:9.9.9]"* ]]
  [[ "$output" == *"docker [pull] [ghcr.io/davidgodefroid/lea-proxy:9.9.9]"* ]]
  [[ "$output" == *"docker [image] [inspect] [locksql:9.0.0]"* ]]
  [[ "$output" == *"docker [build] [-t] [locksql:9.0.0] [https://github.com/DavidGodefroid/locksql.git#v9.0.0]"* ]]
  [[ "$output" != *"locksql:0.8.0"* ]]
}

@test "release update does not rebuild a present locksql image" {
  update_root
  fake_release v9.9.9
  run lea_update
  [ "$status" -eq 0 ]
  run calls
  [[ "$output" == *"docker [image] [inspect] [locksql:9.0.0]"* ]]
  [[ "$output" != *"docker [build]"* ]]
}

@test "release update with LEA_LOCKSQL=0 builds no locksql image" {
  update_root
  fake_release v9.9.9
  lea_user_config_set LEA_LOCKSQL 0
  export FAKE_DOCKER_RC_IMAGE_INSPECT=1
  run lea_update
  [ "$status" -eq 0 ]
  run calls
  [[ "$output" == *"docker [pull] [ghcr.io/davidgodefroid/lea:9.9.9]"* ]]
  [[ "$output" != *locksql* ]]
}

@test "a failing locksql build is a warning, the release update still succeeds" {
  update_root
  fake_release v9.9.9
  export FAKE_DOCKER_RC_IMAGE_INSPECT_AT_LOCKSQL_9_0_0=1 FAKE_DOCKER_RC_BUILD__T=1
  run lea_update
  [ "$status" -eq 0 ]
  [[ "$output" == *"warning: could not build locksql:9.0.0"* ]]
  [[ "$output" == *"updated; running lea containers keep their image"* ]]
}

@test "an installer that does not match the release's checksums.txt is never run" {
  update_root
  fake_release v9.9.9
  echo 'echo tampered >>"$FAKE_LOG"' >>"$REL/install.sh"
  run lea_update
  [ "$status" -eq 1 ]
  [[ "$output" == *"the installer of lea v9.9.9 does not match its checksums.txt"* ]]
  run calls
  [[ "$output" != *installer* ]]
  [[ "$output" != *tampered* ]]
}

@test "an installer missing from checksums.txt is never run" {
  update_root
  fake_release v9.9.9
  echo "0000  lea_9.9.9_linux_amd64.tar.gz" >"$REL/checksums.txt"
  run lea_update
  [ "$status" -eq 1 ]
  [[ "$output" == *"does not match its checksums.txt"* ]]
  run calls
  [[ "$output" != *"installer "* ]]
}

@test "update fails when the latest release cannot be found" {
  update_root
  export FAKE_CURL_EFFECTIVE=https://github.com/DavidGodefroid/lea/releases
  run lea_update
  [ "$status" -eq 1 ]
  [[ "$output" == *"could not find the latest lea release"* ]]
}

@test "update fails when the installer cannot be downloaded" {
  update_root
  fake_release v9.9.9
  rm "$REL/install.sh"
  run lea_update
  [ "$status" -eq 1 ]
  [[ "$output" == *"cannot download the installer of lea v9.9.9"* ]]
}

@test "a failing installer fails the update" {
  update_root
  fake_release v9.9.9
  echo 'exit 3' >"$REL/install.sh"
  (cd "$REL" && sha256sum install.sh >checksums.txt)
  run lea_update
  [ "$status" -eq 1 ]
  [[ "$output" == *"the installer failed"* ]]
  run calls
  [[ "$output" != *"docker [pull]"* ]]
}

@test "a failing image pull in the new release fails the update, naming the rerun" {
  update_root
  fake_release v9.9.9
  export FAKE_DOCKER_RC_PULL_GHCR_IO_DAVIDGODEFROID_LEA_9_9_9=1
  run lea_update
  [ "$status" -eq 1 ]
  [[ "$output" == *"lea v9.9.9 is installed but its images could not be refreshed: rerun lea update"* ]]
}

@test "update of a checkout builds a missing locksql image" {
  update_root checkout
  export FAKE_DOCKER_RC_IMAGE_INSPECT_AT_LOCKSQL_0_8_0=1
  run lea_update
  [ "$status" -eq 0 ]
  run calls
  [[ "$output" == *"docker [build] [-t] [locksql:0.8.0] [https://github.com/DavidGodefroid/locksql.git#v0.8.0]"* ]]
}

@test "update of a checkout with LEA_LOCKSQL=0 builds no locksql image" {
  update_root checkout
  lea_user_config_set LEA_LOCKSQL 0
  export FAKE_DOCKER_RC_IMAGE_INSPECT=1
  run lea_update
  [ "$status" -eq 0 ]
  run calls
  [[ "$output" != *locksql* ]]
}

@test "update rebuilds locally built images instead of pulling them" {
  update_root checkout
  lea_user_config_set LEA_IMAGE lea:dev
  lea_user_config_set LEA_PROXY_IMAGE lea-proxy:dev
  run lea_update
  [ "$status" -eq 0 ]
  run calls
  [[ "$output" != *"docker [pull]"* ]]
  [[ "$output" == *"docker [build] [-t] [lea:dev]"* ]]
  [[ "$output" == *"docker [build] [-t] [lea-proxy:dev]"* ]]
}

@test "update pulls registry images" {
  update_root checkout
  lea_user_config_set LEA_IMAGE registry.example.com/lea/lea:1
  lea_user_config_set LEA_PROXY_IMAGE registry.example.com/lea/lea-proxy:1
  lea_load_config
  run lea_update
  [ "$status" -eq 0 ]
  run calls
  [[ "$output" == *"docker [pull] [$LEA_IMAGE]"* ]]
  [[ "$output" == *"docker [pull] [$LEA_PROXY_IMAGE]"* ]]
}

@test "update rebuilds the core binary after pulling" {
  update_root checkout
  run lea_update
  [ "$status" -eq 0 ]
  run calls
  [[ "$output" == *"make [-C] [$LEA_ROOT] [go-build]"* ]]
}

@test "a failing core build does not fail the update" {
  update_root checkout
  export FAKE_MAKE_RC=2
  run lea_update
  [ "$status" -eq 0 ]
  [[ "$output" == *"make go-build failed"* ]]
}

@test "an interactive init with no locksql profile runs the profile wizard in the console image" {
  interactive
  export FAKE_DOCKER_OUT_RUN___RM_AT_HELP='  profile  add [--project DIR] [--engine E ...]'
  run lea_init <<<"
y"
  [ "$status" -eq 0 ]
  [[ "$output" == *"locksql has no database profile for $P yet"* ]]
  [[ "$output" == *"run locksql (or lea locksql) in a terminal you keep in view"* ]]
  run calls
  [[ "$output" == *"docker [run] [-it] [--rm] [--user] [$(id -u):$(id -g)] [-e] [HOME=/tmp] [-v] [$P:$P] [-w] [$P] [locksql:0.8.0] [profile] [add] [--project] [$P]"* ]]
}

@test "the profile wizard is skipped when declined, without input, or when a profile exists" {
  interactive
  export FAKE_DOCKER_OUT_RUN___RM_AT_HELP='  profile  add [--project DIR] [--engine E ...]'
  run lea_init <<<"
n"
  [[ "$output" == *"later: lea init again"* ]]
  lea_init </dev/null 2>/dev/null
  mkdir -p "$P/.locksql"; printf '[profiles.dev]\nengine = "sqlite"\npath = "a.db"\n' >"$P/.locksql/config.toml"
  lea_init <<<"
y" 2>/dev/null
  run calls
  [[ "$output" != *"[profile] [add]"* ]]
}

@test "a locksql without the profile wizard gets the by-hand hint" {
  interactive
  run lea_init <<<"
y"
  [[ "$output" == *"has no profile wizard: add a database profile to $P/.locksql/config.toml by hand"* ]]
  run calls
  [[ "$output" != *"[profile] [add]"* ]]
}

@test "a Vault profile: vault CLI built in, address saved, vault login as the console account" {
  interactive
  mkdir -p "$P/.locksql"
  printf '[profiles.uat]\nengine = "mariadb"\nhost = "db"\ncredentials = "vault"\nvault_path = "database/creds/uat"\n' >"$P/.locksql/config.toml"
  export FAKE_DOCKER_RC_RUN___RM_AT_COMMAND__V_VAULT=1 FAKE_DOCKER_RC_RUN__I_AT_LOOKUP=1
  run lea_init <<<"
https://vault.example.com
y
"
  [ "$status" -eq 0 ]
  grep -qx "LEA_LOCKSQL_VAULT_ADDR='https://vault.example.com'" "$U"
  run calls
  [[ "$output" == *"docker [build] [--target] [vault] [-t] [locksql:0.8.0] [https://github.com/DavidGodefroid/locksql.git#v0.8.0]"* ]]
  [[ "$output" == *"[-e] [VAULT_ADDR=https://vault.example.com] [--entrypoint] [vault] [locksql:0.8.0] [token] [lookup]"* ]]
  [[ "$output" == *"docker [run] [-i] [--rm] [--user] [locksql] [-v] [locksql-home:/home/locksql] [-e] [VAULT_ADDR=https://vault.example.com] [--entrypoint] [vault] [locksql:0.8.0] [login] [-method=oidc]"* ]]
}

@test "without a terminal the guided locksql steps are skipped" {
  unset LEA_INIT_NONINTERACTIVE
  lea_init </dev/null 2>/dev/null
  run calls
  [[ "$output" != *"[help]"* ]]
}

@test "a missing image is built with the vault CLI when a profile needs it" {
  mkdir -p "$P/.locksql"
  printf '[profiles.uat]\nengine = "postgres"\nhost = "db"\ncredentials = "vault"\nvault_path = "x/y"\n' >"$P/.locksql/config.toml"
  export FAKE_DOCKER_RC_IMAGE_INSPECT_AT_LOCKSQL_0_8_0=1
  lea_init </dev/null 2>/dev/null
  run calls
  [[ "$output" == *"docker [build] [--target] [vault] [-t] [locksql:0.8.0]"* ]]
}

@test "LEA_LOCKSQL_REF may be a local locksql checkout" {
  export LEA_LOCKSQL_REF="$BATS_TEST_TMPDIR/locksql" FAKE_DOCKER_RC_IMAGE_INSPECT_AT_LOCKSQL_0_8_0=1
  mkdir -p "$LEA_LOCKSQL_REF"
  lea_init </dev/null 2>/dev/null
  run calls
  [[ "$output" == *"docker [build] [-t] [locksql:0.8.0] [$LEA_LOCKSQL_REF]"* ]]
}

@test "init links locksql next to lea, takes over an older lea's, keeps a host locksql" {
  lea_init </dev/null 2>/dev/null
  [ "$(readlink "$LEA_BIN_DIR/locksql")" = "$LEA_ROOT/bin/locksql" ]
  old="$BATS_TEST_TMPDIR/old/bin"; mkdir -p "$old"; : >"$old/lea"; : >"$old/locksql"
  ln -sfn "$old/locksql" "$LEA_BIN_DIR/locksql"
  lea_init </dev/null 2>/dev/null
  [ "$(readlink "$LEA_BIN_DIR/locksql")" = "$LEA_ROOT/bin/locksql" ]
  rm "$LEA_BIN_DIR/locksql"; echo host >"$LEA_BIN_DIR/locksql"
  run lea_init </dev/null
  [ "$(cat "$LEA_BIN_DIR/locksql")" = host ]
  [[ "$output" == *"$LEA_BIN_DIR/locksql exists and is not lea's locksql: left as is"* ]]
}

@test "LEA_LOCKSQL=0 links no locksql" {
  lea_user_config_set LEA_LOCKSQL 0; lea_load_config
  lea_init </dev/null 2>/dev/null
  [ ! -e "$LEA_BIN_DIR/locksql" ] && [ ! -L "$LEA_BIN_DIR/locksql" ]
}
