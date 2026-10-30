package tmux

import (
	"reflect"
	"testing"
)

// Only user clients' terminals ring: never a control-mode client, nor a path that is no terminal.
func TestParseClientTTYs(t *testing.T) {
	got := parseClientTTYs("0 /dev/pts/3\n1 /dev/pts/4\n0 /tmp/x\n0 /dev/tty2\n\n")
	if want := []string{"/dev/pts/3", "/dev/tty2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("%q", got)
	}
}
