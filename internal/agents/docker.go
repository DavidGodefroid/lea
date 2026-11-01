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

// Teardown removes what an ended agent started in the container's nested Podman (lea-agent
// teardown); in a container that is not running there is nothing to remove.
func (d Docker) Teardown(ctx context.Context, container, name string) error {
	if !nameRe.MatchString(name) {
		return fmt.Errorf("invalid agent name %q", name)
	}
	if _, err := d.agent(ctx, container, "teardown", name); err != nil {
		return d.failed(ctx, container, err, "lea-agent teardown "+name)
	}
	return nil
}

// ErrStackBusy is a park refused: the agent's stack still works (a background job of the agent).
var ErrStackBusy = errors.New("the stack is still at work")

// Park stops what an idle agent started in the container's nested Podman (lea-agent park,
// #132), without removing it, and returns what it freed ("" when nothing of its own ran). A
// stack still at work is left running: ErrStackBusy.
func (d Docker) Park(ctx context.Context, container, name string) (string, error) {
	if !nameRe.MatchString(name) {
		return "", fmt.Errorf("invalid agent name %q", name)
	}
	out, err := d.agent(ctx, container, "park", name)
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 3 {
			return "", fmt.Errorf("%s: %w%s", name, ErrStackBusy, stderrOf(err))
		}
		return "", d.failed(ctx, container, err, "lea-agent park "+name)
	}
	return strings.TrimSpace(string(out)), nil
}

// Unpark starts again what the last park of the agent stopped (lea-agent unpark).
func (d Docker) Unpark(ctx context.Context, container, name string) (string, error) {
	if !nameRe.MatchString(name) {
		return "", fmt.Errorf("invalid agent name %q", name)
	}
	out, err := d.agent(ctx, container, "unpark", name)
	if err != nil {
		return "", d.failed(ctx, container, err, "lea-agent unpark "+name)
	}
	return strings.TrimSpace(string(out)), nil
}

// Sweep removes what the agents but keep left in the container's nested Podman (lea-agent
// sweep) and returns its report.
func (d Docker) Sweep(ctx context.Context, container string, keep []string) (string, error) {
	for _, n := range keep {
		if !nameRe.MatchString(n) {
			return "", fmt.Errorf("invalid agent name %q", n)
		}
	}
	out, err := d.agent(ctx, container, append([]string{"sweep"}, keep...)...)
	if err != nil {
		return "", d.failed(ctx, container, err, "lea-agent sweep")
	}
	return strings.TrimSpace(string(out)), nil
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

// Volumes is the projects directory the launcher started the container for (its lea.projects
// label) and the names of the volumes the container mounts, as docker inspect reports them: what
// the daemon compares with the locks lox up runs (a lock added while lea runs).
func (d Docker) Volumes(ctx context.Context, container string) (string, []string, error) {
	out, err := exec.CommandContext(ctx, d.bin(), "inspect", "--type", "container", "-f",
		`{{index .Config.Labels "lea.projects"}}{{range .Mounts}}{{if eq .Type "volume"}}{{"\n"}}{{.Name}}{{end}}{{end}}`, container).Output()
	if err != nil {
		return "", nil, fmt.Errorf("docker inspect %s: %w%s", container, err, stderrOf(err))
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	return lines[0], lines[1:], nil
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

// DockerConsoles reaches the console containers an earlier lea started, through the docker CLI.
type DockerConsoles struct {
	Bin string // "docker" when empty
}

func (d DockerConsoles) bin() string {
	if d.Bin == "" {
		return "docker"
	}
	return d.Bin
}

// StopConsolesOf stops the console containers a deck of owner started (an earlier lea started
// its own, labelled lea.lock-for=<owner>): never one of lox up's, which carry no such label.
func (d DockerConsoles) StopConsolesOf(ctx context.Context, owner string) error {
	out, err := exec.CommandContext(ctx, d.bin(), "ps", "-q", "--filter", "label="+lockOwnerLabel+"="+owner).Output()
	if err != nil {
		return fmt.Errorf("docker ps: %w%s", err, stderrOf(err))
	}
	ids := strings.Fields(string(out))
	if len(ids) == 0 {
		return nil
	}
	if out, err := exec.CommandContext(ctx, d.bin(), append([]string{"stop", "-t", "5"}, ids...)...).CombinedOutput(); err != nil {
		return fmt.Errorf("docker stop: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
