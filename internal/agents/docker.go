package agents

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// Docker reaches lea-agent inside a lea container through docker exec, as the user's uid.
type Docker struct {
	Bin      string // "docker" when empty
	UID, GID int
}

func (d Docker) bin() string {
	if d.Bin == "" {
		return "docker"
	}
	return d.Bin
}

func (d Docker) agent(ctx context.Context, container string, args ...string) ([]byte, error) {
	full := append([]string{"exec", "-u", fmt.Sprintf("%d:%d", d.UID, d.GID), container, "lea-agent"}, args...)
	return exec.CommandContext(ctx, d.bin(), full...).Output()
}

// running reports whether the container runs. A container docker does not know is not running;
// any other failure of docker itself (daemon unreachable, binary missing) is an error, never
// "not running": callers would take the container's live agents for ended ones.
func (d Docker) running(ctx context.Context, container string) (bool, error) {
	out, err := exec.CommandContext(ctx, d.bin(), "inspect", "-f", "{{.State.Running}}", container).Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && noSuchContainer(string(ee.Stderr)) {
			return false, nil
		}
		return false, fmt.Errorf("docker inspect %s: %w%s", container, err, stderrOf(err))
	}
	switch v := strings.TrimSpace(string(out)); v {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, fmt.Errorf("docker inspect %s: unexpected state %q", container, v)
	}
}

func noSuchContainer(stderr string) bool {
	s := strings.ToLower(stderr)
	return strings.Contains(s, "no such container") || strings.Contains(s, "no such object")
}

func stderrOf(err error) string {
	var ee *exec.ExitError
	if errors.As(err, &ee) && len(ee.Stderr) > 0 {
		return ": " + strings.TrimSpace(string(ee.Stderr))
	}
	return ""
}

// failed explains a failed lea-agent call: nil when the container is not running (nothing to
// list or stop), an error otherwise, including when docker itself could not run.
func (d Docker) failed(ctx context.Context, container string, err error, what string) error {
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		return fmt.Errorf("%s in %s: %w", what, container, err)
	}
	run, rerr := d.running(ctx, container)
	if rerr != nil {
		return fmt.Errorf("%s in %s: %w (and %v)", what, container, err, rerr)
	}
	if !run {
		return nil
	}
	return fmt.Errorf("%s in %s failed (an image older than the deck? run make build, then lea stop): %w%s",
		what, container, err, stderrOf(err))
}

// Live lists the agents running in the container; none when the container is not running.
func (d Docker) Live(ctx context.Context, container string) ([]string, error) {
	out, err := d.agent(ctx, container, "ls")
	if err != nil {
		return nil, d.failed(ctx, container, err, "lea-agent ls")
	}
	var names []string
	for _, n := range strings.Fields(string(out)) {
		if nameRe.MatchString(n) {
			names = append(names, n)
		}
	}
	return names, nil
}

// Stop ends an agent; stopping in a container that is not running succeeds.
func (d Docker) Stop(ctx context.Context, container, name string) error {
	if _, err := d.agent(ctx, container, "stop", name); err != nil {
		return d.failed(ctx, container, err, "lea-agent stop "+name)
	}
	return nil
}

// Projects is the projects directory the launcher started the container for (its lea.projects
// label), a clean absolute path.
func (d Docker) Projects(ctx context.Context, container string) (string, error) {
	out, err := exec.CommandContext(ctx, d.bin(), "inspect", "-f", `{{index .Config.Labels "lea.projects"}}`, container).Output()
	if err != nil {
		return "", fmt.Errorf("docker inspect %s: %w%s", container, err, stderrOf(err))
	}
	p := strings.TrimSpace(string(out))
	if err := checkWorkdir(p); err != nil {
		return "", fmt.Errorf("container %s has no usable lea.projects label: %w", container, err)
	}
	return p, nil
}

// Shells lists the shells running in the container (the deck's s); none when the container is
// not running.
func (d Docker) Shells(ctx context.Context, container string) ([]string, error) {
	out, err := d.agent(ctx, container, "shells")
	if err != nil {
		return nil, d.failed(ctx, container, err, "lea-agent shells")
	}
	var names []string
	for _, n := range strings.Fields(string(out)) {
		if shellRe.MatchString(n) {
			names = append(names, n)
		}
	}
	return names, nil
}

// StopShell ends a shell; stopping in a container that is not running succeeds.
func (d Docker) StopShell(ctx context.Context, container, name string) error {
	if _, err := d.agent(ctx, container, "stop-shell", name); err != nil {
		return d.failed(ctx, container, err, "lea-agent stop-shell "+name)
	}
	return nil
}

// Pause freezes an agent (paused) or lets it continue. Unlike Stop, it fails in a container that
// is not running: nothing was frozen there.
func (d Docker) Pause(ctx context.Context, container, name string, paused bool) error {
	verb := "resume"
	if paused {
		verb = "pause"
	}
	if _, err := d.agent(ctx, container, verb, name); err != nil {
		if ferr := d.failed(ctx, container, err, "lea-agent "+verb+" "+name); ferr != nil {
			return ferr
		}
		return fmt.Errorf("lea-agent %s %s: container %s is not running", verb, name, container)
	}
	return nil
}
