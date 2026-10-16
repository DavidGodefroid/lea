package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestHookUnknownVendorOrArityExitsZeroSilently(t *testing.T) {
	for _, args := range [][]string{{"hook", "other", "x"}, {"hook"}, {"hook", "claude"}, {"hook", "claude", "Stop", "extra"}} {
		var out, errb bytes.Buffer
		if rc := run(args, strings.NewReader(""), &out, &errb); rc != 0 || out.Len() != 0 {
			t.Errorf("%v: rc=%d out=%q", args, rc, out.String())
		}
	}
}
