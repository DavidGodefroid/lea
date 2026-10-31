# shellcheck shell=sh
# The lea and proxy images: pulled from the registry, else built from this installation.
# Locally built images are named lea:dev and lea-proxy:dev and recorded in the user config.

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

# lea__ensure_image ROLE IMAGE KEY: IMAGE present, else pulled, else built and recorded as KEY.
lea__ensure_image() {
  docker image inspect "$2" >/dev/null 2>&1 && return 0
  case $2 in lea:dev|lea-proxy:dev) lea__build_image "$1"; return 0 ;; esac
  docker pull "$2" >&2 && return 0
  lea_log "cannot pull $2: building it locally instead"
  lea__build_image "$1"
  if [ "$1" = proxy ]; then LEA_PROXY_IMAGE=lea-proxy:dev; else LEA_IMAGE=lea:dev; fi
  lea_user_config_set "$3" "$(if [ "$1" = proxy ]; then echo lea-proxy:dev; else echo lea:dev; fi)"
}

lea_ensure_images() {
  lea__ensure_image lea "$LEA_IMAGE" LEA_IMAGE
  lea__ensure_image proxy "$LEA_PROXY_IMAGE" LEA_PROXY_IMAGE
}

# lea_refresh_images: lea update. Rebuilds local images, pulls registry ones, and builds the
# locksql console image when it is absent (with LEA_LOCKSQL=1; never fatal).
lea_refresh_images() {
  case $LEA_IMAGE in lea:dev) lea__build_image lea ;; *) docker pull "$LEA_IMAGE" ;; esac
  case $LEA_PROXY_IMAGE in lea-proxy:dev) lea__build_image proxy ;; *) docker pull "$LEA_PROXY_IMAGE" ;; esac
  if [ "$LEA_LOCKSQL" = 1 ]; then lea__init_locksql; fi
}
