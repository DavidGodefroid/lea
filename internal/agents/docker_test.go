package agents

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// fakeDocker writes a docker stand-in that logs its arguments and runs body.
func fakeDocker(t *testing.T, body string) (bin, log string) {
	t.Helper()
	dir := t.TempDir()
	bin, log = filepath.Join(dir, "docker"), filepath.Join(dir, "log")
	script := "#!/bin/sh\necho \"$*\" >>" + log + "\n" + body + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, log
}

func logged(t *testing.T, log string) string {
	t.Helper()
	b, _ := os.ReadFile(log)
	return string(b)
}

func TestDockerLive(t *testing.T) {
	bin, log := fakeDocker(t, `case $1 in exec) printf 'claude-1\nBad Name\nclaude-2\n' ;; esac`)
	got, err := Docker{Bin: bin, UID: 1000, GID: 1001}.Live(context.Background(), "lea-a")
	if err != nil || !reflect.DeepEqual(got, []string{"claude-1", "claude-2"}) {
		t.Fatalf("live=%q err=%v", got, err)
	}
	if !strings.Contains(logged(t, log), "exec -u 1000:1001 lea-a lea-agent ls") {
		t.Fatalf("log=%q", logged(t, log))
	}
}

func TestDockerLiveOfAStoppedContainerIsEmpty(t *testing.T) {
	bin, _ := fakeDocker(t, `case $1 in exec) exit 1 ;; inspect) echo false ;; esac`)
	got, err := Docker{Bin: bin}.Live(context.Background(), "lea-a")
	if err != nil || len(got) != 0 {
		t.Fatalf("live=%q err=%v", got, err)
	}
}

func TestDockerLiveFailureOfARunningContainerIsAnError(t *testing.T) {
	bin, _ := fakeDocker(t, `case $1 in exec) exit 127 ;; inspect) echo true ;; esac`)
	if _, err := (Docker{Bin: bin}).Live(context.Background(), "lea-a"); err == nil || !strings.Contains(err.Error(), "make build") {
		t.Fatalf("err=%v", err)
	}
}

func TestDockerStop(t *testing.T) {
	bin, log := fakeDocker(t, `:`)
	if err := (Docker{Bin: bin, UID: 1000, GID: 1000}).Stop(context.Background(), "lea-a", "claude-1"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logged(t, log), "exec -u 1000:1000 lea-a lea-agent stop claude-1") {
		t.Fatalf("log=%q", logged(t, log))
	}
	bin, _ = fakeDocker(t, `case $1 in exec) exit 1 ;; inspect) echo false ;; esac`)
	if err := (Docker{Bin: bin}).Stop(context.Background(), "lea-a", "claude-1"); err != nil {
		t.Fatalf("stop in a stopped container: %v", err)
	}
}

func TestDockerFailuresAreNeverNotRunning(t *testing.T) {
	ctx := context.Background()
	// The daemon is unreachable: inspect fails for another reason than an unknown container.
	bin, _ := fakeDocker(t, `case $1 in exec) exit 1 ;; inspect) echo 'Cannot connect to the Docker daemon' >&2; exit 1 ;; esac`)
	if _, err := (Docker{Bin: bin}).Live(ctx, "lea-a"); err == nil || !strings.Contains(err.Error(), "Cannot connect") {
		t.Fatalf("live: err=%v", err)
	}
	if err := (Docker{Bin: bin}).Stop(ctx, "lea-a", "claude-1"); err == nil {
		t.Fatal("stop: no error")
	}
	// docker itself cannot run.
	missing := filepath.Join(t.TempDir(), "docker")
	if _, err := (Docker{Bin: missing}).Live(ctx, "lea-a"); err == nil {
		t.Fatal("live with a missing docker: no error")
	}
	if err := (Docker{Bin: missing}).Stop(ctx, "lea-a", "claude-1"); err == nil {
		t.Fatal("stop with a missing docker: no error")
	}
	// inspect answers something else than true or false.
	bin, _ = fakeDocker(t, `case $1 in exec) exit 1 ;; inspect) echo maybe ;; esac`)
	if _, err := (Docker{Bin: bin}).Live(ctx, "lea-a"); err == nil {
		t.Fatal("live with an unexpected state: no error")
	}
}

func TestDockerUnknownContainerIsNotRunning(t *testing.T) {
	bin, _ := fakeDocker(t, `case $1 in
exec) echo 'Error response from daemon: No such container: lea-a' >&2; exit 1 ;;
inspect) echo 'Error: No such object: lea-a' >&2; exit 1 ;;
esac`)
	got, err := Docker{Bin: bin}.Live(context.Background(), "lea-a")
	if err != nil || len(got) != 0 {
		t.Fatalf("live=%q err=%v", got, err)
	}
	if err := (Docker{Bin: bin}).Stop(context.Background(), "lea-a", "claude-1"); err != nil {
		t.Fatalf("stop: %v", err)
	}
}

func TestDockerProjects(t *testing.T) {
	bin, log := fakeDocker(t, `case $1 in inspect) echo /home/u/p ;; esac`)
	p, err := Docker{Bin: bin}.Projects(context.Background(), "lea-a")
	if err != nil || p != "/home/u/p" {
		t.Fatalf("projects=%q err=%v", p, err)
	}
	if !strings.Contains(logged(t, log), `inspect -f {{index .Config.Labels "lea.projects"}} lea-a`) {
		t.Fatalf("log=%q", logged(t, log))
	}
	for _, body := range []string{`echo relative/dir`, `echo`, `echo 'Error: No such object: lea-a' >&2; exit 1`} {
		bad, _ := fakeDocker(t, body)
		if _, err := (Docker{Bin: bad}).Projects(context.Background(), "lea-a"); err == nil {
			t.Fatalf("%q accepted", body)
		}
	}
}

func TestDockerShells(t *testing.T) {
	bin, log := fakeDocker(t, `case $1 in exec) printf 'sh-1\nclaude-1\nsh-0\nsh-12\n' ;; esac`)
	d := Docker{Bin: bin, UID: 1000, GID: 1001}
	got, err := d.Shells(context.Background(), "lea-a")
	if err != nil || !reflect.DeepEqual(got, []string{"sh-1", "sh-12"}) {
		t.Fatalf("shells=%q err=%v", got, err)
	}
	if err := d.StopShell(context.Background(), "lea-a", "sh-1"); err != nil {
		t.Fatal(err)
	}
	l := logged(t, log)
	if !strings.Contains(l, "exec -u 1000:1001 lea-a lea-agent shells") || !strings.Contains(l, "exec -u 1000:1001 lea-a lea-agent stop-shell sh-1") {
		t.Fatalf("log=%q", l)
	}
}

func TestDockerShellsOfAnOlderImageIsAnError(t *testing.T) {
	bin, _ := fakeDocker(t, `case $1 in exec) echo "usage: lea-agent run NAME" >&2; exit 2 ;; inspect) echo true ;; esac`)
	if _, err := (Docker{Bin: bin}).Shells(context.Background(), "lea-a"); err == nil || !strings.Contains(err.Error(), "make build") {
		t.Fatalf("err=%v", err)
	}
}

func TestDockerPauseAndResume(t *testing.T) {
	bin, log := fakeDocker(t, `:`)
	d := Docker{Bin: bin, UID: 1000, GID: 1000}
	if err := d.Pause(context.Background(), "lea-a", "claude-1", true); err != nil {
		t.Fatal(err)
	}
	if err := d.Pause(context.Background(), "lea-a", "claude-1", false); err != nil {
		t.Fatal(err)
	}
	if l := logged(t, log); !strings.Contains(l, "exec -u 1000:1000 lea-a lea-agent pause claude-1\n") ||
		!strings.Contains(l, "exec -u 1000:1000 lea-a lea-agent resume claude-1\n") {
		t.Fatalf("log=%q", l)
	}
	// Nothing is frozen in a container that is not running: the deck must not say paused.
	bin, _ = fakeDocker(t, `case $1 in exec) exit 1 ;; inspect) echo false ;; esac`)
	if err := (Docker{Bin: bin}).Pause(context.Background(), "lea-a", "claude-1", true); err == nil {
		t.Fatal("pause in a stopped container: no error")
	}
}

func TestDockerTeardownAndSweep(t *testing.T) {
	ctx := context.Background()
	bin, log := fakeDocker(t, `case $1 in exec) if [ "$6" = sweep ]; then printf 'removed 1 container and 0 pods of ended agents (dev-2)\n'; fi ;; esac`)
	d := Docker{Bin: bin, UID: 1000, GID: 1000}
	if err := d.Teardown(ctx, "lea-a", "dev-1"); err != nil {
		t.Fatal(err)
	}
	out, err := d.Sweep(ctx, "lea-a", []string{"dev-1", "lead-1"})
	if err != nil || out != "removed 1 container and 0 pods of ended agents (dev-2)" {
		t.Fatalf("sweep = %q, %v", out, err)
	}
	for _, want := range []string{"exec -u 1000:1000 lea-a lea-agent teardown dev-1", "exec -u 1000:1000 lea-a lea-agent sweep dev-1 lead-1"} {
		if !strings.Contains(logged(t, log), want) {
			t.Fatalf("log=%q, want %q", logged(t, log), want)
		}
	}
	if err := d.Teardown(ctx, "lea-a", "--all"); err == nil {
		t.Fatal("an invalid name is torn down")
	}
	if _, err := d.Sweep(ctx, "lea-a", []string{"dev-1", "-x"}); err == nil {
		t.Fatal("an invalid name is kept")
	}
	// A container that is not running has nothing to remove.
	bin, _ = fakeDocker(t, `case $1 in exec) exit 1 ;; inspect) echo false ;; esac`)
	if out, err := (Docker{Bin: bin}).Sweep(ctx, "lea-a", nil); err != nil || out != "" {
		t.Fatalf("sweep of a stopped container = %q, %v", out, err)
	}
}

// Park stops what an idle agent started (lea-agent park, #132) and returns what it freed.
func TestDockerPark(t *testing.T) {
	ctx := context.Background()
	bin, log := fakeDocker(t, `case $1 in exec) if [ "$6" = park ]; then printf 'dev-1: stopped 2 containers and 1 pod (freed 812 MiB)\n'; fi ;; esac`)
	d := Docker{Bin: bin, UID: 1000, GID: 1000}
	out, err := d.Park(ctx, "lea-a", "dev-1")
	if err != nil || out != "dev-1: stopped 2 containers and 1 pod (freed 812 MiB)" {
		t.Fatalf("park = %q, %v", out, err)
	}
	if !strings.Contains(logged(t, log), "exec -u 1000:1000 lea-a lea-agent park dev-1") {
		t.Fatalf("log=%q", logged(t, log))
	}
	if _, err := d.Park(ctx, "lea-a", "--all"); err == nil {
		t.Fatal("an invalid name is parked")
	}
	bin, _ = fakeDocker(t, `case $1 in exec) exit 1 ;; inspect) echo false ;; esac`)
	if out, err := (Docker{Bin: bin}).Park(ctx, "lea-a", "dev-1"); err != nil || out != "" {
		t.Fatalf("park in a stopped container = %q, %v", out, err)
	}
	// A stack still at work (lea-agent park exits 3) is ErrStackBusy.
	bin, _ = fakeDocker(t, `case $1 in exec) echo 'dev-1: stack busy (40% of a CPU), not stopped' >&2; exit 3 ;; inspect) echo true ;; esac`)
	if _, err := (Docker{Bin: bin}).Park(ctx, "lea-a", "dev-1"); !errors.Is(err, ErrStackBusy) {
		t.Fatalf("busy park: %v", err)
	}
	// Unpark starts again what park stopped.
	bin, log = fakeDocker(t, `case $1 in exec) printf 'dev-1: started 2 containers and 1 pod again\n' ;; esac`)
	if out, err := (Docker{Bin: bin, UID: 1000, GID: 1000}).Unpark(ctx, "lea-a", "dev-1"); err != nil || out != "dev-1: started 2 containers and 1 pod again" {
		t.Fatalf("unpark = %q, %v", out, err)
	}
	if !strings.Contains(logged(t, log), "exec -u 1000:1000 lea-a lea-agent unpark dev-1") {
		t.Fatalf("log=%q", logged(t, log))
	}
}
