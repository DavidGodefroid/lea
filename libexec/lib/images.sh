# shellcheck shell=sh
# The lea and proxy images: pulled from the registry and verified, or, named lea:dev and
# lea-proxy:dev in the configuration, built from this installation. A registry image that cannot
# be pulled stops lea: it is never replaced by a local build.

# lea__build_image ROLE: builds lea:dev (ROLE lea) or lea-proxy:dev (ROLE proxy).
lea__build_image() {
  lea_log "building $1 image from $LEA_ROOT (several minutes the first time)"
  if [ "$1" = proxy ]; then
    docker build -t lea-proxy:dev -f "$LEA_ROOT/proxy/Containerfile" "$LEA_ROOT" >&2 ||
      lea_die "could not build lea-proxy:dev"
  else
    docker build -t lea:dev --build-arg LEA_VERSION=dev -f "$LEA_ROOT/image/Containerfile" "$LEA_ROOT" >&2 ||
      lea_die "could not build lea:dev"
  fi
}

# lea__co_get: sets _LEA_CO to the cosign that checks the images: the one on PATH, else lea's
# SHA-256 pinned download (in a temporary directory, removed by lea__co_put). _LEA_CO_NEW is 1 when
# this call resolved it (its caller then calls lea__co_put), 0 when an outer caller already had.
# Dies when neither is possible.
lea__co_get() {
  _LEA_CO_NEW=0
  if [ -n "${_LEA_CO:-}" ]; then return 0; fi
  _LEA_CO_NEW=1
  if command -v cosign >/dev/null 2>&1; then _LEA_CO=cosign; _LEA_CO_TMP=; return 0; fi
  _LEA_CO_TMP=$(mktemp -d 2>/dev/null) || _LEA_CO_TMP=
  if [ -n "$_LEA_CO_TMP" ] && _LEA_CO=$(lea__cosign "$_LEA_CO_TMP"); then return 0; fi
  lea__co_put
  _LEA_CO_NEW=0
  lea_die "cosign not found: brew install cosign (lea verifies its images)"
}

# lea__co_put: forgets _LEA_CO and removes the cosign lea downloaded.
lea__co_put() {
  if [ -n "${_LEA_CO_TMP:-}" ]; then rm -rf "$_LEA_CO_TMP"; fi
  _LEA_CO='' _LEA_CO_TMP=''
}

# lea__image_reject IMAGE MESSAGE: removes IMAGE, so the next run pulls and checks it again instead
# of trusting what is on disk, and dies with MESSAGE; when the removal fails, MESSAGE says so.
lea__image_reject() {
  lea__co_put
  if docker image rm "$1" >/dev/null 2>&1; then lea_die "$2"; fi
  lea_die "$2; could not remove it: docker image rm $1"
}

# lea__verify_image IMAGE: IMAGE (a registry image tagged with a release, 0.5.0) is the one lea's
# release workflow signed: cosign checks its digest (the one docker recorded when it pulled it)
# against that workflow's identity at that very tag (a tag that is not a release, latest, against
# any release tag). Fails, removing IMAGE. A :dev image (built here) is skipped.
lea__verify_image() {
  case $1 in *:dev) return 0 ;; esac
  lea__co_get
  _vi_new=$_LEA_CO_NEW
  case ${1##*:} in
    [0-9]*.[0-9]*.[0-9]*) _vi_v=v${1##*:}; _vi_re=$(printf '%s' "$_vi_v" | sed 's/\./\\./g') ;;
    *) _vi_v='a release'; _vi_re='v[0-9]+\.[0-9]+\.[0-9]+' ;;
  esac
  _vi_d=$(docker image inspect --format '{{index .RepoDigests 0}}' "$1" 2>/dev/null) || _vi_d=
  case $_vi_d in *@sha256:*) ;; *) _vi_d= ;; esac
  if [ -n "$_vi_d" ] && "$_LEA_CO" verify --certificate-oidc-issuer https://token.actions.githubusercontent.com \
    --certificate-identity-regexp "^https://github\\.com/DavidGodefroid/lea/\\.github/workflows/release\\.yml@refs/tags/$_vi_re\$" \
    "$_vi_d" >/dev/null 2>&1; then
    lea_log "image $_vi_d: signature verified"
    if [ "$_vi_new" = 1 ]; then lea__co_put; fi
    return 0
  fi
  lea__image_reject "$1" "image $1 is not signed by lea's release $_vi_v: removed; check your network or registry, then run lea again"
}

# lea__ensure_image ROLE IMAGE: IMAGE present, else built (lea:dev, lea-proxy:dev), else pulled and
# verified; a failed pull dies with the command to retry.
lea__ensure_image() {
  docker image inspect "$2" >/dev/null 2>&1 && return 0
  case $2 in lea:dev|lea-proxy:dev) lea__build_image "$1"; return 0 ;; esac
  # cosign first: without one, nothing is pulled.
  lea__co_get
  _ei_new=$_LEA_CO_NEW
  lea_log "${LEA_FIRST_RUN:+first run: }pulling $2"
  if docker pull "$2" >&2; then
    lea__verify_image "$2"
    if [ "$_ei_new" = 1 ]; then lea__co_put; fi
    return 0
  fi
  if [ "$_ei_new" = 1 ]; then lea__co_put; fi
  lea_die "cannot pull $2: check your network or registry (docker pull $2), then run lea again"
}

lea_ensure_images() {
  lea__ensure_image lea "$LEA_IMAGE"
  lea__ensure_image proxy "$LEA_PROXY_IMAGE"
}

# lea_refresh_images: lea update. Rebuilds local images, pulls registry ones, and builds the
# locksql console image when it is absent (with LEA_LOCKSQL=1; never fatal).
lea_refresh_images() {
  # cosign first: without one, nothing is pulled.
  _ri_new=0
  case $LEA_IMAGE:$LEA_PROXY_IMAGE in lea:dev:lea-proxy:dev) ;; *) lea__co_get; _ri_new=$_LEA_CO_NEW ;; esac
  case $LEA_IMAGE in lea:dev) lea__build_image lea ;; *) docker pull "$LEA_IMAGE"; lea__verify_image "$LEA_IMAGE" ;; esac
  case $LEA_PROXY_IMAGE in lea-proxy:dev) lea__build_image proxy ;; *) docker pull "$LEA_PROXY_IMAGE"; lea__verify_image "$LEA_PROXY_IMAGE" ;; esac
  if [ "$_ri_new" = 1 ]; then lea__co_put; fi
  if [ "$LEA_LOCKSQL" = 1 ]; then lea__init_locksql; fi
}
