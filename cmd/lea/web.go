package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"

	"github.com/DavidGodefroid/lea/internal/identity"
	"github.com/DavidGodefroid/lea/internal/web"
)

const webUsage = "usage: lea web --container NAME [--port N] [--open]"

// openerCommand opens the local file path in the user's browser. Its command line is readable by
// every local user (ps, /proc), so it carries a path, never the token (#436).
func openerCommand(path string) *exec.Cmd {
	cmd := "xdg-open"
	if runtime.GOOS == "darwin" {
		cmd = "open"
	}
	return exec.Command(cmd, path)
}

// openFile starts the opener; a test seam.
var openFile = func(path string) error { return openerCommand(path).Start() }

// openBrowser signs the user's browser in without the token in any command line: it writes the
// server's bootstrap page, which POSTs the token, to a private file (0600, in a 0700 directory of
// XDG_RUNTIME_DIR or the temporary directory) and opens that file. cleanup removes it.
func openBrowser(s *web.Server, getenv func(string) string) (cleanup func(), err error) {
	dir, err := os.MkdirTemp(getenv("XDG_RUNTIME_DIR"), "lea-web-")
	if err != nil {
		return func() {}, err
	}
	cleanup = func() { os.RemoveAll(dir) }
	path := filepath.Join(dir, "open.html")
	if err := os.WriteFile(path, s.BootstrapPage(), 0o600); err != nil {
		return cleanup, err
	}
	return cleanup, openFile(path)
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
	fmt.Fprintf(stdout, "The link signs one browser in, once, within %d minutes; ctrl+c stops lea web.\n", int(web.TokenTTL.Minutes()))
	if open {
		cleanup, err := openBrowser(s, os.Getenv)
		defer cleanup()
		if err != nil {
			fmt.Fprintln(stderr, "lea web: could not open a browser:", clean(err.Error(), false))
		}
	}
	if err := s.Serve(ctx, l); err != nil {
		fmt.Fprintln(stderr, "lea web:", clean(err.Error(), false))
		return 1
	}
	return 0
}
