//go:build !linux

package locks

import (
	"errors"
	"net"
)

// socketPeerUID: the handshake probe runs in lea's (Linux) image only.
func socketPeerUID(net.Conn) (int, error) {
	return 0, errors.New("peer credentials are read on Linux only")
}
