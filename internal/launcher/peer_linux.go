package launcher

import (
	"net"
	"os"
	"syscall"
	"time"
)

// socketPeerPid is the pid of the process listening on the unix socket path, from the kernel's
// peer credentials, when it runs as this user; 0 when nothing answers or it cannot be told.
func socketPeerPid(path string) int {
	c, err := net.DialTimeout("unix", path, time.Second)
	if err != nil {
		return 0
	}
	defer c.Close()
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return 0
	}
	rc, err := uc.SyscallConn()
	if err != nil {
		return 0
	}
	var cred *syscall.Ucred
	var cerr error
	if err := rc.Control(func(fd uintptr) {
		cred, cerr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil || cerr != nil || cred == nil || int(cred.Uid) != os.Getuid() {
		return 0
	}
	return int(cred.Pid)
}
