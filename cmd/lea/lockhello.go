package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"time"

	"github.com/DavidGodefroid/lea/internal/locks"
)

// runLockHello is lea lock-hello --uid UID [--protocol N] DIR [SOCKET...]: the hello handshake,
// of protocol major N (1 without: lea doctor always names the one lea's client of the lock
// speaks), of each console socket SOCKET of DIR (every one of DIR without), that UID serves, one
// JSON array on stdout (locks.AskHello). lea doctor runs it in lea's image,
// with only the lock's socket volume mounted, to learn the protocol and version of the lock lox
// runs (lea#134). It sends nothing but hello.
func runLockHello(args []string, stdout, stderr io.Writer) int {
	const usage = "usage: lea lock-hello --uid UID [--protocol N] DIR [SOCKET...]"
	if len(args) < 3 || args[0] != "--uid" {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	uid, err := strconv.Atoi(args[1])
	args = args[2:]
	protocol := 1
	if err == nil && len(args) > 0 && args[0] == "--protocol" {
		if len(args) < 2 {
			err = errors.New("no protocol")
		} else {
			protocol, err = strconv.Atoi(args[1])
			args = args[2:]
		}
	}
	if err != nil || uid < 1 || protocol < 1 || len(args) == 0 || !filepath.IsAbs(args[0]) {
		fmt.Fprintln(stderr, usage+" (an absolute DIR)")
		return 2
	}
	rs, err := locks.AskHello(args[0], args[1:], uid, protocol, 5*time.Second)
	if err != nil {
		fmt.Fprintln(stderr, "lea lock-hello:", err)
		return 1
	}
	b, _ := json.Marshal(rs)
	fmt.Fprintln(stdout, string(b))
	return 0
}
