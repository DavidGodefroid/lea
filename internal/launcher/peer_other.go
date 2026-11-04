//go:build !linux

package launcher

// socketPeerPid is 0 off Linux: the daemon is told from its pid file only (isDaemon reads
// /proc, which is Linux's).
func socketPeerPid(string) int { return 0 }
