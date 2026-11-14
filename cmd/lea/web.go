package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"

	"github.com/DavidGodefroid/lea/internal/deck"
	"github.com/DavidGodefroid/lea/internal/identity"
	"github.com/DavidGodefroid/lea/internal/web"
)

const webUsage = "usage: lea web --container NAME [--port N] [--open] " +
	"[--listen ADDR:PORT (--tls-self-signed | --tls-cert FILE --tls-key FILE) [--new-token]]"

// openURL opens a URL in the user's browser; a test seam.
var openURL = func(url string) error {
	cmd := "xdg-open"
	if runtime.GOOS == "darwin" {
		cmd = "open"
	}
	return exec.Command(cmd, url).Start()
}

// runWeb is lea web (#414): the deck in the browser, for one lea, on the host's loopback. The
// launcher names the container (the projects directory's lea); it serves until ctrl+c.
func runWeb(args []string, stdout, stderr io.Writer) int {
	var container string
	var ro web.RemoteOptions
	port, open := 0, false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--container", "--port", "--listen", "--tls-cert", "--tls-key":
			if i+1 >= len(args) {
				fmt.Fprintln(stderr, webUsage)
				return 2
			}
			switch args[i] {
			case "--container":
				container = args[i+1]
			case "--listen":
				ro.Addr = args[i+1]
			case "--tls-cert":
				ro.CertFile = args[i+1]
			case "--tls-key":
				ro.KeyFile = args[i+1]
			default:
				p, err := strconv.Atoi(args[i+1])
				if err != nil || p < 1 || p > 65535 {
					fmt.Fprintln(stderr, "lea web: --port must be a port number (1-65535)")
					return 2
				}
				port = p
			}
			i++
		case "--open":
			open = true
		case "--tls-self-signed":
			ro.SelfSigned = true
		case "--new-token":
			ro.NewToken = true
		default:
			fmt.Fprintln(stderr, webUsage)
			return 2
		}
	}
	if container == "" {
		fmt.Fprintln(stderr, webUsage)
		return 2
	}
	if ro.Addr == "" && (ro.CertFile != "" || ro.KeyFile != "" || ro.SelfSigned || ro.NewToken) {
		fmt.Fprintln(stderr, "lea web: --tls-cert, --tls-key, --tls-self-signed and --new-token go with --listen")
		return 2
	}
	if ro.Addr != "" && port != 0 {
		fmt.Fprintln(stderr, "lea web: --listen names its port: no --port with it")
		return 2
	}
	if insideLea(os.Getenv, fileExists) {
		fmt.Fprintln(stderr, "lea web: host only, not inside lea")
		return 1
	}
	if err := hostCall("providers_list", nil, nil); err != nil {
		fmt.Fprintln(stderr, "lea web:", clean(err.Error(), false))
		return 1
	}
	s, err := web.New(web.SocketCaller(identity.DaemonSock(os.Getenv)), container)
	if err != nil {
		fmt.Fprintln(stderr, "lea web:", clean(err.Error(), false))
		return 1
	}
	if p, ok := deck.LayoutPath(identity.HostDir(os.Getenv), container); ok {
		s.SetLayoutPath(p)
	}
	var l net.Listener
	url := ""
	if ro.Addr != "" {
		ro.Dir = filepath.Join(identity.HostDir(os.Getenv), "web")
		var r web.Remote
		l, r, err = s.ListenRemote(ro)
		if err != nil {
			fmt.Fprintln(stderr, "lea web:", clean(err.Error(), false))
			return 1
		}
		url = r.URL
		fmt.Fprintf(stderr, "WARNING: lea web listens on the network, on %s: whoever has its token drives your agents and answers their permissions.\n", ro.Addr)
		fmt.Fprintln(stderr, "WARNING: an SSH tunnel (ssh -L) to the default 127.0.0.1 is safer; ctrl+c stops lea web.")
		fmt.Fprintf(stdout, "lea web for %s: %s\n", container, r.URL)
		fmt.Fprintf(stdout, "TLS certificate %s, SHA-256 %s: check it in the browser before you sign in.\n", r.CertFile, r.Fingerprint)
		if r.Token != "" {
			fmt.Fprintf(stdout, "New token (also in %s): %s\n", r.TokenFile, r.Token)
		} else {
			fmt.Fprintf(stdout, "Sign in with the token in %s (--new-token replaces it).\n", r.TokenFile)
		}
	} else {
		l, err = s.Listen(port)
		if err != nil {
			fmt.Fprintln(stderr, "lea web:", clean(err.Error(), false))
			return 1
		}
		url = s.URL()
		fmt.Fprintf(stdout, "lea web for %s: %s\n", container, url)
		fmt.Fprintln(stdout, "The link signs one browser in, once; ctrl+c stops lea web.")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if open {
		if err := openURL(url); err != nil {
			fmt.Fprintln(stderr, "lea web: could not open a browser:", clean(err.Error(), false))
		}
	}
	if err := s.Serve(ctx, l); err != nil {
		fmt.Fprintln(stderr, "lea web:", clean(err.Error(), false))
		return 1
	}
	return 0
}
