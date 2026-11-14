package main

import (
	"bytes"
	"strings"
	"testing"
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
