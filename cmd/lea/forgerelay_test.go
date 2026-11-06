package main

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestForgeRelayCarriesHTTPToTheForwarder(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, r.Host+" "+r.URL.Path)
	}))
	defer up.Close()
	sock := filepath.Join(t.TempDir(), "gh.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() { _ = forgeRelay(ln, up.Listener.Addr().String()) }()
	c := &http.Client{Transport: &http.Transport{Dial: func(_, _ string) (net.Conn, error) { return net.Dial("unix", sock) }}}
	for i := 0; i < 3; i++ {
		res, err := c.Get("http://api.github.com/user")
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if string(b) != "api.github.com /user" {
			t.Errorf("%q", b)
		}
	}
}

func TestForgeRelayUsage(t *testing.T) {
	if runForgeRelay(nil, io.Discard) != 2 {
		t.Error("no arguments accepted")
	}
}
