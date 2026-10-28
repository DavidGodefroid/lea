package main

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/DavidGodefroid/lea/internal/daemon"
	"github.com/DavidGodefroid/lea/internal/launcher"
	"github.com/DavidGodefroid/lea/internal/lockhttp"
	"github.com/DavidGodefroid/lea/internal/providers"
	"github.com/DavidGodefroid/lea/internal/store"
)

// lockHTTPEvery is how often the daemon brings sources.json up to date: an agent started, ended
// or given a task, a container started or the proxy restarted. The gate reads it again within
// 2 s, so a new agent is named within about 4 s; until then its requests are refused
// (E_SOURCE_UNKNOWN), never attributed to someone else.
const lockHTTPEvery = 2 * time.Second

// lockHTTPSources keeps the gate's sources.json (lockhttp's docs/lea.md, §2.3) and
// credentials.json (§2.4) in step with the running lea containers, the agents of the store, the
// containers' Claude logins and the providers' keys, and hands the gate's status (§5) to the
// daemon for the decks, until ctx ends.
func lockHTTPSources(ctx context.Context, st *store.Store, d *daemon.Daemon, reg func() (*providers.Registry, error), dataDir, prefix string) {
	key, err := lockhttp.Key(dataDir)
	if err != nil {
		log.Printf("lea daemon: lockhttp: %v; sources.json is not written, the gate refuses every agent", err)
		return
	}
	t := time.NewTicker(lockHTTPEvery)
	defer t.Stop()
	for {
		running, err := writeLockHTTPSources(ctx, st, key, lockhttp.SourcesPath(dataDir), prefix)
		if err != nil {
			log.Printf("lea daemon: lockhttp: %v", err)
		} else if _, err := lockhttp.WriteJSON(lockhttp.CredentialsPath(dataDir), lockHTTPCredentials(dataDir, prefix, running, reg)); err != nil {
			log.Printf("lea daemon: lockhttp: %v", err)
		}
		d.SetLockHTTPStatus(ctx, readLockHTTPStatus(filepath.Join(lockhttp.StatusDir(dataDir), "status.json")))
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// writeLockHTTPSources writes sources.json and returns the running lea containers (name ->
// projects directory).
func writeLockHTTPSources(ctx context.Context, st *store.Store, key []byte, path, prefix string) (map[string]string, error) {
	nets, err := exec.CommandContext(ctx, "docker", "inspect", "-f",
		`{{range $k, $v := .NetworkSettings.Networks}}{{$k}} {{$v.IPAddress}}{{"\n"}}{{end}}`, prefix+"-proxy").Output()
	if err != nil {
		// No proxy (yet): nothing can reach the gate either.
		nets = nil
	}
	ps, err := exec.CommandContext(ctx, "docker", "ps", "--filter", "label=lea.role=session", "--filter", "label=lea.prefix="+prefix,
		"--format", `{{.Names}}	{{.Label "lea.projects"}}`).Output()
	if err != nil {
		return nil, err
	}
	agents, err := st.Agents()
	if err != nil {
		return nil, err
	}
	running := lockHTTPRunning(string(ps))
	_, err = lockhttp.WriteJSON(path, lockHTTPBuild(key, prefix, string(nets), running, agents))
	return running, err
}

// lockHTTPRunning reads "<name>\t<projects>" lines.
func lockHTTPRunning(ps string) map[string]string {
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
	return running
}

// lockHTTPCredentials is credentials.json: for each running container, the hash of the Claude
// login lea keeps for it (state/claude/<id>/.credentials.json, its OAuth access token, which
// Claude Code rotates: the next pass picks the new one up), and the hashes of the providers'
// keys lea-keyproxy injects. A container without a login gets an empty list.
func lockHTTPCredentials(dataDir, prefix string, running map[string]string, reg func() (*providers.Registry, error)) lockhttp.Credentials {
	c := lockhttp.Credentials{Version: lockhttp.Version, Containers: map[string][]string{}, Keyproxy: []string{}}
	for name := range running {
		id, ok := strings.CutPrefix(name, prefix+"-")
		if !ok || id == "" || strings.ContainsAny(id, "/.") {
			continue
		}
		c.Containers[name] = []string{}
		var login struct {
			ClaudeAiOauth struct {
				AccessToken string `json:"accessToken"`
			} `json:"claudeAiOauth"`
		}
		b, err := os.ReadFile(filepath.Join(dataDir, "state", "claude", id, ".credentials.json"))
		if err == nil && json.Unmarshal(b, &login) == nil && login.ClaudeAiOauth.AccessToken != "" {
			c.Containers[name] = append(c.Containers[name], lockhttp.Hash(login.ClaudeAiOauth.AccessToken))
		}
	}
	if reg != nil {
		if r, err := reg(); err == nil {
			for _, p := range r.Endpoints() {
				if p.Endpoint == nil || p.Endpoint.KeyFile == "" {
					continue
				}
				if k, err := providers.ReadKey(p.Endpoint.KeyFile); err == nil {
					c.Keyproxy = append(c.Keyproxy, lockhttp.Hash(k))
				}
			}
		}
	}
	sort.Strings(c.Keyproxy)
	return c
}

// readLockHTTPStatus is the gate's status file; an unreadable one says the console's state is
// unknown.
func readLockHTTPStatus(path string) daemon.LockHTTPStatus {
	var s daemon.LockHTTPStatus
	b, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(b, &s) != nil || s.Version != lockhttp.Version {
		return daemon.LockHTTPStatus{Version: lockhttp.Version}
	}
	return s
}

// lockHTTPBuild is sources.json from the proxy's networks ("<network> <ip>" lines), the running
// lea containers (lockHTTPRunning) and the store's agents: a network names a
// container only when it is that container's own internal network (<prefix>-internal-<id> for
// <prefix>-<id>) and the container runs.
func lockHTTPBuild(key []byte, prefix, nets string, running map[string]string, agents []store.Agent) lockhttp.Sources {
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

// lockHTTPGrantProxy names the proxy of an agent's pinned grant calls under LockHTTP: the gate on
// lea-proxy, with the agent's own proxy password (the one its HTTPS_PROXY holds): squid is on the
// proxy's loopback only.
func lockHTTPGrantProxy(key []byte) func(container, agent string) string {
	return func(container, agent string) string {
		return "http://" + agent + ":" + lockhttp.Token(key, container, agent) + "@lea-proxy:" + lockhttp.Port
	}
}

// lockHTTPStamp is the daemon's LockHTTP switch as the launcher compares it (launcher.DaemonLockHTTPStamp).
func lockHTTPStamp(getenv func(string) string) string { return launcher.DaemonLockHTTPStamp(getenv) }

func nonEmptyEnv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
