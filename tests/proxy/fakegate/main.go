// Command lockhttp is a stand-in for LockHTTP's gate in lea's proxy tests (tests/proxy/run.sh):
// it relays each connection on --listen to --upstream (squid) unchanged, and logs who it runs as
// and the Proxy-Authorization and X-Lea-Source of each request head to <audit-dir>/fake.log. It
// writes a status file as the real gate does. `lockhttp gate --check --listen ADDR` exits 0 when
// ADDR answers.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

func main() {
	if len(os.Args) < 2 || os.Args[1] != "gate" {
		fmt.Fprintln(os.Stderr, "usage: lockhttp gate ...")
		os.Exit(2)
	}
	fs := flag.NewFlagSet("gate", flag.ExitOnError)
	listen := fs.String("listen", "", "")
	upstream := fs.String("upstream", "", "")
	check := fs.Bool("check", false, "")
	audit := fs.String("audit-dir", "", "")
	status := fs.String("status-file", "", "")
	fs.String("profile", "", "")
	fs.String("sources", "", "")
	fs.Parse(os.Args[2:])
	if *check {
		c, err := net.Dial("tcp", *listen)
		if err != nil {
			os.Exit(1)
		}
		c.Close()
		return
	}
	u, err := url.Parse(*upstream)
	if err != nil {
		panic(err)
	}
	logf, err := os.OpenFile(filepath.Join(*audit, "fake.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		panic(err)
	}
	var mu sync.Mutex
	logLine := func(s string) { mu.Lock(); fmt.Fprintln(logf, s); mu.Unlock() }
	groups, _ := os.Getgroups()
	logLine(fmt.Sprintf("uid=%d gid=%d groups=%v runtime=%s", os.Getuid(), os.Getgid(), groups, os.Getenv("LOCKHTTP_RUNTIME_DIR")))
	if err := os.WriteFile(*status, []byte(`{"version":1,"console":"up","pending":0}`+"\n"), 0o644); err != nil {
		panic(err)
	}
	l, err := net.Listen("tcp", *listen)
	if err != nil {
		panic(err)
	}
	for {
		c, err := l.Accept()
		if err != nil {
			continue
		}
		go relay(c, u.Host, logLine)
	}
}

func relay(c net.Conn, up string, logLine func(string)) {
	defer c.Close()
	br := bufio.NewReader(c)
	var head strings.Builder
	auth, src, first := "-", "-", ""
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return
		}
		head.WriteString(line)
		t := strings.TrimRight(line, "\r\n")
		if first == "" {
			first = t
		}
		if k, v, ok := strings.Cut(t, ":"); ok {
			switch strings.ToLower(k) {
			case "proxy-authorization":
				auth = strings.TrimSpace(v)
			case "x-lea-source":
				src = strings.TrimSpace(v)
			}
		}
		if t == "" {
			break
		}
	}
	logLine(fmt.Sprintf("request %q auth=%s source=%s", first, auth, src))
	u, err := net.Dial("tcp", up)
	if err != nil {
		return
	}
	defer u.Close()
	io.WriteString(u, head.String())
	go func() { io.Copy(u, br); u.Close() }()
	io.Copy(c, u)
}
