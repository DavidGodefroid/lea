package launcher

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
	"testing"
)

// upProject is lock up's compose project of w's projects directory (lockhub's family.Project).
func upProjectOf(w *world) string {
	h := sha256.Sum256([]byte(w.proj))
	return "lockfamily-" + hex.EncodeToString(h[:4])
}

func writeUp(t *testing.T, w *world, locks string) {
	t.Helper()
	d := w.proj + "/.lockfamily"
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	locks = strings.ReplaceAll(locks, "PROJECT", upProjectOf(w))
	if err := os.WriteFile(d+"/up.json", []byte(`{"version":1,"project":"`+upProjectOf(w)+`","workspace":"`+w.proj+`","locks":{`+locks+`}}`), 0o644); err != nil {
		t.Fatal(err)
	}
}

// labels makes the fake docker answer lock up's labels for the socket volume of name (type typ).
func labels(w *world, name, typ, role string) {
	labelsWith(w, name, typ, role, upProjectOf(w), name, w.proj)
}

func labelsWith(w *world, name, typ, role, project, inst, ws string) {
	vol := upProjectOf(w) + "_" + name + "-run"
	w.env["FAKE_DOCKER_OUT_VOLUME_INSPECT_AT_"+fakeKey(vol)] = `{"com.docker.compose.project":"` + project +
		`","com.docker.compose.volume":"` + inst + `-run","dev.lockfamily.role":"` + role + `","dev.lockfamily.instance":"` + inst +
		`","dev.lockfamily.type":"` + typ + `","dev.lockfamily.workspace":"` + ws + `"}`
}

// locksqlUp is lox up running a locksql lock, crm, for w's projects directory (locksql is on by
// default: its socket comes from lox up only).
func locksqlUp(t *testing.T, w *world) {
	t.Helper()
	writeUp(t, w, lockUp("crm", "locksql"))
	labels(w, "crm", "locksql", "socket")
}

// lockUp is up.json's entry of a lock (PROJECT: the project, filled by writeUp).
func lockUp(name, typ string) string {
	return `"` + name + `":{"type":"` + typ + `","socket_volume":"PROJECT_` + name + `-run"}`
}

// Locks of lock up: their socket volumes replace the fixed ones, for the tools that are on.
func TestLockUpSocketVolumes(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	writeUserConf(t, w, "LEA_LOCKMQ=1\nLEA_LOCKFILE=1\n")
	writeUp(t, w, lockUp("crm", "locksql")+","+lockUp("events", "lockmq")+","+lockUp("docs", "lockfile"))
	labels(w, "crm", "locksql", "socket")
	labels(w, "events", "lockmq", "socket")
	labels(w, "docs", "lockfile", "socket")
	args := lockArgs(t, w)
	// Read-only: the console made the directory; connecting writes nothing. No stale-ids check of
	// lockfile's (lock up's console made the volume with lockfile's ids).
	for _, m := range []string{"type=volume,src=" + upProjectOf(w) + "_crm-run,dst=/run/locksql,readonly",
		"type=volume,src=" + upProjectOf(w) + "_events-run,dst=/run/lockmq,readonly",
		"type=volume,src=" + upProjectOf(w) + "_docs-run,dst=/run/lockfile,readonly"} {
		if !contains(args, m) {
			t.Errorf("missing %q in %v", m, args)
		}
	}
	for _, a := range args {
		if strings.Contains(a, "src=locksql-run") || strings.Contains(a, "src=lockmq-run") || strings.Contains(a, "src=lockfile-run") ||
			strings.Contains(a, "-home") || strings.Contains(a, "lockhub") {
			t.Errorf("fixed, home or hub mount: %s", a)
		}
	}
	if strings.Contains(w.errb.String(), "warning") {
		t.Errorf("warnings: %s", w.errb.String())
	}
}

// Whatever up.json says, only a volume docker labels as lock up's socket volume of that lock is
// mounted: not a lock's home, not a volume of another project or instance.
func TestLockUpRefusesOtherVolumes(t *testing.T) {
	for name, c := range map[string]struct{ up, role string }{
		"home volume":     {`"crm":{"type":"locksql","socket_volume":"PROJECT_crm-home"}`, "socket"},
		"role not socket": {lockUp("crm", "locksql"), "gate-home"},
		"no labels":       {lockUp("crm", "locksql"), ""},
		"other project":   {lockUp("crm", "locksql"), "project"},
		"other instance":  {lockUp("crm", "locksql"), "instance"},
		"other workspace": {lockUp("crm", "locksql"), "workspace"},
	} {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t, repoRoot(t))
			writeUp(t, w, c.up)
			switch c.role {
			case "":
			case "project":
				labelsWith(w, "crm", "locksql", "socket", "lockfamily-00000000", "crm", w.proj)
			case "instance":
				labelsWith(w, "crm", "locksql", "socket", upProjectOf(w), "other", w.proj)
			case "workspace":
				labelsWith(w, "crm", "locksql", "socket", upProjectOf(w), "crm", "/elsewhere")
			default:
				labels(w, "crm", "locksql", c.role)
			}
			args := lockArgs(t, w)
			for _, a := range args {
				if strings.Contains(a, upProjectOf(w)) {
					t.Errorf("mounted %s", a)
				}
			}
			// lock up runs the tool: the agents get none of its sockets, not another console's.
			for _, a := range args {
				if strings.Contains(a, "locksql-run") || a == "LEA_LOCKSQL=1" {
					t.Errorf("fixed locksql socket mounted instead: %s", a)
				}
			}
			if !strings.Contains(w.errb.String(), "warning") {
				t.Error("no warning")
			}
		})
	}
}

// Two locks of one tool: the agents reach neither (lea mounts one socket per tool).
func TestLockUpTwoOfATool(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	writeUp(t, w, lockUp("a", "locksql")+","+lockUp("b", "locksql"))
	labels(w, "a", "locksql", "socket")
	labels(w, "b", "locksql", "socket")
	for _, a := range lockArgs(t, w) {
		if strings.Contains(a, upProjectOf(w)) {
			t.Errorf("mounted %s", a)
		}
	}
	for _, a := range lockArgs(t, w) {
		if strings.Contains(a, "locksql-run") {
			t.Errorf("fixed locksql-run mounted: %s", a)
		}
	}
	if !strings.Contains(w.errb.String(), "2 locksql locks") {
		t.Errorf("warning: %s", w.errb.String())
	}
}

// A tool that is off stays off, lock up or not.
func TestLockUpToolOff(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	writeUp(t, w, lockUp("events", "lockmq"))
	labels(w, "events", "lockmq", "socket")
	for _, a := range lockArgs(t, w) {
		if strings.Contains(a, "lockmq") {
			t.Errorf("lockmq mounted while off: %s", a)
		}
	}
}

// An up.json others could write is not lock up's.
func TestLockUpFileMustBeTheUsers(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	writeUp(t, w, lockUp("crm", "locksql"))
	labels(w, "crm", "locksql", "socket")
	if err := os.Chmod(w.proj+"/.lockfamily/up.json", 0o666); err != nil {
		t.Fatal(err)
	}
	for _, a := range lockArgs(t, w) {
		if strings.Contains(a, upProjectOf(w)) {
			t.Errorf("mounted %s", a)
		}
	}
	if !strings.Contains(w.errb.String(), "writable by others") {
		t.Errorf("warning: %s", w.errb.String())
	}
	// A link is not followed.
	if err := os.Rename(w.proj+"/.lockfamily/up.json", w.proj+"/elsewhere.json"); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(w.proj+"/elsewhere.json", 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(w.proj+"/elsewhere.json", w.proj+"/.lockfamily/up.json"); err != nil {
		t.Fatal(err)
	}
	for _, a := range lockArgs(t, w) {
		if strings.Contains(a, upProjectOf(w)) {
			t.Errorf("mounted through a link: %s", a)
		}
	}
}

// An up.json of another projects directory (copied here) is not this one's.
func TestLockUpOtherWorkspace(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	d := w.proj + "/.lockfamily"
	os.MkdirAll(d, 0o755)
	if err := os.WriteFile(d+"/up.json", []byte(`{"version":1,"project":"lockfamily-00000000","workspace":"/other","locks":{"crm":{"type":"locksql","socket_volume":"lockfamily-00000000_crm-run"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, a := range lockArgs(t, w) {
		if strings.Contains(a, "lockfamily-00000000") {
			t.Errorf("mounted %s", a)
		}
	}
	if !strings.Contains(w.errb.String(), "not this projects directory") {
		t.Errorf("warning: %s", w.errb.String())
	}
}

// LoxConsole finds lox up's running console of a lock for the deck's pane: from up.json lea
// trusts, the lock's socket volume as docker labels it, and the one container of lox up's service
// lock-<instance> that runs and mounts that very volume on /run/<tool>; anything else is an error
// that says to run `lox up`.
func TestLoxConsole(t *testing.T) {
	const id = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	setup := func(t *testing.T, locks string) (*world, *Config) {
		w := newWorld(t, repoRoot(t))
		t.Setenv("PATH", w.env["PATH"]) // exec.Command looks docker up in the process's PATH
		if locks != "" {
			writeUp(t, w, locks)
		}
		labels(w, "crm", "locksql", "socket")
		labels(w, "uat", "locksql", "socket")
		w.env["FAKE_DOCKER_OUT_PS__A"] = "0123456789ab running"
		w.env["FAKE_DOCKER_OUT_INSPECT__F_AT_0123456789AB"] = id + " " + upProjectOf(w) + "_crm-run"
		cfg, err := LoadConfig(w.getenv, w.root, w.proj)
		if err != nil {
			t.Fatal(err)
		}
		return w, cfg
	}
	crm := lockUp("crm", "locksql")
	crmProd := `"crm":{"type":"locksql","socket_volume":"PROJECT_crm-run","profile":"prod"}`
	w, cfg := setup(t, crm)
	inst, c, err := LoxConsole(cfg, w.environ(), w.proj, "locksql", "")
	if err != nil || inst != "crm" || c != id {
		t.Fatalf("LoxConsole: %q %q %v", inst, c, err)
	}
	if !w.hasCall("docker [ps] [-a] [--filter] [label=com.docker.compose.project=" + upProjectOf(w) + "] [--filter] [label=com.docker.compose.service=lock-crm] [--format] [{{.ID}} {{.State}}]") {
		t.Fatalf("calls:\n%s", strings.Join(w.calls(), "\n"))
	}
	// The profile picks the lock that serves it.
	w, cfg = setup(t, crmProd+","+`"uat":{"type":"locksql","socket_volume":"PROJECT_uat-run","profile":"uat"}`)
	if inst, _, err := LoxConsole(cfg, w.environ(), w.proj, "locksql", "prod"); err != nil || inst != "crm" {
		t.Fatalf("profile prod: %q %v", inst, err)
	}
	for name, c := range map[string]struct {
		locks, profile string
		env            map[string]string
		want           string
	}{
		"no up.json":          {"", "", nil, "no locksql console of lox up"},
		"no locksql lock":     {lockUp("events", "lockmq"), "", nil, "no locksql lock of lox up"},
		"no lock of profile":  {crmProd, "uat", nil, `serves the profile "uat"`},
		"two locks":           {crm + "," + lockUp("uat", "locksql"), "", nil, "lox up runs 2 locksql locks (crm, uat)"},
		"other socket volume": {`"crm":{"type":"locksql","socket_volume":"PROJECT_crm-home"}`, "", nil, "is not lox up's"},
		"no container":        {crm, "", map[string]string{"FAKE_DOCKER_OUT_PS__A": ""}, "lox up runs no console for it"},
		"two containers":      {crm, "", map[string]string{"FAKE_DOCKER_OUT_PS__A": "0123456789ab running\nba9876543210 running"}, "2 containers"},
		"stopped":             {crm, "", map[string]string{"FAKE_DOCKER_OUT_PS__A": "0123456789ab exited"}, "its console is not running (exited)"},
		"other volume mount":  {crm, "", map[string]string{"FAKE_DOCKER_OUT_INSPECT__F_AT_0123456789AB": id + " lockhttp-run"}, "not its console"},
		"no mount":            {crm, "", map[string]string{"FAKE_DOCKER_OUT_INSPECT__F_AT_0123456789AB": id + " "}, "not its console"},
		"ps fails":            {crm, "", map[string]string{"FAKE_DOCKER_RC_PS__A": "1"}, "could not list lox up's containers"},
	} {
		t.Run(name, func(t *testing.T) {
			w, cfg := setup(t, c.locks)
			for k, v := range c.env {
				w.env[k] = v
			}
			inst, id, err := LoxConsole(cfg, w.environ(), w.proj, "locksql", c.profile)
			if err == nil || !strings.Contains(err.Error(), c.want) || inst != "" || id != "" {
				t.Fatalf("%q %q %v, want %q", inst, id, err, c.want)
			}
			if name != "ps fails" && !strings.Contains(err.Error(), "lox") {
				t.Errorf("no word of lox: %v", err)
			}
		})
	}
	// A socket volume docker does not label as lox up's.
	w, cfg = setup(t, crm)
	labelsWith(w, "crm", "locksql", "socket", "lockfamily-00000000", "crm", w.proj)
	if _, _, err := LoxConsole(cfg, w.environ(), w.proj, "locksql", ""); err == nil || !strings.Contains(err.Error(), "is not lock up's socket volume") {
		t.Fatalf("foreign volume: %v", err)
	}
}
