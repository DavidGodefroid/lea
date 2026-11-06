package locks

import (
	"errors"
	"net"
	"syscall"
)

// socketPeerUID is the uid of the process serving the unix connection c (SO_PEERCRED).
func socketPeerUID(c net.Conn) (int, error) {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return 0, errors.New("not a unix socket")
	}
	rc, err := uc.SyscallConn()
	if err != nil {
		return 0, err
	}
	var cred *syscall.Ucred
	var cerr error
	if err := rc.Control(func(fd uintptr) {
		cred, cerr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil || cerr != nil || cred == nil {
		return 0, errors.New("cannot read the socket's peer credentials")
	}
	return int(cred.Uid), nil
}
