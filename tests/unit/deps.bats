load helpers
setup() {
  setup_fakes; load_lea
  P="$BATS_TEST_TMPDIR/projects"; mkdir -p "$P"
  export LEA_PROJECTS_DIR="$P"
  export LEA_CONFIG="$BATS_TEST_TMPDIR/user.conf"
  R="$BATS_TEST_TMPDIR/host"; mkdir -p "$R/usr/local/bin" "$R/etc" "$R/run"
  OWNER=$(id -u)
  lea__lockhost_root() { printf '%s' "$R"; }
  lea__lockhost_owner() { printf '%s' "$OWNER"; }
  SHA=0123456789abcdef0123456789abcdef01234567
  # A git that logs: clone creates the destination, rev-parse HEAD prints $FAKE_GIT_SHA.
  mkdir -p "$BATS_TEST_TMPDIR/gbin"
  cat >"$BATS_TEST_TMPDIR/gbin/git" <<'EOT'
#!/bin/sh
{ printf git; for a in "$@"; do printf ' [%s]' "$a"; done; printf '\n'; } >>"$FAKE_LOG"
for last in "$@"; do :; done
case " $* " in
  *" clone "*) [ "${FAKE_GIT_CLONE_RC:-0}" -eq 0 ] || exit "$FAKE_GIT_CLONE_RC"; mkdir -p "$last" ;;
  *" rev-parse HEAD "*) printf '%s\n' "$FAKE_GIT_SHA" ;;
esac
EOT
  chmod +x "$BATS_TEST_TMPDIR/gbin/git"
  export PATH="$BATS_TEST_TMPDIR/gbin:$PATH" FAKE_GIT_SHA=$SHA
  # The locksql release lea update downloads: its checksums.txt and a stand-in bundle.
  D="$BATS_TEST_TMPDIR/rel"; mkdir -p "$D"
  echo "00  locksql_0.9.0_linux_amd64.tar.gz" >"$D/checksums.txt"; echo '{}' >"$D/checksums.txt.sigstore.json"
  export FAKE_CURL_DIR="$D"
}
# on_tty: lea update talks to a terminal.
on_tty() { lea__init_tty() { :; }; }
# latest DEP TAG: the latest release of DEP is TAG.
latest() { export "FAKE_CURL_EFFECTIVE_$1=https://github.com/DavidGodefroid/$1/releases/tag/$2"; }
# host_bin TOOL VERSION-LINE: a root's TOOL in the host's /usr/local/bin that prints VERSION-LINE.
host_bin() {
  printf '#!/bin/sh\nif [ "$1" = version ]; then echo "%s"; exit 0; fi\n' "$2" >"$R/usr/local/bin/$1"
  chmod 0755 "$R/usr/local/bin/$1"
}
# release TOOL vX.Y.Z: TOOL's release archive, checksums.txt and bundle, as the fake curl serves them.
release() {
  mkdir -p "$D/x"
  printf '#!/bin/sh\necho "%s %s"\n' "$1" "$2" >"$D/x/$1"; chmod 0755 "$D/x/$1"
  a=${1}_${2#v}_linux_$(lea__lockhost_arch).tar.gz
  tar -czf "$D/$a" -C "$D/x" "$1"
  printf '%s  %s\n' "$(lea__sha256 "$D/$a")" "$a" >"$D/checksums.txt"
}

@test "a newer locksql is shown with its release notes and, accepted, built from the signed commit" {
  on_tty; latest locksql v0.9.0
  run lea_update_deps ask <<<"y"
  [ "$status" -eq 0 ]
  [[ "$output" == *"locksql: v0.8.0 -> v0.9.0 (release notes: https://github.com/DavidGodefroid/locksql/releases/tag/v0.9.0)"* ]]
  [[ "$output" == *"Update locksql to v0.9.0 (its signature is checked first)? [y/N]"* ]]
  [[ "$output" == *"locksql v0.9.0 recorded (profiles and the locksql-home volume kept); a running console keeps locksql:0.8.0 until you restart it"* ]]
  run calls
  [[ "$output" == *"curl [-fsSLI] [-o] [/dev/null] [-w] [%{url_effective}] [https://github.com/DavidGodefroid/locksql/releases/latest]"* ]]
  [[ "$output" == *"[https://github.com/DavidGodefroid/locksql/releases/download/v0.9.0/checksums.txt]"* ]]
  [[ "$output" == *"[https://github.com/DavidGodefroid/locksql/releases/download/v0.9.0/checksums.txt.sigstore.json]"* ]]
  [[ "$output" == *"git [-c] [advice.detachedHead=false] [clone] [-q] [--depth] [1] [--branch] [v0.9.0] [https://github.com/DavidGodefroid/locksql.git] ["*"/src]"* ]]
  [[ "$output" == *"cosign [verify-blob] [--bundle] ["*"/checksums.txt.sigstore.json] [--certificate-identity-regexp] [^https://github\\.com/DavidGodefroid/locksql/\\.github/workflows/release\\.yml@refs/tags/v0\\.9\\.0\$] [--certificate-oidc-issuer] [https://token.actions.githubusercontent.com] [--certificate-github-workflow-repository] [DavidGodefroid/locksql] [--certificate-github-workflow-sha] [$SHA] ["*"/checksums.txt]"* ]]
  [[ "$output" == *"docker [build] [-t] [locksql:0.9.0] [--build-arg] [ALLOWED_UIDS=$(id -u)] ["*"/src]"* ]]
  # The signature is checked before anything is built.
  [ "$(calls | grep -n '^cosign' | cut -d: -f1)" -lt "$(calls | grep -n '^docker \[build\]' | cut -d: -f1)" ]
  grep -qx "LEA_LOCKSQL_REF='v0.9.0'" "$LEA_CONFIG"
  grep -qx "LEA_LOCKSQL_IMAGE='locksql:0.9.0'" "$LEA_CONFIG"
  lea_load_config
  [ "$LEA_LOCKSQL_REF" = v0.9.0 ] && [ "$LEA_LOCKSQL_IMAGE" = locksql:0.9.0 ]
}

@test "a Vault profile gets the new locksql image with the vault CLI" {
  on_tty; latest locksql v0.9.0
  mkdir -p "$P/.locksql"; printf '[profiles.db]\ncredentials = "vault"\n' >"$P/.locksql/config.toml"
  run lea_update_deps all
  run calls
  [[ "$output" == *"docker [build] [--target] [vault] [-t] [locksql:0.9.0]"* ]]
}

@test "the update is asked, no by default: declined or unanswered, nothing is fetched or recorded" {
  on_tty; latest locksql v0.9.0
  for answer in "" "n"; do
    : >"$FAKE_LOG"
    run lea_update_deps ask <<<"$answer"
    [[ "$output" == *"locksql: kept at v0.8.0"* ]]
    if calls | grep -q '^git\|^cosign\|^docker\|releases/download'; then false; fi
  done
  run lea_update_deps ask </dev/null
  [[ "$output" == *"locksql: kept at v0.8.0"* ]]
  [ ! -e "$LEA_CONFIG" ]
}

@test "without a terminal, ask updates nothing and says how" {
  latest locksql v0.9.0
  run lea_update_deps ask
  [ "$status" -eq 0 ]
  [[ "$output" == *"locksql: v0.8.0 -> v0.9.0"*"locksql: not updated (no terminal to ask)"* ]]
  [[ "$output" == *"not updated: locksql (lea update on a terminal asks; lea update --deps all takes them all)"* ]]
  if calls | grep -q '^git\|^cosign\|^docker\|releases/download'; then false; fi
  [ ! -e "$LEA_CONFIG" ]
}

@test "--deps none shows the newer release and updates nothing; --deps all asks nothing" {
  on_tty; latest locksql v0.9.0
  run lea_update_deps none <<<"y"
  [[ "$output" == *"locksql: v0.8.0 -> v0.9.0"*"locksql: not updated (--deps none)"* ]]
  [[ "$output" != *"[y/N]"* ]]
  if calls | grep -q '^git\|^cosign\|^docker'; then false; fi
  run lea_update_deps all </dev/null
  [[ "$output" != *"[y/N]"* ]]
  [[ "$output" == *"locksql v0.9.0 recorded"* ]]
}

@test "lea update refuses an unknown --deps mode" {
  run lea_update_deps maybe
  [ "$status" -ne 0 ]
  run lea_update --deps maybe
  [ "$status" -ne 0 ]
  [[ "$output" == *"usage: lea update [--deps ask|all|none]"* ]]
  run "$LEA_ROOT/libexec/lea-sh" update --deps
  [ "$status" -eq 2 ]
  run "$LEA_ROOT/libexec/lea-sh" update --deps sometimes
  [ "$status" -eq 2 ]
  run "$LEA_ROOT/libexec/lea-sh" update-deps
  [ "$status" -eq 2 ]
  run "$LEA_ROOT/libexec/lea-sh" update-deps all extra
  [ "$status" -eq 2 ]
  [ -z "$(calls)" ]
}

@test "a locksql whose signature does not cover that tag and commit is never built nor recorded" {
  on_tty; latest locksql v0.9.0
  FAKE_COSIGN_RC=1 run lea_update_deps all
  [ "$status" -eq 0 ]
  [[ "$output" == *"locksql: v0.9.0 at $SHA is not what locksql's release workflow signed; nothing built"*"warning: locksql stays at v0.8.0"* ]]
  if calls | grep -q '^docker'; then false; fi
  [ ! -e "$LEA_CONFIG" ]
}

@test "no signature bundle, no clone or no commit id: nothing is built" {
  on_tty; latest locksql v0.9.0
  rm "$D/checksums.txt.sigstore.json"
  run lea_update_deps all
  [[ "$output" == *"could not download https://github.com/DavidGodefroid/locksql/releases/download/v0.9.0/checksums.txt.sigstore.json; nothing built"* ]]
  echo '{}' >"$D/checksums.txt.sigstore.json"
  FAKE_GIT_CLONE_RC=128 run lea_update_deps all
  [[ "$output" == *"could not clone https://github.com/DavidGodefroid/locksql at v0.9.0; nothing built"* ]]
  FAKE_GIT_SHA=main run lea_update_deps all
  [[ "$output" == *"cannot check the signature of v0.9.0; nothing built"* ]]
  if calls | grep -q '^docker\|^cosign'; then false; fi
  [ ! -e "$LEA_CONFIG" ]
}

@test "a failed build records nothing" {
  latest locksql v0.9.0
  FAKE_DOCKER_RC_BUILD__T=1 run lea_update_deps all
  [[ "$output" == *"could not build locksql:0.9.0"*"warning: locksql stays at v0.8.0"* ]]
  [ ! -e "$LEA_CONFIG" ]
}

@test "the same, an older or a non-release latest is never offered (no downgrade, never latest/rc)" {
  on_tty
  for tag in v0.8.0 v0.7.9 latest v0.9.0-rc.1 main ""; do
    : >"$FAKE_LOG"
    latest locksql "$tag"
    run lea_update_deps all
    [ "$status" -eq 0 ]
    [[ "$output" != *"->"* ]]
    if calls | grep -q 'releases/download\|^git\|^docker'; then false; fi
  done
  latest locksql v0.8.0
  run lea_update_deps all
  [[ "$output" == *"locksql v0.8.0: up to date"* ]]
  latest locksql latest
  run lea_update_deps all
  [[ "$output" == *"locksql: cannot find its latest release (https://github.com/DavidGodefroid/locksql/releases/latest): not updated"* ]]
}

@test "locksql off, without a pin, or a local checkout: lea update leaves it" {
  latest locksql v0.9.0
  LEA_LOCKSQL=0 run lea_update_deps all
  [ -z "$output" ] && [ -z "$(calls)" ]
  LEA_LOCKSQL_REF= run lea_update_deps all
  [[ "$output" == *"locksql: no pinned release (LEA_LOCKSQL_REF is empty): lea update installs nothing"* ]]
  [ -z "$(calls)" ]
  LEA_LOCKSQL_REF=$P run lea_update_deps all
  [[ "$output" == *"locksql: LEA_LOCKSQL_REF is $P, not a release: lea update leaves it"* ]]
  [ -z "$(calls)" ]
}

@test "a newer lockfile is installed on the host with the strict signed installer, then recorded" {
  LEA_LOCKSQL=0 LEA_LOCKFILE=1; latest lockfile v0.3.0
  host_bin lockfile "lockfile v0.2.0"
  release lockfile v0.3.0
  run lea_update_deps all
  [ "$status" -eq 0 ]
  [[ "$output" == *"lockfile: v0.2.0 -> v0.3.0 (release notes: https://github.com/DavidGodefroid/lockfile/releases/tag/v0.3.0)"* ]]
  [[ "$output" == *"lea's image has the lockfile v0.2.0 client: a v0.3.0 console may refuse it until a lea release ships that client"* ]]
  [[ "$output" == *"lockfile v0.3.0 installed and recorded; restart the lockfile console in the lockfile account"* ]]
  run calls
  [[ "$output" == *"[--certificate-identity-regexp] [^https://github\\.com/DavidGodefroid/lockfile/\\.github/workflows/release\\.yml@refs/tags/v0\\.3\\.0\$]"* ]]
  [[ "$output" == *"sudo [install] [-m] [0755] [-o] [root] [-g] [root] "*"[$R/usr/local/bin/lockfile]"* ]]
  grep -qx "LEA_LOCKFILE_REF='v0.3.0'" "$LEA_CONFIG"
}

@test "a lockfile or lockmq release that fails its checks is not recorded" {
  LEA_LOCKSQL=0 LEA_LOCKMQ=1; latest lockmq v0.2.0
  host_bin lockmq "lockmq 0.1.0"
  release lockmq v0.2.0
  FAKE_COSIGN_RC=1 run lea_update_deps all
  [[ "$output" == *"is not signed by lockmq's release workflow; nothing installed"*"warning: lockmq stays at v0.1.0"* ]]
  if calls | grep -q '^sudo'; then false; fi
  [ ! -e "$LEA_CONFIG" ]
}

@test "lockfile is never offered below its v0.1.1 security floor" {
  if lea__dep_floor_ok lockfile v0.1.0; then false; fi
  lea__dep_floor_ok lockfile v0.1.1
  lea__dep_floor_ok locksql v0.1.0
  if lea__dep_floor_ok locksql latest; then false; fi
  # Even if lea pinned nothing safer, a newer but unsafe lockfile is refused.
  LEA_LOCKSQL=0 LEA_LOCKFILE=1; latest lockfile v0.1.0
  host_bin lockfile "lockfile v0.0.9"
  lea__dep_ref() { printf v0.0.9; }
  run lea_update_deps all
  [[ "$output" == *"warning: lockfile v0.1.0 is below its security floor: not offered"* ]]
  if calls | grep -q 'releases/download\|^sudo'; then false; fi
}

@test "lockmq and lockfile: off, without a pin or not installed, lea update leaves them" {
  LEA_LOCKSQL=0; latest lockmq v0.2.0; latest lockfile v0.3.0
  run lea_update_deps all
  [ -z "$output" ] && [ -z "$(calls)" ]
  LEA_LOCKMQ=1 LEA_LOCKFILE=1
  run lea_update_deps all
  [[ "$output" == *"lockmq: not installed in /usr/local/bin (root-owned): lea init installs v0.1.0"* ]]
  [[ "$output" == *"lockfile: not installed in /usr/local/bin (root-owned): lea init installs v0.2.0"* ]]
  host_bin lockmq "lockmq 0.1.0"
  LEA_LOCKMQ_REF= run lea_update_deps all
  [[ "$output" == *"lockmq: no pinned release (LEA_LOCKMQ_REF is empty): lea update installs nothing"* ]]
  [ -z "$(calls)" ]
}

@test "each dependency is asked on its own" {
  on_tty; LEA_LOCKMQ=1 LEA_LOCKFILE=1
  latest locksql v0.9.0; latest lockmq v0.2.0; latest lockfile v0.3.0
  host_bin lockmq "lockmq 0.1.0"; host_bin lockfile "lockfile v0.2.0"
  release lockmq v0.2.0
  run lea_update_deps ask <<<$'n\ny\nn'
  [[ "$output" == *"locksql: kept at v0.8.0"*"lockmq v0.2.0 installed and recorded"*"lockfile: kept at v0.2.0"* ]]
  grep -qx "LEA_LOCKMQ_REF='v0.2.0'" "$LEA_CONFIG"
  if grep -q 'LOCKSQL\|LOCKFILE' "$LEA_CONFIG"; then false; fi
}

@test "a recorded release older than lea's own pin gives way to the pin; a newer, empty or checkout ref stays" {
  printf "LEA_LOCKSQL_REF='v0.7.0'\nLEA_LOCKSQL_IMAGE='locksql:0.7.0'\nLEA_LOCKFILE_REF='v0.1.1'\nLEA_LOCKMQ_REF='v0.0.1'\n" >"$LEA_CONFIG"
  lea_load_config
  [ "$LEA_LOCKSQL_REF" = v0.8.0 ] && [ "$LEA_LOCKSQL_IMAGE" = locksql:0.8.0 ]
  [ "$LEA_LOCKFILE_REF" = v0.2.0 ] && [ "$LEA_LOCKMQ_REF" = v0.1.0 ]
  printf "LEA_LOCKSQL_REF='v0.9.0'\nLEA_LOCKSQL_IMAGE='locksql:0.9.0'\nLEA_LOCKFILE_REF=\nLEA_LOCKMQ_REF='v1.0.0'\n" >"$LEA_CONFIG"
  lea_load_config
  [ "$LEA_LOCKSQL_REF" = v0.9.0 ] && [ "$LEA_LOCKSQL_IMAGE" = locksql:0.9.0 ]
  [ -z "$LEA_LOCKFILE_REF" ] && [ "$LEA_LOCKMQ_REF" = v1.0.0 ]
  printf "LEA_LOCKSQL_REF='%s'\nLEA_LOCKSQL_IMAGE='locksql:dev'\n" "$P" >"$LEA_CONFIG"
  lea_load_config
  [ "$LEA_LOCKSQL_REF" = "$P" ] && [ "$LEA_LOCKSQL_IMAGE" = locksql:dev ]
}

@test "the background update never touches a dependency" {
  ! grep -q 'update-deps\|lea_update_deps' <(sed -n '/^lea_update_background()/,/^}/p;/^lea__update_background()/,/^}/p' "$LEA_ROOT/libexec/lib/host.sh")
  ! grep -q 'update-deps\|lea_update_deps' "$LEA_ROOT/internal/launcher/autoupdate.go"
}
