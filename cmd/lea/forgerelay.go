package main

import (
	"fmt"
	"io"
	"net"
	"os"
	"time"
)

// runForgeRelay is lea forge-relay SOCKET HOST:PORT, started by the lea container's entrypoint
// (as nobody, issue #103): gh's requests (its http_unix_socket) go from SOCKET to lea-keyproxy's
// forge forwarder in the proxy, which adds the GitHub token. The relay holds no secret: it copies
// bytes.
func runForgeRelay(args []string, stderr io.Writer) int {
	if len(args) != 2 {
		fmt.Fprintln(stderr, "usage: lea forge-relay SOCKET HOST:PORT")
		return 2
	}
	_ = os.Remove(args[0])
	ln, err := net.Listen("unix", args[0])
	if err != nil {
		fmt.Fprintln(stderr, "lea forge-relay:", err)
		return 1
	}
	// Every user of the container may connect: what it reaches is the forwarder, not the token.
	if err := os.Chmod(args[0], 0o666); err != nil {
		fmt.Fprintln(stderr, "lea forge-relay:", err)
		return 1
	}
	if err := forgeRelay(ln, args[1]); err != nil {
		fmt.Fprintln(stderr, "lea forge-relay:", err)
		return 1
	}
	return 0
}

// forgeRelay copies each connection of ln to a new TCP connection to target, both ways.
func forgeRelay(ln net.Listener, target string) error {
	for {
		c, err := ln.Accept()
		if err != nil {
			return err
		}
		go func() {
			defer c.Close()
			up, err := net.DialTimeout("tcp", target, 10*time.Second)
			if err != nil {
				return
			}
			defer up.Close()
			done := make(chan struct{})
			go func() {
				_, _ = io.Copy(up, c)
				if t, ok := up.(*net.TCPConn); ok {
					_ = t.CloseWrite()
				}
				close(done)
			}()
			_, _ = io.Copy(c, up)
			if u, ok := c.(*net.UnixConn); ok {
				_ = u.CloseWrite()
			}
			<-done
		}()
	}
}
