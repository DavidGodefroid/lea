#!/bin/sh
# End-to-end test of lea update's dependencies (#32), and of lea init's locksql build, against the real releases on GitHub and the
# real cosign (the one on PATH, else lea's pinned v3.0.6):
#   - locksql: the latest release is found, its source fetched at the tag, that commit proven
#     by the release's Sigstore signature, then built (a stand-in docker records the build context)
#     and recorded;
#   - a tag moved to another commit, or a genuine commit with another release's signature, is
#     never built nor recorded; a branch named like the tag is never what is built;
#   - lockfile: the host binary goes from v0.1.1 to the latest release with the strict signed
#     installer (into a stand-in /usr/local/bin) and is recorded;
#   - asked and declined, nothing is fetched.
# Needs git, curl and network access to GitHub. Writes only under a temporary directory.
# shellcheck disable=SC2034,SC2317 # lea's functions read these variables and call these redefinitions
set -eu
root=$(cd "$(dirname "$0")/../.." && pwd -P)
w=$(mktemp -d)
trap 'rm -rf "$w"' EXIT
fail=0
ok() { echo "ok    $1"; }
ko() { echo "FAIL  $1"; fail=1; }

export LEA_ROOT="$root" HOME="$w/home" XDG_DATA_HOME="$w/data" LEA_CONFIG="$w/user.conf" LEA_PROJECTS_DIR="$w/projects"
unset XDG_CONFIG_HOME LEA_LOCKSQL LEA_LOCKMQ LEA_LOCKFILE
mkdir -p "$HOME" "$XDG_DATA_HOME" "$LEA_PROJECTS_DIR" "$w/bin" "$w/co"
: >"$LEA_CONFIG"
for f in "$root"/libexec/lib/*.sh; do
  # shellcheck source=/dev/null
  . "$f"
done
lea_load_config

# A stand-in docker: a build records its context's commit; no image is ever present.
cat >"$w/bin/docker" <<EOT
#!/bin/sh
echo "docker \$*" >>"$w/docker.log"
[ "\$1" = build ] || exit 1
for ctx in "\$@"; do :; done
git -C "\$ctx" rev-parse HEAD >"$w/built.sha"
EOT
chmod +x "$w/bin/docker"
co=$(lea__cosign "$w/co") || { echo "update-deps: no cosign" >&2; exit 1; }
case $co in */*) PATH="$(dirname "$co"):$PATH" ;; esac
export PATH="$w/bin:$PATH"

api_latest() { curl -fsSL "https://api.github.com/repos/DavidGodefroid/$1/releases/latest" | sed -n 's/^ *"tag_name": *"\(v[0-9.]*\)".*/\1/p'; }
peeled() { git ls-remote "https://github.com/DavidGodefroid/$1.git" "refs/tags/$2^{}" | cut -f1; }
reset() { rm -f "$w/docker.log" "$w/built.sha"; : >"$LEA_CONFIG"; lea_load_config; }

# locksql: from an older release to the latest one.
tag=$(api_latest locksql)
want=$(peeled locksql "$tag")
reset; LEA_LOCKSQL_REF=v0.0.1
if out=$(lea_update_deps all 2>&1) && grep -qx "LEA_LOCKSQL_REF='$tag'" "$LEA_CONFIG" &&
  grep -qx "LEA_LOCKSQL_IMAGE='locksql:${tag#v}'" "$LEA_CONFIG" &&
  [ "$(cat "$w/built.sha")" = "$want" ] && grep -q "build -t locksql:${tag#v} " "$w/docker.log"; then
  ok "locksql $tag: signature verified for commit $want, built from it, recorded"
else
  ko "locksql $tag update: $out"
fi

# lea init: an absent image of the pinned release, built from its verified commit.
reset; rm -f "$w/built.sha"
pin=$LEA_LOCKSQL_REF
if out=$(lea__init_locksql 2>&1) && [ "$(cat "$w/built.sha" 2>/dev/null)" = "$(peeled locksql "$pin")" ] &&
  printf '%s' "$out" | grep -q "locksql $pin: signature verified"; then
  ok "lea init: locksql $pin built from its signed commit"
else
  ko "lea init build of $pin: $out"
fi

# A mirror (file://) of locksql: lea__dep_url serves its releases and its repository.
mirror() {
  rm -rf "$w/m" "$w/m.git" "$w/t"
  mkdir -p "$w/m/releases/download/$tag"
  gh_dl=https://github.com/DavidGodefroid/locksql/releases/download/$1
  curl -fsSL -o "$w/m/releases/download/$tag/checksums.txt" "$gh_dl/checksums.txt"
  curl -fsSL -o "$w/m/releases/download/$tag/checksums.txt.sigstore.json" "$gh_dl/checksums.txt.sigstore.json"
  git -c advice.detachedHead=false clone -q --depth 1 --branch "$tag" https://github.com/DavidGodefroid/locksql.git "$w/t"
}
mirrored() {
  git clone -q --bare "$w/t" "$w/m.git"
  reset; LEA_LOCKSQL_REF=v0.0.1
  lea__dep_url() { printf 'file://%s' "$w/m"; }
  lea__dep_latest() { printf '%s' "$tag"; }
  out=$(lea_update_deps all 2>&1) || true
  # lea init (and lea update's refresh) builds an absent image through the same check.
  rm -f "$w/docker.log"
  initout=$(LEA_LOCKSQL_REF=$tag lea__init_locksql 2>&1) || true
  unset -f lea__dep_url lea__dep_latest
  # shellcheck source=/dev/null
  . "$root/libexec/lib/deps.sh"
}
mirror "$tag"
git -C "$w/t" -c user.email=x@x -c user.name=x commit -q --allow-empty -m "not released"
git -C "$w/t" tag -f "$tag" >/dev/null
mirrored
if ! grep -q "^docker build" "$w/docker.log" 2>/dev/null && [ ! -s "$LEA_CONFIG" ] && printf '%s' "$out" | grep -q "is not what locksql's release workflow signed; nothing built" &&
  printf '%s' "$initout" | grep -q "is not what locksql's release workflow signed; nothing built"; then
  ok "locksql: tag $tag moved to another commit, refused (lea update and lea init)"
else
  ko "locksql: moved tag: $out $initout"
fi
prev=
for t in $(git ls-remote --tags --refs https://github.com/DavidGodefroid/locksql.git 'v*' | sed 's|.*refs/tags/||'); do
  if lea__older "$t" "$tag" && { [ -z "$prev" ] || lea__older "$prev" "$t"; }; then prev=$t; fi
done
mirror "$prev"
mirrored
if ! grep -q "^docker build" "$w/docker.log" 2>/dev/null && [ ! -s "$LEA_CONFIG" ] && printf '%s' "$out" | grep -q "is not what locksql's release workflow signed; nothing built" &&
  printf '%s' "$initout" | grep -q "is not what locksql's release workflow signed; nothing built"; then
  ok "locksql: $tag served with the signature of $prev, refused (lea update and lea init)"
else
  ko "locksql: other release's signature: $out $initout"
fi

mirror "$tag"
git -C "$w/t" -c user.email=x@x -c user.name=x commit -q --allow-empty -m "a branch named like the tag"
git -C "$w/t" branch -q "$tag"
mirrored
if [ "$(cat "$w/built.sha" 2>/dev/null)" = "$want" ] && grep -qx "LEA_LOCKSQL_REF='$tag'" "$LEA_CONFIG"; then
  ok "locksql: a branch named $tag is ignored; the tag's signed commit is built"
else
  ko "locksql: branch named like the tag: $out $initout"
fi

# lockfile: a host v0.1.1 (a stand-in: that release ships no binary) replaced by the latest
# release with the strict signed installer, in a stand-in /usr/local/bin.
mkdir -p "$w/host/usr/local/bin"; chmod 0755 "$w/host" "$w/host/usr" "$w/host/usr/local" "$w/host/usr/local/bin"
printf '#!/bin/sh\necho "lockfile v0.1.1"\n' >"$w/host/usr/local/bin/lockfile"; chmod 0755 "$w/host/usr/local/bin/lockfile"
lea__lockhost_root() { printf '%s' "$w/host"; }
lea__lockhost_owner() { id -u; }
# sudo install -m MODE -o root -g root SRC DST, as this user.
sudo() { [ "$1" = install ] && install -m "$3" "$8" "$9"; }
lftag=$(api_latest lockfile)
reset; LEA_LOCKSQL=0 LEA_LOCKFILE=1 LEA_LOCKFILE_REF=v0.1.1
if out=$(lea_update_deps all 2>&1) && printf '%s' "$out" | grep -q "lockfile: v0.1.1 -> $lftag" &&
  [ "$(lea__lockhost_version "$w/host/usr/local/bin/lockfile")" = "$lftag" ] && grep -qx "LEA_LOCKFILE_REF='$lftag'" "$LEA_CONFIG"; then
  ok "lockfile v0.1.1 -> $lftag: signed release installed and recorded"
else
  ko "lockfile update: $out"
fi

# Asked on a terminal and declined: nothing fetched, nothing recorded.
reset; LEA_LOCKSQL_REF=v0.0.1
lea__init_tty() { :; }
out=$(echo n | lea_update_deps ask 2>&1)
if ! grep -q "^docker build" "$w/docker.log" 2>/dev/null && [ ! -s "$LEA_CONFIG" ] && printf '%s' "$out" | grep -q "locksql: kept at v0.0.1"; then
  ok "declined: nothing built or recorded"
else
  ko "declined: $out"
fi

exit "$fail"
