#!/bin/sh
# Installs a sandboxed OCI runtime on this host and registers it with dockerd (#106), as lea's CI
# does before `LEA_TEST_RUNTIME=<name> make test-isolation`. Linux, Docker Engine, sudo.
#   tests/integration/install-runtime.sh runsc   gVisor (GVISOR_RELEASE, under /opt/gvisor), registered as runsc
#   tests/integration/install-runtime.sh kata    Kata Containers (KATA_VERSION, QEMU, runtime-rs), as kata
# It rewrites /etc/docker/daemon.json (keeping its other keys) and restarts dockerd: running
# containers stop, unless they have a restart policy. Pinned releases, checksum-checked.
set -eu
GVISOR_RELEASE=${GVISOR_RELEASE:-20261102.0}
KATA_VERSION=${KATA_VERSION:-4.3.1}
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# register NAME JSON: adds runtime NAME (its dockerd configuration JSON) to daemon.json.
register() {
  sudo mkdir -p /etc/docker
  [ -s /etc/docker/daemon.json ] || echo '{}' | sudo tee /etc/docker/daemon.json >/dev/null
  sudo python3 -I - "$1" "$2" <<'PY'
import json, sys
p = "/etc/docker/daemon.json"
with open(p) as f:
    d = json.load(f)
d.setdefault("runtimes", {})[sys.argv[1]] = json.loads(sys.argv[2])
with open(p, "w") as f:
    json.dump(d, f, indent=2)
PY
  sudo systemctl restart docker
  i=0
  until docker info >/dev/null 2>&1; do
    i=$((i + 1)); [ "$i" -lt 30 ] || { echo "install-runtime: dockerd did not come back" >&2; exit 1; }
    sleep 1
  done
}

case ${1:-} in
  runsc)
    arch=$(uname -m)
    base=https://storage.googleapis.com/gvisor/releases/release/$GVISOR_RELEASE/$arch
    # The release is one tarball: runsc, its shim and its helpers (gvisor-bin/), kept together.
    (cd "$work" && curl -fsSLO "$base/gvisor.tar.bz2" && curl -fsSLO "$base/gvisor.tar.bz2.sha512" && sha512sum -c gvisor.tar.bz2.sha512)
    sudo rm -rf /opt/gvisor; sudo mkdir -p /opt/gvisor
    sudo tar -xjf "$work/gvisor.tar.bz2" -C /opt/gvisor
    sudo ln -sf /opt/gvisor/runsc /usr/local/bin/runsc
    # The flags lea needs (README, "A second kernel: gVisor or Kata"): lea's seccomp profile applied
    # inside the sandbox (--oci-seccomp: runsc ignores it otherwise), its bus socket and the lock
    # consoles' sockets are host Unix sockets the agents connect to (--host-uds=open), the
    # clipboard is a host FIFO (--host-fifo=open).
    register runsc '{"path":"/opt/gvisor/runsc","runtimeArgs":["--oci-seccomp","--host-uds=open","--host-fifo=open"]}'
    runsc --version
    ;;
  kata)
    arch=$(uname -m); case $arch in x86_64) arch=amd64 ;; aarch64) arch=arm64 ;; esac
    tar=kata-static-$KATA_VERSION-$arch.tar.zst
    url=https://github.com/kata-containers/kata-containers/releases/download/$KATA_VERSION
    (cd "$work" && curl -fsSLO "$url/$tar")
    # The release publishes no checksum file: the digest of the tarball lea's CI tested.
    if [ -n "${KATA_SHA256:-}" ]; then echo "$KATA_SHA256  $work/$tar" | sha256sum -c -; fi
    command -v zstd >/dev/null || sudo apt-get install -y -q zstd >/dev/null
    sudo tar --zstd -xf "$work/$tar" -C /
    cfg=/opt/kata/share/defaults/kata-containers/runtime-rs
    sudo ln -sf "$cfg/configuration-qemu-runtime-rs.toml" "$cfg/configuration.toml"
    # Kata applies the container's seccomp profile in the guest only when asked (lea's
    # image/seccomp.json, the AF_ALG refusal of make test-isolation).
    sudo sed -i 's/^#\{0,1\}[[:space:]]*disable_guest_seccomp[[:space:]]*=.*/disable_guest_seccomp = false/' "$cfg/configuration-qemu-runtime-rs.toml"
    # The nested rootless Podman maps its user namespace with newuidmap, whose file capability is an
    # extended attribute of lea's image: virtiofsd passes extended attributes only with --xattr
    # (without it newuidmap gets EPERM writing uid_map, and lea never becomes ready).
    sudo sed -i 's/^virtio_fs_extra_args = \[/virtio_fs_extra_args = ["--xattr", /' "$cfg/configuration-qemu-runtime-rs.toml"
    grep -E '^(virtio_fs_extra_args|disable_guest_seccomp|shared_fs) ' "$cfg/configuration-qemu-runtime-rs.toml"
    sudo ln -sf /opt/kata/runtime-rs/bin/containerd-shim-kata-v2 /usr/local/bin/containerd-shim-kata-v2
    # The shim (root) opens /dev/kvm; the runner's is root-only by default.
    if [ -e /dev/kvm ]; then sudo chmod 0666 /dev/kvm; else echo "install-runtime: no /dev/kvm: Kata needs hardware virtualisation" >&2; exit 1; fi
    register kata '{"runtimeType":"io.containerd.kata.v2"}'
    /opt/kata/bin/kata-runtime --version 2>/dev/null || true
    ;;
  *) echo "usage: $0 runsc|kata" >&2; exit 2 ;;
esac
docker info --format '{{json .Runtimes}}'
