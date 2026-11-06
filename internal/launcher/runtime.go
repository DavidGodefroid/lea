package launcher

import (
	"encoding/json"
	"slices"
	"sort"
	"strings"
)

// The agent container's OCI runtime (#106): LEA_RUNTIME, empty for the engine's default (runc),
// else a runtime registered with dockerd (runsc for gVisor, kata for Kata Containers), which puts
// a second kernel between the agents and the host's. Only the agent container gets it: the proxy,
// the LockHTTP gate and lea-ctl stay on the default runtime (they run no agent code).

// engineRuntimes are the runtimes the engine knows (docker info .Runtimes); ok is false when the
// engine does not tell (Podman has no .Runtimes): lea then lets the engine judge LEA_RUNTIME.
func (l *Launcher) engineRuntimes() (names []string, ok bool) {
	out, err := l.output(false, "docker", "info", "--format", "{{json .Runtimes}}")
	var m map[string]json.RawMessage
	if err != nil || json.Unmarshal([]byte(strings.TrimSpace(out)), &m) != nil || len(m) == 0 {
		return nil, false
	}
	for n := range m {
		names = append(names, n)
	}
	sort.Strings(names)
	return names, true
}

// runtimeArgs is --runtime LEA_RUNTIME for the agent container, nothing when it is empty. A runtime
// the engine says it does not know refuses the start with what it knows, rather than docker's
// "unknown or invalid runtime name" after the start snapshot.
func (l *Launcher) runtimeArgs() ([]string, error) {
	rt := l.Cfg.Get("LEA_RUNTIME")
	if rt == "" {
		return nil, nil
	}
	if names, ok := l.engineRuntimes(); ok && !slices.Contains(names, rt) {
		return nil, die("LEA_RUNTIME=%s: the container engine has no such runtime (it knows %s): install it and register it with dockerd (README, \"A second kernel: gVisor or Kata\"), or leave LEA_RUNTIME empty",
			rt, strings.Join(names, ", "))
	}
	return []string{"--runtime", rt}, nil
}

// checkRuntime warns when a running agent container is not on the runtime LEA_RUNTIME asks for
// (started before it was set or changed): a runtime is only chosen when a container is created.
// Empty LEA_RUNTIME checks nothing (lea doctor shows each container's runtime).
func (l *Launcher) checkRuntime(name string) {
	want := l.Cfg.Get("LEA_RUNTIME")
	if want == "" {
		return
	}
	got, err := l.output(false, "docker", "container", "inspect", "--format", "{{.HostConfig.Runtime}}", name)
	if got = strings.TrimSpace(got); err != nil || got == "" || got == want {
		return
	}
	l.logf("warning: the running %s runs on the %s runtime, not %s (LEA_RUNTIME): run 'lea stop', then lea, to restart it on %s", name, got, want, want)
}
