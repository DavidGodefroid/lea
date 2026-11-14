package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strconv"
	"syscall"

	"github.com/DavidGodefroid/lea/internal/identity"
	"github.com/DavidGodefroid/lea/internal/web"
)

const webUsage = "usage: lea web --container NAME [--port N] [--open]"

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
	port, open := 0, false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--container", "--port":
			if i+1 >= len(args) {
				fmt.Fprintln(stderr, webUsage)
				return 2
			}
			if args[i] == "--container" {
				container = args[i+1]
			} else {
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
		default:
			fmt.Fprintln(stderr, webUsage)
			return 2
		}
	}
	if container == "" {
		fmt.Fprintln(stderr, webUsage)
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
	l, err := s.Listen(port)
	if err != nil {
		fmt.Fprintln(stderr, "lea web:", clean(err.Error(), false))
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	fmt.Fprintf(stdout, "lea web for %s: %s\n", container, s.URL())
	fmt.Fprintln(stdout, "The link signs one browser in, once; ctrl+c stops lea web.")
	if open {
		if err := openURL(s.URL()); err != nil {
			fmt.Fprintln(stderr, "lea web: could not open a browser:", clean(err.Error(), false))
		}
	}
	if err := s.Serve(ctx, l); err != nil {
		fmt.Fprintln(stderr, "lea web:", clean(err.Error(), false))
		return 1
	}
	return 0
}
