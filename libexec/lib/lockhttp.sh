# shellcheck shell=sh
# LockHTTP (LEA_LOCKHTTP=1): lea doctor's checks of lockhttp's launcher and of the gate container
# beside the proxy (contract: lockhttp's docs/lea.md, §6). The gate runs from the image the
# launcher pins, as gate (uid 10003 in the image), with the console's socket volume lockhttp-run.

# LEA_LOCKHTTP_RELEASE: the lockhttp release lea pins: the one this lea is tested with, whose gate
# has every flag lea may pass (hub mode v0.3.0, devices v0.4.0, facade keys v0.5.0). lea runs the
# image of the launcher installed on the host; lea doctor warns when it is older than this one.
LEA_LOCKHTTP_RELEASE=v0.5.1

# lea__lockhttp_version BIN: the launcher_version line of lockhttp's launcher BIN (its first 64 KiB,
# as internal/launcher/lockhttp.go reads it), empty when it has none.
lea__lockhttp_version() {
  head -c 65536 "$1" | sed -n 's/^launcher_version=\([0-9A-Za-z.+-]*\)$/\1/p' | head -n 1
}

# lea__lockhttp_older V: the launcher release V (0.4.0, v0.4.0, dev for a clone) is older than
# LEA_LOCKHTTP_RELEASE, as lockHTTPAtLeast (internal/launcher/lockhubgate.go) decides: on its
# X.Y.Z, a pre-release of the pin itself being older; anything not a release is older.
lea__lockhttp_older() {
  [ "$1" != dev ] || return 1
  _lo_n=${1#v}; _lo_b=${_lo_n%%[-+]*}
  lea__semver "v$_lo_b" >/dev/null || return 0
  lea__older "v$_lo_b" "$LEA_LOCKHTTP_RELEASE" && return 0
  [ "v$_lo_b" = "$LEA_LOCKHTTP_RELEASE" ] || return 1
  case ${_lo_n%%+*} in *-*) return 0 ;; esac
  return 1
}

# lea__lockhttp_image: the gate's image as lea's launcher runs it (internal/launcher/lockhttp.go):
# the release image by the digest lockhttp's launcher pins, or the local image lockhttp for a
# launcher run from a clone. On failure, prints why and returns 1.
lea__lockhttp_image() {
  if ! _lhi_b=$(lea__lockhost_bin lockhttp); then
    printf '%s' "no root-owned launcher in /usr/local/bin: install it (lockhttp's scripts/install.sh, docs/docker.md)"; return 1
  fi
  _lhi_v=$(lea__lockhttp_version "$_lhi_b")
  _lhi_d=$(head -c 65536 "$_lhi_b" | sed -n 's/^image_digest=//p' | head -n 1)
  if [ -z "$_lhi_v" ]; then
    printf '%s' "$_lhi_b is not lockhttp's launcher (a v0.1.0 host install?): install the launcher (docs/docker.md)"; return 1
  fi
  if [ "$_lhi_v" = dev ]; then printf lockhttp; return 0; fi
  if [ -z "$_lhi_d" ]; then
    printf '%s' "$_lhi_b pins no image digest (installed with LOCKHTTP_SKIP_COSIGN=1?): install it again with cosign"; return 1
  fi
  if ! printf '%s\n' "$_lhi_d" | grep -qx 'sha256:[0-9a-f]\{64\}'; then
    printf '%s' "$_lhi_b: invalid image digest"; return 1
  fi
  printf 'ghcr.io/davidgodefroid/lockhttp@%s' "$_lhi_d"
}

lea__doctor_lockhttp() {
  [ "${LEA_LOCKHTTP:-0}" = 1 ] || return 0
  _dh_rc=0 _dh_g=$LEA_PREFIX-lockhttp-gate
  if _dh_img=$(lea__lockhttp_image); then lea__ok "lockhttp launcher (gate image $_dh_img)"
  else lea__fail "lockhttp launcher" "$_dh_img"; _dh_img=; _dh_rc=1; fi
  # The launcher's release, but for a clone's (dev).
  _dh_v=
  if _dh_b=$(lea__lockhost_bin lockhttp); then _dh_v=$(lea__lockhttp_version "$_dh_b"); fi
  if [ -n "$_dh_v" ] && lea__lockhttp_older "$_dh_v"; then
    lea__warn "lockhttp launcher" "v${_dh_v#v}, older than $LEA_LOCKHTTP_RELEASE, the release lea pins (lea leaves out the gate's features it lacks): LOCKHTTP_VERSION=$LEA_LOCKHTTP_RELEASE sh install.sh (lockhttp's docs/docker.md)"
  fi
  if [ -z "${LEA_GIT_SSH:-}" ]; then lea__ok "LockHTTP: no SSH forwarder (LEA_GIT_SSH empty)"
  else lea__fail "LockHTTP" "LEA_GIT_SSH is set: its forwarders would bypass the gate"; _dh_rc=1; fi
  if [ -n "${LEA_OTEL_UPSTREAM:-}" ]; then lea__fail "LockHTTP" "LEA_OTEL_UPSTREAM is set: its TCP forwarder would bypass the gate"; _dh_rc=1; fi
  if lea_container_running "$LEA_PREFIX-proxy"; then
    _dh_restart="lea stop --all, then lea"
    _dh_i=$(docker inspect -f '{{.State.Running}}|{{.Config.Image}}|{{.Config.User}}|{{range .Mounts}}{{.Name}}{{.Source}}>{{.Destination}};{{end}}' "$_dh_g" 2>/dev/null || true)
    _dh_run=${_dh_i%%|*} _dh_i=${_dh_i#*|}
    _dh_gimg=${_dh_i%%|*} _dh_i=${_dh_i#*|}
    _dh_user=${_dh_i%%|*} _dh_mounts=${_dh_i#*|}
    if [ "$_dh_run" != true ]; then
      lea__fail "LockHTTP gate" "$_dh_g is not running: agents' web requests are refused ($_dh_restart)"; _dh_rc=1
    else
      if [ -n "$_dh_img" ] && [ "$_dh_gimg" != "$_dh_img" ]; then
        lea__fail "LockHTTP gate image" "$_dh_gimg, not the launcher's $_dh_img ($_dh_restart)"; _dh_rc=1
      elif [ -n "$_dh_img" ]; then lea__ok "LockHTTP gate runs the pinned image"; fi
      if [ "$_dh_user" = gate ]; then lea__ok "LockHTTP gate runs as gate"
      else lea__fail "LockHTTP gate user" "'$_dh_user', not gate ($_dh_restart)"; _dh_rc=1; fi
      case ";$_dh_mounts" in
        *lockhttp-home*|*docker.sock*|*podman.sock*)
          lea__fail "LockHTTP gate mounts" "the console's home or an engine socket: $_dh_mounts ($_dh_restart)"; _dh_rc=1 ;;
        *";lockhttp-run"*">/run/lockhttp;"*) lea__ok "LockHTTP gate has the console's socket volume only" ;;
        *) lea__fail "LockHTTP gate mounts" "no lockhttp-run volume on /run/lockhttp ($_dh_restart)"; _dh_rc=1 ;;
      esac
      if docker exec "$_dh_g" lockhttp gate --check --listen 127.0.0.1:3130 >/dev/null 2>&1; then lea__ok "LockHTTP gate answers"
      else lea__fail "LockHTTP gate" "does not answer: docker logs $_dh_g ($_dh_restart)"; _dh_rc=1; fi
      # A proxy started again leaves its gate in the old network namespace, still answering there.
      if docker exec "$LEA_PREFIX-proxy" nc -z 127.0.0.1 3130 >/dev/null 2>&1; then lea__ok "LockHTTP gate in the proxy's network namespace"
      else lea__fail "LockHTTP gate" "not in the proxy's network namespace (the proxy started again): lea, or $_dh_restart"; _dh_rc=1; fi
    fi
    # squid behind the gate: on the loopback only, never on an address the agents reach.
    _dh_l=$(docker exec "$LEA_PREFIX-proxy" netstat -ltn 2>/dev/null | awk '$4 ~ /:3128$/ { print $4 }')
    if [ -n "$_dh_l" ] && ! printf '%s\n' "$_dh_l" | grep -qv '^127\.0\.0\.1:3128$'; then lea__ok "squid listens on 127.0.0.1 only"
    else lea__fail "squid" "listens on '$(printf '%s' "$_dh_l" | tr '\n' ' ')', not 127.0.0.1:3128 only: $_dh_restart"; _dh_rc=1; fi
  fi
  if [ "${LEA_LOCKHTTP_MITM:-0}" = 1 ]; then
    _dh_ca=$(lea_data_dir)/lockhttp/ca/ca.pem
    if [ ! -f "$_dh_ca" ]; then lea__warn "LockHTTP CA" "none yet: the next lea makes it (lockhttp ca init)"
    elif ! command -v openssl >/dev/null 2>&1; then lea__warn "LockHTTP CA" "openssl is missing: its expiry is not checked"
    elif openssl x509 -checkend 604800 -noout -in "$_dh_ca" >/dev/null 2>&1; then lea__ok "LockHTTP CA valid for more than 7 days"
    else lea__fail "LockHTTP CA" "expires within 7 days: lockhttp ca init --force --dir $(lea_data_dir)/lockhttp/ca, then lea stop --all, and lea"; _dh_rc=1; fi
    if lea_container_running "$LEA_PREFIX-proxy"; then
      # The signer alone holds the CA key (the volume $LEA_PREFIX-lockhttp-ca), as signer, offline.
      _dh_sg=$LEA_PREFIX-lockhttp-signer
      _dh_i=$(docker inspect -f '{{.State.Running}}|{{.Config.User}}|{{.HostConfig.NetworkMode}}' "$_dh_sg" 2>/dev/null || true)
      if [ "$_dh_i" != "true|signer|none" ]; then
        lea__fail "LockHTTP signer" "$_dh_sg is not running as signer without a network ('$_dh_i'): lea stop --all, then lea"; _dh_rc=1
      elif docker exec "$_dh_sg" test -S /run/lockhttp-signer/signer.sock >/dev/null 2>&1; then lea__ok "LockHTTP signer listening"
      else lea__fail "LockHTTP signer" "no socket: docker logs $_dh_sg"; _dh_rc=1; fi
      case $(docker container inspect --format '{{range .Mounts}}{{.Name}}{{.Source}};{{end}}' "$LEA_PREFIX-lockhttp-gate" "$LEA_PREFIX-proxy" 2>/dev/null) in
        *lockhttp-ca*) lea__fail "LockHTTP CA key" "mounted in the gate or the proxy, not the signer only: lea stop --all, then lea"; _dh_rc=1 ;;
        *) lea__ok "LockHTTP CA key in the signer's container only" ;;
      esac
    fi
  fi
  _dh_s=$(lea_data_dir)/lockhttp/status/status.json
  case $(sed -n 's/.*"console"[[:space:]]*:[[:space:]]*"\([a-z]*\)".*/\1/p' "$_dh_s" 2>/dev/null | head -n 1) in
    up) lea__ok "lockhttp console up" ;;
    down) lea__warn "lockhttp console" "down: agents' requests are refused (E_NO_CONSOLE); in a terminal you keep in view: lockhttp console" ;;
    *) lea__warn "lockhttp console" "unknown (no gate status yet)" ;;
  esac
  return "$_dh_rc"
}
