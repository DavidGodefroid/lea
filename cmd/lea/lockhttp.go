package main

import (
	"context"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/DavidGodefroid/lea/internal/launcher"
	"github.com/DavidGodefroid/lea/internal/lockhttp"
	"github.com/DavidGodefroid/lea/internal/store"
)

// lockHTTPEvery is how often the daemon brings sources.json up to date: an agent started, ended
// or given a task, a container started or the proxy restarted. The gate reads it again within
// 2 s, so a new agent is named within about 4 s; until then its requests are refused
// (E_SOURCE_UNKNOWN), never attributed to someone else.
const lockHTTPEvery = 2 * time.Second

// lockHTTPSources keeps the gate's sources.json (lockhttp's docs/lea.md, §2.3) in step with the
// running lea containers and the agents of the store, until ctx ends.
func lockHTTPSources(ctx context.Context, st *store.Store, dataDir, prefix string) {
	key, err := lockhttp.Key(dataDir)
	if err != nil {
		log.Printf("lea daemon: lockhttp: %v; sources.json is not written, the gate refuses every agent", err)
		return
	}
	path := lockhttp.SourcesPath(dataDir)
	t := time.NewTicker(lockHTTPEvery)
	defer t.Stop()
	for {
		if err := writeLockHTTPSources(ctx, st, key, path, prefix); err != nil {
			log.Printf("lea daemon: lockhttp: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func writeLockHTTPSources(ctx context.Context, st *store.Store, key []byte, path, prefix string) error {
	nets, err := exec.CommandContext(ctx, "docker", "inspect", "-f",
		`{{range $k, $v := .NetworkSettings.Networks}}{{$k}} {{$v.IPAddress}}{{"\n"}}{{end}}`, prefix+"-proxy").Output()
	if err != nil {
		// No proxy (yet): nothing can reach the gate either.
		nets = nil
	}
	ps, err := exec.CommandContext(ctx, "docker", "ps", "--filter", "label=lea.role=session", "--filter", "label=lea.prefix="+prefix,
		"--format", `{{.Names}}	{{.Label "lea.projects"}}`).Output()
	if err != nil {
		return err
	}
	agents, err := st.Agents()
	if err != nil {
		return err
	}
	_, err = lockhttp.WriteJSON(path, lockHTTPBuild(key, prefix, string(nets), string(ps), agents))
	return err
}

// lockHTTPBuild is sources.json from the proxy's networks ("<network> <ip>" lines), the running
// lea containers ("<name>\t<projects>" lines) and the store's agents: a network names a
// container only when it is that container's own internal network (<prefix>-internal-<id> for
// <prefix>-<id>) and the container runs.
func lockHTTPBuild(key []byte, prefix, nets, ps string, agents []store.Agent) lockhttp.Sources {
	running := map[string]string{}
	for _, l := range strings.Split(ps, "\n") {
		name, proj, ok := strings.Cut(strings.TrimSpace(l), "\t")
		if name = strings.TrimSpace(name); name != "" {
			if !ok {
				proj = ""
			}
			running[name] = proj
		}
	}
	var ns []lockhttp.Network
	for _, l := range strings.Split(nets, "\n") {
		f := strings.Fields(l)
		if len(f) != 2 {
			continue
		}
		id, ok := strings.CutPrefix(f[0], prefix+"-internal-")
		if !ok || id == "" {
			continue
		}
		c := prefix + "-" + id
		proj, run := running[c]
		if !run {
			continue
		}
		ns = append(ns, lockhttp.Network{LocalIP: f[1], Container: c, Project: proj})
	}
	var as []lockhttp.AgentSource
	for _, a := range agents {
		if a.State == "done" || a.State == "stopped" {
			continue
		}
		task := ""
		if a.TaskID > 0 {
			task = strconv.FormatInt(a.TaskID, 10)
		}
		as = append(as, lockhttp.AgentSource{Container: a.Container, Agent: a.Name, Role: a.Role, Parent: a.Parent,
			TaskID: task, Branch: a.Branch, SessionID: a.SessionID})
	}
	return lockhttp.Build(key, ns, as)
}

// lockHTTPStamp is the daemon's LockHTTP switch as the launcher compares it (launcher.DaemonLockHTTPStamp).
func lockHTTPStamp(getenv func(string) string) string { return launcher.DaemonLockHTTPStamp(getenv) }

func nonEmptyEnv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
