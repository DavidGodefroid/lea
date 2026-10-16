package tmux

import (
	"context"
	"reflect"
	"testing"
)

func TestControlAttachPinsLeaConfig(t *testing.T) {
	s := Server{Bin: "/x/tmux", Socket: "lea", Conf: "/h/tmux.conf"}
	want := []string{"/x/tmux", "-L", "lea", "-f", "/h/tmux.conf", "-C", "attach-session", "-f", "no-output,ignore-size", "-t", "=s"}
	if got := s.attachCommand("s").Args; !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %q, want %q", got, want)
	}
}

func TestAttachArgvPinsLeaConfig(t *testing.T) {
	s := Server{Bin: "/x/tmux", Socket: "lea", Conf: "/h/tmux.conf"}
	got, err := s.AttachArgv("lea-1")
	want := []string{"/x/tmux", "-L", "lea", "-f", "/h/tmux.conf", "attach-session", "-t", "=lea-1"}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %q, %v", got, err)
	}
	if _, err := (Server{Bin: "/x/tmux", Socket: "lea"}).AttachArgv("s"); err == nil {
		t.Fatal("no configuration file: refused")
	}
}

// Env is the environment of the server's one-shot clients (the launcher's, configuration
// included); nil keeps the process's.
func TestCommandsCarryTheServerEnvironment(t *testing.T) {
	s := Server{Bin: "/x/tmux", Socket: "lea", Conf: "/h/tmux.conf", Env: []string{"A=1"}}
	if got := s.command(context.Background(), "list-sessions").Env; !reflect.DeepEqual(got, []string{"A=1"}) {
		t.Fatalf("env = %q", got)
	}
	if got := (Server{Bin: "/x/tmux", Socket: "lea"}).command(context.Background(), "list-sessions").Env; got != nil {
		t.Fatalf("env = %q, want the process's", got)
	}
}
