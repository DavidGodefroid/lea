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
  LEA_LOCKSQL=1 # on, as the user config of lea init --locksql says (off by default)
  SHA=0123456789abcdef0123456789abcdef01234567
  D="$BATS_TEST_TMPDIR/rel"
  export FAKE_GIT_SHA=$SHA FAKE_CURL_DIR="$D"
  locksql_release
}
# on_tty: lea update talks to a terminal.
on_tty() { lea__init_tty() { :; }; }
# latest DEP TAG: the latest release of DEP is TAG.
latest() { export "FAKE_CURL_EFFECTIVE_$1=https://github.com/DavidGodefroid/$1/releases/tag/$2"; }

@test "a newer locksql is shown with its release notes and, accepted, built from the signed commit" {
  on_tty; latest locksql v0.14.0
  run lea_update_deps ask <<<"y"
  [ "$status" -eq 0 ]
  [[ "$output" == *"locksql: v0.13.0 -> v0.14.0 (release notes: https://github.com/DavidGodefroid/locksql/releases/tag/v0.14.0)"* ]]
  [[ "$output" == *"Update locksql to v0.14.0 (its signature is checked first)? [y/N]"* ]]
  [[ "$output" == *"locksql v0.14.0 recorded (profiles kept); lox up pins the console's own image: lox add locksql NAME again moves a lock to a newer release"* ]]
  run calls
  [[ "$output" == *"curl [-fsSLI] [-o] [/dev/null] [-w] [%{url_effective}] [https://github.com/DavidGodefroid/locksql/releases/latest]"* ]]
  [[ "$output" == *"[https://github.com/DavidGodefroid/locksql/releases/download/v0.14.0/checksums.txt]"* ]]
  [[ "$output" == *"[https://github.com/DavidGodefroid/locksql/releases/download/v0.14.0/checksums.txt.sigstore.json]"* ]]
  [[ "$output" == *"git [-C] ["*"/src] [fetch] [-q] [--depth] [1] [--no-tags] [https://github.com/DavidGodefroid/locksql.git] [+refs/tags/v0.14.0:refs/tags/v0.14.0]"*"git [-C] ["*"/src] [-c] [advice.detachedHead=false] [checkout] [-q] [refs/tags/v0.14.0^{commit}]"* ]]
  [[ "$output" != *"[--branch]"* ]]
  [[ "$output" == *"cosign [verify-blob] [--bundle] ["*"/checksums.txt.sigstore.json] [--certificate-identity-regexp] [^https://github\\.com/DavidGodefroid/locksql/\\.github/workflows/release\\.yml@refs/tags/v0\\.14\\.0\$] [--certificate-oidc-issuer] [https://token.actions.githubusercontent.com] [--certificate-github-workflow-repository] [DavidGodefroid/locksql] [--certificate-github-workflow-sha] [$SHA] ["*"/checksums.txt]"* ]]
  [[ "$output" == *"docker [build] [-t] [locksql:0.14.0] [--build-arg] [ALLOWED_UIDS=$(id -u)] ["*"/src]"* ]]
  # The signature is checked before anything is built.
  [ "$(calls | grep -n '^cosign' | cut -d: -f1)" -lt "$(calls | grep -n '^docker \[build\]' | cut -d: -f1)" ]
  grep -qx "LEA_LOCKSQL_REF='v0.14.0'" "$LEA_CONFIG"
  grep -qx "LEA_LOCKSQL_IMAGE='locksql:0.14.0'" "$LEA_CONFIG"
  lea_load_config
  [ "$LEA_LOCKSQL_REF" = v0.14.0 ]
  [ "$LEA_LOCKSQL_IMAGE" = locksql:0.14.0 ]
}

@test "a Vault profile gets the new locksql image with the vault CLI" {
  on_tty; latest locksql v0.14.0
  mkdir -p "$P/.locksql"; printf '[profiles.db]\ncredentials = "vault"\n' >"$P/.locksql/config.toml"
  run lea_update_deps all
  run calls
  [[ "$output" == *"docker [build] [--target] [vault] [-t] [locksql:0.14.0]"* ]]
}

@test "the update is asked, no by default: declined or unanswered, nothing is fetched or recorded" {
  on_tty; latest locksql v0.14.0
  for answer in "" "n"; do
    : >"$FAKE_LOG"
    run lea_update_deps ask <<<"$answer"
    [[ "$output" == *"locksql: kept at v0.13.0"* ]]
    if calls | grep -q '^git\|^cosign\|^docker\|releases/download'; then false; fi
  done
  run lea_update_deps ask </dev/null
  [[ "$output" == *"locksql: kept at v0.13.0"* ]]
  [ ! -e "$LEA_CONFIG" ]
}

@test "without a terminal, ask updates nothing and says how" {
  latest locksql v0.14.0
  run lea_update_deps ask
  [ "$status" -eq 0 ]
  [[ "$output" == *"locksql: v0.13.0 -> v0.14.0"*"locksql: not updated (no terminal to ask)"* ]]
  [[ "$output" == *"not updated: locksql (lea update on a terminal asks; lea update --deps all takes them all)"* ]]
  if calls | grep -q '^git\|^cosign\|^docker\|releases/download'; then false; fi
  [ ! -e "$LEA_CONFIG" ]
}

@test "--deps none shows the newer release and updates nothing; --deps all asks nothing" {
  on_tty; latest locksql v0.14.0
  run lea_update_deps none <<<"y"
  [[ "$output" == *"locksql: v0.13.0 -> v0.14.0"*"locksql: not updated (--deps none)"* ]]
  [[ "$output" != *"[y/N]"* ]]
  if calls | grep -q '^git\|^cosign\|^docker'; then false; fi
  run lea_update_deps all </dev/null
  [[ "$output" != *"[y/N]"* ]]
  [[ "$output" == *"locksql v0.14.0 recorded"* ]]
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
  on_tty; latest locksql v0.14.0
  FAKE_COSIGN_RC=1 run lea_update_deps all
  [ "$status" -eq 0 ]
  [[ "$output" == *"locksql: v0.14.0 at $SHA is not what locksql's release workflow signed; nothing built"*"warning: locksql stays at v0.13.0"* ]]
  if calls | grep -q '^docker'; then false; fi
  [ ! -e "$LEA_CONFIG" ]
}

@test "no signature bundle, no tag or no commit id: nothing is built" {
  on_tty; latest locksql v0.14.0
  rm "$D/checksums.txt.sigstore.json"
  run lea_update_deps all
  [[ "$output" == *"could not download https://github.com/DavidGodefroid/locksql/releases/download/v0.14.0/checksums.txt.sigstore.json; nothing built"* ]]
  echo '{}' >"$D/checksums.txt.sigstore.json"
  FAKE_GIT_FETCH_RC=128 run lea_update_deps all
  [[ "$output" == *"could not fetch the tag v0.14.0 of https://github.com/DavidGodefroid/locksql; nothing built"* ]]
  FAKE_GIT_SHA=main run lea_update_deps all
  [[ "$output" == *"cannot check the signature of v0.14.0; nothing built"* ]]
  if calls | grep -q '^docker\|^cosign'; then false; fi
  [ ! -e "$LEA_CONFIG" ]
}

@test "a failed build records nothing" {
  latest locksql v0.14.0
  FAKE_DOCKER_RC_BUILD__T=1 run lea_update_deps all
  [[ "$output" == *"could not build locksql:0.14.0"*"warning: locksql stays at v0.13.0"* ]]
  [ ! -e "$LEA_CONFIG" ]
}

@test "the same, an older or a non-release latest is never offered (no downgrade, never latest/rc)" {
  on_tty
  for tag in v0.13.0 v0.7.9 latest v0.14.0-rc.1 main ""; do
    : >"$FAKE_LOG"
    latest locksql "$tag"
    run lea_update_deps all
    [ "$status" -eq 0 ]
    [[ "$output" != *"->"* ]]
    if calls | grep -q 'releases/download\|^git\|^docker'; then false; fi
  done
  latest locksql v0.13.0
  run lea_update_deps all
  [[ "$output" == *"locksql v0.13.0: up to date"* ]]
  latest locksql latest
  run lea_update_deps all
  [[ "$output" == *"locksql: cannot find its latest release (https://github.com/DavidGodefroid/locksql/releases/latest): not updated"* ]]
}

@test "locksql off, without a pin, or a local checkout: lea update leaves it" {
  latest locksql v0.14.0
  LEA_LOCKSQL=0 run lea_update_deps all
  [ -z "$output" ]
  [ -z "$(calls)" ]
  LEA_LOCKSQL_REF= run lea_update_deps all
  [[ "$output" == *"locksql: no pinned release (LEA_LOCKSQL_REF is empty): lea update installs nothing"* ]]
  [ -z "$(calls)" ]
  LEA_LOCKSQL_REF=$P run lea_update_deps all
  [[ "$output" == *"locksql: LEA_LOCKSQL_REF is $P, not a release: lea update leaves it"* ]]
  [ -z "$(calls)" ]
}

@test "lockfile is never offered below its v0.1.1 security floor" {
  if lea__dep_floor_ok lockfile v0.1.0; then false; fi
  lea__dep_floor_ok lockfile v0.1.1
  lea__dep_floor_ok locksql v0.1.0
  if lea__dep_floor_ok locksql latest; then false; fi
}

@test "lockmq and lockfile are updated by their own launchers: lea update only says so" {
  LEA_LOCKSQL=0; latest lockmq v0.2.0; latest lockfile v0.3.0
  run lea_update_deps all
  [ -z "$output" ]
  [ -z "$(calls)" ]
  LEA_LOCKMQ=1 LEA_LOCKFILE=1
  run lea_update_deps all
  [ "$status" -eq 0 ]
  [[ "$output" == *"lockmq: its launcher updates it and its image (lockmq update)"* ]]
  [[ "$output" == *"lockfile: its launcher updates it and its image (lockfile update)"* ]]
  [ -z "$(calls)" ]
  [ ! -e "$LEA_CONFIG" ]
}

@test "a recorded release older than lea's own pin gives way to the pin; a newer, empty or checkout ref stays" {
  printf "LEA_LOCKSQL_REF='v0.7.0'\nLEA_LOCKSQL_IMAGE='locksql:0.7.0'\nLEA_LOCKFILE_REF='v0.1.1'\nLEA_LOCKMQ_REF='v0.0.1'\n" >"$LEA_CONFIG"
  lea_load_config
  [ "$LEA_LOCKSQL_REF" = v0.13.0 ]
  [ "$LEA_LOCKSQL_IMAGE" = locksql:0.13.0 ]
  [ "$LEA_LOCKFILE_REF" = v0.6.0 ]
  [ "$LEA_LOCKMQ_REF" = v0.6.0 ]
  printf "LEA_LOCKSQL_REF='v0.14.0'\nLEA_LOCKSQL_IMAGE='locksql:0.14.0'\nLEA_LOCKFILE_REF=\nLEA_LOCKMQ_REF='v1.0.0'\n" >"$LEA_CONFIG"
  lea_load_config
  [ "$LEA_LOCKSQL_REF" = v0.14.0 ]
  [ "$LEA_LOCKSQL_IMAGE" = locksql:0.14.0 ]
  [ -z "$LEA_LOCKFILE_REF" ]
  [ "$LEA_LOCKMQ_REF" = v1.0.0 ]
  printf "LEA_LOCKSQL_REF='%s'\nLEA_LOCKSQL_IMAGE='locksql:dev'\n" "$P" >"$LEA_CONFIG"
  lea_load_config
  [ "$LEA_LOCKSQL_REF" = "$P" ]
  [ "$LEA_LOCKSQL_IMAGE" = locksql:dev ]
}

@test "the background update never touches a dependency" {
  ! grep -q 'update-deps\|lea_update_deps' <(sed -n '/^lea_update_background()/,/^}/p;/^lea__update_background()/,/^}/p' "$LEA_ROOT/libexec/lib/host.sh") || false
  ! grep -q 'update-deps\|lea_update_deps' "$LEA_ROOT/internal/launcher/autoupdate.go"
}

@test "an update whose choice cannot be recorded says so and records nothing half-way" {
  latest locksql v0.14.0
  : >"$BATS_TEST_TMPDIR/elsewhere"; ln -s "$BATS_TEST_TMPDIR/elsewhere" "$LEA_CONFIG"
  run lea_update_deps all
  [ "$status" -eq 0 ]
  [[ "$output" == *"locksql: locksql:0.14.0 is built but v0.14.0 could not be recorded in $LEA_CONFIG; lea keeps locksql:0.13.0"*"warning: locksql stays at v0.13.0"* ]]
  [[ "$output" != *"v0.14.0 recorded"* ]]
  [ ! -s "$BATS_TEST_TMPDIR/elsewhere" ]
}

@test "the user config takes several keys in one write, and keeps the other lines" {
  printf "LEA_SOUND='1'\nLEA_LOCKSQL_REF='v0.1.0'\n" >"$LEA_CONFIG"
  lea_user_config_set LEA_LOCKSQL_REF v0.14.0 LEA_LOCKSQL_IMAGE "locksql:0.14.0"
  [ "$(cat "$LEA_CONFIG")" = "$(printf "LEA_SOUND='1'\nLEA_LOCKSQL_REF='v0.14.0'\nLEA_LOCKSQL_IMAGE='locksql:0.14.0'")" ]
  [ "$(stat -c %a "$LEA_CONFIG")" = 600 ]
  [ -z "$(ls "$BATS_TEST_TMPDIR" | grep 'user.conf.')" ]
}

@test "a release version is one line of vX.Y.Z without leading zeros" {
  for v in v0.1.0 v1.20.3 v10.0.0; do lea__semver "$v" >/dev/null; done
  [ "$(lea__semver v1.20.3)" = "1 20 3" ]
  for v in v01.2.3 v1.02.3 v1.2.03 v00.1.1 "$(printf 'v1.2.3\nv9.9.9')" "$(printf 'x\nv1.2.3')" v1.2 1.2.3 v1.2.3-rc.1 v1234567890.0.0 ""; do
    if lea__semver "$v" >/dev/null; then echo "accepted: $v"; false; fi
  done
}
