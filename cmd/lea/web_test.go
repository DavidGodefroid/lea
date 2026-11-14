package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DavidGodefroid/lea/internal/web"
)

func TestWebFlags(t *testing.T) {
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"--container", "lea-a", "--tls-self-signed"}, "go with --listen"},
		{[]string{"--container", "lea-a", "--new-token"}, "go with --listen"},
		{[]string{"--container", "lea-a", "--tls-cert", "c.pem", "--tls-key", "k.pem"}, "go with --listen"},
		{[]string{"--container", "lea-a", "--listen", "192.0.2.10:8443", "--port", "8414"}, "no --port"},
		{[]string{"--container", "lea-a", "--listen"}, "usage"},
		{[]string{"--listen", "192.0.2.10:8443"}, "usage"},
	} {
		var out, errb bytes.Buffer
		if code := runWeb(c.args, &out, &errb); code != 2 || !strings.Contains(errb.String(), c.want) {
			t.Fatalf("%q: %d %q", c.args, code, errb.String())
		}
	}
}

// TestOpenBrowserKeepsTheTokenOutOfTheOpenersCommandLine (#436): lea web --open hands the
// opener a private local file, never the token, in its argv or its environment.
func TestOpenBrowserKeepsTheTokenOutOfTheOpenersCommandLine(t *testing.T) {
	s, err := web.New(func(context.Context, string, any, any) error { return nil }, "lea-a")
	if err != nil {
		t.Fatal(err)
	}
	l, err := s.Listen(0)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	_, tok, _ := strings.Cut(s.URL(), "?token=")
	if len(tok) != 64 {
		t.Fatalf("url %q", s.URL())
	}
	run := t.TempDir()
	var opened string
	saved := openFile
	openFile = func(path string) error { opened = path; return nil }
	defer func() { openFile = saved }()
	cleanup, err := openBrowser(s, func(k string) string {
		if k == "XDG_RUNTIME_DIR" {
			return run
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	cmd := openerCommand(opened)
	for _, a := range append(append([]string{}, cmd.Args...), cmd.Env...) {
		if strings.Contains(a, tok) || strings.Contains(a, "token") {
			t.Fatalf("the opener's command line carries the token: %q %q", cmd.Args, cmd.Env)
		}
	}
	if filepath.Dir(filepath.Dir(opened)) != run {
		t.Fatalf("opened %q, not under %q", opened, run)
	}
	fi, err := os.Stat(opened)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("the page: %v %v", fi, err)
	}
	if di, err := os.Stat(filepath.Dir(opened)); err != nil || di.Mode().Perm() != 0o700 {
		t.Fatalf("its directory: %v %v", di, err)
	}
	page, _ := os.ReadFile(opened)
	if !strings.Contains(string(page), tok) || strings.Contains(string(page), "?token=") {
		t.Fatalf("the page does not POST the token: %s", page)
	}
	cleanup()
	if _, err := os.Stat(filepath.Dir(opened)); !os.IsNotExist(err) {
		t.Fatalf("cleanup left %q: %v", opened, err)
	}
}
